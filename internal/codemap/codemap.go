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

// dependsOn reports whether a unit's k-th callee is a module dependency:
// a call reaches it by name or type (see named), not through a value of
// a trait type. Such a call depends on the trait, wherever its only
// implementation is: the implementation's module importing the trait
// closes no loop.
func dependsOn(g *flow.Graph, caller, k int) bool {
	if caller >= len(g.Dispatched) {
		return named(g, caller, k) // a graph without per-call targets
	}
	return directCall(g, caller, g.Callees[caller][k]) >= 0
}

// directCall returns the index among a unit's calls of the first that
// reaches callee by name or type and not through a trait, or -1.
func directCall(g *flow.Graph, caller, callee int) int {
	for k, target := range g.Targets[caller] {
		if target == callee && !g.Dispatched[caller][k] {
			return k
		}
	}
	return -1
}

// Grouping names the group a unit belongs to (its module or its crate),
// or "" to leave the unit out.
type Grouping func(u unit.Unit) string

// ByModule groups units by their module (see Module).
func ByModule(crates *Packages) Grouping { return byFile(Module, crates) }

// ByCrate groups Rust units by their crate (see Crate).
func ByCrate(crates *Packages) Grouping { return byFile(Crate, crates) }

// byFile groups units by a name given to their file.
func byFile(name func(file string, crates *Packages) string, crates *Packages) Grouping {
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
func InCrate(name string, crates *Packages) Grouping {
	return perFile(func(file string) string {
		if Crate(file, crates) != name {
			return ""
		}
		return Module(file, crates)
	})
}

// Crate returns the Rust crate a file belongs to, "." for a crate at the
// repository root, or "" for a file of another language.
func Crate(file string, crates *Packages) string {
	if !strings.HasSuffix(file, ".rs") {
		return ""
	}
	name, _ := rustModule(filepath.ToSlash(file), crates)
	return name
}

// Packages says where a workspace's Rust crates are. A nil *Packages is a
// tree with no manifest.
type Packages struct {
	// Names maps the directories holding a package's Cargo.toml to the
	// name code uses for its crate.
	Names map[string]string
	// Libraries are the libraries whose manifests move their root file
	// out of src/lib.rs.
	Libraries flow.Libraries
}

func (p *Packages) names() map[string]string {
	if p == nil {
		return nil
	}
	return p.Names
}

func (p *Packages) libraries() flow.Libraries {
	if p == nil {
		return flow.Libraries{}
	}
	return p.Libraries
}

// packageOf returns the nearest directory above a file that crates lists,
// "." for the repository root.
func packageOf(file string, crates *Packages) (string, bool) {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if _, ok := crates.names()[dir]; ok {
			return dir, true
		}
		if dir == "." || dir == "/" {
			return "", false
		}
	}
}

// Module returns the module a file belongs to: a Go or TypeScript file's
// directory, or a Rust file's crate with the module directly under it
// ("acme_ledger::store"). crates maps a crate directory to the name code
// uses for it; a directory missing there is named after itself, dashes as
// underscores.
func Module(file string, crates *Packages) string {
	file = filepath.ToSlash(file)
	if !strings.HasSuffix(file, ".rs") {
		return path.Dir(file)
	}
	return moduleName(rustModule(file, crates))
}

// moduleName names a Rust module as Module does: the crate, or the crate
// with its top module, which alone names it in a crate at the root.
func moduleName(name, top string) string {
	switch {
	case name == "":
		return "" // in no package
	case top == "":
		return name
	case name == ".":
		return top
	}
	return name + "::" + top
}

// rustModule returns a Rust file's crate name and the module directly
// under the crate's src/ ("" for the crate's root module).
func rustModule(file string, crates *Packages) (name, top string) {
	name, rel := CratePath(file, crates)
	top, _, nested := strings.Cut(rel, "/")
	if !nested {
		top = strings.TrimSuffix(top, ".rs")
		if top == "lib" || top == "main" || top == "mod" {
			top = ""
		}
	}
	return name, top
}

