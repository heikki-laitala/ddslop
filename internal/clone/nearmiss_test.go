package clone

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// build makes a unit from token lines, one per source line starting at
// startLine. A plain word opens a node, "}" closes the latest one, and
// "i=x", "d=x", "t=x", "l=x" are identifier, declared identifier, type and
// literal leaves.
func build(file string, startLine int, lines ...string) unit.Unit {
	u := unit.Unit{File: file, Name: file, StartLine: startLine, EndLine: startLine + len(lines) - 1}
	for i, text := range lines {
		line := startLine + i
		for _, tok := range strings.Fields(text) {
			var sym uint32
			switch {
			case tok == "}":
				sym = unit.Close
			case len(tok) > 2 && tok[1] == '=':
				kinds := map[byte]unit.LeafKind{'i': unit.Identifier, 'd': unit.Identifier, 't': unit.Type, 'l': unit.Literal}
				kind := kinds[tok[0]]
				u.Leaves = append(u.Leaves, unit.Leaf{Kind: kind, Text: tok[2:], Line: line, Declares: tok[0] == 'd'})
				sym = unit.LeafSymbol(kind)
				u.Nodes++
			default:
				sym = 1<<31 | uint32(len(tok))<<8 | uint32(tok[0]) //nolint:gosec // short test tokens
				u.Nodes++
			}
			u.Seq = append(u.Seq, sym)
			u.SeqLines = append(u.SeqLines, line)
		}
	}
	u.Shape = uint64(len(u.Seq))<<32 | uint64(u.Seq[len(u.Seq)/2])
	for _, s := range u.Seq {
		u.Shape = u.Shape*31 + uint64(s)
	}
	return u
}

// body returns a function of 12 statement lines; extra is inserted after
// statement 'at', and literal 'changed' replaces the literal of statement 3.
func body(at int, extra, changed string) []string {
	lines := []string{"fn d=handler block"}
	for i := 1; i <= 12; i++ {
		lit := fmt.Sprint(i)
		if i == 3 && changed != "" {
			lit = changed
		}
		lines = append(lines, fmt.Sprintf("let d=v%d call i=step%d l=%s } }", i, i, lit))
		if i == at && extra != "" {
			lines = append(lines, extra)
		}
	}
	return append(lines, "} }")
}

func TestNearMissFindsCopyWithExtraStatementAndChangedValue(t *testing.T) {
	a := build("a.rs", 10, body(0, "", "")...)
	b := build("b.rs", 100, body(6, "call i=log l=\"debug\" }", "42")...)

	got := NearMisses([]unit.Unit{b, a}, 20, 0.8)

	if len(got) != 1 {
		t.Fatalf("got %d near misses, want 1", len(got))
	}
	nm := got[0]
	if nm.Units[0].File != "a.rs" || nm.Units[1].File != "b.rs" {
		t.Errorf("pair = %s, %s; want a.rs then b.rs", nm.Units[0].File, nm.Units[1].File)
	}
	if nm.Similarity[1] >= 1 || nm.Similarity[1] < 0.8 {
		t.Errorf("similarity = %v, want in [0.8, 1)", nm.Similarity[1])
	}
	// a.rs is wholly contained in b.rs (the changed literal still aligns as
	// a literal), so they share all of a.rs and less than all of b.rs.
	if nm.MatchedNodes != a.Nodes || nm.MatchedNodes >= b.Nodes {
		t.Errorf("matched nodes = %d, want %d (all of a.rs, less than b.rs's %d)", nm.MatchedNodes, a.Nodes, b.Nodes)
	}
	if len(nm.Unmatched[0]) != 0 {
		t.Errorf("a.rs unmatched lines = %v, want none", nm.Unmatched[0])
	}
	// b.rs line 100 is "fn", so statement i is on line 100+i and the extra
	// statement after statement 6 is on line 107.
	if fmt.Sprint(nm.Unmatched[1]) != "[{107 107}]" {
		t.Errorf("b.rs unmatched lines = %v, want [{107 107}]", nm.Unmatched[1])
	}
	var literal *Difference
	for i, d := range nm.Differences {
		if d.Kind == unit.Literal {
			literal = &nm.Differences[i]
		}
	}
	if literal == nil || fmt.Sprint(literal.Values) != "[3 42]" || fmt.Sprint(literal.Lines) != "[[13] [103]]" {
		t.Errorf("differences = %+v, want literal 3 | 42 on lines 13 and 103", nm.Differences)
	}
}

