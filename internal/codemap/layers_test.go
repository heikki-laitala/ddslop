package codemap

import (
	"fmt"
	"reflect"
	"testing"

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
	m := Layers(graphOf(call{"handlers", "service", 40}, call{"service", "store", 40}), ByModule(nil))
	want := [][]string{{"handlers"}, {"service"}, {"store"}}
	if !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
	if len(m.Breaks) != 0 {
		t.Errorf("breaks = %+v, want none", m.Breaks)
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

func TestAWeakCallClosingACycleIsAnUpwardCall(t *testing.T) {
	m := Layers(graphOf(call{"a", "b", 40}, call{"b", "c", 40}, call{"c", "a", 1}), ByModule(nil))
	want := [][]string{{"a"}, {"b"}, {"c"}}
	if !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
	if len(m.Breaks) != 1 || m.Breaks[0].Kind != Upward || m.Breaks[0].From != "c" || m.Breaks[0].To != "a" || m.Breaks[0].Calls != 1 {
		t.Errorf("breaks = %+v, want the call from c up to a", m.Breaks)
	}
}

func TestTheLighterDirectionBetweenTwoModulesIsAnUpwardCall(t *testing.T) {
	m := Layers(graphOf(call{"api", "db", 30}, call{"db", "api", 2}), ByModule(nil))
	if want := [][]string{{"api"}, {"db"}}; !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
	if len(m.Breaks) != 1 || m.Breaks[0].Kind != Upward || m.Breaks[0].From != "db" {
		t.Errorf("breaks = %+v, want db's call up to api", m.Breaks)
	}
}

func TestABalancedCycleIsOneLayerOfPeers(t *testing.T) {
	m := Layers(graphOf(call{"a", "b", 5}, call{"b", "c", 5}, call{"c", "a", 5}, call{"c", "z", 5}), ByModule(nil))
	want := [][]string{{"a", "b", "c"}, {"z"}}
	if !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
	if len(m.Breaks) != 0 {
		t.Errorf("breaks = %+v, want none: no direction dominates", m.Breaks)
	}
}

func TestEveryPartOfTheGraphGetsLayers(t *testing.T) {
	m := Layers(graphOf(call{"handlers", "store", 10}, call{"worker", "jobs", 10}, call{"jobs", "queue", 10}), ByModule(nil))
	want := [][]string{{"handlers", "worker"}, {"jobs", "store"}, {"queue"}}
	if !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
}

func TestCallsInsideAModuleShapeNoLayers(t *testing.T) {
	m := Layers(graphOf(call{"solo", "solo", 10}), ByModule(nil))
	if len(m.Layers) != 0 || !reflect.DeepEqual(m.Unplaced, []string{"solo"}) {
		t.Errorf("layers = %v, unplaced = %v; want no layers and solo unplaced", m.Layers, m.Unplaced)
	}
}

func TestASetAsideCallThatEndsUpGoingDownIsNoBreak(t *testing.T) {
	// a->b is weak on a->b->c->a and b->c on b->c->d->b; with both set
	// aside c is on top, so a (layer 1) calling b (layer 2) goes down.
	m := Layers(graphOf(call{"a", "b", 1}, call{"b", "c", 10}, call{"c", "a", 40}, call{"c", "d", 100}, call{"d", "b", 100}), ByModule(nil))
	want := [][]string{{"c"}, {"a", "d"}, {"b"}}
	if !reflect.DeepEqual(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
	if len(m.Breaks) != 1 || m.Breaks[0].Kind != Upward || m.Breaks[0].From != "b" || m.Breaks[0].To != "c" {
		t.Errorf("breaks = %+v, want only b's call up to c", m.Breaks)
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
