package cli

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// sites writes the first candidate's occurrences as file:name:line.
func sites(c cycleReport) string {
	var out []string
	for _, o := range c.Candidates[0].Occurrences {
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
	r := wantCycleBetween(t, "capacity -> settle, settle -> capacity", repo(t, constantCycle))
	if got := sites(r); got != "src/capacity.rs:REACHABLE:2 src/settle.rs:REVISION_CHARGE:2" {
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
	r := wantCycles(t, 1, "the cycle a type closes", repo(t, files))
	if got := sites(r); got != "src/capacity.rs:reading:1 src/settle.rs:retained:2" {
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
	wantCycleBetween(t, "config -> store, store -> config", repo(t, files))
	files["src/store/client.rs"] = "pub const SIZE: usize = super::pool::SIZE;\n"
	wantCycles(t, 0, "none: super::pool stays in store", repo(t, files))
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
	wantCycles(t, 0, "none without --tests", root)
	wantCycles(t, 1, "the test module's cycle with --tests", "--tests", root)
}

func TestAnIgnoreOnAReferenceLeavesItsCycleOut(t *testing.T) {
	files := maps.Clone(constantCycle)
	files["src/capacity.rs"] = `pub const EVENT_CHARGE: u64 = 6_112;
// ddslop:ignore capacity counts settle's late revisions on purpose
pub const REACHABLE: usize = 1 + crate::settle::LATE_REVISIONS;
`
	wantCycles(t, 0, "the cycle marked at its reference left out", repo(t, files))
}

// A binary under src/bin is a crate of its own: its crate:: paths name
// its own modules, not the library's.
func TestABinarysCratePathsAreNotTheLibrarys(t *testing.T) {
	files := maps.Clone(constantCycle)
	files["src/capacity.rs"] = "pub const EVENT_CHARGE: u64 = 6_112;\n"
	files["src/settle.rs"] = "pub const REVISION_CHARGE: u64 = crate::capacity::EVENT_CHARGE + 1;\n"
	files["src/bin/tool.rs"] = "mod capacity { pub const X: u8 = 1; }\nfn main() { let _ = crate::capacity::X; app::settle::REVISION_CHARGE; }\n"
	files["src/lib.rs"] += "pub fn root() -> u8 { 1 }\n"
	wantCycles(t, 0, "none: the binary's crate::capacity is its own", repo(t, files))
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
	r := wantCycles(t, 1, "the root and a depending on each other", repo(t, files))
	if got := sites(r); got != "src/lib.rs:VERSION:2 src/a.rs:use:1" {
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
	wantCycles(t, 1, "the root and a depending on each other", repo(t, files))
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
	wantCycles(t, 0, "none: bar calls the root's foo, not the module", repo(t, files))
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
	wantCycles(t, 1, "bar and foo depending on each other", repo(t, files))
}

// mod r#type lives in type.rs: crate::r#type names the module type.
func TestARawIdentifierNamesItsModule(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":  "[package]\nname = \"app\"\n",
		"src/lib.rs":  "pub mod a;\npub mod r#type;\n",
		"src/type.rs": "pub const X: u8 = crate::a::Y;\n",
		"src/a.rs":    "pub const Y: u8 = 1;\npub const Z: u8 = crate::r#type::X;\n",
	}
	wantCycleBetween(t, "a -> type, type -> a", repo(t, files))
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
	wantCycles(t, 0, "none: bar calls the root's fn r#type", repo(t, files))
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
	wantCycles(t, 0, "none: bar invokes the root's macro foo", repo(t, files))
}

// Through use crate as root, root::foo::Y is a dependency on foo.
func TestAPathThroughARootAliasEntersTheModule(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod a;\npub mod foo;\n",
		"src/a.rs":   "use crate as root;\npub const A: u8 = 1;\npub const X: u8 = root::foo::Y;\n",
		"src/foo.rs": "pub const Y: u8 = 1;\npub const Z: u8 = crate::a::A;\n",
	}
	wantCycleBetween(t, "a -> foo, foo -> a", repo(t, files))
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
	wantCycles(t, 0, "none: the binary's call is not the library's", repo(t, files))
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
	wantCycleBetween(t, "a -> foo, foo -> a", repo(t, files))
}

// wrap!(crate::foo!()) invokes the root's macro foo, not the module foo.
func TestANestedRootMacroIsNotTheModuleOfItsName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod bar;\npub mod foo;\n#[macro_export]\nmacro_rules! foo { () => { 1 } }\n",
		"src/bar.rs": "pub fn b() -> u8 { std::convert::identity(crate::foo!()) + wrap!(crate::foo!()) }\n",
		"src/foo.rs": "pub fn f() -> u8 { crate::bar::b() }\n",
	}
	wantCycles(t, 0, "none: bar invokes the root's macro foo", repo(t, files))
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
	wantCycles(t, 0, "none: a's root is its own module", repo(t, files))
}

