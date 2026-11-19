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

/**
 * Prose in a block.
 *
 * ` + "```" + `
 * block_example();
 * ` + "```" + `
 */
pub fn in_block() {}

/// An indented example:
///
///     indented_example();
pub fn indented() {}

#[doc = "attribute_doc()"]
pub fn attributed() {}
`)

	got := slices.Sorted(maps.Keys(reach(t, "src/lib.rs", src).Exported))

	if want := "[attribute_doc block_example by_macro crate crate_example doc doc_test_call exported indented_example inner macro_rules method_example pub reexported use]"; fmt.Sprint(got) != want {
		t.Errorf("Reach.Exported = %v, want %s", got, want)
	}
}

func TestReachListsTheFilesDocAttributesInclude(t *testing.T) {
	src := []byte(`#![doc = include_str!("../README.md")]

#[doc = include_str!("docs/api.md")]
pub fn api() {}

#[doc = include_str!(concat!(env!("CARGO_MANIFEST_DIR"), "/GUIDE.md"))]
pub fn guide() {}
`)
	r := reach(t, "src/lib.rs", src)
	if got := fmt.Sprint(r.DocFiles); got != "[../README.md docs/api.md]" {
		t.Errorf("Reach.DocFiles = %s, want [../README.md docs/api.md]", got)
	}
	if got := fmt.Sprint(r.PackageDocFiles); got != "[GUIDE.md]" {
		t.Errorf("Reach.PackageDocFiles = %s, want [GUIDE.md]", got)
	}
}

func TestDocTestWordsAreThoseOfAMarkdownFilesCodeBlocks(t *testing.T) {
	md := "# Title\n\nprose_word\n\n```rust\nfenced_call();\n```\n\n    indented_call();\n"
	if got := fmt.Sprint(slices.Sorted(maps.Keys(DocTestWords([]byte(md))))); got != "[fenced_call indented_call]" {
		t.Errorf("DocTestWords = %s, want [fenced_call indented_call]", got)
	}
}

func TestReachOptionalWordsAreThoseOfCodeSomeBuildsLeaveOutCommentsAndStrings(t *testing.T) {
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

pub fn named() -> &'static str { "string_word" }
`)

	if got, want := fmt.Sprint(reach(t, "src/lib.rs", src).Optional), "map[Calls:1 also_tested:1 block_note:1 case:1 fn:3 helper:1 in_module:1 mac:1 macos:1 mod:2 on_mac:1 only_tested:1 string_word:1 sys:1 tests:1 trailing_note:1]"; got != want {
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

#[cfg_attr(unix, path = "unix.rs")]
mod imp;

#[cfg_attr(docsrs, doc(cfg(feature = "x")))]
mod always;

mod production;
`
	if got := fmt.Sprint(reach(t, "src/lib.rs", []byte(src)).CfgModules); got != "[fixture real other.rs imp]" {
		t.Errorf("Reach.CfgModules = %s, want [fixture real other.rs imp]", got)
	}
}

// A pub glob's path is kept as read from the file's own module, whatever
// inline module declares it, an imported name as its path, through
// further such names, and each path a name some builds import; a block's, which nothing outside sees, and a
// private one are left out.
func TestReachListsPubGlobsFromTheFilesModule(t *testing.T) {
	src := `pub use shapes::*;
pub use crate::a::{b::*, c::*};
use private::*;

pub mod facade {
    pub use super::super::up::*;
    pub use self::inner::*;

    use super::shapes as aliased;
    pub use aliased::*;
    use crate::chained as first;
    use first as second;
    pub use second::*;
    #[cfg(unix)]
    use crate::unix_shapes as platform;
    #[cfg(windows)]
    use crate::windows_shapes as platform;
    pub use platform::*;

    pub mod deep {
        pub use super::side::*;
        pub use crate::far::*;
    }
}

fn f() {
    pub use hidden::*;
}
`
	want := "[shapes crate::a::b crate::a::c super::up self::facade::inner self::shapes crate::chained crate::unix_shapes crate::windows_shapes self::facade::side crate::far]"
	if got := fmt.Sprint(reach(t, "src/lib.rs", []byte(src)).Globs); got != want {
		t.Errorf("Reach.Globs = %s, want %s", got, want)
	}
}