func TestNearMissIgnoresUnrelatedExactAndSmallUnits(t *testing.T) {
	a := build("a.rs", 1, body(0, "", "")...)
	unrelated := build("u.rs", 1, "fn d=other block", "match i=x arm l=1 } arm l=2 } }", "loop if i=y break } } }", "} }")
	exactCopy := build("c.rs", 1, body(0, "", "")...)

	if got := NearMisses([]unit.Unit{a, unrelated, exactCopy}, 20, 0.8); len(got) != 0 {
		t.Errorf("got %d near misses, want none: unrelated units differ and exact copies are not near misses", len(got))
	}

	b := build("b.rs", 1, body(6, "call i=log l=\"debug\" }", "")...)
	if got := NearMisses([]unit.Unit{a, b}, a.Nodes+1, 0.8); len(got) != 0 {
		t.Errorf("got %d near misses below min nodes, want none", len(got))
	}
}

func TestNearMissUsesOneRepresentativePerShape(t *testing.T) {
	a1 := build("a1.rs", 1, body(0, "", "")...)
	a2 := build("a2.rs", 1, body(0, "", "")...)
	a3 := build("a3.rs", 1, body(0, "", "")...)
	b := build("b.rs", 1, body(6, "call i=log l=\"debug\" }", "")...)

	got := NearMisses([]unit.Unit{a3, b, a2, a1}, 20, 0.8)

	if len(got) != 1 || got[0].Units[0].File != "a1.rs" {
		t.Errorf("near misses = %d, first = %v; want one pair using a1.rs for its shape", len(got), got)
	}
}

// lookalike has a shape of its own close to body's, but every called name
// and literal differs.
func lookalike() unit.Unit {
	var lines []string
	for _, line := range body(9, "let d=w call i=z l=0 } }", "") {
		line = strings.ReplaceAll(line, "i=step", "i=other")
		line = strings.ReplaceAll(line, "l=", "l=9")
		lines = append(lines, line)
	}
	return build("c.rs", 1, lines...)
}

// pairOf aligns two units directly, bypassing LSH and filters.
func pairOf(t *testing.T, a, b unit.Unit) NearMiss {
	t.Helper()
	matches, sim, ok := alignUnits(a, b, 0.5)
	if !ok {
		t.Fatalf("%s and %s do not align", a.File, b.File)
	}
	return nearMiss(a, []unit.Unit{b}, [][]similarity.Match{matches}, []float64{sim})
}

func TestNearMissAgreementSeparatesCopiesFromLookalikes(t *testing.T) {
	a := build("a.rs", 1, body(0, "", "")...)
	copied := build("b.rs", 1, body(6, "call i=log l=\"debug\" }", "42")...)

	if got := pairOf(t, a, copied).Agreement; got < 0.9 {
		t.Errorf("agreement of a copy = %v, want at least 0.9 (one changed literal)", got)
	}
	if got := pairOf(t, a, lookalike()).Agreement; got > 0.5 {
		t.Errorf("agreement of a lookalike = %v, want at most 0.5", got)
	}
}

func TestNearMissesDropLookalikes(t *testing.T) {
	a := build("a.rs", 1, body(0, "", "")...)
	if sim := pairOf(t, a, lookalike()).Similarity[1]; sim < 0.8 {
		t.Fatalf("lookalike similarity = %v; the test needs it above the 0.8 threshold", sim)
	}

	if got := NearMisses([]unit.Unit{a, lookalike()}, 20, 0.8); len(got) != 0 {
		t.Errorf("got %d near misses, want none: the lookalike shares structure but not values", len(got))
	}
}

