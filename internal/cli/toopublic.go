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
// or of another language is outside the library. A library file exposes
// the names its documentation examples mention (see rust.DoctestWords),
// crates of their own, and every name it mentions when it exports a macro
// (#[macro_export], expanded in other crates). A crate exports what its
// plain pub uses re-export (see exports), which a pub(crate) use cannot.
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
	exported := exports{}
	for _, f := range files {
		crate, macros := crateOf(f.Path), f.Words["macro_export"] > 0
		var doctests map[string]bool
		if crate != "" {
			src := readFile(root, f.Path)
			doctests = rust.DoctestWords(src, func(included string) []byte {
				return readFile(root, path.Join(path.Dir(f.Path), included))
			})
			exported.add(crate, f, src)
		}
		for name := range names {
			if n := f.Words[name]; n > 0 {
				mentions[name] = append(mentions[name], simplify.Mention{Crate: crate, Count: n, Exposes: macros || doctests[name]})
			}
		}
	}
	return simplify.TooPublic(units, mentions, crateOf, internal, func(u unit.Unit) bool {
		return exported.of(crateOf(u.File), u)
	})
}

// exports are what each crate's plain pub uses re-export (see
// rust.PublicReexports), by crate: the item each names, its last segment,
// whatever alias it takes (pub use inner::helper as h), and the modules
// globs re-export whole (pub use inner::*), with the declared names of
// the modules #[path] attributes place in other files.
type exports map[string]*crateExports

// crateExports are one crate's exports (see exports).
type crateExports struct {
	items, globs map[string]bool
	paths        map[string]string // module declared with #[path] -> its file's module name
}

// add records what a file of a crate re-exports, and the modules it
// declares with #[path].
func (e exports) add(crate string, f index.File, src []byte) {
	c := e[crate]
	if c == nil {
		c = &crateExports{items: map[string]bool{}, globs: map[string]bool{}, paths: map[string]string{}}
		e[crate] = c
	}
	for name, file := range rust.PathModules(src) {
		c.paths[name] = fileModule(path.Join(path.Dir(f.Path), file))
	}
	if len(f.TopReexports) == 0 {
		return
	}
	for name, target := range rust.PublicReexports(src) {
		if strings.HasPrefix(name, "*") {
			c.globs[simplify.ShortName(target)] = true
		} else {
			c.items[simplify.ShortName(target)] = true
		}
	}
}

// of reports whether a crate re-exports a unit: by its name, or its
// module whole, the module its file is (by its name, or the one a #[path]
// declaration gives it) or the inline module it is in.
func (e exports) of(crate string, u unit.Unit) bool {
	c := e[crate]
	if c == nil {
		return false
	}
	if c.items[simplify.ShortName(u.Name)] || u.Module != "" && c.globs[simplify.ShortName(u.Module)] {
		return true
	}
	module := fileModule(u.File)
	for glob := range c.globs {
		if glob == module || c.paths[glob] == module {
			return true
		}
	}
	return false
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
