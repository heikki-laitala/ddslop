package codemap

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// call is calls from functions of one Go package to another: n distinct
// caller and callee pairs.
type call struct {
	from, to string
	n        int
}

// graphOf builds a call graph with the given calls between packages. Each
// call adds n callers in from, each calling its own function in to.
func graphOf(calls ...call) *flow.Graph {
	g := &flow.Graph{}
	add := func(pkg string) int {
		g.Units = append(g.Units, unit.Unit{File: pkg + "/f.go", Name: fmt.Sprintf("f%d", len(g.Units)), StartLine: len(g.Units) + 1})
		g.Callees = append(g.Callees, nil)
		return len(g.Units) - 1
	}
	for _, c := range calls {
		for range c.n {
			caller, callee := add(c.from), add(c.to)
			g.Callees[caller] = []int{callee}
		}
	}
	return g
}

func TestLayersFollowTheCallDirection(t *testing.T) {
	for name, tc := range map[string]struct {
		calls  []call
		layers [][]string
		breaks []string
	}{
		"calls go down": {
			calls:  []call{{"handlers", "service", 40}, {"service", "store", 40}},
			layers: [][]string{{"handlers"}, {"service"}, {"store"}},
		},
		"a weak call closing a cycle is an upward call": {
			calls:  []call{{"a", "b", 40}, {"b", "c", 40}, {"c", "a", 1}},
			layers: [][]string{{"a"}, {"b"}, {"c"}},
			breaks: []string{"upward c->a 1"},
		},
		"the lighter direction between two modules is an upward call": {
			calls:  []call{{"api", "db", 30}, {"db", "api", 2}},
			layers: [][]string{{"api"}, {"db"}},
			breaks: []string{"upward db->api 2"},
		},
		// No direction dominates: no break.
		"a balanced cycle is one layer of peers": {
			calls:  []call{{"a", "b", 5}, {"b", "c", 5}, {"c", "a", 5}, {"c", "z", 5}},
			layers: [][]string{{"a", "b", "c"}, {"z"}},
		},
		"every part of the graph gets layers": {
			calls:  []call{{"handlers", "store", 10}, {"worker", "jobs", 10}, {"jobs", "queue", 10}},
			layers: [][]string{{"handlers", "worker"}, {"jobs", "store"}, {"queue"}},
		},
		// a->b is weak on a->b->c->a and b->c on b->c->d->b; with both set
		// aside c is on top, so a (layer 1) calling b (layer 2) goes down.
		"a set-aside call that ends up going down is no break": {
			calls:  []call{{"a", "b", 1}, {"b", "c", 10}, {"c", "a", 40}, {"c", "d", 100}, {"d", "b", 100}},
			layers: [][]string{{"c"}, {"a", "d"}, {"b"}},
			breaks: []string{"upward b->c 10"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := Layers(graphOf(tc.calls...), ByModule(nil))
			if !reflect.DeepEqual(m.Layers, tc.layers) {
				t.Errorf("layers = %v, want %v", m.Layers, tc.layers)
			}
			got := testutil.Format(m.Breaks, func(b Break) string {
				return fmt.Sprintf("%s %s->%s %d", b.Kind, b.From, b.To, b.Calls)
			})
			testutil.Lines(t, "breaks", got, tc.breaks)
		})
	}
}

func TestADirectCallPastAMiddleLayerIsABypass(t *testing.T) {
	m := Layers(graphOf(call{"handlers", "service", 40}, call{"service", "store", 40}, call{"handlers", "store", 3}), ByModule(nil))
	want := [][]string{{"handlers"}, {"service"}, {"store"}}
	if !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v: the direct call must not lift store", m.Layers, want)
	}
	if len(m.Breaks) != 1 {
		t.Fatalf("breaks = %+v, want one bypass", m.Breaks)
	}
	b := m.Breaks[0]
	if b.Kind != Bypass || b.From != "handlers" || b.To != "store" || b.Via != "service" || b.Calls != 3 || b.UsualCalls != 40 || len(b.Callers) != 3 {
		t.Errorf("break = %+v, want a bypass of service by 3 of handlers' calls, 40 usual", b)
	}
}

func TestCallsInsideAModuleShapeNoLayers(t *testing.T) {
	m := Layers(graphOf(call{"solo", "solo", 10}), ByModule(nil))
	if len(m.Layers) != 0 || !reflect.DeepEqual(m.Unplaced, []string{"solo"}) {
		t.Errorf("layers = %v, unplaced = %v; want no layers and solo unplaced", m.Layers, m.Unplaced)
	}
}

func TestAModuleCalledFromAboveTheSkippedLayerIsNoBypassTarget(t *testing.T) {
	// types is shared: worker, in the top layer, calls it directly too, so
	// handlers calling it directly skips nothing.
	m := Layers(graphOf(call{"handlers", "service", 40}, call{"service", "types", 40}, call{"handlers", "types", 3},
		call{"worker", "types", 10}), ByModule(nil))
	if len(m.Breaks) != 0 {
		t.Errorf("breaks = %+v, want none: types is not reached only through service", m.Breaks)
	}
}

func TestCallsFoundOnlyByMethodNameShapeNoLayers(t *testing.T) {
	g := graphOf(call{"handlers", "service", 10}, call{"service", "handlers", 10})
	g.ByName = make([][]bool, len(g.Units))
	for i := range g.Callees {
		for range g.Callees[i] {
			// service's calls back are x.m() guesses: only handlers -> service counts.
			g.ByName[i] = append(g.ByName[i], Module(g.Units[i].File, nil) == "service")
		}
	}
	m := Layers(g, ByModule(nil))
	if want := [][]string{{"handlers"}, {"service"}}; !reflect.DeepEqual(m.Layers, want) || len(m.Breaks) != 0 {
		t.Errorf("layers = %v, breaks = %+v; want %v and no breaks", m.Layers, m.Breaks, want)
	}
}

func TestModulesWithoutCallsToOrFromOthersAreUnplaced(t *testing.T) {
	g := graphOf(call{"handlers", "service", 10}, call{"types", "types", 3})
	m := Layers(g, ByModule(nil))
	if want := [][]string{{"handlers"}, {"service"}}; !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v: types has no evidence for a layer", m.Layers, want)
	}
	if want := []string{"types"}; !reflect.DeepEqual(m.Unplaced, want) {
		t.Errorf("unplaced = %v, want %v", m.Unplaced, want)
	}
}

func TestAModuleCalledFromBelowTheSkippedLayerIsNoBypassTarget(t *testing.T) {
	// types is a foundation: service and the lower index both call it, so
	// handlers calling it directly skips nothing.
	m := Layers(graphOf(call{"handlers", "service", 40}, call{"service", "types", 40}, call{"handlers", "types", 3},
		call{"service", "index", 40}, call{"index", "types", 10}), ByModule(nil))
	for _, b := range m.Breaks {
		if b.Kind == Bypass {
			t.Errorf("break %+v: types is reached from below service too", b)
		}
	}
}
