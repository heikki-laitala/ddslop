// Package flow builds an approximate call graph from units and follows it
// from entry points into feature slices.
//
// Calls are resolved by name, without type information, so resolution is
// conservative: when the best candidates are ambiguous the call stays
// unresolved rather than linking to an arbitrary function.
package flow

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Graph is an approximate call graph: calls resolved to units by name.
type Graph struct {
	Units []unit.Unit
	// Callees lists, per unit, the units its calls resolved to, in call
	// order without repeats.
	Callees [][]int
	// ByName marks, parallel to Callees, the callees every call reached
	// only by its method name: x.m() resolved because one method is named
	// m. Such a call may well reach a library's method of the same name.
	ByName [][]bool
	// ResolvedOf counts, per unit, its call sites that resolved.
	ResolvedOf []int
	// Targets holds, per unit and parallel to its calls, the unit each
	// call resolved to by name or type, or -1 (also for a call found only
	// by its method name, which may be another type's method).
	Targets [][]int
	// Calls and Resolved count all call sites and the resolved ones.
	Calls, Resolved int
}

// index finds units by name.
type index struct {
	free        map[string][]int                // free functions by name
	methods     map[string][]int                // methods by method name
	owned       map[string][]int                // methods by "Owner::method", owner simplified
	owners      map[string]bool                 // the types with methods, simplified
	impls       map[string][]string             // Rust trait, simplified -> the crate directory of each implementation
	rustModules map[string]map[string]bool      // Rust crate directory -> its modules' paths (foo::bar), from its files' paths
	crates      map[string]string               // workspace crate name -> crate directory
	imports     map[string]map[string]string    // file -> imported name -> path
	deps        map[string]map[string]bool      // crate directory -> workspace crates it imports from
	fields      map[string]map[string]FieldType // package or crate directory -> "Struct.field" -> its type
	modules     map[string]string               // go.mod directory -> module path
	libraries   Libraries                       // Rust libraries moved out of src/lib.rs
}

// FieldType is a struct field's type, as unit.Call.Type writes types,
// and where the struct is declared: its file and inline module (Rust), as
// a relative type path is read there.
type FieldType struct {
	Type, File, Module string
	Unwrapped          string // see unit.FieldType
}

// Inputs are what call resolution needs besides the units.
type Inputs struct {
	// Imports maps a file to the names it imports and their paths.
	Imports map[string]map[string]string
	// Crates maps a crate directory to the name code uses for the crate.
	// A directory missing here is named after itself, dashes as
	// underscores.
	Crates map[string]string
	// Fields maps a Go package or Rust crate directory to its structs'
	// field types, by "Struct.field".
	Fields map[string]map[string]FieldType
	// Modules maps the directory of each go.mod to its module path.
	Modules map[string]string
	// RustFiles are the Rust files analysed, functions or not: their
	// paths name their crates' modules.
	RustFiles []string
	// Impls are the Rust trait implementations of the code in the graph.
	Impls []unit.Impl
	// Libraries are the Rust libraries whose manifests move their root
	// file out of src/lib.rs.
	Libraries Libraries
}

// Build resolves every unit's calls. A call finds only functions of its
// own language: each language, told by file extension, has its own index.
func Build(in Inputs, units []unit.Unit) *Graph {
	langs := map[string]*index{}
	indexOf := func(file string) *index {
		ext := filepath.Ext(file)
		if langs[ext] == nil {
			langs[ext] = &index{free: map[string][]int{}, methods: map[string][]int{}, owned: map[string][]int{}, owners: map[string]bool{},
				impls: map[string][]string{}, rustModules: map[string]map[string]bool{},
				crates: map[string]string{}, imports: in.Imports, deps: map[string]map[string]bool{},
				fields: in.Fields, modules: in.Modules, libraries: in.Libraries}
		}
		return langs[ext]
	}
	for i, u := range units {
		ix := indexOf(u.File)
		dir := in.Libraries.CrateOf(u.File)
		name, ok := in.Crates[dir]
		if !ok {
			name = strings.ReplaceAll(filepath.Base(dir), "-", "_")
		}
		ix.crates[name] = dir
		if m, ok := in.Libraries.unitModule(u); ok {
			ix.addModules(m, dir)
		}
		owner, name := split(u.Name)
		if owner == "" {
			ix.free[name] = append(ix.free[name], i)
			continue
		}
		ix.methods[name] = append(ix.methods[name], i)
		ix.owned[owner+"::"+name] = append(ix.owned[owner+"::"+name], i)
		ix.owners[owner] = true
	}

	for _, im := range in.Impls {
		ix, crate := indexOf(im.File), in.Libraries.CrateOf(im.File)
		ix.impls[im.Trait] = append(ix.impls[im.Trait], crate)
		// impl api::Store for Disk, without a use: a dependency all the same.
		if dir, ok := ix.crates[firstSegment(im.TraitPath)]; ok && dir != crate && strings.Contains(im.TraitPath, "::") {
			if ix.deps[crate] == nil {
				ix.deps[crate] = map[string]bool{}
			}
			ix.deps[crate][dir] = true
		}
	}
	for _, file := range in.RustFiles {
		if m, ok := in.Libraries.ModuleOf(file); ok {
			indexOf(file).addModules(m, in.Libraries.CrateOf(file))
		}
	}
	for file, imported := range in.Imports {
		ix, crate := indexOf(file), in.Libraries.CrateOf(file)
		for _, path := range imported {
			if dir, ok := ix.crates[firstSegment(path)]; ok && dir != crate {
				if ix.deps[crate] == nil {
					ix.deps[crate] = map[string]bool{}
				}
				ix.deps[crate][dir] = true
			}
		}
	}

	g := &Graph{Units: units, Callees: make([][]int, len(units)), ByName: make([][]bool, len(units)),
		ResolvedOf: make([]int, len(units)), Targets: make([][]int, len(units))}
	for i, u := range units {
		owner, _ := split(u.Name)
		r := callResolver{ix: indexOf(u.File), units: units, caller: u, owner: owner,
			done: make([]resolved, len(u.Calls))}
		g.Targets[i] = make([]int, len(u.Calls))
		for k := range u.Calls {
			g.Calls++
			callee, byName := r.resolve(k)
			g.Targets[i][k] = callee
			if byName {
				g.Targets[i][k] = -1
			}
			if callee < 0 {
				continue
			}
			g.Resolved++
			g.ResolvedOf[i]++
			if k := slices.Index(g.Callees[i], callee); k >= 0 {
				g.ByName[i][k] = g.ByName[i][k] && byName
				continue
			}
			g.Callees[i] = append(g.Callees[i], callee)
			g.ByName[i] = append(g.ByName[i], byName)
		}
	}
	return g
}

