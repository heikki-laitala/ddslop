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
	if got := sites(r, 0); got != "lib/config.rs:load:1 lib/store/client.rs:open:2" {
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