// The crate's alias reaches macro arguments too: wrap!(root::foo::Y).
func TestTheCratesAliasReachesMacroArguments(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub mod foo;\n",
		"src/a.rs":   "pub const A: u8 = 1;\npub fn x() -> u8 { wrap!(root::foo::Y) }\n",
		"src/foo.rs": "pub const Y: u8 = 1;\npub const Z: u8 = crate::a::A;\n",
	}
	wantCycles(t, 1, "a and foo depending on each other", repo(t, files))
}

// use root::* imports the crate's items through its alias: a dependency
// on the root.
func TestAWildcardImportThroughTheCratesAliasDependsOnTheRoot(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub const R: u8 = crate::a::A;\n",
		"src/a.rs":   "use root::*;\npub const A: u8 = 1;\n",
	}
	wantCycles(t, 1, "the root and a depending on each other", repo(t, files))
}

// A generic parameter root hides the crate's alias root in its item.
func TestAGenericParameterHidesTheCratesAlias(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "extern crate self as root;\npub mod a;\npub mod foo;\n",
		"src/a.rs":   "pub trait Tr { type foo; }\npub const A: u8 = 1;\npub fn h<root: Tr>() -> u8 { let _: Option<root::foo> = None; 1 }\n",
		"src/foo.rs": "pub const Z: u8 = crate::a::A;\n",
	}
	wantCycles(t, 0, "none: root::foo is h's type parameter's", repo(t, files))
}

// In the crate root, foo::Y names the child module foo: a dependency on
// it, as crate::foo::Y is.
func TestAPathThroughAChildModuleDependsOnIt(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod foo;\npub const ROOT: u8 = foo::Y;\npub const R: u8 = 1;\n",
		"src/foo.rs": "pub const Y: u8 = crate::R;\n",
	}
	wantCycles(t, 1, "the root and foo depending on each other", repo(t, files))
}

// A crate root re-exporting its children (pub use client::*) while they
// use its items is Rust's facade, not a cycle: a re-export depends on
// nothing.
func TestAReexportIsNoDependency(t *testing.T) {
	for _, reexport := range []string{"pub use client::*;", "pub use crate::client::Client;", "pub(crate) use self::client::Client;"} {
		files := map[string]string{
			"Cargo.toml":    "[package]\nname = \"app\"\n",
			"src/lib.rs":    "pub mod client;\n" + reexport + "\npub struct Refusal;\n",
			"src/client.rs": "use crate::Refusal;\npub struct Client(pub Refusal);\n",
		}
		_, stdout, _ := run("check", "--format", "json", repo(t, files))
		if r := cycles(t, stdout); len(r.Candidates) != 0 {
			t.Errorf("%s: candidates = %+v, want none: a re-export is the facade", reexport, r.Candidates)
		}
	}
	// A plain use is still a dependency.
	files := map[string]string{
		"Cargo.toml":    "[package]\nname = \"app\"\n",
		"src/lib.rs":    "pub mod client;\nuse client::Client;\npub struct Refusal;\npub fn make() -> Client { Client(Refusal) }\n",
		"src/client.rs": "use crate::Refusal;\npub struct Client(pub Refusal);\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("plain use: candidates = %+v, want the root and client depending on each other", r.Candidates)
	}
}

// A root that re-exports a value foo beside its module foo: crate::foo()
// names the value, not the module.
func TestARootReexportedValueIsNotTheModuleOfItsName(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":    "[package]\nname = \"app\"\n",
		"src/lib.rs":    "pub mod bar;\npub mod foo;\npub mod values;\npub use values::foo;\n",
		"src/values.rs": "pub fn foo() -> u8 { 1 }\n",
		"src/bar.rs":    "pub fn b() -> u8 { crate::foo() }\n",
		"src/foo.rs":    "pub fn f() -> u8 { crate::bar::b() }\n",
	}
	wantCycles(t, 0, "none: crate::foo is the re-exported value", repo(t, files))
}

// extern crate self as root inside a function of the crate root holds in
// that block alone: another module's root is not this crate.
func TestABlockLocalCrateAliasIsNotCrateWide(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod a;\npub mod foo;\nfn f() { extern crate self as root; }\n",
		"src/a.rs":   "pub const A: u8 = 1;\npub const X: u8 = root::foo::X;\n",
		"src/foo.rs": "pub const X: u8 = 1;\npub const Z: u8 = crate::a::A;\n",
	}
	wantCycles(t, 0, "none: a's root is not this crate", repo(t, files))
}