// callResolver resolves one unit's calls, each once: a Go method call on
// another call's result (unit.Call.From) needs that call resolved first,
// and a chained call's operand is recorded after it.
type callResolver struct {
	ix     *index
	units  []unit.Unit
	caller unit.Unit
	owner  string
	done   []resolved
}

// resolved is a call's resolution: its callee (-1 for none), whether
// found by name alone, and the state (0 not yet, 1 under way, 2 done).
type resolved struct {
	callee, state int
	byName        bool
}

// resolve returns the unit call k refers to, or -1, and whether it was
// found by its method name alone.
func (r *callResolver) resolve(k int) (int, bool) {
	d := &r.done[k]
	switch d.state {
	case 2:
		return d.callee, d.byName
	case 1:
		return -1, false // a call whose operand is itself: malformed
	}
	d.state = 1
	d.callee, d.byName = r.resolveCall(r.caller.Calls[k])
	d.state = 2
	return d.callee, d.byName
}

// resolveCall resolves a call whose operand may be another call's result:
// typed by that result, read in the producing function's package. A
// producer found by its method name alone may be another type's method,
// so its result types nothing.
func (r *callResolver) resolveCall(c unit.Call) (int, bool) {
	if c.From > 0 && c.From <= len(r.done) {
		callee, guessed := r.resolve(c.From - 1)
		if adapter := r.caller.Calls[c.From-1]; (callee < 0 || guessed) && adapter.Adapts > 0 && (adapter.From > 0 || adapter.Field != "") {
			// A Rust result adapter the repository does not define
			// (anyhow's context, Result's unwrap): the value is still that
			// of the call or field it adapts, unwrapped by one that
			// unwraps; a field from self or a typed variable
			// (self.inner.unwrap()) starts where the adapter's does.
			through := c
			through.From, through.Result, through.Type = adapter.From, adapter.Result|c.Result, adapter.Type
			unwraps := adapter.Adapts == 2 || c.Result&1 == 1
			if adapter.Field != "" {
				// make().inner.unwrap().run(): the fields before the adapter,
				// the last unwrapped when the adapter or a ? unwraps it ("?"),
				// then any after it.
				through.Result = adapter.Result
				if unwraps {
					adapter.Field += "?"
				}
				through.Field = strings.Trim(adapter.Field+"."+c.Field, ".")
			} else if unwraps {
				through.Result |= 1
			}
			return r.resolveCall(through)
		}
		if callee >= 0 && !guessed && c.Result < len(r.units[callee].Results) {
			producer := r.units[callee]
			typ, home := producer.Results[c.Result], producer
			if c.Field != "" && strings.HasSuffix(producer.File, ".rs") {
				typ, home = r.ix.followFields(typ, home, c.Field) // make().inner.run()
			}
			if typ != "" {
				resolve := func() (int, bool) {
					if strings.HasSuffix(producer.File, ".rs") {
						return r.ix.resolveRust(r.units, r.caller, home, typ, c.Name)
					}
					return r.ix.resolveTyped(r.units, r.ix.libraries.CrateOf(producer.File), typ, c.Name)
				}
				if u, known := resolve(); known {
					return u, false
				}
			}
		}
	}
	return r.ix.resolve(r.units, r.caller, r.owner, c)
}

// resolve returns the unit a call refers to, or -1, and whether it was
// found by its method name alone (see Graph.ByName).
func (ix index) resolve(units []unit.Unit, caller unit.Unit, owner string, c unit.Call) (int, bool) {
	if c.Kind != unit.MethodCall || c.Qualifier == "self" {
		return ix.resolveNamed(units, caller, owner, c), false
	}
	typ, home := c.Type, caller
	switch {
	case c.Field == "":
	case strings.HasSuffix(caller.File, ".rs") && c.From > 0:
		typ = "" // a field of a call's result: followed in resolveCall, or not at all
	case strings.HasSuffix(caller.File, ".rs"):
		if typ == "" {
			typ = owner // self.ctx.db
		}
		typ, home = ix.followFields(typ, home, c.Field)
	default:
		typ = ix.fields[ix.libraries.CrateOf(caller.File)][owner+"."+c.Field].Type
	}
	switch {
	case typ == "":
	case strings.HasSuffix(caller.File, ".rs"):
		if u, done := ix.resolveRust(units, caller, home, typ, c.Name); done {
			return u, false
		}
	default:
		if u, known := ix.resolveTyped(units, ix.libraries.CrateOf(caller.File), typ, c.Name); known {
			return u, false
		}
	}
	if stdMethods[c.Name] || (strings.HasSuffix(caller.File, ".go") && goStdMethods[c.Name]) {
		return -1, false // most likely a standard-library method
	}
	return ix.pick(units, caller, ix.methods[c.Name]), true
}

