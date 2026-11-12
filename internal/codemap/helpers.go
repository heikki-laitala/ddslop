package codemap

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// minHelperModules is the fewest other modules that make a function a
// shared helper: one calling module is a dependency.
const minHelperModules = 2

// Helper is a function other modules share: the unit and the modules,
// other than its own, calling it, sorted.
type Helper struct {
	Unit    int
	Modules []string
}

// Helpers returns the functions called from the most other modules, most
// first. Callers count once per module, so one busy module does not make a
// helper. Entry points are features, not helpers, and pass-through
// wrappers are already check candidates: neither is listed.
func Helpers(g *flow.Graph, group Grouping, entries []int) []Helper {
	moduleOf := make([]string, len(g.Units))
	for i, u := range g.Units {
		moduleOf[i] = group(u)
	}
	callers := make([][]string, len(g.Units))
	for caller, callees := range g.Callees {
		for k, callee := range callees {
			if m := moduleOf[caller]; m != "" && moduleOf[callee] != "" && m != moduleOf[callee] && named(g, caller, k) {
				callers[callee] = append(callers[callee], m)
			}
		}
	}
	var out []Helper
	for u, ms := range callers {
		if g.Units[u].Forwards != "" || slices.Contains(entries, u) {
			continue
		}
		slices.Sort(ms)
		ms = slices.Compact(ms)
		if len(ms) >= minHelperModules {
			out = append(out, Helper{Unit: u, Modules: ms})
		}
	}
	slices.SortStableFunc(out, func(x, y Helper) int {
		a, b := g.Units[x.Unit], g.Units[y.Unit]
		return cmp.Or(cmp.Compare(len(y.Modules), len(x.Modules)), cmp.Compare(a.File, b.File), cmp.Compare(a.StartLine, b.StartLine))
	})
	return out
}
