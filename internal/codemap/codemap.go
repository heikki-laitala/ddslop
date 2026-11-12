// Package codemap describes a codebase's conventions for agents: the
// layers its modules form and the helpers shared between them, derived
// from the approximate call graph with fixed rules.
package codemap

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// named reports whether a unit's k-th callee was reached by a call that
// names it (f(), Type::f(), module::f() or a call on self), rather than
// only by its method name. A guess by method name often reaches a library
// method of the same name (Option::or, bool::then), so the map counts only
// named calls.
func named(g *flow.Graph, caller, k int) bool {
	return caller >= len(g.ByName) || k >= len(g.ByName[caller]) || !g.ByName[caller][k]
}

// Grouping names the group a unit belongs to (its module or its crate),
// or "" to leave the unit out.
type Grouping func(u unit.Unit) string

// ByModule groups units by their module (see Module).
func ByModule(crates map[string]string) Grouping { return byFile(Module, crates) }

// ByCrate groups Rust units by their crate (see Crate).
func ByCrate(crates map[string]string) Grouping { return byFile(Crate, crates) }

// byFile groups units by a name given to their file.
func byFile(name func(file string, crates map[string]string) string, crates map[string]string) Grouping {
	return perFile(func(file string) string { return name(file, crates) })
}

// perFile names each file once: a grouping is asked about every unit, and
// a file holds many.
func perFile(name func(file string) string) Grouping {
	names := map[string]string{}
	return func(u unit.Unit) string {
		n, ok := names[u.File]
		if !ok {
			n = name(u.File)
			names[u.File] = n
		}
		return n
	}
}

// InCrate groups the units of one Rust crate by module, leaving out the
// rest.
func InCrate(name string, crates map[string]string) Grouping {
	return perFile(func(file string) string {
		if Crate(file, crates) != name {
			return ""
		}
		return Module(file, crates)
	})
}

// Crate returns the Rust crate a file belongs to, "." for a crate at the
// repository root, or "" for a file of another language.
func Crate(file string, crates map[string]string) string {
	if strings.HasSuffix(file, ".go") {
		return ""
	}
	name, _ := rustModule(filepath.ToSlash(file), crates)
	return name
}

// packageOf returns the nearest directory above a file that crates lists,
// "." for the repository root.
func packageOf(file string, crates map[string]string) (string, bool) {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if _, ok := crates[dir]; ok {
			return dir, true
		}
		if dir == "." || dir == "/" {
			return "", false
		}
	}
}

// Module returns the module a file belongs to: a Go file's package
// directory, or a Rust file's crate with the module directly under it
// ("acme_ledger::store"). crates maps a crate directory to the name code
// uses for it; a directory missing there is named after itself, dashes as
// underscores.
func Module(file string, crates map[string]string) string {
	file = filepath.ToSlash(file)
	if strings.HasSuffix(file, ".go") {
		return path.Dir(file)
	}
	name, top := rustModule(file, crates)
	switch {
	case top == "":
		return name
	case name == ".":
		return top
	}
	return name + "::" + top
}

// rustModule returns a Rust file's crate name and the module directly
// under the crate's src/ ("" for the crate's root module). crates maps the
// directories holding a package's Cargo.toml: a file belongs to the
// nearest one above it, as Cargo has it, and one outside src/ (build.rs,
// examples/) to that crate's root module. Without a manifest above it, the
// crate directory is the one call resolution uses.
func rustModule(file string, crates map[string]string) (name, top string) {
	dir, rel := flow.CrateOf(file), ""
	if root, ok := packageOf(file, crates); ok {
		dir = root
		if after, ok := strings.CutPrefix(strings.TrimPrefix(file, root+"/"), "src/"); ok {
			rel = after
		}
	} else if i := strings.Index(file, "/src/"); i >= 0 {
		rel = file[i+len("/src/"):]
	} else if after, ok := strings.CutPrefix(file, "src/"); ok {
		dir, rel = ".", after // a crate at the root
	}
	// The root crate stays unnamed: whether the crates map names it
	// depends on incidental files such as a root build.rs.
	var ok bool
	name, ok = crates[dir]
	if !ok || dir == "." {
		name = strings.ReplaceAll(path.Base(dir), "-", "_")
	}
	top, _, nested := strings.Cut(rel, "/")
	if !nested {
		top = strings.TrimSuffix(top, ".rs")
		if top == "lib" || top == "main" || top == "mod" {
			top = ""
		}
	}
	return name, top
}