// resolveRust resolves a Rust method call on a value of the type path
// typ, written where home is (the caller for a type it states, the
// producing function for a call's result): in the crate the path names,
// as Type::f is. A path rooted in another crate (reqwest::Client), or a
// type with no methods here (Vec, Option), is from outside the
// repository: its methods are none of ours (done, -1). A repository type
// without the method is not done: the caller guesses by name.
func (ix index) resolveRust(units []unit.Unit, caller, home unit.Unit, typ, method string) (int, bool) {
	typ, trait := strings.CutPrefix(typ, "dyn ")
	name := unit.SimpleName(typ)
	dir, inRepo := ix.crateOfPath(typ, home)
	if trait {
		if !inRepo {
			return -1, true
		}
		return ix.resolveTrait(units, name, dir, method)
	}
	if !ix.owners[name] || !inRepo {
		return -1, true
	}
	cands := slices.DeleteFunc(slices.Clone(ix.owned[name+"::"+method]), func(u int) bool { return ix.libraries.CrateOf(units[u].File) != dir })
	// Two types of one name in a crate: the module the path names (db::Db
	// read in server is server::db's; a bare name, home's own module;
	// b::foo::Db is crate b's foo's).
	module, named := "", true
	switch root := firstSegment(typ); {
	case dir == ix.libraries.CrateOf(home.File) || pathKeyword(root):
		module, _ = ix.libraries.unitModule(home)
		if i := strings.LastIndex(typ, "::"); i >= 0 {
			module = ix.libraries.modulePath(typ[:i], home)
		}
	case ix.crates[root] == dir:
		module = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(typ, name), root), "::")
		module = strings.TrimSuffix(module, "::")
	default:
		named = false
	}
	if named {
		if u := ix.libraries.inModule(units, cands, dir, module); u >= 0 {
			return u, true
		}
	}
	// The path fixed the crate, which a value's type may come from without
	// the caller depending on it (b::make_c() returns c's type): prefer
	// the caller's file, else take the crate's only candidate.
	u := ix.libraries.pickLocal(units, caller, cands)
	if u < 0 {
		u = only(units, cands, func(unit.Unit) bool { return true })
	}
	return u, u >= 0
}

// resolveTrait resolves a method call on a value of a Rust trait (dyn
// Store, impl Store, S: Store) declared in the crate dir: the method of
// the trait's only implementation, or its default when that
// implementation leaves it out. Its implementations are in its crate or
// in crates importing from it; another crate's trait of the name is
// another trait. With several implementations, which runs is not known:
// -1. A method neither the trait nor an implementation declares is
// another trait's, such as a supertrait's: not done, for the caller to
// guess by name.
func (ix index) resolveTrait(units []unit.Unit, trait, dir, method string) (int, bool) {
	sees := func(crate string) bool { return crate == dir || ix.deps[crate][dir] }
	impls := 0
	for _, crate := range ix.impls[trait] {
		if sees(crate) {
			impls++
		}
	}
	cands := slices.DeleteFunc(slices.Clone(ix.methods[method]), func(u int) bool {
		return units[u].Trait != trait || !sees(ix.libraries.CrateOf(units[u].File))
	})
	if len(cands) < impls {
		// An implementation leaves the method to the trait's default.
		cands = append(cands, slices.DeleteFunc(slices.Clone(ix.owned[trait+"::"+method]), func(u int) bool {
			return units[u].Trait != "" || ix.libraries.CrateOf(units[u].File) != dir
		})...)
	}
	switch len(cands) {
	case 0:
		return -1, false
	case 1:
		return cands[0], true
	}
	return -1, true
}

// followFields follows a Rust field path (ctx.db) from a type written
// where home is, through the struct declarations: each field's declared
// type, or what unwrapping leaves of it for a field marked "?" (inner?),
// read in its struct's crate. It returns the last field's type and a unit
// standing for where that type is written, or "" when a field's type is
// not known.
func (ix index) followFields(typ string, home unit.Unit, path string) (string, unit.Unit) {
	for _, field := range strings.Split(path, ".") {
		dir, ok := ix.crateOfPath(typ, home)
		if !ok {
			return "", home
		}
		field, unwrap := strings.CutSuffix(field, "?")
		next := ix.fields[dir][unit.SimpleName(typ)+"."+field]
		if unwrap {
			next.Type = next.Unwrapped
		}
		if next.Type == "" {
			return "", home
		}
		// Read the type where its struct is declared, as db::Db in
		// server.rs is server::db's; the crate's root when not known.
		where := unit.Unit{File: next.File, Module: next.Module}
		if where.File == "" {
			where.File = dir + "/src/lib.rs"
			if dir == "." {
				where.File = "src/lib.rs"
			}
		}
		typ, home = next.Type, where
	}
	return typ, home
}

// crateOfPath tells where a Rust type path's type is declared: the
// crate directory, and whether it is the repository's at all. The
// adapter qualifies imported names, so a bare name is the caller's
// crate's own (a glob import, use b::*, is not followed). A path rooted
// in crate, self, super or one of the crate's modules is the caller's
// crate's, one rooted in a workspace crate that crate's, and any other
// is outside.
func (ix index) crateOfPath(path string, caller unit.Unit) (string, bool) {
	if !strings.Contains(path, "::") {
		return ix.libraries.CrateOf(caller.File), true
	}
	root := firstSegment(path)
	if dir, ok := ix.crates[root]; ok && !pathKeyword(root) {
		return dir, true
	}
	// model::Client: a child module of the caller's, else another crate.
	if pathKeyword(root) || ix.isChildModule(caller, root) {
		return ix.libraries.CrateOf(caller.File), true
	}
	return "", false
}

