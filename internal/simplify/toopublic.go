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
}

// TooPublic returns the public functions and constants that only their
// own library names, which can be visible to the library alone
// (pub(crate) in Rust). library returns the library a file belongs to,
// or nil when it is in no library of an internal crate; words counts
// every word of every source file, as for Dead. A name the library
// writes once only is dead code, and the compiler would report one unused
// in some builds if only optional code wrote it besides its declaration.
func TooPublic(units []unit.Unit, words map[string]int, library func(file string) *Library) []unit.Unit {
	var out []unit.Unit
	for _, u := range units {
		name, ok := publicName(u)
		lib := library(u.File)
		if ok && lib != nil && lib.Words[name] == words[name] && lib.Words[name]-lib.Optional[name] > 1 {
			out = append(out, u)
		}
	}
	return out
}