func TestNearMissFamilyAroundAnchor(t *testing.T) {
	a := build("a.rs", 1, body(0, "", "")...)
	b := build("b.rs", 1, body(6, "call i=log l=\"debug\" }", "")...)
	cLines := body(0, "", "42")
	cLines = append(cLines[:5], cLines[6:]...) // drop statement 5 (line 6)
	c := build("c.rs", 1, cLines...)

	got := NearMisses([]unit.Unit{c, b, a}, 20, 0.8)

	if len(got) != 1 {
		t.Fatalf("got %d candidates, want one family", len(got))
	}
	f := got[0]
	var files []string
	for _, u := range f.Units {
		files = append(files, u.File)
	}
	if fmt.Sprint(files) != "[a.rs b.rs c.rs]" {
		t.Fatalf("family = %v, want anchor a.rs then b.rs, c.rs", files)
	}
	if f.Similarity[0] != 1 || f.Similarity[1] >= 1 || f.Similarity[2] >= 1 {
		t.Errorf("similarities = %v, want 1 for the anchor and below 1 for members", f.Similarity)
	}
	// The anchor's statement 5 (line 6) is missing from c.rs; b.rs adds a
	// statement on line 8; c.rs has nothing the anchor lacks.
	if fmt.Sprint(f.Unmatched) != "[[{6 6}] [{8 8}] []]" {
		t.Errorf("unmatched = %v, want [[{6 6}] [{8 8}] []]", f.Unmatched)
	}
	var literal *Difference
	for i, d := range f.Differences {
		if d.Kind == unit.Literal {
			literal = &f.Differences[i]
		}
	}
	if literal == nil || fmt.Sprint(literal.Values) != "[3 3 42]" {
		t.Errorf("differences = %+v, want the literal 3 | 3 | 42 across the family", f.Differences)
	}
	if f.MatchedNodes != a.Nodes+c.Nodes {
		t.Errorf("matched nodes = %d, want %d (all of a.rs with b.rs, all of c.rs with a.rs)", f.MatchedNodes, a.Nodes+c.Nodes)
	}
}

func TestAlignmentKeepsValuesTogetherAcrossRepeatedStatements(t *testing.T) {
	// Every statement has the same structure, so a structure-only alignment
	// may pair statement 5 of a.rs with statement 6 of c.rs and so on,
	// misaligning every value after the deletion.
	a := build("a.rs", 1, body(0, "", "")...)
	cLines := body(0, "", "")
	cLines = append(cLines[:5], cLines[6:]...) // drop statement 5 (line 6)
	c := build("c.rs", 1, cLines...)

	nm := pairOf(t, a, c)

	if nm.Agreement != 1 {
		t.Errorf("agreement = %v, want 1: c.rs only lacks a statement", nm.Agreement)
	}
	if fmt.Sprint(nm.Unmatched) != "[[{6 6}] []]" {
		t.Errorf("unmatched = %v, want only a.rs statement 5 on line 6", nm.Unmatched)
	}
	if len(nm.Differences) != 0 {
		t.Errorf("differences = %+v, want none", nm.Differences)
	}
}

func TestAlignmentStillPairsRenamedValues(t *testing.T) {
	a := build("a.rs", 1, body(0, "", "")...)
	var renamed []string
	for _, line := range body(6, "call i=log l=\"debug\" }", "") {
		renamed = append(renamed, strings.ReplaceAll(line, "d=handler", "d=other_handler"))
	}
	b := build("b.rs", 1, renamed...)

	nm := pairOf(t, a, b)

	if len(nm.Differences) != 1 || fmt.Sprint(nm.Differences[0].Values) != "[handler other_handler]" || !nm.Differences[0].Local {
		t.Errorf("differences = %+v, want the local rename handler | other_handler", nm.Differences)
	}
}
