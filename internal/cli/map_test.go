package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A Go service: handlers go through service to store, except one handler
// that skips service, and both handlers and service use util.Respond.
var layeredService = map[string]string{
	"handlers/h.go": `package handlers

import (
	"example.com/app/service"
	"example.com/app/store"
	"example.com/app/util"
)

func Create() { service.Create(); service.Validate(); util.Respond() }
func Update() { service.Update(); service.Validate(); util.Respond() }
func Delete() { service.Delete(); service.Validate() }
func Peek()   { store.Load() }
`,
	"service/s.go": `package service

import (
	"example.com/app/store"
	"example.com/app/util"
)

func Create()   { store.Save(); util.Respond() }
func Update()   { store.Save() }
func Delete()   { store.Remove() }
func Validate() {}
`,
	"store/st.go": `package store

func Save()   {}
func Remove() {}
func Load()   {}
`,
	"types/t.go": `package types

func Normalise() { trim() }
func trim()      {}
`,
	"util/u.go": `package util

// Respond writes a response.
func Respond() {}
`,
}

type mapOutput struct {
	Maps []struct {
		Lang          string `json:"lang"`
		Calls         int    `json:"calls"`
		ResolvedCalls int    `json:"resolved_calls"`
		Layers        []struct {
			Depth   int      `json:"depth"`
			Modules []string `json:"modules"`
			More    int      `json:"more"`
		} `json:"layers"`
		Breaks []struct {
			Kind       string   `json:"kind"`
			From       string   `json:"from"`
			To         string   `json:"to"`
			Via        string   `json:"via"`
			Calls      int      `json:"calls"`
			UsualCalls int      `json:"usual_calls"`
			Functions  []string `json:"functions"`
		} `json:"breaks"`
		Unplaced     []string `json:"unplaced"`
		UnplacedMore int      `json:"unplaced_more"`
		Helpers      []struct {
			Name      string   `json:"name"`
			File      string   `json:"file"`
			Line      int      `json:"line"`
			Signature string   `json:"signature"`
			Modules   []string `json:"modules"`
		} `json:"helpers"`
	} `json:"maps"`
}

func TestMapDescribesLayersBreaksAndHelpers(t *testing.T) {
	root := repo(t, layeredService)

	code, stdout, stderr := run("map", "--format", "json", root)

	if code != 0 {
		t.Fatalf("map: exit %d, stderr %s", code, stderr)
	}
	var r mapOutput
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	if len(r.Maps) != 1 || r.Maps[0].Lang != "go" {
		t.Fatalf("maps = %+v, want one Go map", r.Maps)
	}
	m := r.Maps[0]
	var layers [][]string
	for _, l := range m.Layers {
		layers = append(layers, l.Modules)
	}
	if want := [][]string{{"handlers"}, {"service"}, {"store", "util"}}; !reflect.DeepEqual(layers, want) {
		t.Errorf("layers = %v, want %v", layers, want)
	}
	if len(m.Breaks) != 1 || m.Breaks[0].Kind != "bypass" || m.Breaks[0].From != "handlers" || m.Breaks[0].Via != "service" ||
		len(m.Breaks[0].Functions) != 1 || m.Breaks[0].Functions[0] != "handlers/h.go:12 Peek" {
		t.Errorf("breaks = %+v, want handlers' Peek bypassing service", m.Breaks)
	}
	if len(m.Helpers) != 1 || m.Helpers[0].Name != "Respond" || m.Helpers[0].Signature != "func Respond() {}" ||
		!reflect.DeepEqual(m.Helpers[0].Modules, []string{"handlers", "service"}) {
		t.Errorf("helpers = %+v, want util.Respond used by handlers and service", m.Helpers)
	}
	if !reflect.DeepEqual(m.Unplaced, []string{"types"}) {
		t.Errorf("unplaced = %v, want [types]: it calls only itself", m.Unplaced)
	}
	if m.Calls == 0 || m.ResolvedCalls != m.Calls {
		t.Errorf("calls = %d resolved of %d, want all resolved", m.ResolvedCalls, m.Calls)
	}
}

