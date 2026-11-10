// Package clone groups units that are copies of each other.
package clone

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Group is a set of two or more units with the same shape, sorted by file
// and start line.
type Group struct {
	Units []unit.Unit
}

// DuplicatedNodes is how many syntax nodes would disappear if all copies
// but one were removed.
func (g Group) DuplicatedNodes() int {
	return g.Units[0].Nodes * (len(g.Units) - 1)
}

// key identifies a shape. The size is part of it so that a hash collision
// between differently sized units cannot group them.
type key struct {
	shape uint64
	nodes int
}

// Groups returns groups of units sharing a shape, ignoring units smaller
// than minNodes syntax nodes. Groups are ordered by duplicated size, largest
// first, then by the location of their first unit.
func Groups(units []unit.Unit, minNodes int) []Group {
	byKey := map[key][]unit.Unit{}
	for _, x := range units {
		if x.Nodes < minNodes {
			continue
		}
		k := key{x.Shape, x.Nodes}
		byKey[k] = append(byKey[k], x)
	}

	var groups []Group
	for _, members := range byKey {
		if len(members) < 2 {
			continue
		}
		slices.SortFunc(members, compareLocation)
		groups = append(groups, Group{Units: members})
	}
	slices.SortFunc(groups, func(a, b Group) int {
		if c := cmp.Compare(b.DuplicatedNodes(), a.DuplicatedNodes()); c != 0 {
			return c
		}
		return compareLocation(a.Units[0], b.Units[0])
	})
	return groups
}

func compareLocation(a, b unit.Unit) int {
	if c := cmp.Compare(a.File, b.File); c != 0 {
		return c
	}
	return cmp.Compare(a.StartLine, b.StartLine)
}
