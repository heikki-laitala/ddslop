package simplify

import "github.com/heikki-laitala/ddslop/internal/unit"

// Library counts the words of one library's files.
type Library struct {
	// Words leaves out those code outside the library compiles or reaches
	// (doc tests, exported macros, re-exports).
	Words map[string]int
	// Optional are those of Words outside the code every build compiles:
	// comments, test code, code under a #[cfg].
	Optional map[string]int
	// Declared counts the items the library declares of each name
	// (functions, methods, constants, fields, variants): a declaration
	// is no use.
	Declared map[string]int
}

// TooPublic returns the public functions and constants that only their
// own library names, which can be visible to the library alone
// (pub(crate) in Rust). library returns the library a file belongs to,
// or nil when it is in no library of an internal crate; words counts
// every word of every source file, as for Dead. One of the library's
// words must use the name in code every build compiles, not optional and
// no declaration: else it is dead code, or the compiler would report it
// unused in some builds.
func TooPublic(units []unit.Unit, words map[string]int, library func(file string) *Library) []unit.Unit {
	var out []unit.Unit
	for _, u := range units {
		name, ok := publicName(u)
		if !ok {
			continue
		}
		if lib := library(u.File); lib != nil && lib.Words[name] == words[name] && lib.Words[name]-lib.Optional[name]-lib.Declared[name] > 0 {
			out = append(out, u)
		}
	}
	return out
}
