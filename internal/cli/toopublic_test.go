package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A public function of an unpublished crate that only its own library
// names can be pub(crate). Another crate's mention keeps it pub, as do
// the crate's own integration tests and binaries (crates of their own),
// a pub use re-exporting it, under its name or another, or its module
// (file, inline or #[path]) whole, and a #[macro_export] macro naming it; a
// pub(crate) use exposes nothing; a doctest and a binary the manifest
// places under src/ are crates of their own too; a trait
// method and a function nothing names (dead-code) are not reported.
func TestCheckReportsPublicFunctionsOnlyTheirCrateUses(t *testing.T) {
	root := repo(t, map[string]string{
		"Cargo.toml":              "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.package]\npublish = false\n",
		"crates/util/Cargo.toml":  "[package]\nname = \"util\"\npublish.workspace = true\n\n[[bin]]\nname = \"tool\"\npath = \"src/tool.rs\"\n",
		"crates/util/src/wire.rs": "pub fn wired_fn() -> u32 {\n    7\n}\n\nfn wire_user() -> u32 {\n    wired_fn()\n}\n",
		"crates/util/src/tool.rs": "fn main() {\n    println!(\"{}\", util::tool_helper());\n}\n",
		"crates/util/src/lib.rs": "mod inner;\nmod macros;\npub use inner::reexported_helper;\npub use inner::aliased as public_alias;\npub(crate) use inner::crate_reexport;\npub use shapes::*;\n#[path = \"wire.rs\"]\nmod wired;\npub use wired::*;\n\n" +
			"mod shapes {\n    pub fn inline_globbed() -> u32 {\n        2\n    }\n}\n\n" +
			"pub fn only_inside(text: &str) -> String {\n    text.trim().to_owned()\n}\n\n" +
			"fn caller(text: &str) -> String {\n    only_inside(text) + &crate_only(text) + &tested(text) + &binary_use(text)\n}\n\n" +
			"pub(crate) fn crate_only(text: &str) -> String {\n    text.to_owned()\n}\n\n" +
			"pub fn used_by_app(text: &str) -> bool {\n    text == \"true\"\n}\n\n" +
			"pub fn tested(text: &str) -> String {\n    text.to_lowercase()\n}\n\n" +
			"pub fn binary_use(text: &str) -> String {\n    text.to_uppercase()\n}\n\n" +
			"pub fn never_named(text: &str) -> usize {\n    text.len()\n}\n\n" +
			"/// Five.\n///\n/// ```\n/// assert_eq!(util::documented(), 5);\n/// ```\npub fn documented() -> u32 {\n    5\n}\n\n" +
			"pub fn tool_helper() -> u32 {\n    6\n}\n\n" +
			"pub trait Shout {\n    fn shout_loud(&self) -> String;\n}\n\n" +
			"impl Shout for String {\n    fn shout_loud(&self) -> String {\n        self.to_uppercase()\n    }\n}\n",
		"crates/util/src/inner.rs":  "pub fn reexported_helper() -> u32 {\n    1\n}\n\npub fn aliased() -> u32 {\n    3\n}\n\npub fn crate_reexport() -> u32 {\n    4\n}\n\nfn calls_it() -> u32 {\n    reexported_helper() + aliased() + crate_reexport() + super::shapes::inline_globbed()\n}\n",
		"crates/util/src/macros.rs": "pub fn macro_target(x: u32) -> u32 {\n    x + 1\n}\n\n#[macro_export]\nmacro_rules! bump {\n    ($x:expr) => {\n        $crate::macros::macro_target($x)\n    };\n}\n",
		"crates/util/src/main.rs":   "fn main() {\n    println!(\"{}\", util::binary_use(\"x\"));\n}\n",
		"crates/util/tests/it.rs":   "#[test]\nfn lowercases() {\n    assert_eq!(util::tested(\"A\"), \"a\");\n}\n",
		"crates/app/Cargo.toml":     "[package]\nname = \"app\"\npublish.workspace = true\n",
		"crates/app/src/main.rs":    "fn main() {\n    println!(\"{}\", util::used_by_app(\"true\"));\n}\n",
	})

	stdout, _ := runCode(t, 1, "check", "--format", "json", root)
	r := decodeJSON[targetReport](t, stdout)
	var got []string
	for _, c := range r.Candidates {
		if c.Kind == "too-public" || c.Kind == "dead-code" {
			o := c.Occurrences[0]
			got = append(got, fmt.Sprintf("%s %s %s:%d %s", c.Kind, c.Suggestion.Action, filepath.ToSlash(o.File), o.StartLine, o.Name))
		}
	}
	want := []string{
		"dead-code delete-dead crates/util/src/lib.rs:41 never_named",
		"too-public narrow-visibility crates/util/src/inner.rs:9 crate_reexport",
		"too-public narrow-visibility crates/util/src/lib.rs:17 only_inside",
	}
	slices.Sort(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("candidates:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