// pathKeyword reports whether a Rust path's root is crate, self or super,
// which stay in the crate the path is written in.
func pathKeyword(root string) bool {
	return root == "crate" || root == "self" || root == "super"
}

// addModules records a module of a Rust crate and the modules its path
// passes through: model::client gives model and model::client.
func (ix index) addModules(module, crate string) {
	if ix.rustModules[crate] == nil {
		ix.rustModules[crate] = map[string]bool{}
	}
	for path := module; path != ""; {
		ix.rustModules[crate][path] = true
		i := strings.LastIndex(path, "::")
		if i < 0 {
			break
		}
		path = path[:i]
	}
}

// unitModule returns the module a Rust unit is in within its crate: its
// file's module, and the inline module it is declared in inside the file.
func (l Libraries) unitModule(u unit.Unit) (string, bool) {
	module, ok := l.ModuleOf(u.File)
	switch {
	case !ok || u.Module == "":
	case module == "":
		module = u.Module
	default:
		module += "::" + u.Module
	}
	return module, ok
}

// moduleOf returns the module path a Rust file is within its crate:
// src/foo/bar.rs and src/foo/bar/mod.rs are foo::bar, src/lib.rs and
// src/main.rs the crate's root (""). A file outside src/ is none.
func moduleOf(file string) (string, bool) {
	file = filepath.ToSlash(file)
	_, rest, ok := strings.Cut(file, "src/")
	if !strings.HasSuffix(file, ".rs") || !ok {
		return "", false
	}
	if rest == "lib.rs" || rest == "main.rs" {
		return "", true
	}
	return modulePathOf(rest), true
}

// modulePathOf returns the module path of a file's path within its
// crate's source directory: a/b.rs and a/b/mod.rs are a::b.
func modulePathOf(rel string) string {
	segments := strings.Split(strings.TrimSuffix(rel, ".rs"), "/")
	if segments[len(segments)-1] == "mod" {
		segments = segments[:len(segments)-1]
	}
	return strings.Join(segments, "::")
}

// modulePath returns the module a Rust path in the caller's crate names,
// from the module the caller is in: crate::a::b names a::b, self::x the
// file's module's x, super::x (super::super::x) its parent's
// (grandparent's), and a relative x its child x.
func (l Libraries) modulePath(path string, caller unit.Unit) string {
	here, _ := l.unitModule(caller)
	if resolved, ok := ResolvePath(path, here); ok {
		return resolved
	}
	return joinModule(here, path)
}

// ResolvePath returns the module path a Rust path rooted at crate, self or
// super names, read from the module here it is written in: crate::a::b is
// a::b, self::x is here's x, super::x (super::super::x) its parent's
// (grandparent's). ok is false for a path with no such root.
func ResolvePath(path, here string) (resolved string, ok bool) {
	root, rest, _ := strings.Cut(path, "::")
	switch root {
	case "crate":
		return rest, true
	case "super":
		for {
			here = parentOf(here, "::", "")
			next, more, found := strings.Cut(rest, "::")
			if next != "super" {
				break
			}
			rest = ""
			if found {
				rest = more
			}
		}
	case "self":
	default:
		return "", false
	}
	return joinModule(here, rest), true
}

// joinModule joins a module path and a path within it.
func joinModule(here, rest string) string {
	switch {
	case here == "":
		return rest
	case rest == "":
		return here
	}
	return here + "::" + rest
}

// parentOf returns the parent of a path whose parts sep separates, or none
// for a path of one part: a module's parent (crate::a for crate::a::b) or
// a file's directory.
func parentOf(path, sep, none string) string {
	if i := strings.LastIndex(path, sep); i >= 0 {
		return path[:i]
	}
	return none
}

// inModule returns the only candidate in the crate directory whose file
// is the module, or -1.
func (l Libraries) inModule(units []unit.Unit, cands []int, dir, module string) int {
	return only(units, cands, func(u unit.Unit) bool {
		m, ok := l.unitModule(u)
		return ok && l.CrateOf(u.File) == dir && m == module
	})
}

// isChildModule reports whether name is a child module of the module the
// caller is in: a relative path (bar::read) names one of those, or a
// crate.
func (ix index) isChildModule(caller unit.Unit, name string) bool {
	module, ok := ix.libraries.unitModule(caller)
	if !ok {
		return false
	}
	if module != "" {
		name = module + "::" + name
	}
	return ix.rustModules[ix.libraries.CrateOf(caller.File)][name]
}

// resolveTyped resolves a Go method call on an operand of type typ
// (written as unit.Call.Type, unqualified for a type of the package in
// directory home) to that type's method in its package. A
// type from outside the repository's modules has none here (known, -1);
// a type of the repository without the method, such as an interface, or
// of a package whose module is unknown, is unknown, and the caller
// guesses by name.
func (ix index) resolveTyped(units []unit.Unit, home, typ, method string) (int, bool) {
	dir, name := home, typ
	if i := strings.LastIndex(typ, "."); i >= 0 {
		var inRepo bool
		dir, inRepo = ix.packageDir(typ[:i])
		name = typ[i+1:]
		switch {
		case len(ix.modules) == 0:
			return -1, false
		case !inRepo:
			return -1, true
		}
	}
	u := only(units, ix.owned[name+"::"+method], func(u unit.Unit) bool { return ix.libraries.CrateOf(u.File) == dir })
	return u, u >= 0
}

