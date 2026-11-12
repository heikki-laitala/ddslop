package codemap

import (
	"slices"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// Feature is a parallel-flow family: features running one pipeline
// through copied functions. Shape is the family's anchor, the member with
// the most verified pairs to the others, whose steps the members align to.
// It shows what the copies look like, never an example to copy: the way
// forward is extracting the pipeline.
type Feature struct {
	Shape   int
	Members []int
	Steps   []FeatureStep
}

// FeatureStep is a step of a feature's shape and the shared helpers it
// calls by name, in call order.
type FeatureStep struct {
	Unit    int
	Helpers []int
}

// Features describes the parallel-flow families with an entry point in
// the grouping (one crate's under --crate), in the order given. A family
// is shown whole, members and steps in other crates included: it is one
// check candidate, and extracting its pipeline takes every copy.
func Features(g *flow.Graph, flows []flow.ParallelFlow, group Grouping, helpers []Helper) []Feature {
	isHelper := map[int]bool{}
	for _, h := range helpers {
		isHelper[h.Unit] = true
	}
	var out []Feature
	for _, pf := range flows {
		if !slices.ContainsFunc(pf.Entries, func(e int) bool { return group(g.Units[e]) != "" }) {
			continue
		}
		f := Feature{Shape: pf.Entries[0], Members: pf.Entries[1:]}
		for _, s := range pf.Steps {
			step := FeatureStep{Unit: s.Units[0]}
			for k, c := range g.Callees[s.Units[0]] {
				if isHelper[c] && named(g, s.Units[0], k) {
					step.Helpers = append(step.Helpers, c)
				}
			}
			f.Steps = append(f.Steps, step)
		}
		out = append(out, f)
	}
	return out
}
