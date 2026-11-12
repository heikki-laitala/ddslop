// Package codemap describes a codebase's conventions for agents: the
// layers its modules form and the helpers shared between them, derived
// from the approximate call graph with fixed rules.
package codemap

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// named reports whether a unit's k-th callee was reached by a call that
// names it (f(), Type::f(), module::f() or a call on self), rather than
// only by its method name. A guess by method name often reaches a library
// method of the same name (Option::or, bool::then), so the map counts only
// named calls.
func named(g *flow.Graph, caller, k int) bool {
	return caller >= len(g.ByName) || k >= len(g.ByName[caller]) || !g.ByName[caller][k]
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
	// The crate directory is the one call resolution uses; a file outside
	// src/ (build.rs) belongs to its crate's root module.
	dir, rel := flow.CrateOf(file), ""
	if i := strings.Index(file, "/src/"); i >= 0 {
		rel = file[i+len("/src/"):]
	} else if after, ok := strings.CutPrefix(file, "src/"); ok {
		dir, rel = ".", after // a crate at the root
	}
	// The root crate stays unnamed: whether the crates map names it
	// depends on incidental files such as a root build.rs.
	name, ok := crates[dir]
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
	switch {
	case top == "":
		return name
	case name == ".":
		return top
	}
	return name + "::" + top
}
