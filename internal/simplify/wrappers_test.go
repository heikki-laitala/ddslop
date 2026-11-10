package simplify

import (
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func TestPassThroughFindsWrappersAndWhatTheyCall(t *testing.T) {
	units := []unit.Unit{
		{File: "src/a.rs", Name: "visible", Forwards: "in_namespace", Calls: []unit.Call{{Kind: unit.FreeCall, Name: "in_namespace"}}},
		{File: "src/a.rs", Name: "in_namespace"},
		// super::outside resolves, by name, to the wrapper itself: no target
		{File: "src/a.rs", Name: "outside", Forwards: "super::outside", Calls: []unit.Call{{Kind: unit.PathCall, Qualifier: "super", Name: "outside"}}},
		// a library function: the wrapper names it for the domain
		{File: "src/a.rs", Name: "stored_plane", Forwards: "serde_json::to_string", Calls: []unit.Call{{Kind: unit.PathCall, Qualifier: "serde_json", Name: "to_string"}}},
		{File: "src/a.rs", Name: "S::fmt", Forwards: "fmt_inner", Trait: "Display"},
		{File: "src/a.rs", Name: "exported", Forwards: "in_namespace", External: true},
		{File: "src/a.rs", Name: "plain"},
	}
	g := flow.Build(flow.Inputs{}, units)

	var got []string
	for _, w := range PassThrough(g) {
		got = append(got, w.Unit.Name+" -> "+w.Target.Name)
	}
	want := "visible -> in_namespace"
	if strings.Join(got, "\n") != want {
		t.Errorf("wrappers:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}
