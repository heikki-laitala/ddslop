// Package simplify finds code that can be removed or flattened: functions
// nothing calls, wrappers that only forward their arguments, and traits
// with a single implementation.
package simplify

import (
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

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
		name := u.Name[strings.LastIndex(u.Name, ":")+1:]
		if words[name] <= 1 {
			dead = append(dead, u)
		}
	}
	return dead
}
