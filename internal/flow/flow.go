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
	// Calls and Resolved count all call sites and the resolved ones.
	Calls, Resolved int
}

// index finds units by name.
type index struct {
	free    map[string][]int             // free functions by name
	methods map[string][]int             // methods by method name
	owned   map[string][]int             // methods by "Owner::method", owner simplified
	owners  map[string]bool              // the types with methods, simplified
	crates  map[string]string            // workspace crate name -> crate directory
	imports map[string]map[string]string // file -> imported name -> path
	deps    map[string]map[string]bool   // crate directory -> workspace crates it imports from
	fields  map[string]map[string]string // Go package directory -> "Struct.field" -> type
	modules map[string]string            // go.mod directory -> module path
}

// Inputs are what call resolution needs besides the units.
type Inputs struct {
	// Imports maps a file to the names it imports and their paths.
	Imports map[string]map[string]string
	// Crates maps a crate directory to the name code uses for the crate.
	// A directory missing here is named after itself, dashes as
	// underscores.
	Crates map[string]string
	// Fields maps a Go package directory to the types of its structs'
	// fields: "Server.store" to the type, written as unit.Call.Type.
	Fields map[string]map[string]string
	// Modules maps the directory of each go.mod to its module path.
	Modules map[string]string
}

// Build resolves every unit's calls. A call finds only functions of its
// own language: each language, told by file extension, has its own index.
func Build(in Inputs, units []unit.Unit) *Graph {
	langs := map[string]*index{}
	indexOf := func(file string) *index {
		ext := filepath.Ext(file)
		if langs[ext] == nil {
			langs[ext] = &index{free: map[string][]int{}, methods: map[string][]int{}, owned: map[string][]int{}, owners: map[string]bool{},
				crates: map[string]string{}, imports: in.Imports, deps: map[string]map[string]bool{},
				fields: in.Fields, modules: in.Modules}
		}
		return langs[ext]
	}
	for i, u := range units {
		ix := indexOf(u.File)
		dir := CrateOf(u.File)
		name, ok := in.Crates[dir]
		if !ok {
			name = strings.ReplaceAll(filepath.Base(dir), "-", "_")
		}
		ix.crates[name] = dir
		owner, name := split(u.Name)
		if owner == "" {
			ix.free[name] = append(ix.free[name], i)
			continue
		}
		ix.methods[name] = append(ix.methods[name], i)
		ix.owned[owner+"::"+name] = append(ix.owned[owner+"::"+name], i)
		ix.owners[owner] = true
	}

	for file, imported := range in.Imports {
		ix, crate := indexOf(file), CrateOf(file)
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
		ResolvedOf: make([]int, len(units))}
	for i, u := range units {
		owner, _ := split(u.Name)
		r := callResolver{ix: indexOf(u.File), units: units, caller: u, owner: owner,
			done: make([]resolved, len(u.Calls))}
		for k := range u.Calls {
			g.Calls++
			callee, byName := r.resolve(k)
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
		if callee, guessed := r.resolve(c.From - 1); callee >= 0 && !guessed && c.Result < len(r.units[callee].Results) {
			if typ := r.units[callee].Results[c.Result]; typ != "" {
				if u, known := r.ix.resolveTyped(r.units, CrateOf(r.units[callee].File), typ, c.Name); known {
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
	typ := c.Type
	if c.Field != "" {
		typ = ix.fields[CrateOf(caller.File)][owner+"."+c.Field]
	}
	switch {
	case typ == "":
	case strings.HasSuffix(caller.File, ".rs"):
		// A Rust type is a simple name, found as Type::f is: in the
		// caller's file, crate, or a workspace crate it imports from. A
		// type with no methods here (Vec, Option) is from outside the
		// repository: its methods are none of ours.
		if !ix.owners[typ] {
			return -1, false
		}
		if u := ix.pick(units, caller, ix.owned[typ+"::"+c.Name]); u >= 0 {
			return u, false
		}
	default:
		if u, known := ix.resolveTyped(units, CrateOf(caller.File), typ, c.Name); known {
			return u, false
		}
	}
	if stdMethods[c.Name] || (strings.HasSuffix(caller.File, ".go") && goStdMethods[c.Name]) {
		return -1, false // most likely a standard-library method
	}
	return ix.pick(units, caller, ix.methods[c.Name]), true
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
	u := only(units, ix.owned[name+"::"+method], func(u unit.Unit) bool { return CrateOf(u.File) == dir })
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
		// (token_credentials::decode), otherwise one of the caller's own
		// modules (utils::log). Outside crates (serde_json::to_value) find
		// no function of that name in the caller's crate.
		root := firstSegment(c.Qualifier)
		if dir, ok := ix.crates[root]; ok && dir != CrateOf(caller.File) {
			return only(units, ix.free[c.Name], func(u unit.Unit) bool { return CrateOf(u.File) == dir })
		}
		if strings.HasSuffix(caller.File, ".go") {
			return -1 // Go qualifies only with package names: an outside package
		}
		return pickLocal(units, caller, ix.free[c.Name])
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
	if u := pickLocal(units, caller, ix.free[name]); u >= 0 {
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
	switch root := firstSegment(path); root {
	case "crate", "self", "super":
		return pickLocal(units, caller, ix.free[target])
	default:
		dir, ok := ix.crates[root]
		if !ok {
			return -1 // an outside crate, such as tokio
		}
		return only(units, ix.free[target], func(u unit.Unit) bool { return CrateOf(u.File) == dir })
	}
}

// pick chooses among candidates the one in the caller's file, else the
// only one in the caller's crate, else the only one in a workspace crate
// the caller's crate imports from; -1 when the best tier is empty or
// ambiguous. Code cannot call into a crate it does not depend on.
func (ix index) pick(units []unit.Unit, caller unit.Unit, cands []int) int {
	if u := pickLocal(units, caller, cands); u >= 0 {
		return u
	}
	deps := ix.deps[CrateOf(caller.File)]
	return only(units, cands, func(u unit.Unit) bool { return deps[CrateOf(u.File)] })
}

// pickLocal chooses the candidate in the caller's file, else the only one
// in the caller's crate, or -1.
func pickLocal(units []unit.Unit, caller unit.Unit, cands []int) int {
	if u := only(units, cands, func(u unit.Unit) bool { return u.File == caller.File }); u >= 0 {
		return u
	}
	crate := CrateOf(caller.File)
	return only(units, cands, func(u unit.Unit) bool { return CrateOf(u.File) == crate })
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
