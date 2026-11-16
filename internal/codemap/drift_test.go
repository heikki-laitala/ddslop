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

	got := Drifts(g, ByModule(nil), ByModule(nil), Conventions(g, ByModule(nil)))
	want := []Drift{
		{Concern: "error-checks", Form: "message", Convention: "variant", Share: 20.0 / 22, Unit: 20, Lines: []int{205}, Target: 19, TargetLine: 200},
		{Concern: "error-checks", Form: "message", Convention: "variant", Share: 20.0 / 22, Unit: 21, Lines: []int{5}, Target: 1, TargetLine: 20},
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
	g := &flow.Graph{Units: units}
	if got := Drifts(g, ByModule(nil), ByModule(nil), Conventions(g, ByModule(nil))); len(got) != 0 {
		t.Errorf("Drifts = %+v, want none below the drift share", got)
	}
}

func TestDriftsOnOneLineComeInConcernOrder(t *testing.T) {
	var units []unit.Unit
	for i := range 20 {
		units = append(units, formUnit("a/a.go", i+1, "error-checks", "variant", "error-wrapping", "map_err"))
	}
	both := formUnit("a/b.go", 1, "error-wrapping", "context")
	both.Forms = append(both.Forms, unit.Form{Concern: "error-checks", Form: "message", Line: 1})
	units = append(units, both)

	g := &flow.Graph{Units: units}
	got := Drifts(g, ByModule(nil), ByModule(nil), Conventions(g, ByModule(nil)))
	if len(got) != 2 || got[0].Concern != "error-checks" || got[1].Concern != "error-wrapping" {
		t.Errorf("Drifts = %+v, want error-checks then error-wrapping", got)
	}
}

// Test code is held to production code's conventions but sets none: as
// many test functions checking errors by message as production functions
// checking them by variant leave the convention whole, and each test
// function is a drift from it.
func TestTestCodeIsHeldToProductionConventionsButSetsNone(t *testing.T) {
	var units []unit.Unit
	for i := range 20 {
		units = append(units, formUnit("store/s.go", i+1, "error-checks", "variant"))
	}
	for i := range 20 {
		test := formUnit("store/s_test.go", i+1, "error-checks", "message")
		test.Test = true
		units = append(units, test)
	}
	g := &flow.Graph{Units: units}

	conventions := Conventions(g, ByModule(nil))
	if c := conventions["error-checks"]; c != (Convention{Form: "variant", Share: 1, Holds: true}) {
		t.Errorf("convention = %+v, want variant at the whole of production code", c)
	}
	if got := Drifts(g, ByModule(nil), ByModule(nil), conventions); len(got) != 20 || got[0].Unit != 20 {
		t.Errorf("Drifts = %+v, want each of the 20 test functions", got)
	}
}

// Drifts are judged against the conventions given, which need not be the
// code's own: a concern whose convention does not hold there has no drift
// whatever its share here, and one that holds there makes a drift of
// every other form here.
func TestDriftsAreJudgedAgainstTheConventionsGiven(t *testing.T) {
	var units []unit.Unit
	for i := range 20 {
		units = append(units, formUnit("a/a.go", i+1, "error-checks", "variant", "error-wrapping", "map_err"))
	}
	units = append(units, formUnit("a/b.go", 1, "error-checks", "message", "error-wrapping", "context"))
	g := &flow.Graph{Units: units}

	given := map[string]Convention{
		"error-checks":   {Form: "variant", Share: 0.85},
		"error-wrapping": {Form: "map_err", Share: 0.95, Holds: true},
	}
	got := Drifts(g, ByModule(nil), ByModule(nil), given)
	if len(got) != 1 || got[0].Concern != "error-wrapping" || got[0].Share != 0.95 {
		t.Errorf("Drifts = %+v, want only the wrapping drift, at the given share", got)
	}
}