func TestMapTextNamesTheSections(t *testing.T) {
	root := repo(t, layeredService)
	code, stdout, stderr := run("map", "--format", "text", root)
	if code != 0 {
		t.Fatalf("map: exit %d, stderr %s", code, stderr)
	}
	for _, want := range []string{"go, packages: ", "Layers", "handlers", "bypass", "Shared helpers", "Respond"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestMapLimitCapsEachSection(t *testing.T) {
	root := repo(t, layeredService)
	_, stdout, _ := run("map", "--format", "json", "--limit", "1", root)
	var r mapOutput
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	last := r.Maps[0].Layers[len(r.Maps[0].Layers)-1]
	if len(last.Modules) != 1 || last.More != 1 {
		t.Errorf("last layer = %+v, want one module and 1 more", last)
	}
}

// A Rust workspace: api calls service, service calls store, and service
// has a module of its own.
var workspace = map[string]string{
	"crates/api/src/lib.rs":          "pub fn handle() { service::create(); }\n",
	"crates/service/src/lib.rs":      "pub fn create() { validate::check(); store::save(); }\n",
	"crates/service/src/validate.rs": "pub fn check() { store::load(); }\n",
	"crates/store/src/lib.rs":        "pub fn save() {}\npub fn load() {}\n",
}

type scopedMap struct {
	Maps []struct {
		Lang          string `json:"lang"`
		Scope         string `json:"scope"`
		Calls         int    `json:"calls"`
		ResolvedCalls int    `json:"resolved_calls"`
		Crate         string `json:"crate"`
		Layers        []struct {
			Modules []string `json:"modules"`
		} `json:"layers"`
		Crates []struct {
			Name    string `json:"name"`
			Modules int    `json:"modules"`
			Layers  int    `json:"layers"`
		} `json:"crates"`
	} `json:"maps"`
}

func mapOf(t *testing.T, args ...string) scopedMap {
	t.Helper()
	code, stdout, stderr := run(append([]string{"map", "--format", "json"}, args...)...)
	if code != 0 {
		t.Fatalf("map %v: exit %d, stderr %s", args, code, stderr)
	}
	var r scopedMap
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	return r
}

func layerNames(layers []struct {
	Modules []string `json:"modules"`
}) [][]string {
	var out [][]string
	for _, l := range layers {
		out = append(out, l.Modules)
	}
	return out
}

func TestMapOfAWorkspaceLayersItsCrates(t *testing.T) {
	r := mapOf(t, repo(t, workspace))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "crates" {
		t.Fatalf("maps = %+v, want one map of crates", r.Maps)
	}
	m := r.Maps[0]
	if want := [][]string{{"api"}, {"service"}, {"store"}}; !reflect.DeepEqual(layerNames(m.Layers), want) {
		t.Errorf("crate layers = %v, want %v", layerNames(m.Layers), want)
	}
	var summary []string
	for _, c := range m.Crates {
		summary = append(summary, fmt.Sprintf("%s:%d/%d", c.Name, c.Modules, c.Layers))
	}
	// A one-module crate has no calls between its modules, so no layers.
	if want := "api:1/0 service:2/2 store:1/0"; strings.Join(summary, " ") != want {
		t.Errorf("crates = %v, want %s (modules/layers)", summary, want)
	}
}

func TestMapOfOneCrateLayersItsModules(t *testing.T) {
	r := mapOf(t, "--crate", "service", repo(t, workspace))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "crate" || r.Maps[0].Crate != "service" {
		t.Fatalf("maps = %+v, want one map of crate service", r.Maps)
	}
	if want := [][]string{{"service"}, {"service::validate"}}; !reflect.DeepEqual(layerNames(r.Maps[0].Layers), want) {
		t.Errorf("layers = %v, want %v", layerNames(r.Maps[0].Layers), want)
	}
	// service makes 3 calls, all resolved; api's call is not service's.
	if m := r.Maps[0]; m.Calls != 3 || m.ResolvedCalls != 3 {
		t.Errorf("calls = %d resolved of %d, want 3 of 3: the crate's own calls", m.ResolvedCalls, m.Calls)
	}
}

func TestMapOfASingleCrateShowsItsModules(t *testing.T) {
	r := mapOf(t, repo(t, map[string]string{
		"src/lib.rs":   "pub fn run() { store::save(); }\n",
		"src/store.rs": "pub fn save() {}\n",
	}))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "crate" || r.Maps[0].Crate != "." {
		t.Fatalf("maps = %+v, want the root crate's modules", r.Maps)
	}
	if want := [][]string{{"."}, {"store"}}; !reflect.DeepEqual(layerNames(r.Maps[0].Layers), want) {
		t.Errorf("layers = %v, want %v", layerNames(r.Maps[0].Layers), want)
	}
}

func TestMapOfGoKeepsPackages(t *testing.T) {
	r := mapOf(t, repo(t, layeredService))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "packages" {
		t.Errorf("maps = %+v, want Go's map of packages", r.Maps)
	}
}

func TestMapOfAnUnknownCrateFails(t *testing.T) {
	code, _, stderr := run("map", "--crate", "nope", repo(t, workspace))
	if code != 2 || !strings.Contains(stderr, "api, service, store") {
		t.Errorf("exit %d, stderr %q; want 2 and the crates there are", code, stderr)
	}
}

func TestMapTextNamesTheScopeAndSummarisesCrates(t *testing.T) {
	root := repo(t, workspace)
	_, stdout, _ := run("map", "--format", "text", root)
	for _, want := range []string{"rust, crates:", "Crates (--crate NAME for one crate's modules):", "service  2 module(s), 2 layer(s)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text lacks %q:\n%s", want, stdout)
		}
	}
	_, stdout, _ = run("map", "--format", "text", "--crate", "service", root)
	if !strings.Contains(stdout, "rust, crate service:") {
		t.Errorf("text lacks the crate scope:\n%s", stdout)
	}
}

