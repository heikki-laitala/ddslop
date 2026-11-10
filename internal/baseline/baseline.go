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
// cleaned up, and reports a new copy of known duplication.
func Filter(r report.Report, b Baseline) report.Report {
	known := make([]map[string]bool, len(b.Candidates))
	for i, e := range b.Candidates {
		known[i] = map[string]bool{}
		for _, o := range e.Occurrences {
			known[i][o] = true
		}
	}
	suppressed := 0
	r = report.Keep(r, func(c *report.Candidate) bool {
		for _, entry := range known {
			if all(c.Occurrences, entry) {
				suppressed++
				return false
			}
		}
		return true
	})
	r.Summary.Baselined = suppressed
	return r
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
