package codemap

import (
	"reflect"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// helperGraph is a graph of named functions in Go packages: each callers
// entry lists who calls a function, as "pkg.name" strings.
func helperGraph(funcs []string, calls map[string][]string) (*flow.Graph, map[string]int) {
	g := &flow.Graph{}
	at := map[string]int{}
	for _, f := range funcs {
		pkg, name, _ := cut(f)
		at[f] = len(g.Units)
		g.Units = append(g.Units, unit.Unit{File: pkg + "/f.go", Name: name, StartLine: len(g.Units) + 1})
		g.Callees = append(g.Callees, nil)
	}
	for callee, callers := range calls {
		for _, c := range callers {
			g.Callees[at[c]] = append(g.Callees[at[c]], at[callee])
		}
	}
	return g, at
}

func cut(f string) (pkg, name string, ok bool) {
	for i := len(f) - 1; i >= 0; i-- {
		if f[i] == '.' {
			return f[:i], f[i+1:], true
		}
	}
	return "", f, false
}

func TestHelpersAreRankedByTheModulesCallingThem(t *testing.T) {
	g, at := helperGraph(
		[]string{"util.respond", "util.withTx", "util.local", "util.oneUser", "orders.create", "orders.list", "users.create", "billing.charge", "util.other"},
		map[string][]string{
			"util.respond": {"orders.create", "users.create", "billing.charge"},
			// Called twice from orders and once from users: modules count, not calls.
			"util.withTx":  {"orders.create", "orders.list", "users.create"},
			"util.local":   {"util.other"},    // only its own module
			"util.oneUser": {"orders.create"}, // one other module is a dependency, not a shared helper
		})
	got := Helpers(g, ByModule(nil), nil)
	want := []Helper{
		{Unit: at["util.respond"], Modules: []string{"billing", "orders", "users"}},
		{Unit: at["util.withTx"], Modules: []string{"orders", "users"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Helpers = %+v, want %+v", got, want)
	}
}

func TestEntryPointsAndWrappersAreNoHelpers(t *testing.T) {
	g, at := helperGraph(
		[]string{"api.handle", "util.forward", "a.x", "b.y", "c.z"},
		map[string][]string{
			"api.handle":   {"a.x", "b.y", "c.z"},
			"util.forward": {"a.x", "b.y", "c.z"},
		})
	g.Units[at["util.forward"]].Forwards = "load"
	if got := Helpers(g, ByModule(nil), []int{at["api.handle"]}); len(got) != 0 {
		t.Errorf("Helpers = %+v, want none: an entry point and a pass-through wrapper", got)
	}
}

func TestCallsFoundOnlyByMethodNameMakeNoHelper(t *testing.T) {
	// a.x and b.y call util.then only as x.then(): most likely bool::then.
	g, at := helperGraph([]string{"util.then", "a.x", "b.y"},
		map[string][]string{"util.then": {"a.x", "b.y"}})
	g.ByName = make([][]bool, len(g.Units))
	for i := range g.Callees {
		for range g.Callees[i] {
			g.ByName[i] = append(g.ByName[i], true)
		}
	}
	if got := Helpers(g, ByModule(nil), nil); len(got) != 0 {
		t.Errorf("Helpers = %+v, want none: %s is reached only by method name", got, g.Units[at["util.then"]].Name)
	}
}
