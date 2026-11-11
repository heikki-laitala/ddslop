package pattern

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func scoped(name, opens, closes string, line int) unit.Unit {
	return unit.Unit{File: "src/a.rs", Name: name, StartLine: line, EndLine: line + 9,
		Scope: &unit.Scope{Opens: opens, Closes: closes, StartLine: line + 1, EndLine: line + 8, Nodes: 12}}
}

func TestScopesGroupFunctionsOpeningAndClosingTheSameKindOfValue(t *testing.T) {
	var units []unit.Unit
	for i := range 6 {
		units = append(units, scoped(fmt.Sprintf("write%d", i), "transaction", "commit", 10*i))
	}
	for i := range 4 {
		units = append(units, scoped(fmt.Sprintf("lease%d", i), "acquire", "release", 100+10*i))
	}
	units = append(units, unit.Unit{File: "src/a.rs", Name: "plain"})
	test := scoped("write_test", "transaction", "commit", 500)
	test.Test = true
	units = append(units, test)

	got := Scopes(units)
	if len(got) != 1 {
		t.Fatalf("got %d groups, want the six transactions (four leases are too few)", len(got))
	}
	var names []string
	for _, u := range got[0].Units {
		names = append(names, u.Name)
	}
	if g := got[0]; g.Opens != "transaction" || g.Closes != "commit" || strings.Join(names, " ") != "write0 write1 write2 write3 write4 write5" {
		t.Errorf("group = %s..%s %v", g.Opens, g.Closes, names)
	}
}