// packageDir returns the directory of the repository's Go package with
// the import path, under the go.mod whose module path is the longest to
// start it, and whether one does.
func (ix index) packageDir(importPath string) (string, bool) {
	dir, module := "", ""
	for d, m := range ix.modules {
		if (importPath == m || strings.HasPrefix(importPath, m+"/")) && len(m) > len(module) {
			dir, module = d, m
		}
	}
	if module == "" {
		return "", false
	}
	return filepath.ToSlash(filepath.Join(dir, strings.TrimPrefix(importPath, module))), true
}

// resolveNamed resolves a call that names its callee: a free call, a
// path call, or a method call on self.
func (ix index) resolveNamed(units []unit.Unit, caller unit.Unit, owner string, c unit.Call) int {
	switch c.Kind {
	case unit.FreeCall:
		return ix.resolveFree(units, caller, c.Name)
	case unit.PathCall:
		typ := unit.SimpleName(c.Qualifier)
		if strings.HasSuffix(caller.File, ".rs") && strings.Contains(c.Qualifier, "::") && !isModule(typ) && typ != "Self" {
			// chrono::Utc::now: a type's path keeps to its crate, as a
			// typed method call does.
			u, _ := ix.resolveRust(units, caller, caller, c.Qualifier, c.Name)
			return u
		}
		if typ == "Self" {
			typ = owner
		}
		if cands := ix.owned[typ+"::"+c.Name]; len(cands) > 0 {
			return ix.pick(units, caller, cands)
		}
		if !isModule(typ) {
			return -1 // a type defined elsewhere, such as Utc::now
		}
		// A module path: another workspace crate's when it names one
		// (auth::decode), else one of the caller's own modules (utils::log,
		// crate::utils::log). Any other root is an outside crate's
		// (std::fs::read, serde_json::to_value), never a function of the
		// caller's crate that shares the name.
		// The Rust adapter writes an imported root as its import's path.
		root := firstSegment(c.Qualifier)
		dir, ok := ix.crates[root]
		switch {
		case ok && dir != ix.libraries.CrateOf(caller.File):
			// acme_auth::tokens::verify: the module after the crate's name.
			_, module, _ := strings.Cut(c.Qualifier, "::")
			if u := ix.libraries.inModule(units, ix.free[c.Name], dir, module); u >= 0 {
				return u
			}
			return only(units, ix.free[c.Name], func(u unit.Unit) bool { return ix.libraries.CrateOf(u.File) == dir })
		case strings.HasSuffix(caller.File, ".go"):
			return -1 // Go qualifies only with package names: an outside package
		case ok, pathKeyword(root), ix.isChildModule(caller, root):
			// The path names a module of the caller's crate: its function,
			// else (a re-export, say) the crate's only one of that name,
			// never the caller's own file's by preference.
			if u := ix.libraries.inModule(units, ix.free[c.Name], ix.libraries.CrateOf(caller.File), ix.libraries.modulePath(c.Qualifier, caller)); u >= 0 {
				return u
			}
			crate := ix.libraries.CrateOf(caller.File)
			return only(units, ix.free[c.Name], func(u unit.Unit) bool { return ix.libraries.CrateOf(u.File) == crate })
		}
		return -1
	case unit.MethodCall:
		if owner != "" {
			return ix.pick(units, caller, ix.owned[owner+"::"+c.Name])
		}
	}
	return -1
}

// resolveFree resolves a bare call foo(). A name the caller declares itself
// is a closure or function value, not a function. Otherwise the caller's
// file and crate come first; a function of another crate is reachable only
// through a use import, and only a workspace crate's is resolved.
func (ix index) resolveFree(units []unit.Unit, caller unit.Unit, name string) int {
	for _, l := range caller.Leaves {
		if l.Declares && l.Text == name {
			return -1
		}
	}
	if u := ix.libraries.pickLocal(units, caller, ix.free[name]); u >= 0 {
		return u
	}
	path, ok := ix.imports[caller.File][name]
	if !ok {
		return -1
	}
	target := path
	if i := strings.LastIndex(path, "::"); i >= 0 {
		target = path[i+2:] // the imported name, before any alias
	}
	root := firstSegment(path)
	if pathKeyword(root) {
		return ix.libraries.pickLocal(units, caller, ix.free[target])
	}
	dir, ok := ix.crates[root]
	if !ok {
		return -1 // an outside crate, such as tokio
	}
	return only(units, ix.free[target], func(u unit.Unit) bool { return ix.libraries.CrateOf(u.File) == dir })
}

// pick chooses among candidates the one in the caller's file, else the
// only one in the caller's crate, else the only one in a workspace crate
// the caller's crate imports from; -1 when the best tier is empty or
// ambiguous. Code cannot call into a crate it does not depend on.
func (ix index) pick(units []unit.Unit, caller unit.Unit, cands []int) int {
	if u := ix.libraries.pickLocal(units, caller, cands); u >= 0 {
		return u
	}
	deps := ix.deps[ix.libraries.CrateOf(caller.File)]
	return ix.libraries.onlyIn(units, cands, func(crate string) bool { return deps[crate] })
}

// pickLocal chooses the candidate in the caller's file, else the only one
// in the caller's crate, or -1.
func (l Libraries) pickLocal(units []unit.Unit, caller unit.Unit, cands []int) int {
	if u := only(units, cands, func(u unit.Unit) bool { return u.File == caller.File }); u >= 0 {
		return u
	}
	here := l.CrateOf(caller.File)
	return l.onlyIn(units, cands, func(crate string) bool { return crate == here })
}

