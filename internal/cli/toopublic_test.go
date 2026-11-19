package cli

import (
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
)

// A pub function of an unpublished crate's library that only the library
// uses can be pub(crate). Code compiled outside the library keeps it pub:
// another crate, the crate's tests, a binary, a doc test, an exported
// macro, a re-export. So do a function called from outside the source,
// one only some builds call (the library's tests, code under a #[cfg]),
// one only a comment names besides, one of a published crate and one of a
// crate with no library.
func TestCheckReportsRustFunctionsOnlyTheirCrateUses(t *testing.T) {
	root := repo(t, map[string]string{
		"Cargo.toml":             "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.package]\npublish = false\n",
		"crates/util/Cargo.toml": "[package]\nname = \"util\"\npublish.workspace = true\n",
		"crates/util/src/lib.rs": `pub mod text;

pub struct Store;

impl Store {
    pub fn reset(&self) {}
}

pub fn shared() {}

pub fn helper() {}

pub fn tested() {}

/// Runs the example.
///
/// ` + "```" + `
/// util::documented();
/// ` + "```" + `
pub fn documented() {}

pub fn by_macro() {}

#[macro_export]
macro_rules! call {
    () => {
        $crate::by_macro()
    };
}

pub(crate) fn narrow() {}

pub use text::reexported;

pub fn only_tested() {}

#[cfg(target_os = "macos")]
mod real;

pub fn mac_only() {}

/// Not [linked] in code.
pub fn linked() {}

#[cfg(test)]
mod tests {
    #[test]
    fn works() {
        super::only_tested();
    }
}

#[no_mangle]
pub extern "C" fn ffi() {}
`,
		"crates/util/src/text.rs":    "pub fn reexported() {}\n\npub fn run(s: &crate::Store) {\n    s.reset();\n    crate::helper();\n    crate::tested();\n    crate::documented();\n    crate::by_macro();\n    crate::narrow();\n    crate::ffi();\n    reexported();\n}\n",
		"crates/util/src/real.rs":    "fn start() {\n    crate::mac_only();\n}\n",
		"crates/util/src/bin/cli.rs": "fn main() {\n    util::text::run(&util::Store);\n}\n",
		"crates/util/tests/it.rs":    "#[test]\nfn works() {\n    util::tested();\n}\n",
		"crates/app/Cargo.toml":      "[package]\nname = \"app\"\npublish.workspace = true\n",
		"crates/app/src/main.rs":     "pub fn local() {}\n\nfn main() {\n    local();\n    util::shared();\n}\n",
		"crates/open/Cargo.toml":     "[package]\nname = \"open\"\n",
		"crates/open/src/lib.rs":     "pub fn api() {}\n\npub fn caller() {\n    api();\n}\n",
	})

	_, stdout, _ := run("check", "--format", "json", root)

	testutil.Lines(t, "too public", candidateLines(decode(t, stdout), "too-public"), []string{
		"rust too-public [crates/util/src/lib.rs Store::reset]",
		"rust too-public [crates/util/src/lib.rs helper]",
	})
}
