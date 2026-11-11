package pattern

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func matching(name string, line int, values ...string) unit.Unit {
	return unit.Unit{File: "src/a.rs", Name: name, StartLine: line, EndLine: line + 9,
		StringMatches: []unit.StringMatch{{Values: values, StartLine: line + 1, EndLine: line + 6}}}
}

func TestStringKindsFindsTheSameStringsMatchedInThreeFunctions(t *testing.T) {
	statuses := []string{`"committed"`, `"refused"`, `"replayed"`}
	units := []unit.Unit{
		matching("interpret", 10, statuses...),
		matching("refusal", 30, statuses...),
		matching("replay", 50, statuses...),
		// a parser and its inverse: two places are the normal shape
		matching("parse_mode", 70, `"a"`, `"b"`, `"c"`),
		matching("mode_str", 90, `"a"`, `"b"`, `"c"`),
	}
	got := StringKinds(units)
	var out []string
	for _, k := range got {
		var names []string
		for _, m := range k.Matches {
			names = append(names, fmt.Sprintf("%s:%d", m.Unit.Name, m.StartLine))
		}
		out = append(out, fmt.Sprintf("%v %s", k.Values, strings.Join(names, " ")))
	}
	if want := `["committed" "refused" "replayed"] interpret:11 refusal:31 replay:51`; strings.Join(out, "\n") != want {
		t.Errorf("string kinds:\n%s\nwant:\n%s", strings.Join(out, "\n"), want)
	}
}
