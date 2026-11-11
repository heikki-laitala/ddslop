package pattern

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func withParams(name string, params ...string) unit.Unit {
	u := unit.Unit{File: "src/ingest.rs", Name: name}
	for _, p := range params {
		n, t, _ := strings.Cut(p, ":")
		u.Params = append(u.Params, unit.Param{Name: n, Type: t})
	}
	return u
}

func TestParameterGroupsFindsTheLargestSetPassedTogether(t *testing.T) {
	units := []unit.Unit{
		withParams("held", "build:i32", "environment:&str", "transaction:&Transaction", "now:DateTime"),
		withParams("isolate", "environment:&str", "transaction:&Transaction", "build:i32", "now:DateTime", "page:u32"),
		withParams("claim", "transaction:&Transaction", "build:i32", "environment:&str", "now:DateTime"),
		withParams("sweep", "build:i32", "environment:&str", "transaction:&Transaction", "now:DateTime", "limit:u32"),
		// three of the four only
		withParams("partial", "build:i32", "environment:&str", "transaction:&Transaction"),
		// a trait dictates its parameters
		func() unit.Unit {
			u := withParams("implemented", "build:i32", "environment:&str", "transaction:&Transaction", "now:DateTime")
			u.Trait = "Ingest"
			return u
		}(),
		// another type: not the same parameter
		withParams("other", "build:i64", "environment:&str", "transaction:&Transaction"),
	}
	var got []string
	for _, g := range ParameterGroups(units) {
		var params, names []string
		for _, p := range g.Params {
			params = append(params, p.Name)
		}
		for _, u := range g.Units {
			names = append(names, u.Name)
		}
		got = append(got, fmt.Sprintf("(%s) in %s", strings.Join(params, ", "), strings.Join(names, " ")))
	}
	// The four-parameter group in four functions. The three-parameter group
	// a fifth function also passes is the same concept: one group per
	// family of overlapping groups.
	want := []string{
		"(build, environment, now, transaction) in claim held isolate sweep",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("groups:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParameterGroupsKeepUnrelatedGroupsApart(t *testing.T) {
	var units []unit.Unit
	for i := range 4 {
		units = append(units,
			withParams(fmt.Sprintf("ingest%d", i), "build:i32", "environment:&str", "transaction:&Tx"),
			withParams(fmt.Sprintf("worker%d", i), "gate:Gate", "hot:Conn", "stopping:Stopping", "build:i32"))
	}
	// The groups share one parameter only (build): two concepts.
	if got := len(ParameterGroups(units)); got != 2 {
		t.Errorf("got %d groups, want 2", got)
	}
}

// Groups that tie in size are ordered completely, so which one represents
// a family never depends on map iteration order.
func TestParameterGroupsAreDeterministic(t *testing.T) {
	// Two overlapping groups (sharing db and fleet), each in four functions,
	// both led by a function named "handle": equal in size, parameter count
	// and first name.
	var units []unit.Unit
	for i := range 4 {
		units = append(units, withParams("handle", "db:&Db", "fleet:Uuid", "sub:&str", fmt.Sprintf("a%d:u8", i)))
	}
	for i := range 3 {
		units = append(units, withParams(fmt.Sprintf("other%d", i), "db:&Db", "fleet:Uuid", "token:&str"))
	}
	units = append(units, withParams("handle", "db:&Db", "fleet:Uuid", "token:&str"))
	describe := func() string {
		var out []string
		for _, g := range ParameterGroups(units) {
			var ps []string
			for _, p := range g.Params {
				ps = append(ps, p.Name)
			}
			out = append(out, strings.Join(ps, ","))
		}
		return strings.Join(out, " | ")
	}
	first := describe()
	for range 50 {
		if got := describe(); got != first {
			t.Fatalf("ParameterGroups gave %q, then %q", first, got)
		}
	}
}
