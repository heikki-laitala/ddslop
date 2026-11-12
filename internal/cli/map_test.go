package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
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
