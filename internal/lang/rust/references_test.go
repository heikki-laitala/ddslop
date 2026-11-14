package rust

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// show writes references one per line as path@line, with the inline
// module and the flags that are set.
func show(refs []unit.Reference) string {
	var lines []string
	for _, r := range refs {
		s := fmt.Sprintf("%s@%d", r.Path, r.Line)
		if r.Module != "" {
			s += " in " + r.Module
		}
		if r.Test {
			s += " test"
		}
		if r.Ignored {
			s += " ignored"
		}
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n")
}

func TestReferencesNameEveryModuleAPathReaches(t *testing.T) {
	src := `use crate::{a::X, b::Y};
use crate::c::*;
use super::d::Z;
pub(in crate::hidden) fn f() -> crate::e::T<crate::f::U> {
    let v = crate::g::h(super::super::i::J);
    metrics::counter!(crate::telemetry::ATTEMPTS, "m" => 1);
    self::inner::K { x: 1 }
}
pub const C: u64 = crate::settle::LATE + std::u64::MAX;
impl crate::m::M {}
`
	want := `crate::a@1
crate::b@1
crate::c@2
super::d@3
crate::e@4
crate::f@4
crate::g@5
super::super::i@5
crate::telemetry@6
self::inner@7
crate::settle@9
crate::m@10`
	if got := show(References("src/x.rs", []byte(src))); got != want {
		t.Errorf("references:\n%s\nwant:\n%s", got, want)
	}
}

func TestReferencesKnowTheirModuleTestCodeAndIgnoreMarkers(t *testing.T) {
	src := `mod outer {
    pub mod inner {
        pub fn f() { super::super::a::g(); }
    }
}
#[cfg(test)]
mod tests {
    use crate::b::H;
}
#[test]
fn checks() { crate::c::k(); }
// ddslop:ignore reads the store's endpoint on purpose
pub const D: u32 = crate::d::E;
`
	want := `super::super::a@3 in outer::inner
crate::b@8 in tests test
crate::c@11 test
crate::d@13 ignored`
	if got := show(References("src/x.rs", []byte(src))); got != want {
		t.Errorf("references:\n%s\nwant:\n%s", got, want)
	}
	if got := show(References("tests/api.rs", []byte("use app::x;\nfn f() { crate::a::b(); }\n"))); got != "crate::a@2 test" {
		t.Errorf("a test file's references = %q, want them marked test", got)
	}
}
