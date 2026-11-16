// Package baseline records known duplication so that check reports only
// what is new. A baseline lists each known candidate's occurrences as
// "file#function", without line numbers, so unrelated edits do not
// invalidate it.
package baseline

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/report"
)

// Version is the baseline file format version.
const Version = 1

// Baseline is the known duplication of a codebase.
type Baseline struct {
	Version    int     `json:"version"`
	Candidates []Entry `json:"candidates"`
	// Functions are the code's functions, as "file#function", when known: a
	// baseline built from a ref's code knows them, a baseline file does not.
	Functions map[string]bool `json:"-"`
	// Conventions are the code's conventions, by language and concern, when
	// known, as Functions are: a change is judged by the conventions the
	// code it started from has.
	Conventions map[string]map[string]codemap.Convention `json:"-"`
}

// Entry is one known candidate: its occurrences as "file#function".
type Entry struct {
	Fingerprint string   `json:"fingerprint"`
	Kind        string   `json:"kind"`
	Occurrences []string `json:"occurrences"`
}

// key identifies an occurrence independently of its lines.
func key(o report.Occurrence) string {
	return o.File + "#" + o.Name
}

// FromReport builds a baseline from a report's candidates, sorted so the
// file diffs cleanly.
func FromReport(r report.Report) Baseline {
	b := Baseline{Version: Version, Candidates: []Entry{}}
	for _, c := range r.Candidates {
		e := Entry{Fingerprint: c.Fingerprint, Kind: c.Kind}
		for _, o := range c.Occurrences {
			e.Occurrences = append(e.Occurrences, key(o))
		}
		slices.Sort(e.Occurrences)
		e.Occurrences = slices.Compact(e.Occurrences)
		b.Candidates = append(b.Candidates, e)
	}
	slices.SortFunc(b.Candidates, func(x, y Entry) int {
		if c := cmp.Compare(x.Fingerprint, y.Fingerprint); c != 0 {
			return c
		}
		return slices.Compare(x.Occurrences, y.Occurrences)
	})
	return b
}

// Write saves a baseline as indented JSON.
func Write(path string, b Baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // the baseline is meant to be committed and read
}

// Load reads a baseline file.
func Load(path string) (Baseline, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the user's --baseline argument
	if err != nil {
		return Baseline{}, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return Baseline{}, fmt.Errorf("baseline %s: %w", path, err)
	}
	if b.Version != Version {
		return Baseline{}, fmt.Errorf("baseline %s has version %d, this ddslop reads version %d", path, b.Version, Version)
	}
	return b, nil
}

// Filter drops the candidates the baseline already knows: those whose
// occurrences all appear together in one entry. Matching on occurrences
// rather than fingerprints keeps a shrinking family known while it is
// cleaned up, and reports a new copy of known duplication. A convention
// drift has one occurrence, a function that other candidates and other
// drifts can share, so it is known only by its fingerprint (the concern,
// the form and the function) together with its occurrence, and a drift's
// entry knows no candidate of another kind. A dependency cycle is its
// crate and modules, whichever calls close it, so it is known by its
// fingerprint alone, and its entry knows nothing else.
//
// Code moved to another file changes its occurrences' files but not their
// functions, so a candidate no entry knows that way is then matched by
// function names (see moved).
func Filter(r report.Report, b Baseline) report.Report {
	return FilterAgainst(r, b, nil)
}