// reexportingCrate's root re-exports refusal's type and calls request,
// which names the type through the root: crate::Refusal.
func reexportingCrate(refusal string) map[string]string {
	return map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "mod refusal;\nmod request;\npub use refusal::Refusal;\npub fn encode() -> u8 { request::encode() }\n",
		"src/request.rs": `use crate::Refusal;
pub fn encode() -> u8 { refuse(Refusal::Empty) }
pub fn refuse(_: Refusal) -> u8 { 1 }
`,
		"src/refusal.rs": refusal,
	}
}

// A name the root re-exports is its module's, not the root's: request,
// naming crate::Refusal, depends on refusal, so the root calling request
// closes no loop. Where refusal depends on request in turn, that is the
// loop, between the two modules.
func TestARootReexportIsADependencyOnItsModule(t *testing.T) {
	wantCycles(t, 0, "the root re-exports refusal's type; request uses it",
		repo(t, reexportingCrate("pub enum Refusal { Empty }\n")))

	wantCycleBetween(t, "refusal -> request, request -> refusal",
		repo(t, reexportingCrate("pub enum Refusal { Empty }\npub fn code() -> u8 { crate::request::refuse(Refusal::Empty) }\n")))

	// A name the root imports from another crate is no module of its own.
	external := reexportingCrate("pub enum Refusal { Empty }\n")
	external["src/lib.rs"] = "mod refusal;\nmod request;\npub use other::Refusal;\npub fn encode() -> u8 { request::encode() }\n"
	wantCycles(t, 0, "the root re-exports another crate's type; request uses it", repo(t, external))
}

