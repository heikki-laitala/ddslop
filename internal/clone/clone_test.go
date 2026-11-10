package clone

import (
	"fmt"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func u(file string, line int, shape uint64, nodes int) unit.Unit {
	return unit.Unit{
		File:      file,
		Name:      fmt.Sprintf("%s:%d", file, line),
		StartLine: line,
		EndLine:   line + 5,
		Shape:     shape,
		Nodes:     nodes,
	}
}

func locations(g Group) []string {
	out := make([]string, len(g.Units))
	for i, x := range g.Units {
		out[i] = x.Name
	}
	return out
}

func TestGroupsCollectsSameShapeAndDropsSingletons(t *testing.T) {
	units := []unit.Unit{
		u("b.rs", 10, 1, 60),
		u("a.rs", 30, 1, 60),
		u("a.rs", 5, 1, 60),
		u("c.rs", 1, 2, 60), // unique shape
	}

	groups := Groups(units, 50)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	got := fmt.Sprint(locations(groups[0]))
	want := "[a.rs:5 a.rs:30 b.rs:10]"
	if got != want {
		t.Errorf("group members = %s, want %s (sorted by file and line)", got, want)
	}
}

func TestGroupsDropsUnitsBelowMinNodes(t *testing.T) {
	units := []unit.Unit{
		u("a.rs", 1, 1, 49),
		u("b.rs", 1, 1, 49),
	}
	if groups := Groups(units, 50); len(groups) != 0 {
		t.Errorf("got %d groups for units below min size, want 0", len(groups))
	}
}

func TestGroupsRequiresEqualSizeNotJustEqualHash(t *testing.T) {
	// Guards against hash collisions: same hash but different sizes.
	units := []unit.Unit{
		u("a.rs", 1, 7, 60),
		u("b.rs", 1, 7, 80),
	}
	if groups := Groups(units, 50); len(groups) != 0 {
		t.Errorf("units with different sizes were grouped: %+v", groups)
	}
}

func TestGroupsOrderedByDuplicatedSize(t *testing.T) {
	units := []unit.Unit{
		// small group: 2 copies of 60 nodes, 60 duplicated nodes
		u("s1.rs", 1, 1, 60),
		u("s2.rs", 1, 1, 60),
		// large group: 3 copies of 100 nodes, 200 duplicated nodes
		u("l1.rs", 1, 2, 100),
		u("l2.rs", 1, 2, 100),
		u("l3.rs", 1, 2, 100),
		// tie with small group on size, ordered by first location
		u("a1.rs", 1, 3, 60),
		u("a2.rs", 1, 3, 60),
	}

	groups := Groups(units, 50)

	var firsts []string
	for _, g := range groups {
		firsts = append(firsts, g.Units[0].File)
	}
	got := fmt.Sprint(firsts)
	want := "[l1.rs a1.rs s1.rs]"
	if got != want {
		t.Errorf("group order = %s, want %s", got, want)
	}
}
