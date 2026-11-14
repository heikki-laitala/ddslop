package cli

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// sites writes a candidate's occurrences as file:name:line.
func sites(c cycleReport, i int) string {
	var out []string
	for _, o := range c.Candidates[i].Occurrences {
		out = append(out, fmt.Sprintf("%s:%s:%d", filepath.ToSlash(o.File), o.Name, o.StartLine))
	}
	return strings.Join(out, " ")
}

// settle and capacity read each other's constants and call nothing: a
// cycle a call graph cannot see.
var constantCycle = map[string]string{
	"Cargo.toml": "[package]\nname = \"app\"\n",
	"src/lib.rs": "pub mod capacity;\npub mod settle;\n",
	"src/settle.rs": `pub const LATE_REVISIONS: usize = 2;
pub const REVISION_CHARGE: u64 = crate::capacity::EVENT_CHARGE + 1;
`,
	"src/capacity.rs": `pub const EVENT_CHARGE: u64 = 6_112;
pub const REACHABLE: usize = 1 + crate::settle::LATE_REVISIONS;
`,
}

func TestCheckReportsModulesReferringToEachOther(t *testing.T) {
	_, stdout, stderr := run("check", "--format", "json", repo(t, constantCycle))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the constants' cycle; stderr = %s", r.Candidates, stderr)
	}
	if got := strings.Join(r.Candidates[0].Values, ", "); got != "capacity -> settle, settle -> capacity" {
		t.Errorf("values = %s, want capacity and settle depending on each other", got)
	}
	if got := sites(r, 0); got != "src/capacity.rs:REACHABLE:2 src/settle.rs:REVISION_CHARGE:2" {
		t.Errorf("occurrences = %s, want the two referring lines", got)
	}
}

// settle calls capacity; capacity only builds settle's type and adds a
// method to it. A type is a dependency too.
func TestCheckReportsACycleClosedByAType(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod capacity;\npub mod settle;\n",
		"src/settle.rs": `pub struct Usage { pub held: u64 }
pub fn retained() -> u64 { crate::capacity::reading().held }
`,
		"src/capacity.rs": `pub fn reading() -> crate::settle::Usage {
    crate::settle::Usage { held: 1 }
}
impl crate::settle::Usage {
    pub fn total(&self) -> u64 { self.held }
}
`,
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the cycle a type closes", r.Candidates)
	}
	if got := sites(r, 0); got != "src/capacity.rs:reading:1 src/settle.rs:retained:2" {
		t.Errorf("occurrences = %s, want the type's use and the call", got)
	}
}

// super is read from where the path is written: from store/client.rs,
// super::super::config is the config module, and super::pool is store's
// own.
func TestReferencesThroughSuperReachTheModuleTheyName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":          "[package]\nname = \"app\"\n",
		"src/lib.rs":          "pub mod config;\npub mod store;\n",
		"src/config.rs":       "pub fn load() { crate::store::open(); }\npub const PORT: u16 = 1;\n",
		"src/store.rs":        "pub mod client;\npub mod pool;\npub fn open() {}\n",
		"src/store/pool.rs":   "pub const SIZE: usize = 4;\n",
		"src/store/client.rs": "pub const PORT: u16 = super::super::config::PORT;\npub const SIZE: usize = super::pool::SIZE;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 || strings.Join(r.Candidates[0].Values, ", ") != "config -> store, store -> config" {
		t.Fatalf("candidates = %+v, want config and store depending on each other", r.Candidates)
	}
	files["src/store/client.rs"] = "pub const SIZE: usize = super::pool::SIZE;\n"
	_, stdout, _ = run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: super::pool stays in store", r.Candidates)
	}
}

// A test module's references count only with --tests, as its calls do.
func TestTestCodesReferencesCountOnlyWithTests(t *testing.T) {
	files := maps.Clone(constantCycle)
	files["src/capacity.rs"] = `pub const EVENT_CHARGE: u64 = 6_112;
#[cfg(test)]
mod tests {
    const REACHABLE: usize = 1 + crate::settle::LATE_REVISIONS;
}
`
	root := repo(t, files)
	_, stdout, _ := run("check", "--format", "json", root)
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none without --tests", r.Candidates)
	}
	_, stdout, _ = run("check", "--format", "json", "--tests", root)
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("candidates = %+v, want the test module's cycle with --tests", r.Candidates)
	}
}

func TestAnIgnoreOnAReferenceLeavesItsCycleOut(t *testing.T) {
	files := maps.Clone(constantCycle)
	files["src/capacity.rs"] = `pub const EVENT_CHARGE: u64 = 6_112;
// ddslop:ignore capacity counts settle's late revisions on purpose
pub const REACHABLE: usize = 1 + crate::settle::LATE_REVISIONS;
`
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want the cycle marked at its reference left out", r.Candidates)
	}
}

