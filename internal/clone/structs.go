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
// ignoring structs with fewer than minFields fields. Small structs must
// match in their scalar and string field types too (see
// minFieldsForTypeDifferences). Groups are ordered by
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

// minFieldsForTypeDifferences is the fewest fields structs need to be
// grouped despite differing scalar or string field types. Measured on a
// 188k-line Rust codebase: three-field structs sharing names but holding
// a field as another scalar or as text were coincidences ({id, name,
// count} with i64 and usize) or views (a timestamp held as a String),
// while on a 735k-line one, three-field structs differing only in a field
// of the code's own types were one shape around different payloads
// (Receipt { operation, operation_id, result }), a generic struct.
const minFieldsForTypeDifferences = 4

// fieldKey is a struct's field names, sorted. With fewer than
// minFieldsForTypeDifferences fields, a field typed with scalars or
// strings carries its type too.
func fieldKey(s unit.Struct) string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
		if len(s.Fields) < minFieldsForTypeDifferences && scalarOrString(f.Type) {
			names[i] += ":" + f.Type
		}
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

// scalars are the type names whose difference changes how a value is
// represented, rather than which payload a field holds.
var scalars = map[string]bool{
	"bool": true, "char": true, "str": true, "String": true, "f32": true, "f64": true,
	"i8": true, "i16": true, "i32": true, "i64": true, "i128": true, "isize": true,
	"u8": true, "u16": true, "u32": true, "u64": true, "u128": true, "usize": true,
}

// scalarOrString reports whether a type, as written, names a scalar or a
// string anywhere, such as Option<&'a str> or Vec<u64>.
func scalarOrString(ty string) bool {
	return slices.ContainsFunc(strings.FieldsFunc(ty, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}), func(word string) bool { return scalars[word] })
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
