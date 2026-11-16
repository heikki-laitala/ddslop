package clone

import (
	"fmt"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func id(text string, line int) unit.Leaf {
	return unit.Leaf{Kind: unit.Identifier, Text: text, Line: line}
}

func decl(text string, line int) unit.Leaf {
	return unit.Leaf{Kind: unit.Identifier, Text: text, Line: line, Declares: true}
}

func lit(text string, line int) unit.Leaf {
	l := id(text, line)
	l.Kind = unit.Literal
	return l
}

func groupOf(leaves ...[]unit.Leaf) Group {
	g := Group{}
	for i, l := range leaves {
		g.Units = append(g.Units, unit.Unit{File: fmt.Sprintf("f%d.rs", i), Leaves: l})
	}
	return g
}

func TestDifferencesOfExactCopiesIsEmpty(t *testing.T) {
	leaves := []unit.Leaf{decl("f", 1), id("x", 2), lit("1", 2)}
	if d := Differences(groupOf(leaves, leaves)); len(d) != 0 {
		t.Errorf("exact copies have differences: %+v", d)
	}
}

func TestDifferencesCollapseRepeatedValues(t *testing.T) {
	a := []unit.Leaf{decl("load", 1), lit(`"users"`, 2), id("db", 3), lit(`"users"`, 4)}
	b := []unit.Leaf{decl("load", 10), lit(`"orders"`, 11), id("db", 12), lit(`"orders"`, 13)}

	diffs := Differences(groupOf(a, b))

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

// A difference is local when every copy declares its own name for it; a
// name used but not declared, or declared in only some copies, comes from
// outside and is a parameter.
func TestDifferencesAreLocalOnlyWhereEveryCopyDeclaresTheName(t *testing.T) {
	for name, tc := range map[string]struct {
		copies [][]unit.Leaf
		want   []string
	}{
		"consistent renames": {
			copies: [][]unit.Leaf{
				{decl("load_user", 1), decl("user_id", 1), id("user_id", 2)},
				{decl("load_order", 5), decl("order_id", 5), id("order_id", 6)},
			},
			want: []string{"[load_user load_order] local=true", "[user_id order_id] local=true"},
		},
		// db.get_user(id) vs db.get_order(id): the called method differs.
		"used names": {
			copies: [][]unit.Leaf{
				{id("db", 1), id("get_user", 1)},
				{id("db", 5), id("get_order", 5)},
			},
			want: []string{"[get_user get_order] local=false"},
		},
		// limit comes from outside.
		"names declared in only some copies": {
			copies: [][]unit.Leaf{
				{decl("v", 1), id("v", 2)},
				{decl("w", 5), id("limit", 6)},
				{decl("q", 9), id("q", 10)},
			},
			want: []string{"[v w q] local=true", "[v limit q] local=false"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := testutil.Format(Differences(groupOf(tc.copies...)), func(d Difference) string {
				return fmt.Sprintf("%v local=%v", d.Values, d.Local)
			})
			testutil.Lines(t, "differences", got, tc.want)
		})
	}
}

func TestDifferencesListEachLineOnce(t *testing.T) {
	a := []unit.Leaf{lit("1", 3), lit("1", 3), lit("1", 4)}
	b := []unit.Leaf{lit("2", 8), lit("2", 8), lit("2", 9)}

	diffs := Differences(groupOf(a, b))

	if len(diffs) != 1 {
		t.Fatalf("got %d differences, want 1", len(diffs))
	}
	if got := fmt.Sprint(diffs[0].Lines); got != "[[3 4] [8 9]]" {
		t.Errorf("lines = %s, want [[3 4] [8 9]]", got)
	}
}
