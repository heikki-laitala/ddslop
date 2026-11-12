package codemap

import (
	"reflect"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func TestDriftIsAnotherFormWhereAStrongConventionExists(t *testing.T) {
	var units []unit.Unit
	for i := range 20 { // 20 checks by variant across two modules, lines 10 to 200
		units = append(units, formUnit([]string{"api/a.go", "store/s.go"}[i%2], 10*(i+1), "error-checks", "variant"))
	}
	units = append(units,
		formUnit("store/s.go", 205, "error-checks", "message"), // drift: its file has variant checks
		formUnit("store/x.go", 5, "error-checks", "message"),   // drift: its module has them
		formUnit("api/a.go", 1, "error-wrapping", "map_err"),   // wrapping has too few occurrences
		formUnit("api/a.go", 2, "error-wrapping", "context"))
	g := &flow.Graph{Units: units}

	got := Drifts(g, ByModule(nil), ByModule(nil))
	want := []Drift{
		{Concern: "error-checks", Form: "message", Convention: "variant", Unit: 20, StartLine: 205, EndLine: 205, Target: 19, TargetLine: 200},
		{Concern: "error-checks", Form: "message", Convention: "variant", Unit: 21, StartLine: 5, EndLine: 5, Target: 1, TargetLine: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Drifts =\n%+v\nwant\n%+v", got, want)
	}
}

func TestAMixOfFormsIsNoDrift(t *testing.T) {
	var units []unit.Unit
	for i := range 10 { // 7 map_err and 3 context: a mix, not a slip
		form := "map_err"
		if i >= 7 {
			form = "context"
		}
		units = append(units, formUnit("app/a.go", i+1, "error-wrapping", form))
	}
	if got := Drifts(&flow.Graph{Units: units}, ByModule(nil), ByModule(nil)); len(got) != 0 {
		t.Errorf("Drifts = %+v, want none below the drift share", got)
	}
}
