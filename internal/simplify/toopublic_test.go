package simplify

import (
	"fmt"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Which functions may be public at all is Dead's rule too (see
// TestDeadFindsPublicFunctionsOfInternalCratesNamedNowhereElse).
func TestTooPublicFindsPublicFunctionsNamedOnlyInTheirLibrary(t *testing.T) {
	inBinary := fn("in_binary", true)
	inBinary.File = "crates/x/src/main.rs"
	units := []unit.Unit{fn("helper", true), fn("Store::reset", true), fn("used_outside", true), fn("unused", true), fn("only_tested", true), fn("r#type", true), inBinary}
	words := map[string]int{"helper": 2, "reset": 3, "used_outside": 3, "unused": 1, "only_tested": 2, "type": 2, "in_binary": 2}
	libraries := map[string]*Library{"crates/x/src/lib.rs": {
		Words:    map[string]int{"helper": 2, "reset": 3, "used_outside": 2, "unused": 1, "only_tested": 2, "type": 2},
		Optional: map[string]int{"reset": 1, "only_tested": 1}, // pub(crate) would leave it unused outside tests
	}}

	got := TooPublic(units, words, func(file string) *Library { return libraries[file] })

	if want := "[helper Store::reset r#type]"; fmt.Sprint(names(got)) != want {
		t.Errorf("TooPublic = %v, want %s", names(got), want)
	}
}

func names(units []unit.Unit) []string {
	var out []string
	for _, u := range units {
		out = append(out, u.Name)
	}
	return out
}