// FilterAgainst is Filter knowing the current code's functions ("file#function";
// nil when unknown), which tell a moved flow entry point from a new one that
// only shares an old one's name (see regrouped).
func FilterAgainst(r report.Report, b Baseline, current map[string]bool) report.Report {
	known := make([]map[string]bool, len(b.Candidates))
	for i, e := range b.Candidates {
		known[i] = map[string]bool{}
		for _, o := range e.Occurrences {
			known[i][o] = true
		}
	}
	isKnown := make([]bool, len(r.Candidates))
	used := make([]bool, len(b.Candidates))
	// within lists the entries holding each candidate known in place: one
	// such entry is that candidate's; among several, the matching in moved
	// chooses, so it takes the one no moved candidate needs.
	within := make([][]int, len(r.Candidates))
	for ci := range r.Candidates {
		c := &r.Candidates[ci]
		for i, entry := range known {
			drift := c.Kind == report.ConventionDrift || b.Candidates[i].Kind == report.ConventionDrift
			if drift && b.Candidates[i].Fingerprint != c.Fingerprint {
				continue
			}
			// A cycle is its crate and modules, whichever calls close it.
			if c.Kind == report.DependencyCycle || b.Candidates[i].Kind == report.DependencyCycle {
				if b.Candidates[i].Fingerprint == c.Fingerprint {
					isKnown[ci], used[i] = true, true
					break
				}
				continue
			}
			if all(c.Occurrences, entry) {
				isKnown[ci] = true
				if drift {
					used[i] = true
					break
				}
				within[ci] = append(within[ci], i)
			}
		}
		if len(within[ci]) == 1 {
			used[within[ci][0]] = true
		}
	}
	moved(r.Candidates, b.Candidates, isKnown, used, within)
	regrouped(r.Candidates, b, isKnown, current)
	suppressed, next := 0, 0
	r = report.Keep(r, func(*report.Candidate) bool {
		k := isKnown[next]
		next++
		if k {
			suppressed++
		}
		return !k
	})
	r.Summary.Baselined = suppressed
	return r
}

// moved marks as known the candidates whose code moved to other files: a
// candidate of an entry's kind with no more copies of each function name
// than the entry recorded (occurrences counted per file and function, as
// the entry keeps them), and either an occurrence still in place or the
// entry's fingerprint, as the same code keeps (for a kind whose fingerprint
// a move can change, exactly the entry's functions). Each entry no candidate
// matched otherwise is matched so at most once, so a second family of the
// same functions, or a further copy, is still new. Candidates are matched
// to entries as a whole (a maximum matching), the same code first, so one
// that fits two entries does not strand another that fits one of them.
// A candidate known in place by several entries takes part too, so it
// takes the entry no moved candidate needs. Drifts and cycles are known by
// fingerprint only and are left out.
func moved(cs []report.Candidate, entries []Entry, isKnown, used []bool, within [][]int) {
	// fits[ci] lists the entries candidate ci may be, the same code first.
	fits := make([][]int, len(cs))
	same := make([][]bool, len(cs))
	for ci, c := range cs {
		if len(within[ci]) > 1 {
			// Known in place by several entries: it takes one of them.
			for _, ei := range within[ci] {
				if !used[ei] {
					fits[ci] = append(fits[ci], ei)
					same[ci] = append(same[ci], true)
				}
			}
			continue
		}
		// Flows regroup rather than move, and regrouped tells which.
		if isKnown[ci] || byFingerprint(c.Kind) || c.Kind == report.ParallelFlow {
			continue
		}
		keys := map[string]bool{}
		names := map[string]int{}
		for _, o := range c.Occurrences {
			if k := key(o); !keys[k] {
				keys[k] = true
				names[o.Name]++
			}
		}
		for ei, e := range entries {
			if used[ei] || e.Kind != c.Kind || byFingerprint(e.Kind) || !holds(e, names) {
				continue
			}
			kept := false
			for _, o := range e.Occurrences {
				kept = kept || keys[o]
			}
			if !kept && !sameCode(c, e, names) {
				continue
			}
			fits[ci] = append(fits[ci], ei)
			same[ci] = append(same[ci], c.Fingerprint == e.Fingerprint)
		}
	}
	match := make([]int, len(entries)) // entry -> candidate, or -1
	for i := range match {
		match[i] = -1
	}
	// augment finds an entry for candidate ci, moving other candidates to
	// other entries they fit if need be (an augmenting path).
	var augment func(ci int, seen []bool, sameOnly bool) bool
	augment = func(ci int, seen []bool, sameOnly bool) bool {
		for i, ei := range fits[ci] {
			if seen[ei] || (sameOnly && !same[ci][i]) {
				continue
			}
			seen[ei] = true
			if match[ei] < 0 || augment(match[ei], seen, sameOnly) {
				match[ei] = ci
				return true
			}
		}
		return false
	}
	matched := make([]bool, len(cs))
	// Candidates known in place first, then the same code, then the rest:
	// augmenting paths move earlier ones to other entries, never unmatch them.
	for _, phase := range []struct{ inPlace, sameOnly bool }{{true, true}, {false, true}, {false, false}} {
		for ci := range cs {
			if matched[ci] || len(fits[ci]) == 0 || (len(within[ci]) > 1) != phase.inPlace {
				continue
			}
			if augment(ci, make([]bool, len(entries)), phase.sameOnly) {
				matched[ci] = true
			}
		}
	}
	for ei, ci := range match {
		if ci >= 0 {
			isKnown[ci], used[ei] = true, true
		}
	}
}

