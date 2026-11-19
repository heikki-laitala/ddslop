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
func Dead(units []unit.Unit, words map[string]int, internal func(file string) bool) []unit.Unit {
	var dead []unit.Unit
	for _, u := range units {
		if name, ok := publicName(u); ok && internal(u.File) && words[name] <= 1 {
			dead = append(dead, u)
		}
	}
	return dead
}

// publicName returns the name a function is called by, the last part of
// its own as the words count it (Store::reset is called reset, r#type
// type), and whether it is public API the words can judge. Private
// functions are left to the compiler, which already reports them unused;
// trait methods are called through the trait, External functions from
// outside the source, and test code is no API.
func publicName(u unit.Unit) (string, bool) {
	if !u.Public || u.External || u.ViaInterface() || u.Test {
		return "", false
	}
	return strings.TrimPrefix(u.Name[strings.LastIndex(u.Name, ":")+1:], "r#"), true
}
