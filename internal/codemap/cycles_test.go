package codemap

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// describe renders cycles as text: each group's modules, then its two-way
// links (calls each way, an example call site each way), or the loop it
// shows when it has none.
func describe(g *flow.Graph, cs []Cycle) string {
	site := func(s Site) string { return g.Units[s.Caller].Name + " -> " + g.Units[s.Callee].Name }
	var out []string
	for _, c := range cs {
		out = append(out, strings.Join(c.Modules, " "))
		for _, l := range c.Links {
			out = append(out, fmt.Sprintf("  %s <-> %s %d/%d %s / %s", l.A, l.B, l.CallsAB, l.CallsBA, site(l.AB), site(l.BA)))
		}
		for _, h := range c.Loop {
			out = append(out, fmt.Sprintf("  %s -> %s %s", h.From, h.To, site(h.Site)))
		}
	}
	return strings.Join(out, "\n")
}

func TestCyclesListTwoWayLinksOrALoop(t *testing.T) {
	g := graphOf(
		call{"config", "store", 4}, call{"store", "config", 3},
		call{"a", "b", 2}, call{"b", "c", 1}, call{"c", "a", 5},
		call{"handlers", "service", 40},
	)
	got := describe(g, Cycles(g, ByModule(nil), nil))
	want := strings.Join([]string{
		"a b c",
		"  a -> b f14 -> f15",
		"  b -> c f18 -> f19",
		"  c -> a f20 -> f21",
		"config store",
		"  config <-> store 4/3 f0 -> f1 / f8 -> f9",
	}, "\n")
	if got != want {
		t.Errorf("cycles:\n%s\nwant:\n%s", got, want)
	}
}

// A weak call back up, which the layers set aside as an upward break, is
// still part of the cycle.
func TestCyclesIncludeWeakCalls(t *testing.T) {
	g := graphOf(call{"service", "store", 40}, call{"store", "service", 1})
	got := describe(g, Cycles(g, ByModule(nil), nil))
	if !strings.HasPrefix(got, "service store\n  service <-> store 40/1") {
		t.Errorf("cycles:\n%s\nwant service and store with 40 calls one way and 1 back", got)
	}
}
