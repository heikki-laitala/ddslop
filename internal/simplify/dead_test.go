package simplify

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func fn(name string, public bool) unit.Unit {
	return unit.Unit{File: "crates/x/src/lib.rs", Name: name, StartLine: 1, EndLine: 3, Nodes: 30, Public: public}
}

func TestDeadFindsPublicFunctionsOfInternalCratesNamedNowhereElse(t *testing.T) {
	units := []unit.Unit{
		fn("unused", true),
		fn("Store::into_string", true),
		fn("used", true),
		fn("private_unused", false), // rustc reports it already
		func() unit.Unit { u := fn("exported", true); u.External = true; return u }(),
		func() unit.Unit { u := fn("Store::fmt", true); u.Trait = "Display"; return u }(),
		func() unit.Unit { u := fn("published_unused", true); u.File = "crates/lib/src/lib.rs"; return u }(),
	}
	words := map[string]int{"unused": 1, "into_string": 1, "used": 3, "private_unused": 1, "exported": 1, "fmt": 1, "published_unused": 1}
	internal := func(file string) bool { return strings.HasPrefix(file, "crates/x/") }

	var got []string
	for _, u := range Dead(units, words, internal) {
		got = append(got, u.Name)
	}
	if want := "[unused Store::into_string]"; fmt.Sprint(got) != want {
		t.Errorf("Dead = %v, want %s", got, want)
	}
}
