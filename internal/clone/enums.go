package clone

import (
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/group"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// EnumGroup is a set of two or more enums with the same variants, sorted
// by file and start line.
type EnumGroup struct {
	Enums []unit.Enum
}

// first is where the group's first copy is.
func (g EnumGroup) first() group.Located { return g.Enums[0] }

// DuplicatedNodes is how many syntax nodes would disappear if all copies
// but one were removed.
func (g EnumGroup) DuplicatedNodes() int {
	return g.Enums[0].Nodes * (len(g.Enums) - 1)
}

// minDistinctVariants is the fewest bare variants (without payloads) that
// make two enums a copy rather than a coincidence. Measured on a 735k- and
// a 188k-line Rust codebase: enums sharing three or more variants, or two
// with payloads, were one type declared again (a sandbox state and a run
// status with the same seven states, one error enum per module with the
// same failures); two bare variants ({Bind, Volume}) were coincidences
// unless the enums also shared their name.
const minDistinctVariants = 3

// Enums groups enums declaring the same variants, names and payloads, in
// any order. Groups are ordered by duplicated size, largest first, then by
// the location of their first enum.
// ddslop:ignore one group.Collect call with its own key and filter, like Structs; shape, not logic
func Enums(enums []unit.Enum) []EnumGroup {
	groups := group.Collect(enums, func(e unit.Enum) (string, bool) { return variantKey(e), len(e.Variants) >= 2 },
		func(_ string, members []unit.Enum) (EnumGroup, bool) {
			return EnumGroup{Enums: members}, len(members) >= 2 && copied(members)
		})
	rankGroups(groups)
	return groups
}

// variantKey is an enum's variants with their payloads, sorted.
func variantKey(e unit.Enum) string {
	vs := make([]string, len(e.Variants))
	for i, v := range e.Variants {
		vs[i] = v.Name + v.Payload
	}
	slices.Sort(vs)
	return strings.Join(vs, "|")
}

// copied reports whether enums sharing their variants are a copy: they
// have minDistinctVariants variants, or a payload, or the same name.
func copied(members []unit.Enum) bool {
	first := members[0]
	if len(first.Variants) >= minDistinctVariants ||
		slices.ContainsFunc(first.Variants, func(v unit.Variant) bool { return v.Payload != "" }) {
		return true
	}
	return !slices.ContainsFunc(members, func(e unit.Enum) bool { return e.Name != first.Name })
}
