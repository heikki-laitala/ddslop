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
