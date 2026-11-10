package clone

import (
	"fmt"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func id(text string, line int) unit.Leaf {
	return unit.Leaf{Kind: unit.Identifier, Text: text, Line: line}
}

func decl(text string, line int) unit.Leaf {
	return unit.Leaf{Kind: unit.Identifier, Text: text, Line: line, Declares: true}
}

func lit(text string, line int) unit.Leaf {
	return unit.Leaf{Kind: unit.Literal, Text: text, Line: line}
}

func group(leaves ...[]unit.Leaf) Group {
	g := Group{}
	for i, l := range leaves {
		g.Units = append(g.Units, unit.Unit{File: fmt.Sprintf("f%d.rs", i), Leaves: l})
	}
	return g
}

func TestDifferencesOfExactCopiesIsEmpty(t *testing.T) {
	leaves := []unit.Leaf{decl("f", 1), id("x", 2), lit("1", 2)}
	if d := Differences(group(leaves, leaves)); len(d) != 0 {
		t.Errorf("exact copies have differences: %+v", d)
	}
}

func TestDifferencesCollapseRepeatedValues(t *testing.T) {
	a := []unit.Leaf{decl("load", 1), lit(`"users"`, 2), id("db", 3), lit(`"users"`, 4)}
	b := []unit.Leaf{decl("load", 10), lit(`"orders"`, 11), id("db", 12), lit(`"orders"`, 13)}

	diffs := Differences(group(a, b))

	if len(diffs) != 1 {
		t.Fatalf("got %d differences, want 1: %+v", len(diffs), diffs)
	}
	d := diffs[0]
	if d.Kind != unit.Literal || d.Local {
		t.Errorf("difference = %+v, want a non-local literal", d)
	}
	if got := fmt.Sprint(d.Values); got != `["users" "orders"]` {
		t.Errorf("values = %s", got)
	}
	if got := fmt.Sprint(d.Lines); got != "[[2 4] [11 13]]" {
		t.Errorf("lines = %s, want [[2 4] [11 13]]", got)
	}
}

func TestDifferencesMarkConsistentLocalRenames(t *testing.T) {
	a := []unit.Leaf{decl("load_user", 1), decl("user_id", 1), id("user_id", 2)}
	b := []unit.Leaf{decl("load_order", 5), decl("order_id", 5), id("order_id", 6)}

	diffs := Differences(group(a, b))

	if len(diffs) != 2 {
		t.Fatalf("got %d differences, want 2: %+v", len(diffs), diffs)
	}
	for _, d := range diffs {
		if !d.Local {
			t.Errorf("difference %v should be local: both copies declare their own name", d.Values)
		}
	}
}

func TestDifferencesInUsedNamesAreNotLocal(t *testing.T) {
	// db.get_user(id) vs db.get_order(id): the called method differs.
	a := []unit.Leaf{id("db", 1), id("get_user", 1)}
	b := []unit.Leaf{id("db", 5), id("get_order", 5)}

	diffs := Differences(group(a, b))

	if len(diffs) != 1 || diffs[0].Local {
		t.Errorf("differences = %+v, want one non-local difference", diffs)
	}
}

func TestDifferencesDeclaredInOnlySomeUnitsAreNotLocal(t *testing.T) {
	a := []unit.Leaf{decl("v", 1), id("v", 2)}
	b := []unit.Leaf{decl("w", 5), id("limit", 6)} // limit comes from outside
	c := []unit.Leaf{decl("q", 9), id("q", 10)}

	diffs := Differences(group(a, b, c))

	if len(diffs) != 2 {
		t.Fatalf("got %d differences, want 2: %+v", len(diffs), diffs)
	}
	if !diffs[0].Local {
		t.Errorf("first difference %v should be local", diffs[0].Values)
	}
	if diffs[1].Local {
		t.Errorf("second difference %v uses an outside name in one unit, should not be local", diffs[1].Values)
	}
}