// onlyIn returns the single candidate whose crate directory in accepts,
// or -1.
func (l Libraries) onlyIn(units []unit.Unit, cands []int, in func(crate string) bool) int {
	return only(units, cands, func(u unit.Unit) bool { return in(l.CrateOf(u.File)) })
}

// only returns the single candidate accepted by keep, or -1 when there is
// none or more than one.
func only(units []unit.Unit, cands []int, keep func(unit.Unit) bool) int {
	found := -1
	for _, c := range cands {
		if !keep(units[c]) {
			continue
		}
		if found >= 0 {
			return -1
		}
		found = c
	}
	return found
}

// firstSegment returns the part of a path before its first "::".
func firstSegment(path string) string {
	if i := strings.Index(path, "::"); i >= 0 {
		return path[:i]
	}
	return path
}

// isModule reports whether a path segment names a module rather than a
// type: Rust modules are lower case, types upper case.
func isModule(segment string) bool {
	return segment != "" && !unicode.IsUpper(rune(segment[0]))
}

// Libraries maps the root file of each Rust library whose manifest moves
// it out of src/lib.rs ([lib] path) to the crate directory holding the
// manifest. Files under the root file's directory are that library's
// modules, as files under src/ are a default library's.
type Libraries struct {
	// Roots maps each moved library's root file to its crate directory.
	Roots map[string]string
	// Packages are every package's directory: a moved library's files
	// never reach into another package.
	Packages map[string]bool
	// Targets are the files of the targets manifests place themselves
	// ([[bin]], [[example]], [[test]], [[bench]] paths), each mapped to its
	// target's root file: the root, the modules it declares (x.rs, and x/
	// for a directory, ending in /), and the directory of a main.rs root.
	Targets map[string]string
}

// CrateOf is the package CrateOf, knowing the libraries.
func (l Libraries) CrateOf(file string) string {
	if crate, _, _, ok := l.Locate(file); ok {
		return crate
	}
	if target := l.targetOf(filepath.ToSlash(file)); target != "" {
		return target // a target the manifest places: a crate of its own
	}
	if pkg := l.MovedPackage(file); pkg != "" {
		// another target of the package: a crate of its own, apart from
		// the library's (src for its binary, build.rs for its build script)
		if pkg == "." {
			first, _, _ := strings.Cut(filepath.ToSlash(file), "/")
			return first
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(filepath.ToSlash(file), pkg+"/"), "/")
		return pkg + "/" + first
	}
	return CrateOf(file)
}

// targetOf returns the root file of the target a manifest places that a
// file belongs to (see Targets), or "": the file itself, else the deepest
// directory holding it.
func (l Libraries) targetOf(file string) string {
	if root := l.Targets[file]; root != "" {
		return root
	}
	found, depth := "", -1
	for entry, root := range l.Targets {
		if strings.HasSuffix(entry, "/") && strings.HasPrefix(file, entry) && strings.Count(entry, "/") > depth {
			found, depth = root, strings.Count(entry, "/")
		}
	}
	return found
}

// MovedPackage returns the directory of the package a Rust file belongs
// to, its nearest, when that package's library is a moved one, or "".
func (l Libraries) MovedPackage(file string) string {
	file = filepath.ToSlash(file)
	if !strings.HasSuffix(file, ".rs") {
		return "" // a Go package keeps its directory
	}
	for d := parentOf(file, "/", "."); ; d = parentOf(d, "/", ".") {
		if l.Packages[d] {
			for _, crate := range l.Roots {
				if crate == d {
					return d
				}
			}
			return ""
		}
		if d == "." {
			return ""
		}
	}
}

// ModuleOf is moduleOf, knowing the libraries: a moved library's root
// file is its crate's root, and the files beside and below it are its
// modules, as files under src/ are a default library's.
func (l Libraries) ModuleOf(file string) (string, bool) {
	_, rel, root, ok := l.Locate(file)
	switch {
	case !ok:
		return moduleOf(file)
	case root:
		return "", true
	}
	return modulePathOf(rel), true
}

// owns reports whether a file under the directory of a moved library's
// root file, dir, is that library's: not in another package nested in its
// crate's, nor one of that package's other Cargo targets (a binary, a
// build script, an example, a test or a bench) outside the library's own
// directory. A library moved out of src/ leaves src/ to the package's
// binary.
func (l Libraries) owns(file, dir, crate string) bool {
	for d := parentOf(file, "/", "."); d != crate && d != "."; d = parentOf(d, "/", ".") {
		if l.Packages[d] {
			return false
		}
	}
	within := func(p string) string {
		if crate == "." {
			return p
		}
		return strings.TrimPrefix(p, crate+"/")
	}
	rel, home := within(file), within(dir)+"/"
	if rel == "build.rs" || rel == "src/main.rs" || l.targetOf(file) != "" {
		return false
	}
	// A target directory is another target's, unless the manifest roots
	// the library in it (autoexamples = false, path = "examples/lib.rs").
	for _, target := range []string{"src/bin/", "examples/", "benches/", "tests/"} {
		if strings.HasPrefix(rel, target) && !strings.HasPrefix(home, target) {
			return false
		}
	}
	inSrc := within(dir) == "src" || strings.HasPrefix(within(dir), "src/")
	return inSrc || !strings.HasPrefix(rel, "src/")
}

