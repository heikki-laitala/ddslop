package codemap

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// weak is the largest share of the calls it is measured against that still
// makes an edge weak: a call closing a cycle is set aside when it carries
// less than this share of the weakest edge on the strongest cycle through
// it, and a direct call past a middle layer is a bypass when it carries
// less than this share of the calls through that layer. To be tuned by
// measurement.
const weak = 0.2

// BreakKind says how a call breaks the layers.
type BreakKind string

// Break kinds.
const (
	Upward BreakKind = "upward"
	Bypass BreakKind = "bypass"
)

// Break is calls from one module to another that go against the layers.
type Break struct {
	Kind     BreakKind
	From, To string
	// Via is the module a bypass skips.
	Via string
	// Calls counts the breaking caller and callee pairs. UsualCalls counts
	// the calls the break goes against: the weakest edge of the strongest
	// cycle an upward call closes, or the calls into the layer a bypass
	// skips.
	Calls, UsualCalls int
	// Callers are the units making the breaking calls, in unit order.
	Callers []int
}

// LayerMap is the layers a codebase's modules form and the calls breaking
// them.
type LayerMap struct {
	// Layers lists the modules of each layer, the top layer first, each
	// layer sorted.
	Layers [][]string
	Breaks []Break
}

// edge is the calls from one module to another: how many caller and callee
// pairs, and the callers.
type edge struct {
	calls   int
	callers []int
}

// edges are a module graph's edges: per module, its edges by target.
type edges []map[int]*edge

// Layers works out the layers of the graph's modules from the dominant
// direction of their calls. In each cycle, an edge is set aside when it is
// weak next to the strongest cycle through it; what is still cyclic after
// that are peers, collapsed into one layer. Each module's layer is then its
// longest-path depth from the modules nothing calls. A set-aside edge
// whose caller ends up below its callee is an upward call (with
// overlapping cycles, one can end up going down, and is no break); an
// edge skipping a layer that the caller's module mostly
// reaches through a module in between is a bypass.
func Layers(g *flow.Graph, crates map[string]string) LayerMap {
	names, all := moduleGraph(g, crates)
	n := len(names)

	// Edges are judged against the whole graph, so the order they are
	// looked at does not matter.
	var out LayerMap
	var setAside []Break // upward calls if they still go up once the layers are known
	positions := map[string]int{}
	kept := make(edges, n)
	for a := range n {
		kept[a] = map[int]*edge{}
		positions[names[a]] = a
	}
	for b := range n {
		back := all.widestFrom(b) // back[a]: the strongest path from b to a
		for a := range n {
			e := all[a][b]
			if e == nil {
				continue
			}
			if back[a] > 0 && float64(e.calls) < weak*float64(back[a]) {
				setAside = append(setAside, Break{Kind: Upward, From: names[a], To: names[b],
					Calls: e.calls, UsualCalls: back[a], Callers: e.callers})
				continue
			}
			kept[a][b] = e
		}
	}

	comp := kept.components()
	depth := kept.depths(comp)
	for m := range n {
		d := depth[comp[m]]
		for len(out.Layers) <= d {
			out.Layers = append(out.Layers, nil)
		}
		out.Layers[d] = append(out.Layers[d], names[m]) // names are sorted
	}

	layer := func(m int) int { return depth[comp[m]] }
	for _, b := range setAside {
		if layer(positions[b.From]) > layer(positions[b.To]) {
			out.Breaks = append(out.Breaks, b)
		}
	}
	for a := range n {
		for c, e := range kept[a] {
			if via, usual := kept.bypassed(a, c, layer); via >= 0 && float64(e.calls) < weak*float64(usual) {
				out.Breaks = append(out.Breaks, Break{Kind: Bypass, From: names[a], To: names[c], Via: names[via],
					Calls: e.calls, UsualCalls: usual, Callers: e.callers})
			}
		}
	}
	slices.SortFunc(out.Breaks, func(x, y Break) int {
		return cmp.Or(cmp.Compare(x.From, y.From), cmp.Compare(x.To, y.To), cmp.Compare(x.Kind, y.Kind))
	})
	return out
}

