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
		s := fmt.Sprintf("%s@%d %s", r.Path, r.Line, r.Item)
		if r.Module != "" {
			s += " in " + r.Module
		}
		if r.Test {
			s += " test"
		}
		if r.Ignored {
			s += " ignored"
		}
		if r.Leaf {
			s += " leaf"
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
	want := `crate::a@1 use
crate::b@1 use
crate::c@2 use
super::d@3 use
crate::e@4 f
crate::f@4 f
crate::g@5 f
super::super::i@5 f
crate::telemetry@6 f
self::inner@7 f
crate::settle@9 C
crate::m@10 crate::m::M`
	if got := show(References("src/x.rs", []byte(src))); got != want {
		t.Errorf("references:\n%s\nwant:\n%s", got, want)
	}
}

func TestReferencesIncludeWildcardAndAliasedImportsOfARoot(t *testing.T) {
	src := "use crate::*;\nuse super::*;\nuse self::inner::*;\nuse crate as root;\nuse super as parent;\nuse crate::{*};\nuse crate::{self as base, a::B};\n"
	want := "crate@1 use\nsuper@2 use\nself::inner@3 use\ncrate@4 use\nsuper@5 use\ncrate@6 use\ncrate@7 use\ncrate::a@7 use"
	if got := show(References("src/x.rs", []byte(src))); got != want {
		t.Errorf("references:\n%s\nwant:\n%s", got, want)
	}
}

// A path ending at the name it records (crate::foo, not crate::foo::Bar)
// may name a root value rather than the module foo.
func TestReferencesKnowWhetherThePathEndsAtTheirName(t *testing.T) {
	src := "pub fn f() -> u8 { crate::foo() + crate::foo::BAR }\nuse crate::{foo, bar::Baz, qux as q};\nfn g() { m!(crate::foo, crate::foo::X) }\n"
	want := "crate::foo@1 f leaf\ncrate::foo@1 f\ncrate::foo@2 use leaf\ncrate::bar@2 use\ncrate::qux@2 use leaf\ncrate::foo@3 g leaf\ncrate::foo@3 g"
	if got := show(References("src/x.rs", []byte(src))); got != want {
		t.Errorf("references:\n%s\nwant:\n%s", got, want)
	}
}

func TestReferencesInAnImplNameItsMethods(t *testing.T) {
	src := `impl<T> Store<T> {
    fn open(&self) { crate::config::load(); }
}
trait Read {
    fn read(&self) -> crate::config::Value { todo!() }
}
`
	want := "crate::config@2 Store<T>::open\ncrate::config@5 Read::read"
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
	want := `super::super::a@3 f in outer::inner
crate::b@8 use in tests test
crate::c@11 checks test
crate::d@13 D ignored`
	if got := show(References("src/x.rs", []byte(src))); got != want {
		t.Errorf("references:\n%s\nwant:\n%s", got, want)
	}
	if got := show(References("tests/api.rs", []byte("use app::x;\nfn f() { crate::a::b(); }\n"))); got != "crate::a@2 f test" {
		t.Errorf("a test file's references = %q, want them marked test", got)
	}
}
