package clone

import (
	"cmp"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// StructGroup is a set of two or more structs with the same field names,
// sorted by file and start line.
type StructGroup struct {
	Structs []unit.Struct
	// Differences are the struct names, when they differ (a local
	// difference: each struct declares its own), and each field whose type
	// differs.
	Differences []Difference
}

// DuplicatedNodes is how many syntax nodes would disappear if all copies
// but one were removed.
func (g StructGroup) DuplicatedNodes() int {
	return g.Structs[0].Nodes * (len(g.Structs) - 1)
}

// Structs groups structs declaring the same field names, in any order,
// ignoring structs with fewer than minFields fields. Groups are ordered by
// duplicated size, largest first, then by the location of their first
// struct.
func Structs(structs []unit.Struct, minFields int) []StructGroup {
	byFields := map[string][]unit.Struct{}
	for _, s := range structs {
		if len(s.Fields) < minFields {
			continue
		}
		k := fieldKey(s)
		byFields[k] = append(byFields[k], s)
	}
	var groups []StructGroup
	for _, members := range byFields {
		members = withoutConverted(members)
		if len(members) < 2 {
			continue
		}
		slices.SortFunc(members, func(a, b unit.Struct) int {
			if c := cmp.Compare(a.File, b.File); c != 0 {
				return c
			}
			return cmp.Compare(a.StartLine, b.StartLine)
		})
		groups = append(groups, StructGroup{Structs: members, Differences: structDifferences(members)})
	}
	slices.SortFunc(groups, func(a, b StructGroup) int {
		if c := cmp.Compare(b.DuplicatedNodes(), a.DuplicatedNodes()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Structs[0].File, b.Structs[0].File); c != 0 {
			return c
		}
		return cmp.Compare(a.Structs[0].StartLine, b.Structs[0].StartLine)
	})
	return groups
}

// withoutConverted drops the members another member converts from or
// into (#[serde(try_from = "AuthoredPort")] on Port drops AuthoredPort):
// such a type is the validated input or the wire form of the other, kept
// apart on purpose.
func withoutConverted(members []unit.Struct) []unit.Struct {
	converted := map[string]bool{}
	for _, s := range members {
		for _, name := range s.Converts {
			converted[name] = true
		}
	}
	return slices.DeleteFunc(members, func(s unit.Struct) bool { return converted[s.Name] })
}

// fieldKey is a struct's field names, sorted.
func fieldKey(s unit.Struct) string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

// structDifferences lists differing names and field types, fields in the
// first struct's order.
func structDifferences(members []unit.Struct) []Difference {
	var diffs []Difference
	add := func(kind unit.LeafKind, local bool, value func(s unit.Struct) (string, int)) {
		d := Difference{Kind: kind, Local: local}
		for _, s := range members {
			v, line := value(s)
			d.Values = append(d.Values, v)
			d.Lines = append(d.Lines, []int{line})
		}
		if slices.ContainsFunc(d.Values, func(v string) bool { return v != d.Values[0] }) {
			diffs = append(diffs, d)
		}
	}
	add(unit.Identifier, true, func(s unit.Struct) (string, int) { return s.Name, s.StartLine })
	for _, f := range members[0].Fields {
		add(unit.Type, false, func(s unit.Struct) (string, int) {
			for _, g := range s.Fields {
				if g.Name == f.Name {
					return g.Type, g.Line
				}
			}
			return "", 0 // unreachable: members share their field names
		})
	}
	return diffs
}
