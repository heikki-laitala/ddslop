package rust

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
)

func TestExportedWordsAreThoseOfDocTestsExportedMacrosAndReexports(t *testing.T) {
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

	got := slices.Sorted(maps.Keys(ExportedWords(src)))

	if want := "[by_macro crate crate_example doc_test_call exported inner macro_rules method_example pub reexported use]"; fmt.Sprint(got) != want {
		t.Errorf("ExportedWords = %v, want %s", got, want)
	}
}

func TestOptionalWordsAreThoseOfCodeSomeBuildsLeaveOutAndOfComments(t *testing.T) {
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
`)

	if got, want := fmt.Sprint(OptionalWords(syntax.Source{Path: "src/lib.rs", Src: src})), "map[Calls:1 also_tested:1 block_note:1 case:1 fn:3 helper:1 in_module:1 mac:1 mod:1 on_mac:1 only_tested:1 tests:1 trailing_note:1]"; got != want {
		t.Errorf("OptionalWords = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(OptionalWords(syntax.Source{Path: "src/tests.rs", Src: []byte("fn f() {}"), Test: true})), "map[f:1 fn:1]"; got != want {
		t.Errorf("OptionalWords of a test file = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(OptionalWords(syntax.Source{Path: "src/real.rs", Src: []byte("#![cfg(target_os = \"macos\")]\nfn f() {}")})), "map[cfg:1 f:1 fn:1 macos:1 target_os:1]"; got != want {
		t.Errorf("OptionalWords of a file under #![cfg] = %s, want %s", got, want)
	}
}

func TestCfgModulesListsOutOfLineModulesUnderAnyCfg(t *testing.T) {
	src := `#[cfg(test)]
mod fixture;

#[cfg(target_os = "macos")]
mod real;

#[cfg(feature = "x")]
mod inline {}

mod production;
`
	if got := fmt.Sprint(CfgModules([]byte(src))); got != "[fixture real]" {
		t.Errorf("CfgModules = %s, want [fixture real]", got)
	}
}
