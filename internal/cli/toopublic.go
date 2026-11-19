package cli

import (
	"path"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/simplify"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// tooPublic finds the public Rust functions of internal crates that only
// their own library names (see simplify.TooPublic). A name's mentions are
// the words of every analysed file: a file of another crate, of the
// package's other targets (a binary, an integration test, an example),
// or of another language is outside the library. A file exposes the
// names it re-exports with pub use, every name when it exports a macro
// (#[macro_export], expanded in other crates), and the names it declares
// when its crate re-exports its module whole (pub use module::*).
func tooPublic(units []unit.Unit, files []index.File, crates *codemap.Packages, internal func(file string) bool) []unit.Unit {
	libraries := libraryCrates(files, crates)
	crateOf := func(file string) string {
		if !strings.HasSuffix(file, ".rs") || !inLibrary(file, crates, libraries) {
			return ""
		}
		return codemap.Crate(file, crates)
	}
	names := map[string]bool{}
	for _, u := range units {
		if u.Public && strings.HasSuffix(u.File, ".rs") {
			names[u.Name[strings.LastIndex(u.Name, ":")+1:]] = true
		}
	}
	globbed := map[string]bool{} // crate + "\x00" + module: re-exported whole
	for _, f := range files {
		for name, target := range f.TopReexports {
			if strings.HasPrefix(name, "*") {
				globbed[crateOf(f.Path)+"\x00"+target[strings.LastIndex(target, ":")+1:]] = true
			}
		}
	}
	mentions := map[string][]simplify.Mention{}
	for _, f := range files {
		crate := crateOf(f.Path)
		macros := f.Words["macro_export"] > 0
		whole := globbed[crate+"\x00"+fileModule(f.Path)]
		for name := range names {
			if n := f.Words[name]; n > 0 {
				_, reexported := f.TopReexports[name]
				mentions[name] = append(mentions[name], simplify.Mention{Crate: crate, Count: n, Exposes: macros || reexported || whole})
			}
		}
	}
	return simplify.TooPublic(units, mentions, crateOf, internal)
}

// fileModule returns the name of the module a Rust file is: inner for
// inner.rs and inner/mod.rs.
func fileModule(file string) string {
	if base := path.Base(file); base != "mod.rs" {
		return strings.TrimSuffix(base, ".rs")
	}
	return path.Base(path.Dir(file))
}