// CratePath returns a Rust file's crate name and its path under the
// crate's src/ ("" outside it), or under the directory of the root file
// a manifest moves its library to, that root file being lib.rs. crates
// lists the directories holding a package's Cargo.toml: a file belongs to
// the nearest one above it, as Cargo has it, and one outside src/
// (build.rs, examples/) to that crate's root module. A file in no package, once there are packages, belongs to
// no crate (both results ""), such as a script beside a virtual
// workspace's members. Without any manifest, the crate directory is the
// one call resolution uses.
func CratePath(file string, crates *Packages) (name, rel string) {
	file = filepath.ToSlash(file)
	dir := flow.CrateOf(file)
	if crate, inside, root, ok := crates.libraries().Locate(file); ok {
		dir, rel = crate, inside
		if root {
			rel = "lib.rs" // the library's root, as src/lib.rs is by default
		}
	} else if crates.libraries().MovedPackage(file) != "" {
		return "", "" // another target of a package whose library moved
	} else if root, ok := packageOf(file, crates); ok {
		dir = root
		if after, ok := strings.CutPrefix(strings.TrimPrefix(file, root+"/"), "src/"); ok {
			rel = after
		}
	} else if len(crates.names()) > 0 {
		return "", ""
	} else if i := strings.Index(file, "/src/"); i >= 0 {
		rel = file[i+len("/src/"):]
	} else if after, ok := strings.CutPrefix(file, "src/"); ok {
		rel = after // a crate at the root
	}
	// The root crate stays unnamed: whether the crates map names it
	// depends on incidental files such as a root build.rs.
	var ok bool
	name, ok = crates.names()[dir]
	if !ok || dir == "." {
		name = strings.ReplaceAll(path.Base(dir), "-", "_")
	}
	return name, rel
}

// CrateFacts are what resolving a crate's references needs to know of it:
// its modules, as Module names them, the functions and constants of its
// root module, and the names extern crate self gives the crate.
type CrateFacts struct {
	Modules, RootValues, SelfAliases map[string]bool
	// RootImports are the names the root module imports, each with the
	// path its use declaration names (pub use refusal::Refusal): a path
	// through the root to one of them reaches that path's module.
	RootImports map[string]string
}

// ReferencedModule returns the module of its crate a reference written in
// a Rust file names, as Module names it, or "" for none. super and self
// are read from where the path is written: the file's place in the crate
// and the inline module within it. A path naming an item of the root
// module rather than one of modules (the crate's modules, as Module names
// them), or a path ending at one of rootValues (the root module's
// functions and constants), names the root module. It is for a file of the library target,
// whose crate:: is the crate Module names (see the caller).
func ReferencedModule(file string, crates *Packages, ref unit.Reference, crate CrateFacts) string {
	name, rel := CratePath(file, crates)
	if name == "" {
		return ""
	}
	here := fileModules(rel)
	if ref.Module != "" {
		here = append(here, strings.Split(ref.Module, "::")...)
	}
	segments := strings.Split(ref.Path, "::")
	if ref.Relative {
		if !crate.SelfAliases[segments[0]] {
			return "" // another crate's path, or a module-relative one
		}
		segments[0] = "crate"
	}
	for i, s := range segments {
		segments[i] = strings.TrimPrefix(s, "r#") // mod r#type is type.rs
	}
	resolved, ok := flow.ResolvePath(strings.Join(segments, "::"), strings.Join(here, "::"))
	if !ok {
		return ""
	}
	var at []string
	if resolved != "" {
		at = strings.Split(resolved, "::")
	}
	// A name the root imports (crate::Refusal for its pub use
	// refusal::Refusal) is the imported module's, not the root's: the root
	// only passes it on.
	if len(at) > 0 && !ref.Macro {
		if target, ok := crate.RootImports[at[0]]; ok {
			return importedModule(name, target, crate)
		}
	}
	// A path ending at a root value's name (crate::foo for the root's fn
	// foo) names that value, though a module foo may exist beside it.
	rootValue := ref.Leaf && len(at) == 1 && (ref.Macro || crate.RootValues[at[0]])
	if len(at) > 0 && crate.Modules[moduleName(name, at[0])] && !rootValue {
		return moduleName(name, at[0])
	}
	return moduleName(name, "")
}

// importedModule returns the module of crate a root import's path names
// (refusal::Refusal or crate::refusal::Refusal names refusal), or "" when
// the path leads out of the crate.
func importedModule(crate, path string, facts CrateFacts) string {
	segments := strings.Split(path, "::")
	for len(segments) > 0 && (segments[0] == "crate" || segments[0] == "self") {
		segments = segments[1:]
	}
	if len(segments) > 0 && facts.Modules[moduleName(crate, strings.TrimPrefix(segments[0], "r#"))] {
		return moduleName(crate, strings.TrimPrefix(segments[0], "r#"))
	}
	return ""
}

// fileModules returns the module path of a file under src/: store/client
// for store/client.rs, store for store/mod.rs, none for lib.rs.
func fileModules(rel string) []string {
	if rel == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(rel, ".rs"), "/")
	switch parts[len(parts)-1] {
	case "lib", "main", "mod":
		parts = parts[:len(parts)-1]
	}
	return parts
}