// sameCode reports whether a candidate none of whose occurrences stayed in
// place is an entry's code moved elsewhere: the same fingerprint, or, for a
// kind whose fingerprint a move can change, exactly the entry's functions.
// Only names in common is no evidence.
func sameCode(c report.Candidate, e Entry, names map[string]int) bool {
	return c.Fingerprint == e.Fingerprint || (report.MovesFingerprint(c.Kind) && sameNames(e, names))
}

// sameNames reports whether an entry recorded exactly the names names
// counts, as many of each.
func sameNames(e Entry, names map[string]int) bool {
	return holds(e, names) && len(e.Occurrences) == total(names)
}

// total is the number of occurrences names counts.
func total(names map[string]int) int {
	n := 0
	for _, count := range names {
		n += count
	}
	return n
}

// holds reports whether an entry recorded at least as many occurrences of
// each name as names counts.
func holds(e Entry, names map[string]int) bool {
	recorded := map[string]int{}
	for _, o := range e.Occurrences {
		recorded[name(o)]++
	}
	for n, count := range names {
		if count > recorded[n] {
			return false
		}
	}
	return true
}

// regrouped marks as known the parallel flows whose entry points all
// existed before: entry points of the baseline's flows, or, when the
// baseline knows the code's functions, any of them. A flow is a suggestion
// over entry points, and a change that makes existing ones more alike
// regroups them into other flows, which is no new duplication. An entry
// point in another file counts by its name only where a known entry point
// of that name is gone from the current code, as many as are gone, as a
// move leaves it; with the current code unknown, names are no evidence. A
// flow with an entry point the code did not have is new.
func regrouped(cs []report.Candidate, b Baseline, isKnown []bool, current map[string]bool) {
	keys, gone := map[string]bool{}, map[string]int{}
	for _, e := range b.Candidates {
		if e.Kind != report.ParallelFlow {
			continue
		}
		for _, o := range e.Occurrences {
			if !keys[o] && current != nil && !current[o] {
				gone[name(o)]++
			}
			keys[o] = true
		}
	}
	for ci, c := range cs {
		if isKnown[ci] || c.Kind != report.ParallelFlow {
			continue
		}
		byName := map[string]int{}
		known := true
		for _, o := range c.Occurrences {
			if !keys[key(o)] && !b.Functions[key(o)] {
				byName[o.Name]++
			}
		}
		for n, count := range byName {
			known = known && count <= gone[n]
		}
		isKnown[ci] = known
	}
}

// byFingerprint reports whether a kind is known by its fingerprint rather
// than its occurrences.
func byFingerprint(kind string) bool {
	return kind == report.ConventionDrift || kind == report.DependencyCycle
}

// name is the function an occurrence key names.
func name(k string) string {
	if i := strings.LastIndex(k, "#"); i >= 0 {
		return k[i+1:]
	}
	return k
}

// all reports whether every occurrence is in the entry.
func all(occs []report.Occurrence, entry map[string]bool) bool {
	for _, o := range occs {
		if !entry[key(o)] {
			return false
		}
	}
	return true
}
