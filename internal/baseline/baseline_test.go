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
		// Another kind in a function known for a drift: new.
		cand("go:dead:load", "dead-code", occ("s.go", "load", 1)),
	)

	if got, want := ids(Filter(now, base)), "[c1:go:convention-drift:== c2:go:convention-drift:save c3:go:dead:load]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}

// A cycle is its crate and modules: known by its fingerprint whichever
// calls close it now, and its entry knows nothing else.
func TestACycleIsKnownByItsFingerprintAlone(t *testing.T) {
	base := FromReport(rep(
		cand("rust:dependency-cycle:config-store", "dependency-cycle", occ("config.rs", "load", 1), occ("store.rs", "open", 1)),
	))

	now := rep(
		// The same modules, now closed by other calls: known.
		cand("rust:dependency-cycle:config-store", "dependency-cycle", occ("config.rs", "validate", 9), occ("store.rs", "access", 4)),
		// Another pair calling each other: new.
		cand("rust:dependency-cycle:store-cache", "dependency-cycle", occ("config.rs", "load", 1), occ("store.rs", "open", 1)),
		// Another kind in the cycle's functions: new.
		cand("rust:dead:load", "dead-code", occ("config.rs", "load", 1)),
	)

	if got, want := ids(Filter(now, base)), "[c1:rust:dependency-cycle:store-cache c2:rust:dead:load]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}

// Code moved to another file keeps its duplication known: an occurrence
// is matched by its function's name when its file changed, as long as the
// candidate has no more copies of a name than the entry recorded. Each
// entry is matched so at most once, by the candidate that kept the most of
// its files, so a second family of the same functions is still new. A
// candidate none of whose occurrences stayed in place must be the same
// code, by its fingerprint.
func TestMovedDuplicationStaysKnownAndNewCopiesDoNot(t *testing.T) {
	base := FromReport(rep(
		cand("rust:pair", "exact-clone", occ("a.rs", "f", 1), occ("b.rs", "f", 1)),
		cand("rust:fam", "near-miss", occ("x.rs", "load", 1), occ("y.rs", "store", 1)),
	))

	now := rep(
		// Another pair of f, in files the baseline never saw: new.
		cand("rust:elsewhere", "exact-clone", occ("d.rs", "f", 1), occ("e.rs", "f", 1)),
		// One copy moved from a.rs to c.rs: known.
		cand("rust:one-moved", "exact-clone", occ("c.rs", "f", 4), occ("b.rs", "f", 1)),
		// Both copies moved, the same code (its fingerprint): known.
		cand("rust:fam", "near-miss", occ("z.rs", "load", 1), occ("w.rs", "store", 1)),
		// The same names under another kind: new.
		cand("rust:other-kind", "renamed-clone", occ("q.rs", "load", 1), occ("r.rs", "store", 1)),
	)

	got := Filter(now, base)

	if want := "[c1:rust:elsewhere c2:rust:other-kind]"; ids(got) != want {
		t.Errorf("kept = %s, want %s", ids(got), want)
	}
	if got.Summary.Baselined != 2 {
		t.Errorf("baselined = %d, want 2", got.Summary.Baselined)
	}
}

// A further copy of a known pair is new even when no other candidate
// claims the pair's entry: the candidate has more copies of f than the
// entry recorded.
func TestAFurtherCopyOfAMovedPairIsNew(t *testing.T) {
	base := FromReport(rep(cand("rust:pair", "exact-clone", occ("a.rs", "f", 1), occ("b.rs", "f", 1))))
	now := rep(cand("rust:three", "exact-clone", occ("c.rs", "f", 1), occ("b.rs", "f", 1), occ("g.rs", "f", 1)))

	if got, want := ids(Filter(now, base)), "[c1:rust:three]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}

// Unrelated clones that only share a known pair's function names, in files
// the baseline never saw, are new: nothing stayed in place and the code is
// not the same, though the pair they share names with is gone.
func TestClonesSharingOnlyNamesWithAGonePairAreNew(t *testing.T) {
	base := FromReport(rep(cand("rust:old", "exact-clone", occ("old/a.rs", "load", 1), occ("old/b.rs", "load", 1))))
	now := rep(cand("rust:unrelated", "exact-clone", occ("new/c.rs", "load", 1), occ("new/d.rs", "load", 1)))

	if got, want := ids(Filter(now, base)), "[c1:rust:unrelated]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}

// A baseline keeps one key per function, so two blocks copied inside one
// function are one key: a pure move of that function stays known.
func TestAMovedFunctionWithTwoCopiedBlocksStaysKnown(t *testing.T) {
	base := FromReport(rep(cand("rust:block", "block-clone", occ("a.rs", "run", 1), occ("a.rs", "run", 30))))
	now := rep(cand("rust:block", "block-clone", occ("b.rs", "run", 1), occ("b.rs", "run", 30)))

	if got := ids(Filter(now, base)); got != "[]" {
		t.Errorf("kept = %s, want none", got)
	}
}

// Moved candidates are matched to entries as a whole, not first come first
// served: a candidate that fits two entries does not take the only entry
// another one fits, and the same code (its fingerprint) is preferred.
func TestMovedCandidatesAreMatchedWithoutStrandingAnother(t *testing.T) {
	base := FromReport(rep(
		cand("rust:a", "exact-clone", occ("a.rs", "f", 1), occ("c.rs", "f", 1)),
		cand("rust:b", "exact-clone", occ("a.rs", "f", 1), occ("b.rs", "f", 1)),
	))
	now := rep(
		// Kept a.rs in place, so it fits both entries.
		cand("rust:n", "exact-clone", occ("a.rs", "f", 1), occ("n.rs", "f", 1)),
		// Both copies moved, the code of rust:a: fits that entry only.
		cand("rust:a", "exact-clone", occ("m.rs", "f", 1), occ("k.rs", "f", 1)),
	)

	if got := ids(Filter(now, base)); got != "[]" {
		t.Errorf("kept = %s, want none: each fits an entry of its own", got)
	}
}

// The same, with no fingerprint to go by: each candidate kept a copy in
// place, one of them in a file only one entry has.
func TestKeptCopiesAloneAreMatchedWithoutStrandingAnother(t *testing.T) {
	base := FromReport(rep(
		cand("rust:a", "exact-clone", occ("a.rs", "f", 1), occ("c.rs", "f", 1)),
		cand("rust:b", "exact-clone", occ("a.rs", "f", 1), occ("b.rs", "f", 1)),
	))
	now := rep(
		cand("rust:n", "exact-clone", occ("a.rs", "f", 1), occ("n.rs", "f", 1)),
		cand("rust:m", "exact-clone", occ("c.rs", "f", 1), occ("m.rs", "f", 1)),
	)

	if got := ids(Filter(now, base)); got != "[]" {
		t.Errorf("kept = %s, want none: each fits an entry of its own", got)
	}
}

// Two candidates fitting one entry: the same code (its fingerprint) is the
// moved one, and the other is new.
func TestTheSameCodeIsPreferredForAMovedEntry(t *testing.T) {
	base := FromReport(rep(cand("rust:pair", "exact-clone", occ("a.rs", "f", 1), occ("b.rs", "f", 1))))
	now := rep(
		cand("rust:new", "exact-clone", occ("a.rs", "f", 1), occ("n.rs", "f", 1)),
		cand("rust:pair", "exact-clone", occ("m.rs", "f", 1), occ("k.rs", "f", 1)),
	)

	if got, want := ids(Filter(now, base)), "[c1:rust:new]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}

// Some kinds' fingerprints hash their files (a flow's entry points, a
// single function or trait), so a move changes them: moved whole, the same
// functions by name are the evidence instead. A different function is new.
func TestMovedFindingsKeyedByTheirFilesStayKnown(t *testing.T) {
	base := FromReport(rep(
		cand("rust:dead-code:1", "dead-code", occ("a.rs", "unused", 1)),
		cand("rust:flow:1", "parallel-flow", occ("a.rs", "create", 1), occ("b.rs", "update", 1)),
	))
	now := rep(
		cand("rust:dead-code:2", "dead-code", occ("moved.rs", "unused", 1)),
		cand("rust:flow:2", "parallel-flow", occ("c.rs", "create", 1), occ("d.rs", "update", 1)),
		cand("rust:dead-code:3", "dead-code", occ("moved.rs", "other", 1)),
	)

	if got, want := ids(Filter(now, base)), "[c1:rust:dead-code:3]"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
}
