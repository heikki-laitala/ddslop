package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A library whose manifest roots it at lib/core.rs, not src/lib.rs: store
// calls config, and config reads a constant of store's.
var movedLibrary = map[string]string{
	"Cargo.toml":          "[package]\nname = \"app\"\n\n[lib]\npath = \"lib/core.rs\"\n",
	"lib/core.rs":         "pub mod config;\npub mod store;\n",
	"lib/config.rs":       "pub fn load() -> u16 { crate::store::client::PORT }\n",
	"lib/store.rs":        "pub mod client;\n",
	"lib/store/client.rs": "pub const PORT: u16 = 1;\npub fn open() -> u16 { crate::config::load() }\n",
}

func TestCheckReportsCyclesInALibraryMovedOutOfSrc(t *testing.T) {
	_, stdout, stderr := run("check", "--format", "json", repo(t, movedLibrary))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want config and store depending on each other; stderr = %s", r.Candidates, stderr)
	}
	if got := strings.Join(r.Candidates[0].Values, ", "); got != "config -> store, store -> config" {
		t.Errorf("values = %s, want the moved library's modules", got)
	}
	if got := sites(r); got != "lib/config.rs:load:1 lib/store/client.rs:open:2" {
		t.Errorf("occurrences = %s, want config's path and store's call", got)
	}
}

func TestMapNamesTheModulesOfALibraryMovedOutOfSrc(t *testing.T) {
	code, stdout, stderr := run("map", "--format", "json", repo(t, movedLibrary))
	if code != 0 {
		t.Fatalf("map: exit %d, stderr %s", code, stderr)
	}
	var r mapOutput
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	var modules []string
	for _, m := range r.Maps {
		for _, l := range m.Layers {
			modules = append(modules, l.Modules...)
		}
	}
	slices.Sort(modules)
	if got := strings.Join(modules, " "); got != "config store" {
		t.Errorf("modules = %q, want config and store, the files under lib/", got)
	}
}

// A library rooted at the package's top claims its own modules, not the
// package's binary under src/.
func TestALibraryRootedAtThePackageTopLeavesTheBinaryAlone(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":  "[package]\nname = \"app\"\n\n[lib]\npath = \"core.rs\" # beside the manifest\n",
		"core.rs":     "pub mod config;\npub mod store;\n",
		"config.rs":   "pub fn load() -> u16 { crate::store::open() }\n",
		"store.rs":    "pub fn open() -> u16 { 1 }\n",
		"src/main.rs": "mod cli;\nfn main() { cli::run(); }\n",
		"src/cli.rs":  "pub fn run() { app::config::load(); }\n",
	}
	modules := mapModules(t, files)
	if slices.Contains(modules, "src") || slices.Contains(modules, "cli") || !slices.Contains(modules, "config") || !slices.Contains(modules, "store") {
		t.Errorf("modules = %v, want the library's config and store, and nothing of the binary's", modules)
	}
}

// A library its manifest roots under tests/ (autotests = false) is
// production code there: its cycles count without --tests.
func TestALibraryRootedUnderTestsIsNoTestCode(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":      "[package]\nname = \"app\"\nautotests = false\n\n[lib]\npath = \"tests/lib.rs\"\n",
		"tests/lib.rs":    "pub mod config;\npub mod store;\n",
		"tests/config.rs": "pub fn load() -> u16 { crate::store::open() }\n",
		"tests/store.rs":  "pub fn open() -> u16 { crate::config::load() }\n",
	}
	_, stdout, stderr := run("check", "--format", "json", repo(t, files))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 || strings.Join(r.Candidates[0].Values, ", ") != "config -> store, store -> config" {
		t.Errorf("candidates = %+v, want config and store calling each other; stderr = %s", r.Candidates, stderr)
	}
}

// A binary the manifest roots beside a moved library keeps the modules it
// declares: tool.rs's mod cli is the binary's, not the library's.
func TestAPlacedBinaryKeepsItsModulesFromAMovedLibrary(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n\n[lib]\npath = \"core.rs\"\n\n[[bin]]\nname = \"tool\"\npath = \"tool.rs\"\n",
		"core.rs":    "pub mod config;\npub mod store;\n",
		"config.rs":  "pub fn load() -> u16 { crate::store::open() }\n",
		"store.rs":   "pub fn open() -> u16 { 1 }\n",
		"tool.rs":    "mod cli;\nfn main() { cli::run(); }\n",
		"cli.rs":     "pub fn run() { app::config::load(); }\n",
	}
	modules := mapModules(t, files)
	if slices.Contains(modules, "cli") || slices.Contains(modules, "tool") {
		t.Errorf("modules = %v, want none of the binary's", modules)
	}
}

// A placed binary of an unpublished package is internal too: its unused
// public function is dead code.
func TestAPlacedTargetReadsItsPackagesPublishSetting(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\npublish = false\n\n[lib]\npath = \"core.rs\"\n\n[[bin]]\nname = \"tool\"\npath = \"tool.rs\"\n",
		"core.rs":    "pub fn used() -> u8 { 1 }\n",
		"tool.rs":    "fn main() { app::used(); }\npub fn never_called_anywhere() -> u8 { 2 }\n",
	}
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := decodeTargets(t, stdout)
	var dead []string
	for _, c := range r.Candidates {
		if c.Kind == "dead-code" {
			dead = append(dead, c.Occurrences[0].Name)
		}
	}
	if strings.Join(dead, " ") != "never_called_anywhere" {
		t.Errorf("dead code = %v, want the binary's unused public function", dead)
	}
}

// A module a placed binary declares with #[path] is the binary's where
// the attribute puts it.
func TestAPlacedBinarysPathModuleIsNotTheLibrarys(t *testing.T) {
	files := map[string]string{
		"Cargo.toml":      "[package]\nname = \"app\"\n\n[lib]\npath = \"core.rs\"\n\n[[bin]]\nname = \"tool\"\npath = \"tool.rs\"\n",
		"core.rs":         "pub mod config;\npub mod store;\n",
		"config.rs":       "pub fn load() -> u16 { crate::store::open() }\n",
		"store.rs":        "pub fn open() -> u16 { 1 }\n",
		"tool.rs":         "#[path = \"commands/cli.rs\"]\nmod cli;\nfn main() { cli::run(); }\n",
		"commands/cli.rs": "pub fn run() { app::config::load(); }\n",
	}
	modules := mapModules(t, files)
	if slices.Contains(modules, "commands") || slices.Contains(modules, "cli") {
		t.Errorf("modules = %v, want none of the binary's", modules)
	}
}

// mapModules runs map over files and returns the modules of its layers.
func mapModules(t *testing.T, files map[string]string) []string {
	t.Helper()
	_, stdout, stderr := run("map", "--format", "json", repo(t, files))
	var r mapOutput
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not JSON: %v\n%s\n%s", err, stdout, stderr)
	}
	var modules []string
	for _, m := range r.Maps {
		for _, l := range m.Layers {
			modules = append(modules, l.Modules...)
		}
	}
	return modules
}
