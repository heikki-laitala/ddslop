package cli

import (
	"encoding/json"
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
	for _, want := range []string{"go: ", "Layers", "handlers", "bypass", "Shared helpers", "Respond"} {
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
