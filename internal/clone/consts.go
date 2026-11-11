package clone

import (
	"cmp"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// ConstGroup is a set of two or more constants with the same name and
// value, sorted by file and line.
type ConstGroup struct {
	Consts []unit.Const
}

// Constants groups constants declared with the same name and value. A
// shared value alone is no evidence (64 and 1000 are common); a shared name
// too means one constant was copied. Macro values are left out, since
// include_str!("x") means a different file in each crate, and so are
// unnamed constants (const _), which hold compile-time assertions.
func Constants(consts []unit.Const) []ConstGroup {
	byKey := map[[2]string][]unit.Const{}
	for _, c := range consts {
		if c.Macro || c.Name == "_" {
			continue
		}
		k := [2]string{c.Name, c.Value}
		byKey[k] = append(byKey[k], c)
	}
	var groups []ConstGroup
	for _, members := range byKey {
		if len(members) < 2 {
			continue
		}
		slices.SortFunc(members, compareConst)
		groups = append(groups, ConstGroup{Consts: members})
	}
	slices.SortFunc(groups, func(a, b ConstGroup) int { return compareConst(a.Consts[0], b.Consts[0]) })
	return groups
}

// InlinedConst is a string constant whose value is also written out
// inline in its crate.
type InlinedConst struct {
	Const unit.Const
	Uses  []LiteralUse
}

// LiteralUse is one inline occurrence of a literal, in the named function.
type LiteralUse struct {
	File, Name string
	Line       int
}

// InlinedConstants finds string literals in units that repeat the value of
// a string constant declared in the same crate (crateOf maps a file to its
// crate), so the code could name the constant instead. Only distinctive
// values count: a short plain word such as "status" is as likely a JSON
// key as the constant's meaning. When several constants of a crate hold
// the value, the first by location is the one to use.
func InlinedConstants(consts []unit.Const, units []unit.Unit, crateOf func(file string) string) []InlinedConst {
	sorted := slices.Clone(consts)
	slices.SortFunc(sorted, compareConst)
	byValue := map[[2]string]int{} // crate, value -> index in found
	var found []InlinedConst
	for _, c := range sorted {
		if !c.Literal || !distinctive(c.Value) {
			continue
		}
		k := [2]string{crateOf(c.File), c.Value}
		if _, ok := byValue[k]; !ok {
			byValue[k] = len(found)
			found = append(found, InlinedConst{Const: c})
		}
	}
	for _, u := range units {
		for _, l := range u.Leaves {
			if l.Kind != unit.Literal {
				continue
			}
			if i, ok := byValue[[2]string{crateOf(u.File), l.Text}]; ok {
				found[i].Uses = append(found[i].Uses, LiteralUse{File: u.File, Name: u.Name, Line: l.Line})
			}
		}
	}
	out := found[:0]
	for _, f := range found {
		if len(f.Uses) > 0 {
			slices.SortFunc(f.Uses, func(a, b LiteralUse) int {
				if c := cmp.Compare(a.File, b.File); c != 0 {
					return c
				}
				return cmp.Compare(a.Line, b.Line)
			})
			out = append(out, f)
		}
	}
	return out
}

// minDistinctiveLength is the shortest plain-word string value taken as
// distinctive. Measured on a 735k-line Rust codebase: constants holding
// short words ("status", "model", "pending") matched JSON keys and Redis
// field names that only happen to be spelled the same, while longer
// values ("ledger_unavailable", "history_backlog") were the constant's
// own meaning written out again.
const minDistinctiveLength = 10

// distinctive reports whether a string literal's content has a letter and
// is long, or holds anything other than letters, digits and underscores.
// Without a letter it is a wildcard, a separator or a number ("*", "/",
// "1.5"), which means something different wherever it is written.
func distinctive(literal string) bool {
	content := strings.Trim(strings.TrimLeft(literal, "rb#"), `"#`)
	if !strings.ContainsFunc(content, func(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }) {
		return false
	}
	if len(content) >= minDistinctiveLength {
		return true
	}
	return strings.ContainsFunc(content, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
}

func compareConst(a, b unit.Const) int {
	if c := cmp.Compare(a.File, b.File); c != 0 {
		return c
	}
	return cmp.Compare(a.Line, b.Line)
}