// Locate returns the moved library a Rust file belongs to, the one whose
// root file's directory is the deepest holding it: its crate directory,
// the file's path within that directory, and whether it is the root file.
func (l Libraries) Locate(file string) (crate, rel string, root, ok bool) {
	file = filepath.ToSlash(file)
	if !strings.HasSuffix(file, ".rs") {
		return "", "", false, false
	}
	depth := -1
	for r, c := range l.Roots {
		dir := parentOf(r, "/", ".")
		inside, found := file, dir == "."
		if !found {
			inside, found = strings.CutPrefix(file, dir+"/")
		}
		if found && strings.Count(dir, "/") > depth && (file == r || l.owns(file, dir, c)) {
			crate, rel, root, ok, depth = c, inside, file == r, true, strings.Count(dir, "/")
		}
	}
	return crate, rel, root, ok
}

// CrateOf returns the crate directory of a file: for Rust the path before
// /src/ ("." for a crate at the root), otherwise the file's directory (a Go
// package).
func CrateOf(file string) string {
	file = filepath.ToSlash(file)
	if !strings.HasSuffix(file, ".rs") {
		return filepath.Dir(file)
	}
	if i := strings.Index(file, "/src/"); i >= 0 {
		return file[:i]
	}
	if strings.HasPrefix(file, "src/") {
		return "."
	}
	return filepath.Dir(file)
}

// split separates a unit name into its owner (simplified) and name: Rust
// names methods Owner::name, Go Type.Name.
func split(name string) (owner, short string) {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return unit.SimpleName(name[:i]), name[i+2:]
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[:i], name[i+1:] // Go: Type.Method
	}
	return "", name
}

// Entries returns the non-test units implementing one of the traits (Rust),
// or methods named like one of them (Go implements interfaces implicitly,
// so traits holds a generated interface's method names).
func (g *Graph) Entries(traits map[string]bool) []int {
	var out []int
	for i, u := range g.Units {
		_, short := split(u.Name)
		if !u.Test && (traits[u.Trait] || (u.Dispatched && traits[short])) {
			out = append(out, i)
		}
	}
	return out
}

// Step is one function of a slice: the unit, its call depth from the entry
// (0 for the entry) and the unit that called it (-1 for the entry).
type Step struct {
	Unit, Depth, Caller int
}

// Slice follows calls from entry, breadth first, up to maxDepth, visiting
// each unit once and skipping test code.
func (g *Graph) Slice(entry, maxDepth int) []Step {
	steps := []Step{{Unit: entry, Depth: 0, Caller: -1}}
	seen := map[int]bool{entry: true}
	for i := 0; i < len(steps); i++ {
		s := steps[i]
		if s.Depth == maxDepth {
			continue
		}
		for _, c := range g.Callees[s.Unit] {
			if seen[c] || g.Units[c].Test {
				continue
			}
			seen[c] = true
			steps = append(steps, Step{Unit: c, Depth: s.Depth + 1, Caller: s.Unit})
		}
	}
	return steps
}

// ParallelOptions control how feature slices are compared.
type ParallelOptions struct {
	Depth          int     // slice depth from each entry point
	MinNodes       int     // steps smaller than this are ignored
	MinSteps       int     // least number of aligned steps for a parallel flow
	MinCover       float64 // least share of the features' own code aligned
	MinStepSim     float64 // least similarity of two aligned steps
	SharedFraction float64 // steps reached by more than this share of entries (and more than 2) are shared infrastructure
}

// ParallelFlow is a family of features that run the same pipeline through
// different functions: the anchor entry first, then the members.
type ParallelFlow struct {
	Entries []int
	// Steps aligns, per anchor step, the matching step of each member
	// (-1 when a member has none), in the anchor's slice order.
	Steps []AlignedStep
	// MatchedNodes is the code the members' aligned steps share with the
	// anchor's, summed over members.
	MatchedNodes int
	// Cover is the lowest share, over members, of the two features' own
	// code that is aligned.
	Cover float64
}

// AlignedStep is one step of a parallel flow: a unit per entry of the flow
// (-1 when missing) and each unit's similarity to the anchor's step.
type AlignedStep struct {
	Units      []int
	Similarity []float64
}

// Parallel finds families of features whose slices run the same pipeline
// through different functions. Each pair of features is compared on its
// own steps: steps both reach are reuse, not duplication, and steps most
// features reach are shared infrastructure, so neither counts. The own
// steps are matched greedily by similarity; a pair with at least MinSteps
// matched steps covering at least MinCover of its own code is a parallel
// pair, and parallel pairs are grouped into families around anchors.
func (g *Graph) Parallel(entries []int, opts ParallelOptions) []ParallelFlow {
	sliceOf := make([][]int, len(entries))
	reach := map[int]int{}
	for i, e := range entries {
		for _, s := range g.Slice(e, opts.Depth) {
			sliceOf[i] = append(sliceOf[i], s.Unit)
			reach[s.Unit]++
		}
	}
	comparable := map[int]bool{}
	for u, n := range reach {
		shared := n > 2 && float64(n) > opts.SharedFraction*float64(len(entries))
		if g.Units[u].Nodes >= opts.MinNodes && !shared {
			comparable[u] = true
		}
	}
	similar := g.similarPairs(comparable, opts.MinStepSim)
	sets := make([]map[int]bool, len(entries))
	for i, sl := range sliceOf {
		sets[i] = make(map[int]bool, len(sl))
		for _, u := range sl {
			sets[i][u] = true
		}
	}

	pairs := map[[2]int]pairing{}
	var edges []clone.Edge
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			p := g.matchSteps(sliceOf[i], sliceOf[j], sets[i], sets[j], comparable, similar)
			if len(p.steps) >= opts.MinSteps && p.cover >= opts.MinCover {
				pairs[[2]int{i, j}] = p
				edges = append(edges, clone.Edge{A: i, B: j, Weight: p.matched})
			}
		}
	}

	var out []ParallelFlow
	for _, fam := range clone.Cluster(len(entries), edges) {
		out = append(out, g.parallelFlow(entries, sliceOf, pairs, fam))
	}
	slices.SortFunc(out, func(x, y ParallelFlow) int {
		if c := cmp.Compare(y.MatchedNodes, x.MatchedNodes); c != 0 {
			return c
		}
		a, b := g.Units[x.Entries[0]], g.Units[y.Entries[0]]
		if c := cmp.Compare(a.File, b.File); c != 0 {
			return c
		}
		return cmp.Compare(a.StartLine, b.StartLine)
	})
	return out
}

