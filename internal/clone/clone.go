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

// minNodesPerParameter is the least code a candidate needs per parameter.
// Below it, extracting the shared code would turn most of its body into
// arguments: the copies share a shape, not logic. Measured on a 735k-line
// Rust codebase: every audited real target had 24 or more nodes per
// parameter, while Debug and Display impls, match tables mapping variants
// to different strings, and same-shaped one-line delegations had 12.7-19.8.
const minNodesPerParameter = 20

// MostlyParameters reports whether shared code of the given size would
// need too many parameters to be worth extracting: fewer than
// minNodesPerParameter nodes per parameter.
func MostlyParameters(nodes, parameters int) bool {
	return nodes < minNodesPerParameter*parameters
}

// parameters counts the differences that become parameters: those that
// are not local renames.
func parameters(diffs []Difference) int {
	n := 0
	for _, d := range diffs {
		if !d.Local {
			n++
		}
	}
	return n
}
