package baseline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/report"
)

func occ(file, name string, line int) report.Occurrence {
	return report.Occurrence{File: file, Name: name, StartLine: line, EndLine: line + 9}
}

func cand(fp, kind string, occs ...report.Occurrence) report.Candidate {
	return report.Candidate{Fingerprint: fp, Kind: kind, Occurrences: occs}
}

func rep(cs ...report.Candidate) report.Report {
	for i := range cs {
		cs[i].ID = fmt.Sprintf("c%d", i+1)
	}
	return report.Report{Candidates: cs, Summary: report.Summary{Candidates: len(cs)}}
}

func ids(r report.Report) string {
	var out []string
	for _, c := range r.Candidates {
		out = append(out, c.ID+":"+c.Fingerprint)
	}
	return fmt.Sprint(out)
}

func TestFromReportWriteAndLoadRoundTrip(t *testing.T) {
	r := rep(
		cand("rust:b", "near-miss", occ("z.rs", "z", 1), occ("a.rs", "a", 1)),
		cand("rust:a", "exact-clone", occ("b.rs", "f", 5), occ("c.rs", "f", 5)),
	)
	path := filepath.Join(t.TempDir(), ".ddslop-baseline.json")

	if err := Write(path, FromReport(r)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if b.Version != 1 || len(b.Candidates) != 2 {
		t.Fatalf("baseline = %+v, want version 1 and 2 entries", b)
	}
	// Sorted by fingerprint, occurrences sorted: stable diffs in Git.
	if got := fmt.Sprint(b.Candidates[0]); got != "{rust:a exact-clone [b.rs#f c.rs#f]}" {
		t.Errorf("first entry = %s", got)
	}
	if got := fmt.Sprint(b.Candidates[1].Occurrences); got != "[a.rs#a z.rs#z]" {
		t.Errorf("second entry occurrences = %s, want sorted", got)
	}
	content, _ := os.ReadFile(path) //nolint:gosec // test file in a temp dir
	if strings.Contains(string(content), "start_line") {
		t.Error("baseline stores line numbers, which shift with unrelated edits")
	}
}

func TestFilterDropsKnownAndKeepsNew(t *testing.T) {
	base := FromReport(rep(
		cand("rust:fam", "near-miss", occ("a.rs", "a", 1), occ("b.rs", "b", 1), occ("c.rs", "c", 1)),
		cand("rust:pair", "exact-clone", occ("d.rs", "d", 1), occ("e.rs", "e", 1)),
	))

	now := rep(
		// The family shrank and its anchor changed: still known.
		cand("rust:other", "near-miss", occ("b.rs", "b", 40), occ("c.rs", "c", 3)),
		// A new copy joined a known pair: new.
		cand("rust:pair", "exact-clone", occ("d.rs", "d", 1), occ("e.rs", "e", 1), occ("n.rs", "n", 1)),
		// Occurrences from two different known entries: new duplication.
		cand("rust:mix", "renamed-clone", occ("a.rs", "a", 1), occ("d.rs", "d", 1)),
		// Lines moved: still known.
		cand("rust:pair", "exact-clone", occ("d.rs", "d", 90), occ("e.rs", "e", 7)),
	)

	got := Filter(now, base)

	if want := "[c1:rust:pair c2:rust:mix]"; ids(got) != want {
		t.Errorf("kept = %s, want %s (renumbered)", ids(got), want)
	}
	if got.Summary.Candidates != 2 || got.Summary.Baselined != 2 {
		t.Errorf("summary = %+v, want 2 candidates and 2 baselined", got.Summary)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing baseline file returned no error")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version": 2, "candidates": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("unsupported version: err = %v, want a version error", err)
	}
}

func TestADriftIsKnownOnlyByItsOwnFingerprint(t *testing.T) {
	base := FromReport(rep(
		cand("go:convention-drift:message", "convention-drift", occ("s.go", "load", 5)),
		cand("go:fam", "near-miss", occ("s.go", "save", 1), occ("t.go", "store", 1)),
	))

	now := rep(
		// The same drift, its line moved: known.
		cand("go:convention-drift:message", "convention-drift", occ("s.go", "load", 30)),
		// Another form drifting in the same function: new.
		cand("go:convention-drift:==", "convention-drift", occ("s.go", "load", 8)),
		// A drift in a function known for another kind: new.
		cand("go:convention-drift:save", "convention-drift", occ("s.go", "save", 3)),
	)

	if got, want := ids(Filter(now, base)), "[c1:go:convention-drift:== c2:go:convention-drift:save]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}