// A binary under src/bin is a crate of its own: its crate:: paths name
// its own modules, not the library's.
func TestABinarysCratePathsAreNotTheLibrarys(t *testing.T) {
	files := maps.Clone(constantCycle)
	files["src/capacity.rs"] = "pub const EVENT_CHARGE: u64 = 6_112;\n"
	files["src/settle.rs"] = "pub const REVISION_CHARGE: u64 = crate::capacity::EVENT_CHARGE + 1;\n"
	files["src/bin/tool.rs"] = "mod capacity { pub const X: u8 = 1; }\nfn main() { let _ = crate::capacity::X; app::settle::REVISION_CHARGE; }\n"
	files["src/lib.rs"] += "pub fn root() -> u8 { 1 }\n"
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: the binary's crate::capacity is its own", r.Candidates)
	}
}

// src/main.rs beside a library, build.rs and examples are Cargo targets of
// their own: their crate:: paths name their own modules, so a binary's
// mod a is not the library's a.
func TestOtherCargoTargetsPathsAreNotTheLibrarys(t *testing.T) {
	library := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod a;\npub const VERSION: u8 = 1;\n",
		"src/a.rs":   "pub const V: u8 = crate::VERSION;\n",
	}
	target := "mod a { pub const X: u8 = 1; }\nfn main() { let _ = crate::a::X; }\n"
	for _, path := range []string{"src/main.rs", "build.rs", "examples/demo.rs"} {
		files := maps.Clone(library)
		files[path] = target
		_, stdout, _ := run("check", "--format", "json", repo(t, files))
		if r := cycles(t, stdout); len(r.Candidates) != 0 {
			t.Errorf("%s: candidates = %+v, want none: its crate::a is its own", path, r.Candidates)
		}
	}
}

// use crate::* in a top-level module imports the root module's items: a
// dependency on the root.
func TestAWildcardImportOfTheRootIsADependencyOnIt(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod a;\npub const VERSION: u8 = crate::a::V;\n",
		"src/a.rs":   "use crate::*;\npub const V: u8 = 1;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the root and a depending on each other", r.Candidates)
	}
	if got := sites(r, 0); got != "src/lib.rs:VERSION:2 src/a.rs:use:1" {
		t.Errorf("occurrences = %s, want the root's path and a's wildcard import", got)
	}
}

// use crate as root names the root module: paths through the alias are
// dependencies on it.
func TestAnAliasOfTheRootIsADependencyOnIt(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod a;\npub const Y: u8 = crate::a::X;\n",
		"src/a.rs":   "use crate as root;\npub const X: u8 = 1;\npub const Z: u8 = root::Y;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("candidates = %+v, want the root and a depending on each other", r.Candidates)
	}
}

// crate::foo naming the root's function foo is a dependency on the root,
// not on the module foo beside it: Rust keeps the two apart.
func TestARootValueIsNotTheModuleOfItsName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod bar;\npub mod foo;\npub fn foo() -> u8 { 1 }\n",
		"src/bar.rs": "pub fn b() -> u8 { crate::foo() }\n",
		"src/foo.rs": "pub fn f() -> u8 { crate::bar::b() }\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: bar calls the root's foo, not the module", r.Candidates)
	}
}

// use crate::foo imports the module foo as well as the root's fn foo:
// a dependency on the module.
func TestAUseOfANameBothRootValueAndModuleDependsOnTheModule(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod bar;\npub mod foo;\npub fn foo() -> u8 { 1 }\n",
		"src/bar.rs": "use crate::foo;\npub const B: u8 = foo::X;\n",
		"src/foo.rs": "pub const X: u8 = 1;\npub const Y: u8 = crate::bar::B;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("candidates = %+v, want bar and foo depending on each other", r.Candidates)
	}
}

// mod r#type lives in type.rs: crate::r#type names the module type.
func TestARawIdentifierNamesItsModule(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":  "[package]\nname = \"app\"\n",
		"src/lib.rs":  "pub mod a;\npub mod r#type;\n",
		"src/type.rs": "pub const X: u8 = crate::a::Y;\n",
		"src/a.rs":    "pub const Y: u8 = 1;\npub const Z: u8 = crate::r#type::X;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 || strings.Join(r.Candidates[0].Values, ", ") != "a -> type, type -> a" {
		t.Errorf("candidates = %+v, want a and type depending on each other", r.Candidates)
	}
}

// pub fn r#type beside mod r#type: crate::r#type() calls the root's fn,
// not the module.
func TestARawRootValueIsNotTheModuleOfItsName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":  "[package]\nname = \"app\"\n",
		"src/lib.rs":  "pub mod bar;\npub mod r#type;\npub fn r#type() -> u8 { 1 }\n",
		"src/bar.rs":  "pub fn b() -> u8 { crate::r#type() }\n",
		"src/type.rs": "pub fn t() -> u8 { crate::bar::b() }\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: bar calls the root's fn r#type", r.Candidates)
	}
}

