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
	for _, g := range ParameterGroups(units, nil) {
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
	if got := len(ParameterGroups(units, nil)); got != 2 {
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
		for _, g := range ParameterGroups(units, nil) {
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

// Methods that may implement an interface (Go) have their signature
// dictated, like trait methods.
func TestParameterGroupsLeaveOutMethodsCalledViaInterfaces(t *testing.T) {
	var units []unit.Unit
	for i := range 4 {
		u := withParams(fmt.Sprintf("Server.handle%d", i), "w:http.ResponseWriter", "r:*http.Request", "id:string")
		u.Dispatched = true
		units = append(units, u)
	}
	if got := ParameterGroups(units, nil); len(got) != 0 {
		t.Errorf("got %d groups of interface methods, want none", len(got))
	}
}

// Functions with the parameters of a function type declared in their own
// Go package have their signature dictated: they are values of that type,
// such as the commands of a command table. A type elsewhere, or in test
// code, dictates nothing about them.
func TestParameterGroupsLeaveOutFunctionsOfDeclaredFunctionTypes(t *testing.T) {
	commands := func(file string) []unit.Unit {
		var units []unit.Unit
		for i := range 4 {
			u := withParams(fmt.Sprintf("run%d", i), "args:[]string", "stdout:io.Writer", "stderr:io.Writer")
			u.File = file
			u.Types = "([]string, io.Writer, io.Writer) (int)"
			units = append(units, u)
		}
		return units
	}
	sig := "([]string, io.Writer, io.Writer) (int)"
	for _, tc := range []struct {
		name     string
		units    []unit.Unit
		funcType unit.FuncType
		want     int
	}{
		{"same package", commands("internal/cli/map.go"), unit.FuncType{File: "internal/cli/cli.go", Types: sig}, 0},
		{"other package", commands("internal/cli/map.go"), unit.FuncType{File: "internal/report/report.go", Types: sig}, 1},
		{"test code", commands("internal/cli/map.go"), unit.FuncType{File: "internal/cli/cli_test.go", Types: sig, Test: true}, 1},
		{"not Go", commands("internal/cli/map.rs"), unit.FuncType{File: "internal/cli/cli.go", Types: sig}, 1},
		{"other results", commands("internal/cli/map.go"), unit.FuncType{File: "internal/cli/cli.go", Types: "([]string, io.Writer, io.Writer) (bool)"}, 1},
		{"with a Context", commands("internal/cli/map.go"), unit.FuncType{File: "internal/cli/cli.go", Types: "(context.Context, []string, io.Writer, io.Writer) (int)"}, 1},
	} {
		if got := len(ParameterGroups(tc.units, []unit.FuncType{tc.funcType})); got != tc.want {
			t.Errorf("%s: got %d groups, want %d", tc.name, got, tc.want)
		}
	}
}
