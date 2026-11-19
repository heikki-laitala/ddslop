package simplify

import (
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Library counts the words of one library's files.
type Library struct {
	// Words leaves out those code outside the library compiles or reaches
	// (doc tests, exported macros, re-exports).
	Words map[string]int
	// Optional are those of Words that are no use in some or all builds:
	// comments, test code, code under a #[cfg], export clauses.
	Optional map[string]int
	// Declared counts the items the library declares of each name
	// (functions, methods, constants, fields, variants): a declaration
	// is no use.
	Declared map[string]int
	// Exposed are the modules a glob re-exports whole (pub use shapes::*),
	// whose items other crates reach: a module's file, or a file and an
	// inline module in it, as "file#module".
	Exposed map[string]bool
}

// exposed reports whether a glob re-exports the module a unit is in (see
// Library.Exposed): its file's, or an inline module it is in, or one
// around that (shapes for shapes::nested).
func exposed(lib *Library, u unit.Unit) bool {
	if lib.Exposed[u.File] {
		return true
	}
	for module := u.Module; module != ""; {
		if lib.Exposed[u.File+"#"+module] {
			return true
		}
		i := strings.LastIndex(module, "::")
		if i < 0 {
			break
		}
		module = module[:i]
	}
	return false
}

// TooPublic returns the public functions and constants that only their
// own library names, which can be visible to the library alone
// (pub(crate) in Rust, no export in TypeScript). library returns the
// library a file belongs to, or nil when it is in no library of an
// internal crate or package; words counts every word of every source
// file, as for Dead. One of the library's words must use the name in
// code every build compiles, not optional and no declaration: else it is
// dead code, or the compiler would report it unused in some builds.
func TooPublic(units []unit.Unit, words map[string]int, library func(file string) *Library) []unit.Unit {
	var out []unit.Unit
	for _, u := range units {
		name, ok := publicName(u)
		if !ok {
			continue
		}
		lib := library(u.File)
		if lib == nil || exposed(lib, u) {
			continue
		}
		if lib.Words[name] == words[name] && lib.Words[name]-lib.Optional[name]-lib.Declared[name] > 0 {
			out = append(out, u)
		}
	}
	return out
}