// crate::foo!() names the root's macro foo, never the module foo: macros
// and modules are apart.
func TestARootMacroIsNotTheModuleOfItsName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod bar;\npub mod foo;\n#[macro_export]\nmacro_rules! foo { () => { 1 } }\n",
		"src/bar.rs": "pub fn b() -> u8 { crate::foo!() }\n",
		"src/foo.rs": "pub fn f() -> u8 { crate::bar::b() }\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: bar invokes the root's macro foo", r.Candidates)
	}
}

// Through use crate as root, root::foo::Y is a dependency on foo.
func TestAPathThroughARootAliasEntersTheModule(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod a;\npub mod foo;\n",
		"src/a.rs":   "use crate as root;\npub const A: u8 = 1;\npub const X: u8 = root::foo::Y;\n",
		"src/foo.rs": "pub const Y: u8 = 1;\npub const Z: u8 = crate::a::A;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 || strings.Join(r.Candidates[0].Values, ", ") != "a -> foo, foo -> a" {
		t.Errorf("candidates = %+v, want a and foo depending on each other", r.Candidates)
	}
}

// A library module named bin and a binary under src/bin share a module
// name, but the binary is a crate of its own: its calls into the library
// close no cycle of the library's.
func TestABinarysCallsCloseNoCycleOfTheLibrarys(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":      "[package]\nname = \"app\"\n",
		"src/lib.rs":      "pub mod bin;\npub fn root() -> u8 { crate::bin::VALUE }\n",
		"src/bin.rs":      "pub const VALUE: u8 = 1;\n",
		"src/bin/tool.rs": "fn main() { app::root(); }\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: the binary's call is not the library's", r.Candidates)
	}
}

// extern crate self as root at the crate root names the crate in every
// module: root::foo::Y in a.rs is a dependency on foo.
func TestAnExternCrateSelfAliasNamesTheCrateEverywhere(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub mod foo;\n",
		"src/a.rs":   "pub const A: u8 = 1;\npub const X: u8 = root::foo::Y;\n",
		"src/foo.rs": "pub const Y: u8 = 1;\npub const Z: u8 = crate::a::A;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 || strings.Join(r.Candidates[0].Values, ", ") != "a -> foo, foo -> a" {
		t.Errorf("candidates = %+v, want a and foo depending on each other", r.Candidates)
	}
}

// wrap!(crate::foo!()) invokes the root's macro foo, not the module foo.
func TestANestedRootMacroIsNotTheModuleOfItsName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod bar;\npub mod foo;\n#[macro_export]\nmacro_rules! foo { () => { 1 } }\n",
		"src/bar.rs": "pub fn b() -> u8 { std::convert::identity(crate::foo!()) + wrap!(crate::foo!()) }\n",
		"src/foo.rs": "pub fn f() -> u8 { crate::bar::b() }\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: bar invokes the root's macro foo", r.Candidates)
	}
}

// A module root in a file hides the crate's alias root there: root::foo
// enters that module, not the crate's foo.
func TestALocalModuleHidesTheCratesAlias(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub mod foo;\n",
		"src/a.rs":   "mod root { pub mod foo { pub const X: u8 = 1; } }\npub const A: u8 = 1;\npub const X: u8 = root::foo::X;\n",
		"src/foo.rs": "pub const Z: u8 = crate::a::A;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: a's root is its own module", r.Candidates)
	}
}

// The crate's alias reaches macro arguments too: wrap!(root::foo::Y).
func TestTheCratesAliasReachesMacroArguments(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub mod foo;\n",
		"src/a.rs":   "pub const A: u8 = 1;\npub fn x() -> u8 { wrap!(root::foo::Y) }\n",
		"src/foo.rs": "pub const Y: u8 = 1;\npub const Z: u8 = crate::a::A;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("candidates = %+v, want a and foo depending on each other", r.Candidates)
	}
}

// use root::* imports the crate's items through its alias: a dependency
// on the root.
func TestAWildcardImportThroughTheCratesAliasDependsOnTheRoot(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub const R: u8 = crate::a::A;\n",
		"src/a.rs":   "use root::*;\npub const A: u8 = 1;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("candidates = %+v, want the root and a depending on each other", r.Candidates)
	}
}

// A generic parameter root hides the crate's alias root in its item.
func TestAGenericParameterHidesTheCratesAlias(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub mod foo;\n",
		"src/a.rs":   "pub trait Tr { type foo; }\npub const A: u8 = 1;\npub fn h<root: Tr>() -> u8 { let _: Option<root::foo> = None; 1 }\n",
		"src/foo.rs": "pub const Z: u8 = crate::a::A;\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: root::foo is h's type parameter's", r.Candidates)
	}
}
