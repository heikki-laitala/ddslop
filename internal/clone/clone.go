// Package clone groups units that are copies of each other.
package clone

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/group"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Group is a set of two or more units with the same shape, sorted by file
// and start line.
type Group struct {
	Units []unit.Unit
	// TestHelpers marks copies among test helpers (see check), reported
	// with fingerprints of their own.
	TestHelpers bool
}

// first is where the group's first copy is.
func (g Group) first() group.Located { return g.Units[0] }

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
	groups := group.Collect(units, func(x unit.Unit) (key, bool) { return key{x.Shape, x.Nodes}, x.Nodes >= minNodes },
		func(_ key, members []unit.Unit) (Group, bool) { return Group{Units: members}, len(members) >= 2 })
	rankGroups(groups)
	return groups
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

// ranked is a group of copies: its size and where it starts.
type ranked interface {
	DuplicatedNodes() int
	first() group.Located
}

// rankGroups orders groups of copies by duplicated size, largest first,
// then by the location of their first copy.
func rankGroups[G ranked](groups []G) {
	slices.SortFunc(groups, func(a, b G) int {
		fa, la := a.first().At()
		fb, lb := b.first().At()
		return cmp.Or(cmp.Compare(b.DuplicatedNodes(), a.DuplicatedNodes()), cmp.Compare(fa, fb), cmp.Compare(la, lb))
	})
}
