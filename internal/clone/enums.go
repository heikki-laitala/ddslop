package clone

import (
	"cmp"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// EnumGroup is a set of two or more enums with the same variants, sorted
// by file and start line.
type EnumGroup struct {
	Enums []unit.Enum
}

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
func Enums(enums []unit.Enum) []EnumGroup {
	byVariants := map[string][]unit.Enum{}
	for _, e := range enums {
		if len(e.Variants) < 2 {
			continue
		}
		k := variantKey(e)
		byVariants[k] = append(byVariants[k], e)
	}
	var groups []EnumGroup
	for _, members := range byVariants {
		if len(members) < 2 || !copied(members) {
			continue
		}
		slices.SortFunc(members, func(a, b unit.Enum) int {
			if c := cmp.Compare(a.File, b.File); c != 0 {
				return c
			}
			return cmp.Compare(a.StartLine, b.StartLine)
		})
		groups = append(groups, EnumGroup{Enums: members})
	}
	slices.SortFunc(groups, func(a, b EnumGroup) int {
		if c := cmp.Compare(b.DuplicatedNodes(), a.DuplicatedNodes()); c != 0 {
			return c
		}
		return cmp.Compare(a.Enums[0].File, b.Enums[0].File)
	})
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
