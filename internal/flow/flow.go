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
	// Calls and Resolved count all call sites and the resolved ones.
	Calls, Resolved int
}

// index finds units by name.
type index struct {
	free    map[string][]int // free functions by name
	methods map[string][]int // methods by method name
	owned   map[string][]int // methods by "Owner::method", owner simplified
}

// Build resolves every unit's calls.
func Build(units []unit.Unit) *Graph {
	ix := index{free: map[string][]int{}, methods: map[string][]int{}, owned: map[string][]int{}}
	for i, u := range units {
		owner, name := split(u.Name)
		if owner == "" {
			ix.free[name] = append(ix.free[name], i)
			continue
		}
		ix.methods[name] = append(ix.methods[name], i)
		ix.owned[owner+"::"+name] = append(ix.owned[owner+"::"+name], i)
	}

	g := &Graph{Units: units, Callees: make([][]int, len(units))}
	for i, u := range units {
		owner, _ := split(u.Name)
		for _, c := range u.Calls {
			g.Calls++
			callee := ix.resolve(units, u, owner, c)
			if callee < 0 {
				continue
			}
			g.Resolved++
			if !slices.Contains(g.Callees[i], callee) {
				g.Callees[i] = append(g.Callees[i], callee)
			}
		}
	}
	return g
}

// resolve returns the unit a call refers to, or -1.
func (ix index) resolve(units []unit.Unit, caller unit.Unit, owner string, c unit.Call) int {
	switch c.Kind {
	case unit.FreeCall:
		return pick(units, caller, ix.free[c.Name])
	case unit.PathCall:
		typ := unit.SimpleName(c.Qualifier)
		if typ == "Self" {
			typ = owner
		}
		if cands := ix.owned[typ+"::"+c.Name]; len(cands) > 0 {
			return pick(units, caller, cands)
		}
		if isModule(typ) {
			return pick(units, caller, ix.free[c.Name]) // utils::log, crate::x::f
		}
		return -1 // a type defined elsewhere, such as Utc::now
	case unit.MethodCall:
		if c.Qualifier == "self" && owner != "" {
			return pick(units, caller, ix.owned[owner+"::"+c.Name])
		}
		if stdMethods[c.Name] {
			return -1 // most likely a standard-library method
		}
		return pick(units, caller, ix.methods[c.Name])
	}
	return -1
}

// pick chooses among candidates the one in the caller's file, else the
// only one in the caller's crate, else the only one at all; -1 when the
// best tier is empty everywhere or ambiguous.
func pick(units []unit.Unit, caller unit.Unit, cands []int) int {
	crate := crateOf(caller.File)
	for _, same := range []func(u unit.Unit) bool{
		func(u unit.Unit) bool { return u.File == caller.File },
		func(u unit.Unit) bool { return crateOf(u.File) == crate },
		func(unit.Unit) bool { return true },
	} {
		found := -1
		for _, c := range cands {
			if !same(units[c]) {
				continue
			}
			if found >= 0 {
				return -1 // ambiguous at the best tier with candidates
			}
			found = c
		}
		if found >= 0 {
			return found
		}
	}
	return -1
}

// isModule reports whether a path segment names a module rather than a
// type: Rust modules are lower case, types upper case.
func isModule(segment string) bool {
	return segment != "" && !unicode.IsUpper(rune(segment[0]))
}

// crateOf returns the crate directory of a file: the path before /src/,
// or the file's directory.
func crateOf(file string) string {
	file = filepath.ToSlash(file)
	if i := strings.Index(file, "/src/"); i >= 0 {
		return file[:i]
	}
	return filepath.Dir(file)
}

// split separates a unit name into its owner (simplified) and name.
func split(name string) (owner, short string) {
	i := strings.LastIndex(name, "::")
	if i < 0 {
		return "", name
	}
	return unit.SimpleName(name[:i]), name[i+2:]
}

// Entries returns the non-test units implementing one of the traits.
func (g *Graph) Entries(traits map[string]bool) []int {
	var out []int
	for i, u := range g.Units {
		if !u.Test && traits[u.Trait] {
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

	pairs := map[[2]int]pairing{}
	var edges []clone.Edge
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			p := g.matchSteps(sliceOf[i], sliceOf[j], comparable, similar)
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

// similarPairs returns the similarity of every pair of comparable units at
// least minSim similar, keyed both ways. Candidates come from MinHash LSH.
func (g *Graph) similarPairs(comparable map[int]bool, minSim float64) map[[2]int]float64 {
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
	out := map[[2]int]float64{}
	for _, c := range similarity.Candidates(sigs) {
		a, b := ids[c[0]], ids[c[1]]
		if s, ok := similarity.Similar(g.Units[a].Seq, g.Units[b].Seq, minSim); ok {
			out[[2]int{a, b}], out[[2]int{b, a}] = s, s
		}
	}
	return out
}

// matchSteps matches the own comparable steps of two slices greedily, most
// similar first.
func (g *Graph) matchSteps(a, b []int, comparable map[int]bool, similar map[[2]int]float64) pairing {
	inA, inB := map[int]bool{}, map[int]bool{}
	for _, u := range a {
		inA[u] = true
	}
	for _, u := range b {
		inB[u] = true
	}
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
	bOwn, bNodes := own(b, inA)

	type cand struct {
		a, b int
		sim  float64
	}
	var cands []cand
	for _, x := range aOwn {
		for _, y := range bOwn {
			if s, ok := similar[[2]int{x, y}]; ok {
				cands = append(cands, cand{x, y, s})
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
