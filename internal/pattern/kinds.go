package pattern

import (
	"cmp"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// StringKind is a set of string literals that several functions match on:
// kinds or states spelled as strings.
type StringKind struct {
	Values  []string
	Matches []KindMatch
}

// KindMatch is one match on a StringKind's values.
type KindMatch struct {
	Unit               unit.Unit
	StartLine, EndLine int
}

// minKindFunctions is the fewest functions matching one set of strings for
// it to be a kind worth an enum. Measured on a 735k- and a 188k-line Rust
// codebase: sets matched in two functions were mostly a parser and its
// inverse, or external values (HTTP statuses, MIME types, API paths);
// from three on they were outcomes and states the code passes around as
// strings (an answer's "committed", "refused", "replayed" interpreted in
// seven functions).
const minKindFunctions = 3

// StringKinds groups the string matches of non-test units by their set of
// values, keeping sets matched in at least minKindFunctions functions,
// most functions first.
func StringKinds(units []unit.Unit) []StringKind {
	bySet := map[string]*StringKind{}
	functions := map[string]map[string]bool{}
	for _, u := range units {
		if u.Test {
			continue
		}
		for _, m := range u.StringMatches {
			k := strings.Join(m.Values, "\x00")
			if bySet[k] == nil {
				bySet[k] = &StringKind{Values: m.Values}
				functions[k] = map[string]bool{}
			}
			bySet[k].Matches = append(bySet[k].Matches, KindMatch{Unit: u, StartLine: m.StartLine, EndLine: m.EndLine})
			functions[k][u.File+"#"+u.Name] = true
		}
	}
	var out []StringKind
	for k, kind := range bySet {
		if len(functions[k]) < minKindFunctions {
			continue
		}
		slices.SortFunc(kind.Matches, func(a, b KindMatch) int {
			if c := cmp.Compare(a.Unit.File, b.Unit.File); c != 0 {
				return c
			}
			return cmp.Compare(a.StartLine, b.StartLine)
		})
		out = append(out, *kind)
	}
	slices.SortFunc(out, func(a, b StringKind) int {
		if c := cmp.Compare(len(b.Matches), len(a.Matches)); c != 0 {
			return c
		}
		return slices.Compare(a.Values, b.Values)
	})
	return out
}
