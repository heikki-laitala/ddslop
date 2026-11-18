package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
)

type cycleReport struct {
	Candidates []struct {
		Kind        string   `json:"kind"`
		Concern     string   `json:"concern"`
		Values      []string `json:"values"`
		Occurrences []struct {
			File      string `json:"file"`
			Name      string `json:"name"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		} `json:"occurrences"`
		Suggestion struct {
			Action string `json:"action"`
		} `json:"suggestion"`
	} `json:"candidates"`
}

// cycles keeps a report's dependency-cycle candidates.
func cycles(t *testing.T, stdout string) cycleReport {
	t.Helper()
	var r cycleReport
	decodeInto(t, stdout, &r)
	kept := r.Candidates[:0]
	for _, c := range r.Candidates {
		if c.Kind == "dependency-cycle" {
			kept = append(kept, c)
		}
	}
	r.Candidates = kept
	return r
}

// wantCycles runs check --format json with args (flags, then the root)
// and stops the test unless it reports want dependency cycles, saying why.
func wantCycles(t *testing.T, want int, why string, args ...string) cycleReport {
	t.Helper()
	_, stdout, stderr := run(append([]string{"check", "--format", "json"}, args...)...)
	r := cycles(t, stdout)
	if len(r.Candidates) != want {
		t.Fatalf("candidates = %+v, want %d: %s; stderr = %s", r.Candidates, want, why, stderr)
	}
	return r
}

// wantCycleBetween runs check as wantCycles does and stops the test
// unless it reports one cycle, closed by the dependencies in values.
func wantCycleBetween(t *testing.T, values string, args ...string) cycleReport {
	t.Helper()
	r := wantCycles(t, 1, values, args...)
	if got := strings.Join(r.Candidates[0].Values, ", "); got != values {
		t.Fatalf("values = %s, want %s", got, values)
	}
	return r
}

func TestCheckReportsModulesCallingEachOther(t *testing.T) {
	stdout, _ := runCode(t, 1, "check", "--format", "json", repo(t, cyclicCrate))
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want one dependency cycle", r.Candidates)
	}
	c := r.Candidates[0]
	if c.Concern != "layers" || c.Suggestion.Action != "break-cycle" || strings.Join(c.Values, ", ") != "config -> store, store -> config" {
		t.Errorf("candidate = %+v, want config and store calling each other, to break", c)
	}
	var sites []string
	for _, o := range c.Occurrences {
		sites = append(sites, fmt.Sprintf("%s:%s:%d-%d", filepath.ToSlash(o.File), o.Name, o.StartLine, o.EndLine))
	}
	if got := strings.Join(sites, " "); got != "src/config.rs:load:1-1 src/store.rs:open:1-1" {
		t.Errorf("occurrences = %s, want the two call lines", got)
	}
}

func TestCheckIgnoresACycleMarkedAtACallSite(t *testing.T) {
	files := maps.Clone(cyclicCrate)
	files["src/config.rs"] = "// ddslop:ignore config reads the store's endpoint on purpose\n" + files["src/config.rs"]
	wantCycles(t, 0, "the marked cycle left out", repo(t, files))
}

// In diff mode a cycle is reported when the change touched one of its
// call sites, as the change closing it here does.
func TestDiffReportsTheChangeClosingACycle(t *testing.T) {
	files := maps.Clone(cyclicCrate)
	files["src/store.rs"] = "pub fn open() {}\npub fn endpoint() {}\n" // no call back yet
	root := committedRepo(t, files)
	testutil.WriteFiles(t, root, map[string]string{"src/store.rs": cyclicCrate["src/store.rs"]})
	wantCycles(t, 1, "the cycle the change closed", "--diff", "HEAD", root)
	gitIn(t, root, "commit", "-qam", "close")
	if err := os.WriteFile(filepath.Join(root, "src/api.rs"), []byte("pub fn serve() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantCycles(t, 0, "none: the change touched no call of the cycle", "--diff", "HEAD", root)
}

// config::load calls Other::endpoint before Store::endpoint, which
// closes the cycle: the occurrence is the store call's line.
var cycleAfterAnotherCall = map[string]string{
	"Cargo.toml": "[package]\nname = \"app\"\n",
	"src/lib.rs": "pub mod config;\npub mod other;\npub mod store;\n",
	"src/config.rs": `pub fn load() {
    crate::other::Other::endpoint();
    crate::store::Store::endpoint();
}
pub fn value() -> u32 { 1 }
`,
	"src/other.rs": "pub struct Other;\nimpl Other { pub fn endpoint() {} }\n",
	"src/store.rs": "pub struct Store;\nimpl Store { pub fn endpoint() {} }\npub fn open() { crate::config::value(); }\n",
}

func TestACyclesOccurrenceIsTheCallClosingIt(t *testing.T) {
	r := wantCycles(t, 1, "one cycle", repo(t, cycleAfterAnotherCall))
	if o := r.Candidates[0].Occurrences[0]; o.Name != "load" || o.StartLine != 3 {
		t.Errorf("occurrence = %+v, want load's call into store, line 3", o)
	}
}

func TestAnIgnoreOnACallLineLeavesItsCycleOut(t *testing.T) {
	files := maps.Clone(cycleAfterAnotherCall)
	files["src/config.rs"] = `pub fn load() {
    crate::other::Other::endpoint();
    // ddslop:ignore config reads the store's endpoint on purpose
    crate::store::Store::endpoint();
}
pub fn value() -> u32 { 1 }
`
	wantCycles(t, 0, "the cycle marked at its call left out", repo(t, files))
}

// With --tests, test code's calls count, cycles included.
func TestCheckWithTestsReportsCyclesInTestCode(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod config;\npub mod store;\n",
		"src/config.rs": `pub fn value() -> u32 { 1 }
#[cfg(test)]
mod tests {
    #[test]
    fn loads() { crate::store::endpoint(); }
}
`,
		"src/store.rs": `pub fn endpoint() {}
#[cfg(test)]
mod tests {
    #[test]
    fn opens() { crate::config::value(); }
}
`,
	}
	root := repo(t, files)
	wantCycles(t, 0, "none without --tests", root)
	wantCycles(t, 1, "the test code's cycle with --tests", "--tests", root)
}

// The crate's root module (lib.rs) is a module like the others: a loop
// through it is a cycle.
func TestCheckReportsCyclesThroughTheRootModule(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod foo;\npub fn root_fn() { crate::foo::child_fn(); }\n",
		"src/foo.rs": "pub fn child_fn() { crate::root_helper(); }\n",
	}
	files["src/lib.rs"] += "pub fn root_helper() {}\n"
	r := wantCycles(t, 1, "the root module and foo calling each other", repo(t, files))
	if got := strings.Join(r.Candidates[0].Values, ", "); !strings.Contains(got, "foo") {
		t.Errorf("values = %s, want a cycle through foo and the root module", got)
	}
}

// A call through a trait (a dyn field, a generic bound) depends on the
// trait, not on the implementation that happens to be its only one: the
// implementation's module, which imports the trait, closes no loop.
func TestCheckCallsThroughATraitCloseNoCycle(t *testing.T) {
	trait := "pub trait Provide {\n    fn provide(&self) -> u32;\n}\n"
	for name, caller := range map[string]string{
		"dyn field": "pub struct Chain {\n    provider: Box<dyn Provide>,\n}\n" +
			"impl Chain {\n    pub fn get(&self) -> u32 {\n        self.provider.provide()\n    }\n}\n",
		"generic bound": "pub fn run<A: Provide>(a: &A) -> u32 {\n    a.provide()\n}\n",
		// A guess by name is no dependency either, beside a trait call.
		"trait call and a guess": "pub fn run(a: &dyn Provide) -> u32 {\n    let p = other::make();\n    a.provide() + p.provide()\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"Cargo.toml":    "[package]\nname = \"app\"\n",
				"src/lib.rs":    "pub mod source;\npub mod pod;\n",
				"src/source.rs": trait + caller,
				"src/pod.rs": "use crate::source::Provide;\npub struct Pod;\n" +
					"impl Provide for Pod {\n    fn provide(&self) -> u32 {\n        7\n    }\n}\n",
			}
			_, stdout, stderr := run("check", "--format", "json", repo(t, files))
			if r := cycles(t, stdout); len(r.Candidates) != 0 {
				t.Errorf("candidates = %+v, want none: source calls pod only through the trait; stderr = %s", r.Candidates, stderr)
			}
		})
	}
}

// A function calling an implementation both through its trait and
// directly depends on it by the direct call, and that call is the site.
func TestACycleThroughATraitAndADirectCallIsTheDirectCall(t *testing.T) {
	files := map[string]string{
		"Cargo.toml": "[package]\nname = \"app\"\n",
		"src/lib.rs": "pub mod source;\npub mod pod;\n",
		"src/source.rs": "pub trait Provide {\n    fn provide(&self) -> u32;\n}\n" +
			"pub fn both(a: &dyn Provide, b: &crate::pod::Pod) -> u32 {\n    let x = a.provide();\n    x + b.provide()\n}\n",
		"src/pod.rs": "use crate::source::Provide;\npub struct Pod;\n" +
			"impl Provide for Pod {\n    fn provide(&self) -> u32 {\n        7\n    }\n}\n",
	}
	r := wantCycles(t, 1, "source and pod depending on each other", repo(t, files))
	for _, o := range r.Candidates[0].Occurrences {
		if filepath.ToSlash(o.File) == "src/source.rs" && o.StartLine != 6 {
			t.Errorf("source's site = line %d, want 6, the direct call", o.StartLine)
		}
	}
}

// wantSites fails the test unless the report's first cycle closes at
// sites, each file:unit:line.
func wantSites(t *testing.T, r cycleReport, sites string) {
	t.Helper()
	var got []string
	for _, o := range r.Candidates[0].Occurrences {
		got = append(got, fmt.Sprintf("%s:%s:%d", o.File, o.Name, o.StartLine))
	}
	if strings.Join(got, " ") != sites {
		t.Errorf("occurrences = %s, want %s", strings.Join(got, " "), sites)
	}
}

// TypeScript directories calling each other are a cycle, as Rust modules
// are: the calls each way close it.
func TestCheckReportsTypeScriptDirectoriesCallingEachOther(t *testing.T) {
	r := wantCycleBetween(t, "src/codec -> src/store, src/store -> src/codec", repo(t, map[string]string{
		"src/store/load.ts":  tsLoad,
		"src/codec/parse.ts": tsParse,
	}))
	wantSites(t, r, "src/codec/parse.ts:save:10 src/store/load.ts:loadRow:4")
}

// An import is a dependency whether or not the file calls what it
// imports: a type or a constant from a directory that calls back closes
// a cycle, at the import's line.
func TestCheckReportsATypeScriptCycleThroughAnImport(t *testing.T) {
	r := wantCycleBetween(t, "src/api -> src/db, src/db -> src/api", repo(t, map[string]string{
		"src/api/routes.ts": "import { connect } from \"../db/client\";\n\nexport const LIMIT = 10;\n\nexport function serve() {\n  return connect();\n}\n",
		"src/db/client.ts":  "import type { Row } from \"./row\";\nimport { LIMIT } from \"../api/routes\";\n\nexport function connect(): Row[] {\n  return new Array(LIMIT);\n}\n",
		"src/db/row.ts":     "export interface Row {\n  id: string;\n}\n",
	}))
	wantSites(t, r, "src/api/routes.ts:serve:6 src/db/client.ts:import:2")
}

// What closes no TypeScript cycle: files of one directory, a barrel
// re-exporting a directory below it, a dynamic import (loaded later, as
// code breaking a cycle does), an import of types only (compiled away), a
// test's import, an import marked ddslop:ignore, and a module of no file
// of ours.
func TestCheckReportsNoTypeScriptCycleWithoutOne(t *testing.T) {
	wantCycles(t, 0, "no cycle", repo(t, map[string]string{
		"src/index.ts":          "export * from \"./db/client\";\nexport { serve } from \"./api/routes\";\n",
		"src/version.ts":        "export const VERSION = \"1\";\n",
		"src/db/client.ts":      "import { VERSION } from \"..\";\nimport { helper } from \"./helper\";\n\nexport function connect() {\n  return helper(VERSION);\n}\n",
		"src/db/helper.ts":      "import { connect } from \"./client\";\n\nexport function helper(v: string) {\n  return v;\n}\n\nexport function again() {\n  return connect();\n}\n",
		"src/api/routes.ts":     "import { connect } from \"../db/client\";\nimport { z } from \"zod\";\n\nexport async function serve() {\n  const { audit } = await import(\"../audit/log\");\n  audit();\n  return connect();\n}\n",
		"src/audit/log.ts":      "import { serve } from \"../api/routes\";\n\nexport function audit() {\n  return 1;\n}\n\nexport const restart = () => serve;\n",
		"src/db/client.test.ts": "import { serve } from \"../api/routes\";\n\nserve();\n",
		"src/jobs/run.ts":       "// ddslop:ignore the job reads the api's limit on purpose\nimport { serve } from \"../api/routes\";\n\nexport function run() {\n  return 1;\n}\n",
		"src/api/jobs.ts":       "import { run } from \"../jobs/run\";\n\nexport function schedule() {\n  return run();\n}\n",
		"src/model/user.ts":     "import type { Db } from \"../store/db\";\nimport { type Conn, type Pool } from \"../store/pool\";\n\nexport function makeUser(db: Db, c: Conn, p: Pool) {\n  return { db, c, p };\n}\n",
		"src/store/db.ts":       "import { makeUser } from \"../model/user\";\n\nexport interface Db {\n  id: string;\n}\n\nexport function load(db: Db) {\n  return makeUser(db, 1, 2);\n}\n",
		"src/store/pool.ts":     "export type Conn = number;\nexport type Pool = number;\n",
	}))
}

// A directory and directories below it that depend on each other with
// no file loop joining them are one candidate, for the upper directory:
// its aggregator imports them, and they import back a file of it that
// belongs below them (move-shared-down), each such file named, every way
// back an occurrence. Sibling
// directories with no file loop are none.
func TestCheckReportsADirectoryItsSubdirectoriesImportBackOnce(t *testing.T) {
	r := wantCycles(t, 1, "one candidate for src/lib", repo(t, map[string]string{
		"src/lib/library.ts":             "import { accordion } from \"./accordion/accordion\";\nimport { card } from \"./card/card\";\n\nexport function library() {\n  return [accordion(), card()];\n}\n",
		"src/lib/rules.ts":               "export function rules() {\n  return 1;\n}\n",
		"src/lib/helpers.ts":             "export function helper() {\n  return 2;\n}\n",
		"src/lib/accordion/accordion.ts": "import { rules } from \"../rules\";\n\nexport function accordion() {\n  return rules();\n}\n",
		"src/lib/card/card.ts":           "import { helper } from \"../helpers\";\nimport { rules } from \"../rules\";\n\nexport function card() {\n  return helper() + rules();\n}\n",
		"src/x/one.ts":                   "import { two } from \"../y/two\";\n\nexport function one() {\n  return two();\n}\n\nexport function three() {\n  return 3;\n}\n",
		"src/y/two.ts":                   "export function two() {\n  return 2;\n}\n",
		"src/y/four.ts":                  "import { three } from \"../x/one\";\n\nexport function four() {\n  return three();\n}\n",
	}))
	c := r.Candidates[0]
	if c.Suggestion.Action != "move-shared-down" {
		t.Errorf("action = %s, want move-shared-down", c.Suggestion.Action)
	}
	want := "src/lib -> src/lib/accordion, src/lib/accordion -> src/lib/rules.ts, src/lib -> src/lib/card, src/lib/card -> src/lib/helpers.ts, src/lib/card -> src/lib/rules.ts"
	if got := strings.Join(c.Values, ", "); got != want {
		t.Errorf("values = %s, want %s", got, want)
	}
	wantSites(t, r, "src/lib/accordion/accordion.ts:accordion:4 src/lib/card/card.ts:card:5 src/lib/card/card.ts:card:5")
}

// A directory and one below it joined by a loop of files are a cycle to
// break, as any pair is.
func TestCheckReportsADirectoryAndOneBelowItInAFileLoopAsACycle(t *testing.T) {
	r := wantCycleBetween(t, "src/lib -> src/lib/accordion, src/lib/accordion -> src/lib", repo(t, map[string]string{
		"src/lib/rules.ts":               "import { accordion } from \"./accordion/accordion\";\n\nexport function rules(n: number): number {\n  return n > 0 ? accordion(n - 1) : 0;\n}\n",
		"src/lib/accordion/accordion.ts": "import { rules } from \"../rules\";\n\nexport function accordion(n: number): number {\n  return rules(n);\n}\n",
	}))
	if a := r.Candidates[0].Suggestion.Action; a != "break-cycle" {
		t.Errorf("action = %s, want break-cycle", a)
	}
}

// A file loop through several files of a directory closes a cycle of
// the directories: one.ts calls two.ts, which calls three.ts, which calls
// one.ts back.
func TestCheckReportsATypeScriptCycleThroughAFileLoop(t *testing.T) {
	wantCycleBetween(t, "src/a -> src/b, src/b -> src/a", repo(t, map[string]string{
		"src/a/one.ts":   "import { two } from \"../b/two\";\n\nexport function one(n: number): number {\n  return n > 0 ? two(n - 1) : 0;\n}\n",
		"src/b/two.ts":   "import { three } from \"../a/three\";\n\nexport function two(n: number): number {\n  return three(n);\n}\n",
		"src/a/three.ts": "import { one } from \"./one\";\n\nexport function three(n: number): number {\n  return one(n);\n}\n",
	}))
}

// Two loops of files joining the same two directories, one of them
// through a third directory too, are one cycle of the two: a pair is
// reported once.
func TestCheckReportsTwoFileLoopsOfTheSameDirectoriesOnce(t *testing.T) {
	r := wantCycles(t, 1, "one pair", repo(t, map[string]string{
		"src/a/one.ts":   "import { two } from \"../b/two\";\n\nexport function one(n: number): number {\n  return two(n);\n}\n",
		"src/b/two.ts":   "import { one } from \"../a/one\";\n\nexport function two(n: number): number {\n  return n > 0 ? one(n - 1) : 0;\n}\n",
		"src/a/three.ts": "import { four } from \"../b/four\";\n\nexport function three(n: number): number {\n  return four(n);\n}\n",
		"src/b/four.ts":  "import { three } from \"../a/three\";\nimport { five } from \"../c/five\";\n\nexport function four(n: number): number {\n  return n > 0 ? three(n - 1) : five();\n}\n",
		"src/c/five.ts":  "import { three } from \"../a/three\";\n\nexport function five(): number {\n  return three(0);\n}\n",
	}))
	if got := strings.Join(r.Candidates[0].Values, ", "); got != "src/a -> src/b, src/b -> src/a" {
		t.Fatalf("values = %s, want the pair", got)
	}
}
