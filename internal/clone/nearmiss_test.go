package clone

import (
	"fmt"
	"strings"
	"testing"

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
	if nm.Similarity >= 1 || nm.Similarity < 0.8 {
		t.Errorf("similarity = %v, want in [0.8, 1)", nm.Similarity)
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