// Root imports, as Rust scopes them: an inline module's use is its own,
// not the root's; an alias of the root's own item is the root's; and a
// macro the root imports is its module's.
func TestRootImportsAreTheRootsOwn(t *testing.T) {
	crate := func(lib, child string, more ...string) map[string]string {
		files := map[string]string{"Cargo.toml": "[package]\nname = \"app\"\n", "src/lib.rs": lib, "src/child.rs": child}
		for i := 0; i+1 < len(more); i += 2 {
			files[more[i]] = more[i+1]
		}
		return files
	}
	t.Run("an inline module's use is its own", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\nmod foo;\npub struct Thing;\nmod nested {\n    use crate::foo::Thing;\n    pub fn n(_: Thing) {}\n}\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _t: Option<crate::Thing> = None; 1 }\n",
			"src/foo.rs", "pub struct Thing;\n")))
	})
	t.Run("an alias of the root's own item is the root's", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\npub struct Error;\npub use crate::Error as PublicError;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _e: Option<crate::PublicError> = None; 1 }\n")))
	})
	t.Run("a use in a root function is that function's", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\nmod foo;\npub struct Thing;\npub fn local() {\n    use crate::foo::Thing;\n    let _t: Option<Thing> = None;\n}\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _t: Option<crate::Thing> = None; 1 }\n",
			"src/foo.rs", "pub struct Thing;\n")))
	})
	t.Run("a path through a module is the module's, though a function shares its name", func(t *testing.T) {
		wantCycleBetween(t, "child -> one, one -> child", repo(t, crate(
			"mod child;\nmod one;\nmod values;\npub use values::one;\n",
			"pub fn f() -> u8 { let _t: Option<crate::one::Thing> = None; 1 }\n",
			"src/one.rs", "pub struct Thing;\npub fn g() -> u8 { crate::child::f() }\n",
			"src/values.rs", "pub fn one() -> u8 { 1 }\n")))
	})
	t.Run("a chained re-export reaches the module it started from", func(t *testing.T) {
		wantCycleBetween(t, "child -> foo, foo -> child", repo(t, crate(
			"mod child;\nmod foo;\npub use foo::X;\npub use X as Y;\n",
			"pub fn f() -> u8 { let _y: Option<crate::Y> = None; 1 }\n",
			"src/foo.rs", "pub struct X;\npub fn g() -> u8 { crate::child::f() }\n")))
	})
	t.Run("a root item is the root's though a nested use shares its name", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\nmod foo;\npub fn make() -> u8 { 1 }\npub use make as public_make;\nmod nested {\n    use crate::foo::make;\n    pub fn n() -> u8 { make() }\n}\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { crate::public_make() }\n",
			"src/foo.rs", "pub fn make() -> u8 { 2 }\n")))
	})
	t.Run("a chained re-export passes an inline module's type of that name", func(t *testing.T) {
		wantCycleBetween(t, "child -> foo, foo -> child", repo(t, crate(
			"mod child;\nmod foo;\nmod nested {\n    pub struct X;\n}\npub use foo::X;\npub use X as Y;\n",
			"pub fn f() -> u8 { let _y: Option<crate::Y> = None; 1 }\n",
			"src/foo.rs", "pub struct X;\npub fn g() -> u8 { crate::child::f() }\n")))
	})
	t.Run("an alias of the crate itself is the root", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\npub struct Thing;\npub(crate) use crate as facade;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _t: Option<crate::facade::Thing> = None; 1 }\n")))
	})
	t.Run("a long chain of re-exports reaches the module it started from", func(t *testing.T) {
		wantCycleBetween(t, "child -> foo, foo -> child", repo(t, crate(
			"mod child;\nmod foo;\npub use foo::X as X1;\npub use X1 as X2;\npub use X2 as X3;\npub use X3 as X4;\npub use X4 as X5;\npub use X5 as X6;\npub use X6 as X7;\npub use X7 as X8;\npub use X8 as X9;\npub use X9 as X10;\n",
			"pub fn f() -> u8 { let _y: Option<crate::X10> = None; 1 }\n",
			"src/foo.rs", "pub struct X;\npub fn g() -> u8 { crate::child::f() }\n")))
	})
	t.Run("an alias loop ends", func(t *testing.T) {
		wantCycles(t, 0, "two aliases naming each other lead nowhere", repo(t, crate(
			"mod child;\npub use A as B;\npub use B as A;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _a: Option<crate::A> = None; 1 }\n")))
	})
	t.Run("a glob re-export is the module that defines the name", func(t *testing.T) {
		wantCycles(t, 0, "the root re-exports refusal's items by glob; child uses one", repo(t, crate(
			"mod child;\nmod refusal;\npub use refusal::*;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _r: Option<crate::Refusal> = None; 1 }\n",
			"src/refusal.rs", "pub enum Refusal { Empty }\n")))
	})
	t.Run("a test-only root import is not production's", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\nmod foo;\n#[cfg(test)]\nuse crate::foo::Thing;\n#[cfg(not(test))]\npub struct Thing;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _t: Option<crate::Thing> = None; 1 }\n",
			"src/foo.rs", "pub struct Thing;\n")))
	})
	t.Run("a re-export from an inline module is the root's", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\nmod nested {\n    pub struct Thing;\n}\npub use nested::Thing;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _t: Option<crate::Thing> = None; 1 }\n")))
	})
	t.Run("a glob re-export follows its module's own re-exports", func(t *testing.T) {
		wantCycleBetween(t, "child -> foo, foo -> child", repo(t, crate(
			"mod child;\nmod facade;\nmod foo;\npub use facade::*;\n",
			"pub fn f() -> u8 { let _x: Option<crate::X> = None; 1 }\n",
			"src/facade.rs", "pub use crate::foo::X;\n",
			"src/foo.rs", "pub struct X;\npub fn g() -> u8 { crate::child::f() }\n")))
	})
	t.Run("a glob's own use of a name another glob defines is no re-export of it", func(t *testing.T) {
		wantCycleBetween(t, "client -> envelope, envelope -> client", repo(t, map[string]string{
			"Cargo.toml":      "[package]\nname = \"app\"\n",
			"src/lib.rs":      "mod client;\nmod envelope;\npub use client::*;\npub use envelope::*;\n",
			"src/client.rs":   "use crate::Response;\npub enum Failure { Down }\npub fn call() -> Option<Response> { None }\n",
			"src/envelope.rs": "pub struct Response;\npub fn fail() -> Option<crate::Failure> { None }\n",
		}))
	})
	t.Run("a comment between cfg(test) and its use keeps the use test code's", func(t *testing.T) {
		wantCycleBetween(t, ". -> child, child -> .", repo(t, crate(
			"mod child;\nmod foo;\n#[cfg(test)]\n// only the tests take foo's\nuse crate::foo::Thing;\n#[cfg(not(test))]\npub struct Thing;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { let _t: Option<crate::Thing> = None; 1 }\n",
			"src/foo.rs", "pub struct Thing;\n")))
	})
	t.Run("a macro the root imports is its module's", func(t *testing.T) {
		wantCycles(t, 0, "child invokes the macro the root imports from macros", repo(t, crate(
			"#[macro_use]\nmod macros;\nmod child;\npub(crate) use macros::one;\npub fn run() -> u8 { child::f() }\n",
			"pub fn f() -> u8 { crate::one!() }\n",
			"src/macros.rs", "macro_rules! one_ { () => { 1 } }\npub(crate) use one_ as one;\n")))
	})
}
