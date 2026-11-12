package codemap

import (
	"reflect"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func formUnit(file string, line int, forms ...string) unit.Unit {
	u := unit.Unit{File: file, Name: "f", StartLine: line}
	for at := line; len(forms) >= 2; at++ {
		u.Forms = append(u.Forms, unit.Form{Concern: forms[0], Form: forms[1], Line: at})
		forms = forms[2:]
	}
	return u
}

func TestIdiomsNameTheMajorityFormOfEachConcern(t *testing.T) {
	g := &flow.Graph{Units: []unit.Unit{
		formUnit("api/a.go", 10, "error-checks", "errors.Is/As", "error-wrapping", "%w"),
		formUnit("api/b.go", 20, "error-checks", "errors.Is/As", "error-checks", "message"),
		formUnit("store/c.go", 5, "error-checks", "errors.Is/As", "error-checks", "errors.Is/As"),
		formUnit("store/d.go", 7, "error-checks", "errors.Is/As", "error-wrapping", "%v"),
		formUnit("other/e.go", 1, "error-checks", "message", "error-checks", "message"), // out of scope
	}}
	inScope := Grouping(func(u unit.Unit) string {
		if u.File == "other/e.go" {
			return ""
		}
		return ByModule(nil)(u)
	})
	idiomatic := map[string]string{"error-checks": "errors.Is/As", "error-wrapping": "%w"}

	got := Idioms(g, inScope, idiomatic)
	want := []Idiom{
		{Concern: "error-checks", Convention: "errors.Is/As", Occurrences: 6, Forms: []FormCount{
			{Form: "errors.Is/As", Count: 5, Unit: 0, Line: 10},
			{Form: "message", Count: 1, Unit: 1, Line: 21},
		}},
		// Two occurrences make no convention: the idiomatic form is named.
		{Concern: "error-wrapping", Idiomatic: "%w", Occurrences: 2, Forms: []FormCount{
			{Form: "%v", Count: 1, Unit: 3, Line: 8},
			{Form: "%w", Count: 1, Unit: 0, Line: 11},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Idioms =\n%+v\nwant\n%+v", got, want)
	}
}

func TestIgnoredFunctionsShapeNoIdiom(t *testing.T) {
	ignored := formUnit("api/a.go", 10, "error-checks", "message")
	ignored.Ignored = true
	g := &flow.Graph{Units: []unit.Unit{ignored, formUnit("api/b.go", 20, "error-checks", "errors.Is/As")}}

	got := Idioms(g, ByModule(nil), nil)
	if len(got) != 1 || got[0].Occurrences != 1 || got[0].Forms[0].Form != "errors.Is/As" {
		t.Errorf("Idioms = %+v, want only the errors.Is/As check", got)
	}
}
