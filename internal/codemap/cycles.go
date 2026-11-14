package codemap

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// Cycle is a group of modules whose calls form a loop: each can reach
// every other through calls. Links are the pairs that call each other
// directly; a group with none shows one shortest Loop through it instead.
// Which side of a link is the wrong way is not for the map to say: that
// takes knowing what each module is for.
type Cycle struct {
	Modules []string
	Links   []Link
	Loop    []Hop
}

// Link is two modules of a cycle calling each other: the calls each way
// and an example call site each way.
type Link struct {
	A, B             string
	CallsAB, CallsBA int
	AB, BA           Site
}

// Hop is one step of a loop: a module calling the next.
type Hop struct {
	From, To string
	Site     Site
}

// Site is a call: the calling unit, the called one, and the call's index
// among the caller's calls (-1 when the graph does not say). For an edge
// no call makes, it is the Dependency that does instead.
type Site struct {
	Caller, Callee, Call int
	Dependency           *Dependency
}

// Dependency is a module depending on another without calling it: a path
// naming it, written in an item of a file at a line (see unit.Reference).
type Dependency struct {
	From, To   string
	File, Item string
	Line       int
	Ignored    bool
}

// Cycles finds the groups of modules in the grouping whose calls, and
// deps, form a loop, largest first, counting every call between modules:
// also the weak ones the layers set aside as upward breaks, as they close
// loops too. A site is a call where one makes the edge, else its first
// dependency.
func Cycles(g *flow.Graph, group Grouping, deps []Dependency) []Cycle {
	names, all := moduleGraph(g, group)
	names, all = withDependencies(names, all, deps)
	members := map[int][]int{}
	for m, c := range all.components() {
		members[c] = append(members[c], m)
	}
	// site returns the first call from module a into module b.
	site := func(a, b int) Site {
		for _, caller := range all[a][b].callers {
			for k, callee := range g.Callees[caller] {
				if group(g.Units[callee]) == names[b] && named(g, caller, k) {
					call := -1
					if caller < len(g.Targets) {
						call = slices.Index(g.Targets[caller], callee)
					}
					return Site{Caller: caller, Callee: callee, Call: call}
				}
			}
		}
		if e := all[a][b]; e != nil && len(e.deps) > 0 {
			return Site{Caller: -1, Callee: -1, Call: -1, Dependency: &deps[e.deps[0]]}
		}
		return Site{Caller: -1, Callee: -1, Call: -1}
	}
	var out []Cycle
	for _, ms := range members {
		if len(ms) < 2 {
			continue
		}
		slices.Sort(ms) // module names are sorted, so their indexes are too
		c := Cycle{}
		in := map[int]bool{}
		for _, m := range ms {
			c.Modules = append(c.Modules, names[m])
			in[m] = true
		}
		for i, a := range ms {
			for _, b := range ms[i+1:] {
				if all[a][b] != nil && all[b][a] != nil {
					c.Links = append(c.Links, Link{A: names[a], B: names[b],
						CallsAB: all[a][b].calls, CallsBA: all[b][a].calls, AB: site(a, b), BA: site(b, a)})
				}
			}
		}
		if len(c.Links) == 0 {
			for _, step := range all.shortestLoop(ms[0], in) {
				c.Loop = append(c.Loop, Hop{From: names[step[0]], To: names[step[1]], Site: site(step[0], step[1])})
			}
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y Cycle) int {
		return cmp.Or(cmp.Compare(len(y.Modules), len(x.Modules)), cmp.Compare(x.Modules[0], y.Modules[0]))
	})
	return out
}

// withDependencies adds deps to a module graph as edges, with the modules
// only they name: a module of constants and types calls nothing.
func withDependencies(names []string, es edges, deps []Dependency) ([]string, edges) {
	if len(deps) == 0 {
		return names, es
	}
	all := slices.Clone(names)
	for _, d := range deps {
		all = append(all, d.From, d.To)
	}
	all, index, out := newModuleGraph(all)
	for a, targets := range es {
		for b, e := range targets {
			out[index[names[a]]][index[names[b]]] = e
		}
	}
	for i, d := range deps {
		a, b := index[d.From], index[d.To]
		if a == b {
			continue
		}
		e := out[a][b]
		if e == nil {
			e = &edge{}
			out[a][b] = e
		}
		e.deps = append(e.deps, i)
	}
	return all, out
}

// shortestLoop returns the steps of a shortest loop from start back to it
// through the modules in, breadth first, targets in module order.
func (es edges) shortestLoop(start int, in map[int]bool) [][2]int {
	prev := map[int]int{}
	queue := []int{start}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		for _, t := range sortedTargets(es[m]) {
			if !in[t] {
				continue
			}
			if t == start {
				steps := [][2]int{{m, start}}
				for m != start {
					steps = append([][2]int{{prev[m], m}}, steps...)
					m = prev[m]
				}
				return steps
			}
			if _, seen := prev[t]; !seen {
				prev[t] = m
				queue = append(queue, t)
			}
		}
	}
	return nil
}
