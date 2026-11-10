// Package flow builds an approximate call graph from units and follows it
// from entry points into feature slices.
//
// Calls are resolved by name, without type information, so resolution is
// conservative: when the best candidates are ambiguous the call stays
// unresolved rather than linking to an arbitrary function.
package flow

import (
	"path/filepath"
	"slices"
	"strings"

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
		return pick(units, caller, ix.free[c.Name]) // a module path: utils::log
	case unit.MethodCall:
		if c.Qualifier == "self" && owner != "" {
			return pick(units, caller, ix.owned[owner+"::"+c.Name])
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
