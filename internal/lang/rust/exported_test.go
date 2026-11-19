package rust

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func reach(t *testing.T, path string, src []byte) unit.Reach {
	t.Helper()
	_, decls := testutil.Parse(t, Parse, path, string(src))
	return decls.Reach
}

func TestReachExportsTheWordsOfDocTestsExportedMacrosAndReexports(t *testing.T) {
	src := []byte(`//! ` + "```" + `
//! crate_example();
//! ` + "```" + `

/// Not code: prose_word.
///
/// ` + "```" + `
/// doc_test_call();
/// ` + "```" + `
pub fn documented() {}

// ` + "```" + `
// plain_comment();
// ` + "```" + `
fn plain() {}

#[macro_export]
// a comment between the attribute and the macro
macro_rules! exported {
    () => { $crate::by_macro() };
}

macro_rules! local {
    () => { local_body() };
}

pub use inner::reexported;
pub(crate) use inner::narrowed;

mod inner {
    impl S {
        /// ` + "```" + `
        /// method_example();
        /// ` + "```" + `
        pub fn m() {}
    }
}
`)

	got := slices.Sorted(maps.Keys(reach(t, "src/lib.rs", src).Exported))

	if want := "[by_macro crate crate_example doc_test_call exported inner macro_rules method_example pub reexported use]"; fmt.Sprint(got) != want {
		t.Errorf("Reach.Exported = %v, want %s", got, want)
	}
}

func TestReachOptionalWordsAreThoseOfCodeSomeBuildsLeaveOutAndOfComments(t *testing.T) {
	src := []byte(`/// Calls [helper].
pub fn production() { helper(); } // trailing_note

/* block_note */
#[test]
fn case() { only_tested(); }

#[cfg(test)]
mod tests {
    fn in_module() { also_tested(); }
}

#[cfg(target_os = "macos")]
fn mac() { on_mac(); }

#[cfg(unix)]
mod sys;
`)

	if got, want := fmt.Sprint(reach(t, "src/lib.rs", src).Optional), "map[Calls:1 also_tested:1 block_note:1 case:1 fn:3 helper:1 in_module:1 mac:1 mod:2 on_mac:1 only_tested:1 sys:1 tests:1 trailing_note:1]"; got != want {
		t.Errorf("Reach.Optional = %s, want %s", got, want)
	}
	if !reach(t, "src/tests.rs", []byte("fn f() {}")).AllOptional {
		t.Error("a test file should be all optional")
	}
	if r := reach(t, "src/real.rs", []byte("#![cfg(target_os = \"macos\")]\n#[cfg(test)]\nmod fixture;")); !r.AllOptional || fmt.Sprint(r.CfgModules) != "[fixture]" {
		t.Errorf("a file under #![cfg] = %+v, want all optional, with its cfg modules", r)
	}
}

func TestReachListsOutOfLineModulesUnderAnyCfg(t *testing.T) {
	src := `#[cfg(test)]
mod fixture;

#[cfg(target_os = "macos")]
mod real;

#[cfg(feature = "x")]
mod inline {}

#[cfg(feature = "y")]
#[path = "other.rs"]
mod placed;

mod production;
`
	if got := fmt.Sprint(reach(t, "src/lib.rs", []byte(src)).CfgModules); got != "[fixture real other.rs]" {
		t.Errorf("Reach.CfgModules = %s, want [fixture real other.rs]", got)
	}
}
