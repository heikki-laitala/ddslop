package simplify

import (
	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Wrapper is a function that only passes its parameters on to another.
type Wrapper struct {
	Unit unit.Unit
	// Target is the function the wrapper calls.
	Target unit.Unit
}

// PassThrough returns the graph's units that only forward their
// parameters (see unit.Unit.Forwards) to another unit of the graph, in
// graph order. A wrapper around a library function is left out: it gives
// the call a name in the code's own terms. So are trait methods, which the
// trait requires, functions called from outside the source, and calls the
// graph resolves, by name, back to the wrapper itself (super::f from f).
func PassThrough(g *flow.Graph) []Wrapper {
	var out []Wrapper
	for i, u := range g.Units {
		if u.Forwards == "" || u.ViaInterface() || u.External || u.Test || u.Ignored {
			continue
		}
		if callees := g.Callees[i]; len(callees) == 1 && callees[0] != i {
			out = append(out, Wrapper{Unit: u, Target: g.Units[callees[0]]})
		}
	}
	return out
}
