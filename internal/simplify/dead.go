// Package simplify finds code that can be removed or flattened: functions
// nothing calls, wrappers that only forward their arguments, and traits
// with a single implementation.
package simplify

import (
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Mention is where a name appears in one file (see TooPublic): the
// library crate the file belongs to ("" for a file of no library: another
// target, such as a binary or an integration test, or another language),
// how many times, and whether the file exposes the name to other crates
// anyway (a #[macro_export] macro or a pub use re-export naming it).
type Mention struct {
	Crate   string
	Count   int
	Exposes bool
}

// TooPublic returns the public functions of internal crates (see Dead)
// that only their own library crate names (crateOf, "" for a file of no
// library): every mention of the name (mentions, by name), other than
// its declaration, is in a file of that crate, and none exposes it. Such
// a function can be pub(crate). One named nowhere else is Dead's; trait
// methods, External functions and test code are left out, as Dead leaves
// them.
func TooPublic(units []unit.Unit, mentions map[string][]Mention, crateOf func(file string) string, internal func(file string) bool) []unit.Unit {
	var out []unit.Unit
	for _, u := range units {
		crate := crateOf(u.File)
		if !u.Public || u.External || u.ViaInterface() || u.Test || crate == "" || !internal(u.File) {
			continue
		}
		total, inside := 0, true
		for _, m := range mentions[simpleName(u.Name)] {
			total += m.Count
			inside = inside && m.Crate == crate && !m.Exposes
		}
		if inside && total > 1 {
			out = append(out, u)
		}
	}
	return out
}

// simpleName is a unit's own name, without its owner (Type::new is new).
func simpleName(name string) string {
	return name[strings.LastIndex(name, ":")+1:]
}

// Dead returns the public functions of internal crates (internal reports,
// for a file, whether its crate is used only inside the analysed code)
// whose name appears nowhere but in their own declaration: words counts
// every identifier-like word of every source file, comments, strings and
// macros included, so a name mentioned anywhere counts as used.
//
// Private functions are left to the compiler, which already reports them
// unused; trait methods are called through the trait, and External
// functions from outside the source.
func Dead(units []unit.Unit, words map[string]int, internal func(file string) bool) []unit.Unit {
	var dead []unit.Unit
	for _, u := range units {
		if !u.Public || u.External || u.ViaInterface() || u.Test || !internal(u.File) {
			continue
		}
		if words[simpleName(u.Name)] <= 1 {
			dead = append(dead, u)
		}
	}
	return dead
}