// bypassed returns the module an edge a->c would skip: of the modules in
// a layer between a's and c's that a calls and that call c, the one a calls
// most, with a's calls into it; -1 when there is none. Only a module
// otherwise reached from that layer or below can be skipped: one that
// modules above it call directly too (shared types, utilities) is used by
// everyone, not layered.
func (es edges) bypassed(a, c int, layer func(int) int) (via, usual int) {
	via = -1
	for _, v := range sortedTargets(es[a]) {
		if lv := layer(v); lv <= layer(a) || lv >= layer(c) || es[v][c] == nil {
			continue
		}
		if es[a][v].calls > usual {
			via, usual = v, es[a][v].calls
		}
	}
	if via < 0 {
		return -1, 0
	}
	for m, out := range es {
		if m != a && out[c] != nil && layer(m) < layer(via) {
			return -1, 0
		}
	}
	return via, usual
}

// moduleGraph counts the calls between the modules of the graph's units,
// returning the module names, sorted, and the edges between them. Calls
// inside a module say nothing about layers and are left out.
func moduleGraph(g *flow.Graph, crates map[string]string) ([]string, edges) {
	moduleOf := make([]string, len(g.Units))
	var names []string
	for i, u := range g.Units {
		moduleOf[i] = Module(u.File, crates)
		names = append(names, moduleOf[i])
	}
	slices.Sort(names)
	names = slices.Compact(names)
	index := make(map[string]int, len(names))
	for i, m := range names {
		index[m] = i
	}
	es := make(edges, len(names))
	for i := range es {
		es[i] = map[int]*edge{}
	}
	for caller, callees := range g.Callees {
		a := index[moduleOf[caller]]
		for _, callee := range callees {
			b := index[moduleOf[callee]]
			if a == b {
				continue
			}
			e := es[a][b]
			if e == nil {
				e = &edge{}
				es[a][b] = e
			}
			e.calls++
			if !slices.Contains(e.callers, caller) {
				e.callers = append(e.callers, caller)
			}
		}
	}
	return names, es
}

// widestFrom returns, per module, the largest bottleneck of a path from
// start to it: the most calls the path's weakest edge carries, 0 without a
// path. It is Dijkstra's algorithm with min in place of +; module graphs
// are small enough for the quadratic form.
func (es edges) widestFrom(start int) []int {
	best := make([]int, len(es))
	done := make([]bool, len(es))
	for v, e := range es[start] {
		best[v] = e.calls
	}
	for {
		u := -1
		for v := range best {
			if !done[v] && best[v] > 0 && (u < 0 || best[v] > best[u]) {
				u = v
			}
		}
		if u < 0 {
			return best
		}
		done[u] = true
		for v, e := range es[u] {
			if w := min(best[u], e.calls); w > best[v] {
				best[v] = w
			}
		}
	}
}

// components returns each module's strongly connected component, numbered
// from 0 (Tarjan's algorithm).
func (es edges) components() []int {
	n := len(es)
	comp, low, order := make([]int, n), make([]int, n), make([]int, n)
	onStack := make([]bool, n)
	var stack []int
	next, count := 1, 0
	var visit func(v int)
	visit = func(v int) {
		order[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range sortedTargets(es[v]) {
			switch {
			case order[w] == 0:
				visit(w)
				low[v] = min(low[v], low[w])
			case onStack[w]:
				low[v] = min(low[v], order[w])
			}
		}
		if low[v] != order[v] {
			return
		}
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp[w] = count
			if w == v {
				break
			}
		}
		count++
	}
	for v := range n {
		if order[v] == 0 {
			visit(v)
		}
	}
	return comp
}

// depths returns each component's longest-path depth from the components
// nothing calls, in the graph with each component collapsed into one node.
func (es edges) depths(comp []int) []int {
	count := 0
	for _, c := range comp {
		count = max(count, c+1)
	}
	succ := make([]map[int]bool, count)
	indeg := make([]int, count)
	for i := range succ {
		succ[i] = map[int]bool{}
	}
	for a, out := range es {
		for b := range out {
			ca, cb := comp[a], comp[b]
			if ca != cb && !succ[ca][cb] {
				succ[ca][cb] = true
				indeg[cb]++
			}
		}
	}
	depth := make([]int, count)
	var ready []int
	for c := range count {
		if indeg[c] == 0 {
			ready = append(ready, c)
		}
	}
	for len(ready) > 0 {
		c := ready[0]
		ready = ready[1:]
		for d := range succ[c] {
			depth[d] = max(depth[d], depth[c]+1)
			if indeg[d]--; indeg[d] == 0 {
				ready = append(ready, d)
			}
		}
	}
	return depth
}

// sortedTargets returns an edge map's targets in order, so the result does
// not depend on map iteration.
func sortedTargets(out map[int]*edge) []int {
	targets := make([]int, 0, len(out))
	for t := range out {
		targets = append(targets, t)
	}
	slices.Sort(targets)
	return targets
}