func TestMapCountsCratesWithoutFunctions(t *testing.T) {
	files := map[string]string{
		"crates/app/src/lib.rs":   "pub fn run() { app_helper(); }\nfn app_helper() {}\n",
		"crates/types/src/lib.rs": "pub struct Order { pub id: u64 }\n",
		"crates/app/tests/it.rs":  "#[test]\nfn works() {}\n",
	}
	r := mapOf(t, repo(t, files))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "crates" {
		t.Fatalf("maps = %+v, want a workspace of crates: types has no functions but is a crate", r.Maps)
	}
	var names []string
	for _, c := range r.Maps[0].Crates {
		names = append(names, c.Name)
	}
	if want := []string{"app", "types"}; !reflect.DeepEqual(names, want) {
		t.Errorf("crates = %v, want %v (tests/ is no crate)", names, want)
	}
	if r := mapOf(t, "--crate", "types", repo(t, files)); r.Maps[0].Crate != "types" {
		t.Errorf("--crate types = %+v, want the types crate", r.Maps)
	}
}

func TestMapOfOnePackageWithExamplesIsOneCrate(t *testing.T) {
	r := mapOf(t, repo(t, map[string]string{
		"Cargo.toml":       "[package]\nname = \"acme\"\n",
		"src/lib.rs":       "pub fn run() { store::save(); }\n",
		"src/store.rs":     "pub fn save() {}\n",
		"examples/demo.rs": "fn main() { acme::run(); }\n",
	}))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "crate" || r.Maps[0].Crate != "." {
		t.Errorf("maps = %+v, want one crate: examples/ belongs to the package", r.Maps)
	}
}

func TestMapLeavesOutRustFilesInNoPackage(t *testing.T) {
	r := mapOf(t, repo(t, map[string]string{
		"Cargo.toml":              "[workspace]\nmembers = [\"crates/app\"]\n",
		"crates/app/Cargo.toml":   "[package]\nname = \"app\"\n",
		"crates/app/src/lib.rs":   "pub fn run() { store::save(); }\n",
		"crates/app/src/store.rs": "pub fn save() {}\n",
		"scripts/release.rs":      "fn main() { bump(); }\nfn bump() {}\n",
	}))
	if len(r.Maps) != 1 || r.Maps[0].Scope != "crate" || r.Maps[0].Crate != "app" {
		t.Errorf("maps = %+v, want app's modules: scripts/ is in no package", r.Maps)
	}
}

type featureOutput struct {
	Maps []struct {
		Features []struct {
			Concern     string   `json:"concern"`
			Flagged     string   `json:"flagged"`
			Pattern     string   `json:"pattern"`
			Shape       string   `json:"shape"`
			Members     []string `json:"members"`
			MembersMore int      `json:"members_more"`
			Steps       []struct {
				Function string   `json:"function"`
				Helpers  []string `json:"helpers"`
			} `json:"steps"`
		} `json:"features"`
	} `json:"maps"`
}

// featureMapRepo is featureRepo with min-nodes lowered in .ddslop.yaml, as
// check would read it, so its two features are a parallel-flow family.
func featureMapRepo(t *testing.T) string {
	t.Helper()
	return repo(t, map[string]string{"api/src/generated/server.rs": featureAPI, "svc/src/lib.rs": featureService,
		".ddslop.yaml": "min-nodes: 10\n"})
}