// pairing is how two features' own steps match.
type pairing struct {
	steps   map[int]match // a's step -> b's matched step
	matched int           // nodes the matched steps share
	cover   float64       // share of both features' own code matched
}

// match is a step of one feature matched to a step of another.
type match struct {
	unit int
	sim  float64
}

// neighbour is a unit similar to another, with their similarity.
type neighbour struct {
	unit int
	sim  float64
}

// similarPairs returns, per comparable unit, the comparable units at least
// minSim similar to it, in unit order. Candidates come from MinHash LSH.
func (g *Graph) similarPairs(comparable map[int]bool, minSim float64) map[int][]neighbour {
	ids := make([]int, 0, len(comparable))
	for u := range comparable {
		ids = append(ids, u)
	}
	slices.Sort(ids)
	sigs := make([][]uint64, len(ids))
	for i, u := range ids {
		sigs[i] = g.Units[u].Signature
		if len(sigs[i]) == 0 {
			sigs[i] = similarity.Signature(g.Units[u].Seq)
		}
	}
	out := map[int][]neighbour{}
	for _, c := range similarity.Candidates(sigs) {
		a, b := ids[c[0]], ids[c[1]]
		if s, ok := similarity.Similar(g.Units[a].Seq, g.Units[b].Seq, minSim); ok {
			out[a] = append(out[a], neighbour{b, s})
			out[b] = append(out[b], neighbour{a, s})
		}
	}
	return out
}

// matchSteps matches the own comparable steps of two slices greedily, most
// similar first. inA and inB are the slices as sets.
func (g *Graph) matchSteps(a, b []int, inA, inB map[int]bool, comparable map[int]bool, similar map[int][]neighbour) pairing {
	own := func(units []int, other map[int]bool) ([]int, int) {
		var out []int
		nodes := 0
		for _, u := range units {
			if comparable[u] && !other[u] {
				out = append(out, u)
				nodes += g.Units[u].Nodes
			}
		}
		return out, nodes
	}
	aOwn, aNodes := own(a, inB)
	_, bNodes := own(b, inA)

	type cand struct {
		a, b int
		sim  float64
	}
	var cands []cand
	for _, x := range aOwn {
		for _, nb := range similar[x] {
			if inB[nb.unit] && !inA[nb.unit] {
				cands = append(cands, cand{x, nb.unit, nb.sim})
			}
		}
	}
	slices.SortFunc(cands, func(x, y cand) int {
		if c := cmp.Compare(y.sim, x.sim); c != 0 {
			return c
		}
		if c := cmp.Compare(x.a, y.a); c != 0 {
			return c
		}
		return cmp.Compare(x.b, y.b)
	})
	p := pairing{steps: map[int]match{}}
	usedB := map[int]bool{}
	for _, c := range cands {
		if _, done := p.steps[c.a]; done || usedB[c.b] {
			continue
		}
		p.steps[c.a] = match{c.b, c.sim}
		usedB[c.b] = true
		p.matched += min(g.Units[c.a].Nodes, g.Units[c.b].Nodes)
	}
	if aNodes+bNodes > 0 {
		p.cover = 2 * float64(p.matched) / float64(aNodes+bNodes)
	}
	return p
}

// parallelFlow builds a family's flow: the anchor's steps in slice order,
// each with the member steps matched to it.
func (g *Graph) parallelFlow(entries []int, sliceOf [][]int, pairs map[[2]int]pairing, fam clone.Family) ParallelFlow {
	f := ParallelFlow{Entries: []int{entries[fam.Anchor]}, Cover: 1}
	perMember := make([]map[int]match, len(fam.Members))
	for k, m := range fam.Members {
		f.Entries = append(f.Entries, entries[m])
		p := pairs[[2]int{min(fam.Anchor, m), max(fam.Anchor, m)}]
		perMember[k] = oriented(p.steps, fam.Anchor < m)
		f.MatchedNodes += p.matched
		f.Cover = min(f.Cover, p.cover)
	}
	for _, u := range sliceOf[fam.Anchor] {
		step := AlignedStep{Units: []int{u}, Similarity: []float64{1}}
		any := false
		for _, m := range perMember {
			mt, ok := m[u]
			if !ok {
				step.Units = append(step.Units, -1)
				step.Similarity = append(step.Similarity, 0)
				continue
			}
			any = true
			step.Units = append(step.Units, mt.unit)
			step.Similarity = append(step.Similarity, mt.sim)
		}
		if any {
			f.Steps = append(f.Steps, step)
		}
	}
	return f
}

// oriented returns a pair's matching from the anchor's steps to the
// member's; a pair's matching goes from its lower-indexed feature.
func oriented(steps map[int]match, anchorFirst bool) map[int]match {
	if anchorFirst {
		return steps
	}
	out := map[int]match{}
	for a, mt := range steps {
		out[mt.unit] = match{a, mt.sim}
	}
	return out
}
