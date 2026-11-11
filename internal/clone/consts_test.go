package clone

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func cst(file, name, value string, line int) unit.Const {
	return unit.Const{File: file, Name: name, Value: value, Line: line, Nodes: 1, Literal: strings.HasPrefix(value, `"`)}
}

func TestConstantsGroupsSameNameAndValue(t *testing.T) {
	consts := []unit.Const{
		cst("router/model_grant.rs", "MAX_GRANTED_MODELS", "64", 3),
		cst("ledger/model_grant.rs", "MAX_GRANTED_MODELS", "64", 5),
		cst("ledger/query.rs", "MAX_BUCKETS", "64", 1),          // same value, other name
		cst("router/limits.rs", "MAX_GRANTED_MODELS", "128", 1), // same name, other value
		{File: "a/lib.rs", Name: "CONTRACT", Value: `include_str!("x.json")`, Macro: true},
		{File: "b/lib.rs", Name: "CONTRACT", Value: `include_str!("x.json")`, Macro: true},
		cst("a.rs", "_", "assert!(true)", 1),
		cst("b.rs", "_", "assert!(true)", 1),
	}
	var got []string
	for _, g := range Constants(consts) {
		var members []string
		for _, c := range g.Consts {
			members = append(members, fmt.Sprintf("%s:%d", c.File, c.Line))
		}
		got = append(got, strings.Join(members, " | "))
	}
	if want := "ledger/model_grant.rs:5 | router/model_grant.rs:3"; strings.Join(got, "\n") != want {
		t.Errorf("groups:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func literalUnit(file, name string, line int, literals ...string) unit.Unit {
	u := unit.Unit{File: file, Name: name, StartLine: line, EndLine: line + len(literals)}
	for i, l := range literals {
		u.Leaves = append(u.Leaves, unit.Leaf{Kind: unit.Literal, Text: l, Line: line + i})
	}
	return u
}

func TestInlinedConstantsFindsTheValueWrittenInlineInTheSameCrate(t *testing.T) {
	consts := []unit.Const{
		cst("services/router/src/gate.rs", "LEDGER_UNAVAILABLE", `"ledger_unavailable"`, 4),
		cst("services/router/src/upgrade.rs", "MODEL", `"model"`, 1), // a plain short word: coincidences
		cst("services/router/src/http.rs", "TIMEOUT", "5", 1),        // not a string
	}
	units := []unit.Unit{
		literalUnit("services/router/src/api.rs", "refuse", 10, `"ledger_unavailable"`, `"model"`, `"ledger_unavailable"`),
		literalUnit("services/router/src/gate.rs", "check", 20, `"ledger_unavailable"`),
		literalUnit("services/ledger/src/api.rs", "status", 1, `"ledger_unavailable"`), // another crate
	}
	crateOf := func(file string) string { return strings.Join(strings.Split(file, "/")[:2], "/") }
	found := InlinedConstants(consts, units, crateOf)
	if len(found) != 1 {
		t.Fatalf("got %d inlined constants, want 1: %+v", len(found), found)
	}
	var uses []string
	for _, u := range found[0].Uses {
		uses = append(uses, fmt.Sprintf("%s:%d %s", u.File, u.Line, u.Name))
	}
	want := "services/router/src/api.rs:10 refuse services/router/src/api.rs:12 refuse services/router/src/gate.rs:20 check"
	if found[0].Const.Name != "LEDGER_UNAVAILABLE" || strings.Join(uses, " ") != want {
		t.Errorf("found %s used at %s, want LEDGER_UNAVAILABLE at %s", found[0].Const.Name, strings.Join(uses, " "), want)
	}
}

func TestInlinedConstantsAreDistinctiveValues(t *testing.T) {
	cases := map[string]bool{
		`"ledger_unavailable"`: true,  // long enough
		`"[DONE]"`:             true,  // punctuation
		`"x-request-id"`:       true,  // punctuation
		`"pending"`:            false, // a short word
		`""`:                   false,
		`"*"`:                  false, // punctuation alone: a wildcard, a separator
		`"1.5"`:                false, // a number in a string
		`"/"`:                  false,
	}
	for value, want := range cases {
		if got := distinctive(value); got != want {
			t.Errorf("distinctive(%s) = %v, want %v", value, got, want)
		}
	}
}
