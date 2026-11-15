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

	"github.com/heikki-laitala/ddslop/internal/report"
)

// version is the baseline file format version.
const version = 1

// Baseline is the known duplication of a codebase.
type Baseline struct {
	Version    int     `json:"version"`
	Candidates []Entry `json:"candidates"`
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
	b := Baseline{Version: version, Candidates: []Entry{}}
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
	if b.Version != version {
		return Baseline{}, fmt.Errorf("baseline %s has version %d, this ddslop reads version %d", path, b.Version, version)
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
	known := make([]map[string]bool, len(b.Candidates))
	for i, e := range b.Candidates {
		known[i] = map[string]bool{}
		for _, o := range e.Occurrences {
			known[i][o] = true
		}
	}
	isKnown := make([]bool, len(r.Candidates))
	used := make([]bool, len(b.Candidates))
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
				isKnown[ci], used[i] = true, true
				break
			}
		}
	}
	moved(r.Candidates, b.Candidates, isKnown, used)
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
// than the entry recorded. Each entry no candidate matched otherwise is
// matched so at most once, by the candidate that kept the most of its
// occurrences in place, so a second family of the same functions, or a
// further copy, is still new. Drifts and cycles are known by fingerprint
// only and are left out.
func moved(cs []report.Candidate, entries []Entry, isKnown, used []bool) {
	type pair struct{ c, e, kept int }
	var pairs []pair
	for ci, c := range cs {
		if isKnown[ci] || byFingerprint(c.Kind) {
			continue
		}
		names := map[string]int{}
		for _, o := range c.Occurrences {
			names[o.Name]++
		}
		for ei, e := range entries {
			if used[ei] || e.Kind != c.Kind || byFingerprint(e.Kind) {
				continue
			}
			recorded := map[string]int{}
			keys := map[string]bool{}
			for _, o := range e.Occurrences {
				recorded[name(o)]++
				keys[o] = true
			}
			fits := true
			for n, count := range names {
				fits = fits && count <= recorded[n]
			}
			if !fits {
				continue
			}
			kept := 0
			for _, o := range c.Occurrences {
				if keys[key(o)] {
					kept++
				}
			}
			pairs = append(pairs, pair{ci, ei, kept})
		}
	}
	slices.SortStableFunc(pairs, func(x, y pair) int { return cmp.Compare(y.kept, x.kept) })
	for _, p := range pairs {
		if !isKnown[p.c] && !used[p.e] {
			isKnown[p.c], used[p.e] = true, true
		}
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