func TestMapFlagsEachParallelFlowFamilyAsATypicalFeature(t *testing.T) {
	root := featureMapRepo(t)

	code, stdout, stderr := run("map", "--format", "json", root)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	var r featureOutput
	decodeInto(t, stdout, &r)
	if len(r.Maps) != 1 || len(r.Maps[0].Features) != 1 {
		t.Fatalf("features = %+v, want one family", r.Maps)
	}
	f := r.Maps[0].Features[0]
	if f.Concern != "feature-pipelines" || f.Flagged != "parallel-flow" || f.Pattern != "generic-pipeline" {
		t.Errorf("feature = %+v, want the feature-pipelines concern flagged as parallel-flow, generic-pipeline the way forward", f)
	}
	if f.Shape != "svc/src/lib.rs:2 Service::create" || !reflect.DeepEqual(f.Members, []string{"svc/src/lib.rs:8 Service::update"}) {
		t.Errorf("shape = %q, members = %v; want create, with update the other member", f.Shape, f.Members)
	}
	var steps []string
	for _, s := range f.Steps {
		steps = append(steps, s.Function)
	}
	if !slices.Contains(steps, "svc/src/lib.rs:19 validate_create") {
		t.Errorf("steps = %v, want the shape's validate_create among them", steps)
	}

	_, text, _ := run("map", "--format", "text", root)
	for _, want := range []string{"Typical features", "Service::create", "parallel-flow", "extract"} {
		if !strings.Contains(text, want) {
			t.Errorf("text map lacks %q:\n%s", want, text)
		}
	}
}

func TestMapLimitCapsAFeaturesSteps(t *testing.T) {
	root := featureMapRepo(t)

	_, stdout, _ := run("map", "--format", "json", "--limit", "1", root)
	var r struct {
		Maps []struct {
			Features []struct {
				Steps     []any `json:"steps"`
				StepsMore int   `json:"steps_more"`
			} `json:"features"`
		} `json:"maps"`
	}
	decodeInto(t, stdout, &r)
	if len(r.Maps) != 1 || len(r.Maps[0].Features) != 1 {
		t.Fatalf("maps = %+v, want one feature", r.Maps)
	}
	if f := r.Maps[0].Features[0]; len(f.Steps) != 1 || f.StepsMore < 1 {
		t.Errorf("steps = %d, steps_more = %d; want 1 step listed and the rest counted", len(f.Steps), f.StepsMore)
	}
}

// errorsService checks errors with errors.Is in five places and by their
// message in one, and wraps one error with %w.
var errorsService = map[string]string{
	"store/s.go": `package store

import (
	"errors"
	"fmt"
	"strings"
)

var ErrMissing = errors.New("missing")

func A(err error) bool { return errors.Is(err, ErrMissing) }
func B(err error) bool { return errors.Is(err, ErrMissing) }
func C(err error) bool { return errors.Is(err, ErrMissing) }
func D(err error) bool { return errors.Is(err, ErrMissing) }
func E(err error) bool { return errors.Is(err, ErrMissing) }
func F(err error) bool { return strings.Contains(err.Error(), "missing") }
func G(err error) error { return fmt.Errorf("g: %w", err) }
`,
}

func TestMapNamesTheCodebasesFormForEachErrorConcern(t *testing.T) {
	root := repo(t, errorsService)

	code, stdout, stderr := run("map", "--format", "json", root)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	var r struct {
		Maps []struct {
			Idioms []struct {
				Concern     string  `json:"concern"`
				Convention  string  `json:"convention"`
				Idiomatic   string  `json:"idiomatic"`
				Share       float64 `json:"share"`
				Occurrences int     `json:"occurrences"`
				Forms       []struct {
					Form    string `json:"form"`
					Count   int    `json:"count"`
					Example string `json:"example"`
				} `json:"forms"`
			} `json:"idioms"`
		} `json:"maps"`
	}
	decodeInto(t, stdout, &r)
	if len(r.Maps) != 1 || len(r.Maps[0].Idioms) != 2 {
		t.Fatalf("idioms = %+v, want error checks and error wrapping", r.Maps)
	}
	checks, wrapping := r.Maps[0].Idioms[0], r.Maps[0].Idioms[1]
	if checks.Concern != "error-checks" || checks.Convention != "errors.Is/As" || checks.Occurrences != 6 || checks.Share < 0.83 || checks.Share > 0.84 {
		t.Errorf("checks = %+v, want errors.Is/As in 5 of 6", checks)
	}
	if len(checks.Forms) != 2 || checks.Forms[1].Form != "message" || checks.Forms[1].Example != "store/s.go:16 F" {
		t.Errorf("check forms = %+v, want the message check at F as the other form", checks.Forms)
	}
	if wrapping.Concern != "error-wrapping" || wrapping.Convention != "" || wrapping.Idiomatic != "%w" {
		t.Errorf("wrapping = %+v, want no convention from one occurrence, %%w named idiomatic", wrapping)
	}

	_, text, _ := run("map", "--format", "text", root)
	for _, want := range []string{"Idioms", "error-checks: errors.Is/As in 5 of 6", "message 1, e.g. store/s.go:16 F", "error-wrapping: no convention"} {
		if !strings.Contains(text, want) {
			t.Errorf("text map lacks %q:\n%s", want, text)
		}
	}
}
