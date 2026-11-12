package codemap

import (
	"reflect"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func TestFeaturesShowEachFamilysShapeWithTheHelpersItsStepsCall(t *testing.T) {
	g, at := helperGraph(
		[]string{"api.create", "api.update", "svc.checkCreate", "svc.checkUpdate", "util.respond", "billing.charge",
			"tool.run", "tool.rerun"},
		map[string][]string{"util.respond": {"svc.checkCreate", "svc.checkUpdate", "billing.charge"}})
	inScope := Grouping(func(u unit.Unit) string {
		if u.File == "tool/f.go" {
			return "" // another crate under --crate
		}
		return ByModule(nil)(u)
	})
	flows := []flow.ParallelFlow{
		{Entries: []int{at["api.create"], at["api.update"]}, Steps: []flow.AlignedStep{
			{Units: []int{at["api.create"], at["api.update"]}},
			{Units: []int{at["svc.checkCreate"], at["svc.checkUpdate"]}},
		}},
		{Entries: []int{at["tool.run"], at["tool.rerun"]}, Steps: []flow.AlignedStep{
			{Units: []int{at["tool.run"], at["tool.rerun"]}},
		}},
	}

	got := Features(g, flows, inScope, Helpers(g, inScope, nil))
	want := []Feature{{
		Shape:   at["api.create"],
		Members: []int{at["api.update"]},
		Steps: []FeatureStep{
			{Unit: at["api.create"]},
			{Unit: at["svc.checkCreate"], Helpers: []int{at["util.respond"]}},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Features = %+v, want %+v", got, want)
	}
}
