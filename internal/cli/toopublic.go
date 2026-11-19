package cli

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/simplify"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// tooPublic finds the public Rust functions of internal crates that only
// their own library names (see simplify.TooPublic). A name's mentions are
// the words of every analysed file: a file of another crate, of the
// package's other targets (a binary, an integration test, an example),
// or of another language is outside the library. A file exporting a macro
// (#[macro_export], expanded in other crates) exposes every name it
// mentions, and its documentation examples (see rust.DoctestWords), crates
// of their own, the names they mention. A crate exports what its plain pub uses re-export (see
// reexported), which a pub(crate) use cannot.
func tooPublic(root string, units []unit.Unit, files []index.File, crates *codemap.Packages, internal func(file string) bool) []unit.Unit {
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
			names[simplify.ShortName(u.Name)] = true
		}
	}
	mentions := map[string][]simplify.Mention{}
	for _, f := range files {
		crate, macros := crateOf(f.Path), f.Words["macro_export"] > 0
		var doctests map[string]bool
		if crate != "" {
			doctests = rust.DoctestWords(readFile(root, f.Path))
		}
		for name := range names {
			if n := f.Words[name]; n > 0 {
				mentions[name] = append(mentions[name], simplify.Mention{Crate: crate, Count: n, Exposes: macros || doctests[name]})
			}
		}
	}
	exported := reexported(root, files, crateOf)
	return simplify.TooPublic(units, mentions, crateOf, internal, func(u unit.Unit) bool {
		crate := crateOf(u.File)
		return exported[crate+"\x00"+simplify.ShortName(u.Name)] || exported[crate+"\x00*"+fileModule(u.File)] ||
			u.Module != "" && exported[crate+"\x00*"+simplify.ShortName(u.Module)]
	})
}

// reexported returns, by crate + "\x00" + name, what each crate's plain
// pub uses re-export (see rust.PublicReexports): the item each names, its
// last segment, whatever alias it takes (pub use inner::helper as h), and
// "*" + the module a glob re-exports whole (pub use inner::*). Only the
// files with visible uses are read again.
func reexported(root string, files []index.File, crateOf func(file string) string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		crate := crateOf(f.Path)
		if crate == "" || len(f.TopReexports) == 0 {
			continue
		}
		for name, target := range rust.PublicReexports(readFile(root, f.Path)) {
			if strings.HasPrefix(name, "*") {
				out[crate+"\x00*"+simplify.ShortName(target)] = true
			} else {
				out[crate+"\x00"+simplify.ShortName(target)] = true
			}
		}
	}
	return out
}

// readFile returns a file under the analysed root, or nothing when it
// cannot be read (a dangling symlink), which then adds nothing.
func readFile(root, file string) []byte {
	src, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(file))) //nolint:gosec // a file under the analysed root
	return src
}

// fileModule returns the name of the module a Rust file is: inner for
// inner.rs and inner/mod.rs.
func fileModule(file string) string {
	if base := path.Base(file); base != "mod.rs" {
		return strings.TrimSuffix(base, ".rs")
	}
	return path.Base(path.Dir(file))
}
