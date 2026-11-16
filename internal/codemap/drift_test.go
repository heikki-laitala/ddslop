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

// twoConcerns is 20 functions checking errors by variant and wrapping
// them with map_err, and one in a/b.go checking by message and wrapping
// with context, on one line.
func twoConcerns() *flow.Graph {
	var units []unit.Unit
	for i := range 20 {
		units = append(units, formUnit("a/a.go", i+1, "error-checks", "variant", "error-wrapping", "map_err"))
	}
	both := formUnit("a/b.go", 1, "error-wrapping", "context")
	both.Forms = append(both.Forms, unit.Form{Concern: "error-checks", Form: "message", Line: 1})
	return &flow.Graph{Units: append(units, both)}
}

func TestDriftsOnOneLineComeInConcernOrder(t *testing.T) {
	g := twoConcerns()
	got := Drifts(g, ByModule(nil), ByModule(nil), Conventions(g, ByModule(nil)))
	if len(got) != 2 || got[0].Concern != "error-checks" || got[1].Concern != "error-wrapping" {
		t.Errorf("Drifts = %+v, want error-checks then error-wrapping", got)
	}
}

// Conventions measure the units in the group they are given: production
// functions checking errors by variant and test functions checking them
// by message are two conventions, each whole, and neither drifts from the
// other.
func TestConventionsMeasureTheGroupGiven(t *testing.T) {
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
	side := func(test bool) Grouping {
		return func(u unit.Unit) string {
			if u.Test != test {
				return ""
			}
			return "side"
		}
	}

	for _, tc := range []struct {
		test bool
		want Convention
	}{{false, Convention{Form: "variant", Share: 1, Holds: true}}, {true, Convention{Form: "message", Share: 1, Holds: true}}} {
		conventions := Conventions(g, side(tc.test))
		if c := conventions["error-checks"]; c != tc.want {
			t.Errorf("test %v: convention = %+v, want %+v", tc.test, c, tc.want)
		}
		if got := Drifts(g, side(tc.test), ByModule(nil), conventions); len(got) != 0 {
			t.Errorf("test %v: Drifts = %+v, want none", tc.test, got)
		}
	}
}

// Drifts are judged against the conventions given, which need not be the
// code's own: a concern whose convention does not hold there has no drift
// whatever its share here, and one that holds there makes a drift of
// every other form here.
func TestDriftsAreJudgedAgainstTheConventionsGiven(t *testing.T) {
	g := twoConcerns()
	given := map[string]Convention{
		"error-checks":   {Form: "variant", Share: 0.85},
		"error-wrapping": {Form: "map_err", Share: 0.95, Holds: true},
	}
	got := Drifts(g, ByModule(nil), ByModule(nil), given)
	if len(got) != 1 || got[0].Concern != "error-wrapping" || got[0].Share != 0.95 {
		t.Errorf("Drifts = %+v, want only the wrapping drift, at the given share", got)
	}
}

// A convention given holds even where no occurrence of its form is left:
// a change replacing every check by variant with one by message drifts
// from the base's convention, with no target to follow in its own code.
func TestADriftNeedsNoSurvivingConventionalOccurrence(t *testing.T) {
	var units []unit.Unit
	for i := range 6 {
		units = append(units, formUnit("a/a.go", i+1, "error-checks", "message"))
	}
	g := &flow.Graph{Units: units}

	got := Drifts(g, ByModule(nil), ByModule(nil), map[string]Convention{"error-checks": {Form: "variant", Share: 1, Holds: true}})
	if len(got) != 6 || got[0].Target != -1 || got[0].TargetLine != 0 {
		t.Errorf("Drifts = %+v, want each of the six, with no target", got)
	}
}
