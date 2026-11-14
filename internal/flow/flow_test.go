package flow

import (
	"fmt"
	"slices"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func free(name string) unit.Call { return unit.Call{Kind: unit.FreeCall, Name: name} }

func path(qualifier, name string) unit.Call {
	return unit.Call{Kind: unit.PathCall, Qualifier: qualifier, Name: name}
}

func method(receiver, name string) unit.Call {
	return unit.Call{Kind: unit.MethodCall, Qualifier: receiver, Name: name}
}

func fn(file, name string, calls ...unit.Call) unit.Unit {
	return unit.Unit{File: file, Name: name, Calls: calls}
}

// callees names the units a unit's calls resolved to.
func callees(g *Graph, name string) string {
	for i, u := range g.Units {
		if u.File+" "+u.Name == name {
			var out []string
			for _, c := range g.Callees[i] {
				out = append(out, g.Units[c].File+" "+g.Units[c].Name)
			}
			return fmt.Sprint(out)
		}
	}
	return "no unit " + name
}

func TestFreeCallsPreferSameFileThenCrate(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/x.rs", "helper"),
		fn("a/src/y.rs", "helper"),
		fn("b/src/z.rs", "helper"),
		fn("a/src/x.rs", "run", free("helper")),
		fn("a/src/w.rs", "ambiguous_in_crate", free("helper")),
		fn("b/src/k.rs", "unique_in_crate", free("helper")),
		fn("c/src/q.rs", "ambiguous_everywhere", free("helper")),
	})

	cases := map[string]string{
		"a/src/x.rs run":                  "[a/src/x.rs helper]",
		"a/src/w.rs ambiguous_in_crate":   "[]",
		"b/src/k.rs unique_in_crate":      "[b/src/z.rs helper]",
		"c/src/q.rs ambiguous_everywhere": "[]",
	}
	for caller, want := range cases {
		if got := callees(g, caller); got != want {
			t.Errorf("%s calls %s, want %s", caller, got, want)
		}
	}
	if g.Calls != 4 || g.Resolved != 2 {
		t.Errorf("calls = %d, resolved = %d; want 4 and 2", g.Calls, g.Resolved)
	}
}

func TestPathCalls(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/acc.rs", "Accounts::create"),
		fn("a/src/end.rs", "Endpoint<P>::priced"),
		fn("a/src/led.rs", "Ledger::finish"),
		fn("a/src/util.rs", "log"),
		fn("a/src/led.rs", "Ledger::create",
			path("Accounts", "create"), path("Endpoint", "priced"),
			path("Self", "finish"), path("crate::util", "log"), path("Missing", "nothing")),
	})

	want := "[a/src/acc.rs Accounts::create a/src/end.rs Endpoint<P>::priced a/src/led.rs Ledger::finish a/src/util.rs log]"
	if got := callees(g, "a/src/led.rs Ledger::create"); got != want {
		t.Errorf("Ledger::create calls %s, want %s", got, want)
	}
}

func TestMethodCalls(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/led.rs", "Ledger::audit"),
		fn("a/src/other.rs", "Other::audit"),
		fn("a/src/store.rs", "Store::get"),
		fn("a/src/cache.rs", "Cache::get"),
		fn("a/src/rare.rs", "Rare::settle_window"),
		fn("a/src/led.rs", "Ledger::create",
			method("self", "audit"), method("", "get"), method("", "settle_window"), method("", "get")),
	})

	// self.audit is Ledger's own; get is ambiguous; settle_window is unique.
	want := "[a/src/led.rs Ledger::audit a/src/rare.rs Rare::settle_window]"
	if got := callees(g, "a/src/led.rs Ledger::create"); got != want {
		t.Errorf("Ledger::create calls %s, want %s", got, want)
	}
}

func TestEntriesAreNonTestTraitImplementations(t *testing.T) {
	units := []unit.Unit{
		{File: "a.rs", Name: "Api::create", Trait: "LedgerApi"},
		{File: "a.rs", Name: "Api::fmt", Trait: "Display"},
		{File: "a.rs", Name: "Api::helper"},
		{File: "t.rs", Name: "Fake::create", Trait: "LedgerApi", Test: true},
	}
	g := Build(Inputs{}, units)

	if got := fmt.Sprint(g.Entries(map[string]bool{"LedgerApi": true})); got != "[0]" {
		t.Errorf("entries = %s, want [0]", got)
	}
}

func TestSliceIsBreadthFirstDepthLimitedAndSkipsTests(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/a.rs", "entry", free("one"), free("two")),        // 0
		fn("a/src/a.rs", "one", free("deep"), free("entry")),       // 1: cycle back
		fn("a/src/a.rs", "two", free("deep"), free("test_helper")), // 2
		fn("a/src/a.rs", "deep", free("deeper")),                   // 3
		fn("a/src/a.rs", "deeper"),                                 // 4
		{File: "a/src/a.rs", Name: "test_helper", Test: true},      // 5
	})

	var got []string
	for _, s := range g.Slice(0, 2) {
		got = append(got, fmt.Sprintf("%s@%d<-%d", g.Units[s.Unit].Name, s.Depth, s.Caller))
	}

	if want := "[entry@0<--1 one@1<-0 two@1<-0 deep@2<-1]"; fmt.Sprint(got) != want {
		t.Errorf("slice = %v, want %s", got, want)
	}
}

func TestPathCallsOnUnknownTypesDoNotFallBackToFreeFunctions(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/clock.rs", "now"),
		fn("a/src/util.rs", "log"),
		fn("a/src/h.rs", "handler",
			path("Utc", "now"), path("chrono::Utc", "now"),
			path("crate::util", "log"), path("super", "log")),
	})

	// Utc is a type defined elsewhere; crate::util and super are modules.
	want := "[a/src/util.rs log]"
	if got := callees(g, "a/src/h.rs handler"); got != want {
		t.Errorf("handler calls %s, want %s", got, want)
	}
}

func TestStandardMethodNamesAreNotResolvedByUniqueness(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/id.rs", "ModelId::as_ref"),
		fn("a/src/ext.rs", "Extensions::len"),
		fn("a/src/rare.rs", "Rare::settle_window"),
		fn("a/src/h.rs", "Handler::run",
			method("", "as_ref"), method("", "len"), method("", "settle_window")),
		fn("a/src/id.rs", "ModelId::show", method("self", "as_ref")),
	})

	// x.as_ref() and v.len() are most likely std methods; settle_window is
	// unique to this codebase. self.as_ref() is still the caller's own.
	if got := callees(g, "a/src/h.rs Handler::run"); got != "[a/src/rare.rs Rare::settle_window]" {
		t.Errorf("Handler::run calls %s, want only Rare::settle_window", got)
	}
	if got := callees(g, "a/src/id.rs ModelId::show"); got != "[a/src/id.rs ModelId::as_ref]" {
		t.Errorf("ModelId::show calls %s, want its own as_ref", got)
	}
}

func local(file, name string, declared string, calls ...unit.Call) unit.Unit {
	u := fn(file, name, calls...)
	u.Leaves = []unit.Leaf{{Kind: unit.Identifier, Text: declared, Declares: true}}
	return u
}

func TestCallsToLocalClosuresAreNotResolved(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/conv.rs", "unsigned"),
		fn("b/src/session.rs", "unsigned"),
		// let unsigned = |value| ...; unsigned(x)
		local("a/src/api.rs", "capacity_of", "unsigned", free("unsigned")),
	})

	if got := callees(g, "a/src/api.rs capacity_of"); got != "[]" {
		t.Errorf("capacity_of calls %s, want nothing: unsigned is its own closure", got)
	}
}

func TestBareCallsLeaveTheCrateOnlyThroughImports(t *testing.T) {
	imports := map[string]map[string]string{
		"crates/translation/src/a.rs": { //nolint:gosec // crate and function names, not credentials
			"split_qualified_name": "translation_protocol::split_qualified_name",
			"channel":              "tokio::sync::mpsc::channel",
			"decode_token":         "token_credentials::decode",
		},
	}
	g := Build(Inputs{Imports: imports}, []unit.Unit{
		fn("crates/translation-protocol/src/lib.rs", "split_qualified_name"),
		fn("crates/token-credentials/src/lib.rs", "decode"),
		fn("services/acme-router/src/chan.rs", "channel"),
		fn("services/acme-ledger/src/fill.rs", "fill"),
		fn("crates/translation/src/a.rs", "encode",
			free("split_qualified_name"), free("channel"), free("decode_token"), free("fill")),
	})

	// split_qualified_name and decode come from workspace crates; channel is
	// tokio's; fill is not imported, so it is not some other crate's fill.
	want := "[crates/translation-protocol/src/lib.rs split_qualified_name crates/token-credentials/src/lib.rs decode]"
	if got := callees(g, "crates/translation/src/a.rs encode"); got != want {
		t.Errorf("encode calls %s, want %s", got, want)
	}
}

func TestModulePathCallsStayInTheirCrate(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("crates/token-credentials/src/lib.rs", "decode"),
		fn("services/acme-ledger/src/decode.rs", "decode"),
		fn("services/acme-ledger/src/json.rs", "to_value"),
		fn("services/acme-router/src/api.rs", "handle",
			path("token_credentials", "decode"), path("serde_json", "to_value")),
	})

	// token_credentials is a workspace crate; serde_json is not, and the
	// router has no to_value of its own.
	if got := callees(g, "services/acme-router/src/api.rs handle"); got != "[crates/token-credentials/src/lib.rs decode]" {
		t.Errorf("handle calls %s, want only token_credentials' decode", got)
	}
}

func TestCrateNamesComeFromInputsNotDirectories(t *testing.T) {
	inputs := Inputs{
		Crates: map[string]string{"crates/token-credentials": "acme_token_credentials"}, //nolint:gosec // a crate name, not a credential
		Imports: map[string]map[string]string{
			"services/acme-ledger/src/a.rs": {"decode": "acme_token_credentials::decode"},
		},
	}
	g := Build(inputs, []unit.Unit{
		fn("crates/token-credentials/src/lib.rs", "decode"),
		fn("crates/token-credentials/src/lib.rs", "is_canonical_key_id"),
		fn("services/acme-ledger/src/a.rs", "check",
			path("acme_token_credentials", "is_canonical_key_id"), free("decode")),
	})

	want := "[crates/token-credentials/src/lib.rs is_canonical_key_id crates/token-credentials/src/lib.rs decode]"
	if got := callees(g, "services/acme-ledger/src/a.rs check"); got != want {
		t.Errorf("check calls %s, want %s", got, want)
	}
}

func TestOtherCratesMethodsNeedADependency(t *testing.T) {
	inputs := Inputs{Imports: map[string]map[string]string{
		// The ledger uses the translation crate somewhere, never the router.
		"ledger/src/other.rs": {"Engine": "translation::Engine"},
	}}
	g := Build(inputs, []unit.Unit{
		fn("router/src/location.rs", "Location::then"),
		fn("translation/src/engine.rs", "Engine::encode_frames"),
		fn("ledger/src/api.rs", "handle",
			method("", "then"), method("", "encode_frames")),
	})

	// .then() could be any type's; the router's Location is out of reach.
	if got := callees(g, "ledger/src/api.rs handle"); got != "[translation/src/engine.rs Engine::encode_frames]" {
		t.Errorf("handle calls %s, want only the translation crate's encode_frames", got)
	}
}

func TestBuildResolvesGoCalls(t *testing.T) {
	units := []unit.Unit{
		fn("store/store.go", "Store.Visible", method("self", "inNamespace")),
		fn("store/store.go", "Store.inNamespace"),
		fn("other/other.go", "Other.inNamespace"),
		fn("store/load.go", "load", unit.Call{Kind: unit.FreeCall, Name: "open"}),
		fn("store/open.go", "open"),
		fn("store/check.go", "Check", unit.Call{Kind: unit.PathCall, Qualifier: "auth", Name: "Verify"}),
		fn("internal/auth/auth.go", "Verify"),
		// json is an outside package: its Unmarshal is not this package's
		fn("store/decode.go", "Decode", unit.Call{Kind: unit.PathCall, Qualifier: "json", Name: "Unmarshal"}),
		fn("store/unmarshal.go", "Unmarshal"),
	}
	g := Build(Inputs{}, units)
	for caller, want := range map[string]string{
		"store/store.go Store.Visible": "[store/store.go Store.inNamespace]",
		"store/load.go load":           "[store/open.go open]",
		"store/check.go Check":         "[internal/auth/auth.go Verify]",
		"store/decode.go Decode":       "[]",
	} {
		if got := callees(g, caller); got != want {
			t.Errorf("%s calls %s, want %s", caller, got, want)
		}
	}
}

// Go methods implement generated interfaces implicitly: a method named
// like one of the interface's methods is an entry point.
func TestEntriesIncludeGoMethodsNamedInGeneratedInterfaces(t *testing.T) {
	handler := fn("api/server.go", "oapiServer.GetDocument")
	handler.Dispatched = true
	helper := fn("api/server.go", "oapiServer.helper")
	helper.Dispatched = true
	rustImpl := fn("src/api.rs", "Service::create")
	rustImpl.Trait = "OrdersApi"
	g := Build(Inputs{}, []unit.Unit{handler, helper, rustImpl, fn("api/free.go", "GetDocument")})

	var got []string
	for _, e := range g.Entries(map[string]bool{"OrdersApi": true, "GetDocument": true}) {
		got = append(got, g.Units[e].Name)
	}
	if want := "[oapiServer.GetDocument Service::create]"; fmt.Sprint(got) != want {
		t.Errorf("entries = %v, want %s", got, want)
	}
}

// w.Header() on an http.ResponseWriter is the standard library's, even
// when the code defines a single Header method; other names still resolve.
func TestGoStandardLibraryMethodsAreNotGuessed(t *testing.T) {
	units := []unit.Unit{
		fn("api/auth.go", "writeError", method("", "Header")),
		fn("api/writer.go", "bufferingWriter.Header"),
		fn("api/cache.go", "lookup", method("", "fetchCached")),
		fn("api/store.go", "cache.fetchCached"),
	}
	g := Build(Inputs{}, units)
	if got := callees(g, "api/auth.go writeError"); got != "[]" {
		t.Errorf("Go w.Header() resolved to %s, want nothing", got)
	}
	if got := callees(g, "api/cache.go lookup"); got != "[api/store.go cache.fetchCached]" {
		t.Errorf("c.fetchCached() resolved to %s, want the one fetchCached method", got)
	}
}

func TestCalleesFoundOnlyByMethodNameAreMarked(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/x.rs", "Store::save"),
		fn("a/src/x.rs", "Store::load"),
		fn("a/src/x.rs", "Store::flush_all"),
		fn("a/src/y.rs", "named", path("Store", "save")),
		fn("a/src/y.rs", "guessed", method("store", "load")),
		fn("a/src/y.rs", "both", method("store", "flush_all"), path("Store", "flush_all")),
		fn("a/src/x.rs", "Store::own", method("self", "load")),
	})
	cases := map[string][]bool{
		"named":      {false}, // Type::f names its callee
		"guessed":    {true},  // x.m() found only because one method is called load
		"both":       {false}, // one named call is enough
		"Store::own": {false}, // a call on self names its type
	}
	for i, u := range g.Units {
		want, ok := cases[u.Name]
		if !ok {
			continue
		}
		if got := g.ByName[i]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: ByName = %v, want %v", u.Name, got, want)
		}
	}
}

func TestResolvedCallsAreCountedPerUnit(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/x.rs", "helper"),
		fn("a/src/y.rs", "two", free("helper"), free("helper"), free("missing")),
		fn("a/src/y.rs", "none", free("missing")),
	})
	if fmt.Sprint(g.ResolvedOf) != "[0 2 0]" || g.Resolved != 2 {
		t.Errorf("ResolvedOf = %v, Resolved = %d; want [0 2 0] and 2", g.ResolvedOf, g.Resolved)
	}
}

func TestFilesUnderARootCrateShareOneCrate(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("src/util.rs", "helper"),
		fn("other/src/lib.rs", "helper"),
		fn("src/store/pg.rs", "save", free("helper")),
	})

	if got, want := callees(g, "src/store/pg.rs save"), "[src/util.rs helper]"; got != want {
		t.Errorf("save calls %s, want %s", got, want)
	}
}

func TestGoPackagesUnderSrcStayApart(t *testing.T) {
	for file, want := range map[string]string{
		"src/a/x.go":      "src/a",
		"svc/src/b/y.go":  "svc/src/b",
		"src/store/pg.rs": ".",
		"svc/src/lib.rs":  "svc",
	} {
		if got := CrateOf(file); got != want {
			t.Errorf("CrateOf(%s) = %s, want %s", file, got, want)
		}
	}
}

func TestAMemberNamedSrcIsItsOwnCrate(t *testing.T) {
	if got := CrateOf("src/src/lib.rs"); got != "src" {
		t.Errorf("CrateOf(src/src/lib.rs) = %s, want src", got)
	}
}

func TestCallsStayInTheirLanguage(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("helper.go", "helper"),
		fn("tool.go", "Tool.Name"),
		fn("src/lib.rs", "run", free("helper"), method("t", "Name")),
		fn("src/util.rs", "only_rust"),
		fn("main.go", "start", free("only_rust")),
	})

	for caller, want := range map[string]string{
		"src/lib.rs run": "[]",
		"main.go start":  "[]",
	} {
		if got := callees(g, caller); got != want {
			t.Errorf("%s calls %s, want %s", caller, got, want)
		}
	}
}

// typed is a Go method call on an operand of a known type.
func typed(typ, name string) unit.Call {
	return unit.Call{Kind: unit.MethodCall, Type: typ, Name: name}
}

func TestGoMethodCallsResolveByTheOperandsType(t *testing.T) {
	onField := unit.Call{Kind: unit.MethodCall, Field: "store", Name: "Save"}
	units := []unit.Unit{
		fn("api/server.go", "Server.Get",
			typed("example.com/app/internal/store.DB", "Load"),     // another package's type
			typed("Cache", "Fill"),                                 // the caller's own package
			typed("strings.Builder", "WriteString"),                // an outside type: no guess
			onField,                                                // s.store.Save()
			typed("Saver", "Flush"),                                // an interface: guessed by name
			typed("example.com/app.Config", "Read"),                // the module's root package
			typed("example.com/other/internal/store.DB", "Close")), // another module ending alike: outside
		fn("internal/store/db.go", "DB.Load"),
		fn("other/db.go", "DB.Load"),
		fn("api/cache.go", "Cache.Fill"),
		fn("other/cache.go", "Cache.Fill"),
		fn("api/writer.go", "writer.WriteString"),
		fn("internal/store/store.go", "Store.Save"),
		fn("other/store.go", "Store.Save"),
		fn("api/file.go", "File.Flush"),
		// store is also the last part of another directory's path
		fn("store/db.go", "DB.Load"),
		fn("config.go", "Config.Read"),
		fn("internal/store/db.go", "DB.Close"),
	}
	in := Inputs{Fields: map[string]map[string]FieldType{
		"api": {"Server.store": {Type: "example.com/app/internal/store.Store"}},
	}, Modules: map[string]string{".": "example.com/app"}}
	g := Build(in, units)
	want := "[internal/store/db.go DB.Load api/cache.go Cache.Fill internal/store/store.go Store.Save api/file.go File.Flush config.go Config.Read]"
	if got := callees(g, "api/server.go Server.Get"); got != want {
		t.Errorf("Server.Get calls %s,\nwant %s", got, want)
	}
	if got, want := fmt.Sprint(g.ByName[0]), "[false false false true false]"; got != want {
		t.Errorf("ByName = %s, want %s: only the interface's call is a guess", got, want)
	}
}

// Without a go.mod, a typed call's package is unknown, and the call is
// guessed by its method name as before.
func TestGoTypedCallsWithoutAModuleAreGuessed(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("api/server.go", "Server.Get", typed("example.com/app/api.Cache", "Fill")),
		fn("api/cache.go", "Cache.Fill"),
	})
	if got := callees(g, "api/server.go Server.Get"); got != "[api/cache.go Cache.Fill]" || !g.ByName[0][0] {
		t.Errorf("Server.Get calls %s (by name %v), want Cache.Fill guessed by name", got, g.ByName[0])
	}
}

func TestGoMethodCallsOnACallsResultResolveByItsResultType(t *testing.T) {
	open := unit.Call{Kind: unit.PathCall, Qualifier: "store", Name: "Open"}
	units := []unit.Unit{
		fn("api/server.go", "Server.Get",
			open, // db, err := store.Open()
			unit.Call{Kind: unit.MethodCall, Name: "Save", From: 1},             // db.Save()
			unit.Call{Kind: unit.MethodCall, Name: "Close", From: 1, Result: 1}, // err.Close(): error has no type
			unit.Call{Kind: unit.MethodCall, Name: "Flush", From: 5},            // store.Open().Flush(), chained
			open,
			unit.Call{Kind: unit.MethodCall, Name: "Run", From: 7}, // on an unresolved call's result: guessed
			unit.Call{Kind: unit.FreeCall, Name: "unknown"}),
		// a type of Open's own package is written unqualified
		{File: "internal/store/open.go", Name: "Open", Results: []string{"DB", ""}},
		fn("internal/store/db.go", "DB.Save"),
		fn("other/db.go", "DB.Save"),
		fn("internal/store/db.go", "DB.Close"),
		fn("other/db.go", "DB.Close"),
		fn("internal/store/db.go", "DB.Flush"),
		fn("other/db.go", "DB.Flush"),
		fn("api/job.go", "job.Run"),
	}
	in := Inputs{Modules: map[string]string{".": "example.com/app"}, Crates: map[string]string{"internal/store": "store"}}
	g := Build(in, units)
	want := "[internal/store/open.go Open internal/store/db.go DB.Save internal/store/db.go DB.Flush api/job.go job.Run]"
	if got := callees(g, "api/server.go Server.Get"); got != want {
		t.Errorf("Server.Get calls %s,\nwant %s", got, want)
	}
	if got, want := fmt.Sprint(g.ByName[0]), "[false false false true]"; got != want {
		t.Errorf("ByName = %s, want %s: only the call on an unresolved result is a guess", got, want)
	}
}

// A producing call found only by its method name may be another type's
// method: its result types nothing.
func TestGoGuessedProducersTypeNothing(t *testing.T) {
	g := Build(Inputs{Modules: map[string]string{".": "example.com/app"}}, []unit.Unit{
		fn("api/server.go", "Server.Get",
			method("x", "Open"), // x untyped: Open guessed by its name
			unit.Call{Kind: unit.MethodCall, Name: "Save", From: 1}),
		{File: "api/conn.go", Name: "conn.Open", Results: []string{"DB"}},
		fn("api/db.go", "DB.Save"),
		fn("api/other.go", "Other.Save"),
	})
	if got, want := callees(g, "api/server.go Server.Get"), "[api/conn.go conn.Open]"; got != want {
		t.Errorf("Server.Get calls %s, want %s: Save on a guessed result stays unresolved", got, want)
	}
}

func TestRustMethodCallsResolveByTheOperandsType(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			typed("Store", "save"), // store: &Store
			unit.Call{Kind: unit.MethodCall, Field: "db", Name: "query"}, // self.db.query()
			typed("Thing", "frobnicate")),                                // a repository type, called directly
		fn("a/src/store.rs", "Store::save"),
		fn("a/src/cache.rs", "Cache::save"),
		fn("b/src/store.rs", "Store::save"), // another crate's Store
		fn("a/src/db.rs", "Database::query"),
		fn("a/src/other.rs", "Other::query"),
		fn("a/src/thing.rs", "Thing::frobnicate"),
	}
	g := Build(Inputs{Fields: map[string]map[string]FieldType{"a": {"Server.db": {Type: "Database", File: "a/src/server.rs"}}}}, units)
	want := "[a/src/store.rs Store::save a/src/db.rs Database::query a/src/thing.rs Thing::frobnicate]"
	if got := callees(g, "a/src/server.rs Server::run"); got != want {
		t.Errorf("Server::run calls %s,\nwant %s", got, want)
	}
	if got, want := fmt.Sprint(g.ByName[0]), "[false false false]"; got != want {
		t.Errorf("ByName = %s, want %s: every call is typed", got, want)
	}
}

// A Rust type often comes from another workspace crate: the call
// resolves into crates the caller's crate imports from, as Type::f does.
func TestRustTypedCallsReachImportedCrates(t *testing.T) {
	units := []unit.Unit{
		// use b::Client: the adapter writes the type as b::Client
		fn("a/src/server.rs", "Server::run", typed("b::Client", "send")),
		fn("b/src/client.rs", "Client::send"),
		fn("c/src/client.rs", "Client::send"), // a crate a does not import from
		fn("c/src/mailer.rs", "Mailer::send"),
	}
	in := Inputs{
		Imports: map[string]map[string]string{"a/src/server.rs": {"Client": "b::Client"}},
		Crates:  map[string]string{"a": "a", "b": "b", "c": "c"},
	}
	g := Build(in, units)
	if got, want := callees(g, "a/src/server.rs Server::run"), "[b/src/client.rs Client::send]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
	if !slices.Equal(g.ByName[0], []bool{false}) {
		t.Errorf("ByName = %v, want a named call", g.ByName[0])
	}
}

// A value of a type with no methods in the repository (Vec, Option) never
// calls a repository method, however its method is named.
func TestRustCallsOnOutsideTypesAreNotGuessed(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			typed("Vec", "frobnicate"),   // Vec has no methods here: nothing
			typed("Store", "frobnicate"), // Store has others: perhaps a trait default, guessed
			method("x", "frobnicate")),   // untyped: guessed as before
		fn("a/src/thing.rs", "Thing::frobnicate"),
		fn("a/src/store.rs", "Store::save"),
	})
	if got, want := fmt.Sprint(g.ByName[0]), "[true]"; got != want || callees(g, "a/src/server.rs Server::run") != "[a/src/thing.rs Thing::frobnicate]" {
		t.Errorf("calls %s (by name %s), want Thing::frobnicate guessed once, by the Store and untyped calls", callees(g, "a/src/server.rs Server::run"), got)
	}
	if g.ResolvedOf[0] != 2 {
		t.Errorf("resolved %d calls, want 2: the call on Vec reaches nothing", g.ResolvedOf[0])
	}
}

// A type's path tells an outside crate's type from the repository's of
// the same name: reqwest::Client is not the workspace's Client.
func TestRustTypedCallsTellOutsideCratesByThePath(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			typed("reqwest::Client", "send"),      // an outside crate's Client
			typed("crate::store::Store", "save"),  // this crate's
			typed("b::Ledger", "post"),            // a workspace crate's
			typed("super::queue::Queue", "push")), // a parent module's
		fn("a/src/client.rs", "Client::send"),
		fn("a/src/store.rs", "Store::save"),
		fn("b/src/ledger.rs", "Ledger::post"),
		fn("a/src/queue.rs", "Queue::push"),
	}
	in := Inputs{
		Imports: map[string]map[string]string{"a/src/server.rs": {"Ledger": "b::Ledger"}},
		Crates:  map[string]string{"a": "a", "b": "b"},
	}
	g := Build(in, units)
	want := "[a/src/store.rs Store::save b/src/ledger.rs Ledger::post a/src/queue.rs Queue::push]"
	if got := callees(g, "a/src/server.rs Server::run"); got != want {
		t.Errorf("Server::run calls %s,\nwant %s", got, want)
	}
}

// A path naming a workspace crate stays in that crate, even when the
// caller's own crate has a type of the same name.
func TestRustQualifiedTypesStayInTheirCrate(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run", typed("b::Client", "send"), typed("crate::Client", "close")),
		fn("a/src/client.rs", "Client::send"),
		fn("a/src/client.rs", "Client::close"),
		fn("b/src/client.rs", "Client::send"),
		fn("b/src/client.rs", "Client::close"),
	}
	in := Inputs{
		Imports: map[string]map[string]string{"a/src/server.rs": {"Client": "b::Client"}},
		Crates:  map[string]string{"a": "a", "b": "b"},
	}
	want := "[b/src/client.rs Client::send a/src/client.rs Client::close]"
	if got := callees(Build(in, units), "a/src/server.rs Server::run"); got != want {
		t.Errorf("Server::run calls %s,\nwant %s", got, want)
	}
}

// model::Client may name a child module of the caller's crate: a root
// that is one of the crate's modules stays in it; any other root is an
// outside crate.
func TestRustRelativeModulePathsStayInTheCallersCrate(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/lib.rs", "Server::run", typed("model::Client", "send"), typed("reqwest::Client", "close")),
		fn("a/src/model/client.rs", "Client::send"),
		fn("a/src/model/client.rs", "Client::close"),
	}
	if got, want := callees(Build(Inputs{Crates: map[string]string{"a": "a"}}, units), "a/src/lib.rs Server::run"), "[a/src/model/client.rs Client::send]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
}

// A module of only types has no units, but its file still names it.
func TestRustModulesOfOnlyTypesKeepPathsInTheCrate(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/lib.rs", "Server::run", typed("model::Client", "send")),
		fn("a/src/client.rs", "Client::send"), // model.rs declares Client; client.rs implements it
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{"a/src/lib.rs", "a/src/model.rs", "a/src/client.rs"}}
	if got, want := callees(Build(in, units), "a/src/lib.rs Server::run"), "[a/src/client.rs Client::send]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
}

// The adapter qualifies every imported type, so a bare type name is the
// caller's crate's own: its methods are never another crate's.
func TestRustBareTypesStayInTheCallersCrate(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run", typed("Client", "send")),
		fn("a/src/client.rs", "Client::close"),
		fn("b/src/client.rs", "Client::send"),
	}
	in := Inputs{
		Imports: map[string]map[string]string{"a/src/other.rs": {"Thing": "b::Thing"}}, // a depends on b
		Crates:  map[string]string{"a": "a", "b": "b"},
	}
	if got := callees(Build(in, units), "a/src/server.rs Server::run"); got != "[]" {
		t.Errorf("Server::run calls %s, want nothing: b's Client is not in scope", got)
	}
}

// A Rust value built by a call takes the producer's result type, read in
// the producer's crate: b's Store::new returns b's Store.
func TestRustCallsOnACallsResultResolveInTheProducersCrate(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			unit.Call{Kind: unit.PathCall, Qualifier: "b::Store", Name: "new"}, // let s = b::Store::new()
			unit.Call{Kind: unit.MethodCall, Name: "save", From: 1},            // s.save()
			unit.Call{Kind: unit.MethodCall, Name: "flush", From: 1, Result: 1}),
		{File: "b/src/store.rs", Name: "Store::new", Results: []string{"Store", "Store"}},
		fn("b/src/store.rs", "Store::save"),
		fn("b/src/store.rs", "Store::flush"),
		fn("a/src/store.rs", "Store::save"), // a's own Store
		fn("a/src/store.rs", "Store::flush"),
	}
	in := Inputs{
		Imports: map[string]map[string]string{"a/src/server.rs": {"Store": "b::Store"}},
		Crates:  map[string]string{"a": "a", "b": "b"},
	}
	want := "[b/src/store.rs Store::new b/src/store.rs Store::save b/src/store.rs Store::flush]"
	if got := callees(Build(in, units), "a/src/server.rs Server::run"); got != want {
		t.Errorf("Server::run calls %s,\nwant %s", got, want)
	}
}

// A Rust result type keeps its path: crate:: is the producer's crate, and
// an outside crate's type (anyhow::Result) is never guessed.
func TestRustResultPathsResolveFromTheProducer(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			unit.Call{Kind: unit.PathCall, Qualifier: "b::Db", Name: "connect"}, // Db::connect()
			unit.Call{Kind: unit.MethodCall, Name: "query", From: 1, Result: 1}, // db.query() after ?
			unit.Call{Kind: unit.MethodCall, Name: "frobnicate", From: 1}),      // on the anyhow::Result
		{File: "b/src/db.rs", Name: "Db::connect", Results: []string{"anyhow::Result", "crate::db::Db"}},
		fn("b/src/db.rs", "Db::query"),
		fn("a/src/db.rs", "Db::query"), // the caller's crate's Db: not this one
		fn("a/src/thing.rs", "Thing::frobnicate"),
	}
	in := Inputs{
		Imports:   map[string]map[string]string{"a/src/server.rs": {"Db": "b::Db"}},
		Crates:    map[string]string{"a": "a", "b": "b"},
		RustFiles: []string{"a/src/server.rs", "b/src/db.rs"},
	}
	want := "[b/src/db.rs Db::connect b/src/db.rs Db::query]"
	if got := callees(Build(in, units), "a/src/server.rs Server::run"); got != want {
		t.Errorf("Server::run calls %s,\nwant %s", got, want)
	}
}

// A value's type may come from a crate the caller does not depend on:
// b::make_c() returns c's Widget, whose methods a may call.
func TestRustResultTypesNeedNoDirectDependency(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/run.rs", "run",
			unit.Call{Kind: unit.PathCall, Qualifier: "b", Name: "make_c"},
			unit.Call{Kind: unit.MethodCall, Name: "spin", From: 1}),
		{File: "b/src/lib.rs", Name: "make_c", Results: []string{"c::Widget", "c::Widget"}},
		fn("c/src/widget.rs", "Widget::spin"),
		fn("d/src/gear.rs", "Gear::spin"),
	}
	in := Inputs{
		Imports: map[string]map[string]string{"a/src/run.rs": {"make_c": "b::make_c"}},
		Crates:  map[string]string{"a": "a", "b": "b", "c": "c", "d": "d"},
	}
	want := "[b/src/lib.rs make_c c/src/widget.rs Widget::spin]"
	if got := callees(Build(in, units), "a/src/run.rs run"); got != want {
		t.Errorf("run calls %s, want %s", got, want)
	}
}

// A result adapter the repository does not define (anyhow's context) is
// looked through to the call it adapts; one it does define is a call
// like any other, with its own result.
func TestRustResultAdaptersAreLookedThroughUnlessTheyAreOurs(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/run.rs", "run",
			unit.Call{Kind: unit.PathCall, Qualifier: "Db", Name: "connect"},                 // 1: Db::connect()
			unit.Call{Kind: unit.MethodCall, Name: "context", From: 1, Result: 2, Adapts: 1}, // 2: .await.context(..)
			unit.Call{Kind: unit.MethodCall, Name: "query", From: 2, Result: 1},              // 3: db.query() after ?
			unit.Call{Kind: unit.PathCall, Qualifier: "Wrapper", Name: "make"},               // 4: Wrapper::make()
			unit.Call{Kind: unit.MethodCall, Name: "unwrap", From: 4, Adapts: 2},             // 5: .unwrap(): Wrapper's own
			unit.Call{Kind: unit.MethodCall, Name: "render", From: 5}),                       // 6: on Widget
		{File: "a/src/db.rs", Name: "Db::connect", Results: []string{"", "", "anyhow::Result", "Db"}},
		fn("a/src/db.rs", "Db::query"),
		fn("a/src/other.rs", "Other::query"),
		{File: "a/src/wrapper.rs", Name: "Wrapper::make", Results: []string{"Wrapper", "Wrapper"}},
		{File: "a/src/wrapper.rs", Name: "Wrapper::unwrap", Results: []string{"Widget", "Widget"}},
		fn("a/src/wrapper.rs", "Wrapper::render"),
		fn("a/src/widget.rs", "Widget::render"),
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{"a/src/run.rs"}}
	want := "[a/src/db.rs Db::connect a/src/db.rs Db::query a/src/wrapper.rs Wrapper::make a/src/wrapper.rs Wrapper::unwrap a/src/widget.rs Widget::render]"
	if got := callees(Build(in, units), "a/src/run.rs run"); got != want {
		t.Errorf("run calls %s,\nwant %s", got, want)
	}
}

// std::fs::read names the standard library, not the crate's own read: a
// module path stays in the caller's crate only when its root is one of
// the crate's modules (or crate, self, super).
func TestRustOutsideModulePathsResolveToNothing(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/store.rs", "password",
			path("std::fs", "read"),    // the standard library's
			path("tokio::fs", "write"), // another outside crate's
			path("tls", "load"),        // one of the crate's modules
			path("crate::tls", "load")),
		fn("a/src/tls.rs", "read"),
		fn("a/src/tls.rs", "write"),
		fn("a/src/tls.rs", "load"),
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{"a/src/store.rs", "a/src/tls.rs"}}
	if got, want := callees(Build(in, units), "a/src/store.rs password"), "[a/src/tls.rs load]"; got != want {
		t.Errorf("password calls %s, want %s: std and tokio are no modules of the crate", got, want)
	}
}

// The adapter writes an imported root as its import's path: crate::util
// resolves in the crate, a workspace crate's path there, std::fs nowhere.
func TestRustExpandedModulePathsResolve(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/run.rs", "run", path("crate::util", "log"), path("std::fs", "read"), path("b", "post")),
		fn("a/src/util.rs", "log"),
		fn("a/src/util.rs", "read"),
		fn("b/src/lib.rs", "post"),
	}
	in := Inputs{Crates: map[string]string{"a": "a", "b": "b"}, RustFiles: []string{"a/src/run.rs", "a/src/util.rs", "b/src/lib.rs"}}
	if got, want := callees(Build(in, units), "a/src/run.rs run"), "[a/src/util.rs log b/src/lib.rs post]"; got != want {
		t.Errorf("run calls %s, want %s", got, want)
	}
}

// A relative module path names a child of the caller's module: from the
// crate root, bar is no module when only src/foo/bar.rs exists.
func TestRustRelativeModulePathsNameChildModules(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/lib.rs", "root", path("bar", "read"), path("foo", "load")),
		fn("a/src/foo.rs", "inside", path("bar", "write")), // foo's child bar
		fn("a/src/foo/bar.rs", "read"),
		fn("a/src/foo/bar.rs", "write"),
		fn("a/src/foo.rs", "load"),
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{"a/src/lib.rs", "a/src/foo.rs", "a/src/foo/bar.rs"}}
	g := Build(in, units)
	if got, want := callees(g, "a/src/lib.rs root"), "[a/src/foo.rs load]"; got != want {
		t.Errorf("root calls %s, want %s: bar is no module of the crate root", got, want)
	}
	if got, want := callees(g, "a/src/foo.rs inside"), "[a/src/foo/bar.rs write]"; got != want {
		t.Errorf("inside calls %s, want %s: bar is foo's child", got, want)
	}
}

// Type::f on a path keeps to the type's crate: chrono::Utc::now is not
// the repository's Utc::now.
func TestRustTypePathCallsKeepToTheirCrate(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/run.rs", "run", path("chrono::Utc", "now"), path("crate::clock::Clock", "now"), path("b::Ledger", "open")),
		fn("a/src/clock.rs", "Utc::now"),
		fn("a/src/clock.rs", "Clock::now"),
		fn("a/src/ledger.rs", "Ledger::open"),
		fn("b/src/ledger.rs", "Ledger::open"),
	}
	in := Inputs{
		Imports:   map[string]map[string]string{"a/src/run.rs": {"Ledger": "b::Ledger"}},
		Crates:    map[string]string{"a": "a", "b": "b"},
		RustFiles: []string{"a/src/run.rs", "a/src/clock.rs", "a/src/ledger.rs", "b/src/ledger.rs"},
	}
	if got, want := callees(Build(in, units), "a/src/run.rs run"), "[a/src/clock.rs Clock::now b/src/ledger.rs Ledger::open]"; got != want {
		t.Errorf("run calls %s, want %s", got, want)
	}
}

// Targets records calls resolved by name or type; a guess by the method
// name alone is no target, even when a named call reaches the same unit.
func TestTargetsLeaveOutGuessedCalls(t *testing.T) {
	g := Build(Inputs{}, []unit.Unit{
		fn("a/src/x.rs", "run", method("x", "save"), path("Store", "save")),
		fn("a/src/store.rs", "Store::save"),
	})
	if got := fmt.Sprint(g.Targets[0]); got != "[-1 1]" {
		t.Errorf("targets = %s, want the guessed call left out and the named one kept", got)
	}
}

// A module path names the module whose function is called: of several
// free functions named endpoint, the one in that module's file.
func TestRustModulePathsPickTheNamedModulesFunction(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/lib.rs", "root", path("crate::store", "endpoint"), path("store", "endpoint")),
		fn("a/src/config.rs", "load", path("super::store", "endpoint"), path("self::inner", "endpoint")),
		fn("a/src/store.rs", "endpoint"),
		fn("a/src/config.rs", "endpoint"),
		fn("a/src/config/inner.rs", "endpoint"),
		fn("a/src/api.rs", "reexported", path("crate::store", "helper")), // re-exported from a child: falls back
		fn("a/src/store/helpers.rs", "helper"),
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{
		"a/src/lib.rs", "a/src/config.rs", "a/src/store.rs", "a/src/config/inner.rs", "a/src/api.rs", "a/src/store/helpers.rs",
	}}
	g := Build(in, units)
	for caller, want := range map[string]string{
		"a/src/lib.rs root":       "[a/src/store.rs endpoint]",
		"a/src/config.rs load":    "[a/src/store.rs endpoint a/src/config/inner.rs endpoint]",
		"a/src/api.rs reexported": "[a/src/store/helpers.rs helper]",
	} {
		if got := callees(g, caller); got != want {
			t.Errorf("%s calls %s, want %s", caller, got, want)
		}
	}
}

func TestModulePathFollowsSelfSuperAndChildren(t *testing.T) {
	for _, c := range []struct{ path, file, want string }{
		{"crate::a::b", "x/src/z.rs", "a::b"},
		{"crate", "x/src/z.rs", ""},
		{"self::inner", "x/src/a/b.rs", "a::b::inner"},
		{"super::c", "x/src/a/b.rs", "a::c"},
		{"super::super::c", "x/src/a/b.rs", "c"},
		{"super", "x/src/a/b.rs", "a"},
		{"child", "x/src/a.rs", "a::child"},
		{"child", "x/src/lib.rs", "child"},
	} {
		if got := (Libraries{}).modulePath(c.path, unit.Unit{File: c.file}); got != c.want {
			t.Errorf("modulePath(%q, %q) = %q, want %q", c.path, c.file, got, c.want)
		}
	}
}

// A function in an inline module is that module's: inner::f from the
// file's module, self::inner::f, and crate::outer::inner::f reach it.
func TestRustInlineModulesResolve(t *testing.T) {
	inner := fn("a/src/lib.rs", "f")
	inner.Module = "inner"
	units := []unit.Unit{
		fn("a/src/lib.rs", "root", path("inner", "f"), path("self::inner", "g")),
		inner,
		{File: "a/src/lib.rs", Name: "g", Module: "inner"},
		fn("a/src/other.rs", "f"), // another f the call must not reach
		fn("a/src/other.rs", "g"),
		fn("a/src/api.rs", "serve", path("crate::inner", "f")),
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{"a/src/lib.rs", "a/src/other.rs", "a/src/api.rs"}}
	g := Build(in, units)
	for caller, want := range map[string]string{
		"a/src/lib.rs root":  "[a/src/lib.rs f a/src/lib.rs g]",
		"a/src/api.rs serve": "[a/src/lib.rs f]",
	} {
		if got := callees(g, caller); got != want {
			t.Errorf("%s calls %s, want %s", caller, got, want)
		}
	}
}

// A path naming a module that does not declare the function (it
// re-exports one) falls back only to the crate's single function of that
// name, never to the caller's own file's.
func TestRustModulePathFallbackSkipsTheCallersOwnFunction(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/lib.rs", "root", path("inner", "f")),
		fn("a/src/lib.rs", "f"),
		fn("a/src/other.rs", "f"), // what inner re-exports
		{File: "a/src/lib.rs", Name: "marker", Module: "inner"},
	}
	in := Inputs{Crates: map[string]string{"a": "a"}, RustFiles: []string{"a/src/lib.rs", "a/src/other.rs"}}
	if got := callees(Build(in, units), "a/src/lib.rs root"); got != "[]" {
		t.Errorf("root calls %s, want nothing: two functions named f, neither inner's", got)
	}
}

// A Rust field chain is followed through the struct declarations: from
// self, a typed variable or a call's result, each field's type in turn.
func TestRustFieldChainsResolve(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			unit.Call{Kind: unit.MethodCall, Field: "ctx.db", Name: "query"},               // self.ctx.db.query()
			unit.Call{Kind: unit.MethodCall, Type: "Request", Field: "store", Name: "get"}, // req.store.get()
			unit.Call{Kind: unit.PathCall, Qualifier: "Made", Name: "make"},                // Made::make()
			unit.Call{Kind: unit.MethodCall, From: 3, Field: "inner", Name: "run"},         // .inner.run()
			unit.Call{Kind: unit.MethodCall, Field: "ctx.missing", Name: "query"}),         // an unknown field: nothing
		{File: "a/src/made.rs", Name: "Made::make", Results: []string{"Made", "Made"}},
		fn("a/src/db.rs", "Db::query"), fn("a/src/other.rs", "Other::query"),
		fn("a/src/store.rs", "Store::get"), fn("a/src/other.rs", "Other::get"),
		fn("a/src/inner.rs", "Inner::run"), fn("a/src/other.rs", "Other::run"),
	}
	in := Inputs{
		Crates:    map[string]string{"a": "a"},
		RustFiles: []string{"a/src/server.rs"},
		Fields: map[string]map[string]FieldType{"a": {
			"Server.ctx":    {Type: "crate::context::Context", File: "a/src/server.rs"},
			"Context.db":    {Type: "Db", File: "a/src/context.rs"},
			"Request.store": {Type: "Store", File: "a/src/request.rs"},
			"Made.inner":    {Type: "Inner", File: "a/src/made.rs"},
		}},
	}
	want := "[a/src/db.rs Db::query a/src/store.rs Store::get a/src/made.rs Made::make a/src/inner.rs Inner::run]"
	if got := callees(Build(in, units), "a/src/server.rs Server::run"); got != want {
		t.Errorf("Server::run calls %s,\nwant %s", got, want)
	}
}

// A field's type is read where its struct is declared: db::Db written in
// server.rs is server::db's Db, not a root db module's.
func TestRustFieldTypesKeepTheirDeclaringModule(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run", unit.Call{Kind: unit.MethodCall, Field: "db", Name: "query"}),
		fn("a/src/server/db.rs", "Db::query"),
		fn("a/src/db.rs", "Db::query"), // a root db module's Db: not this one
	}
	in := Inputs{
		Crates:    map[string]string{"a": "a"},
		RustFiles: []string{"a/src/server.rs", "a/src/server/db.rs", "a/src/db.rs"},
		Fields:    map[string]map[string]FieldType{"a": {"Server.db": {Type: "db::Db", File: "a/src/server.rs"}}},
	}
	if got, want := callees(Build(in, units), "a/src/server.rs Server::run"), "[a/src/server/db.rs Db::query]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
}

// Looking through a result adapter keeps the fields before it:
// make().inner.unwrap().run() runs on inner's type, never on make()'s.
func TestRustAdaptersKeepTheFieldPathBeforeThem(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/run.rs", "go",
			unit.Call{Kind: unit.PathCall, Qualifier: "Made", Name: "make"},
			unit.Call{Kind: unit.MethodCall, From: 1, Field: "inner", Name: "unwrap", Adapts: 2},
			unit.Call{Kind: unit.MethodCall, From: 2, Name: "run"}),
		{File: "a/src/made.rs", Name: "Made::make", Results: []string{"Made", "Made"}},
		fn("a/src/made.rs", "Made::run"), // make()'s own run: never this
		fn("a/src/inner.rs", "Inner::run"),
	}
	in := Inputs{
		Crates:    map[string]string{"a": "a"},
		RustFiles: []string{"a/src/run.rs"},
		// inner: Option<Inner>: unwrap() takes Inner out of the field.
		Fields: map[string]map[string]FieldType{"a": {"Made.inner": {Type: "Option", Unwrapped: "Inner", File: "a/src/made.rs"}}},
	}
	if got, want := callees(Build(in, units), "a/src/run.rs go"), "[a/src/made.rs Made::make a/src/inner.rs Inner::run]"; got != want {
		t.Errorf("go calls %s, want %s", got, want)
	}
}

// A path into another workspace crate's module (b::foo::Db) names that
// module's type among the crate's types of the name.
func TestRustWorkspaceModulePathsPickTheNamedModulesType(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run", typed("b::foo::Db", "query"), typed("b::Db", "query")),
		fn("b/src/foo.rs", "Db::query"),
		fn("b/src/bar.rs", "Db::query"),
		fn("b/src/lib.rs", "Db::query"),
	}
	in := Inputs{
		Crates:    map[string]string{"a": "a", "b": "b"},
		Imports:   map[string]map[string]string{"a/src/server.rs": {"Db": "b::foo::Db"}},
		RustFiles: []string{"a/src/server.rs", "b/src/foo.rs", "b/src/bar.rs", "b/src/lib.rs"},
	}
	if got, want := callees(Build(in, units), "a/src/server.rs Server::run"), "[b/src/foo.rs Db::query b/src/lib.rs Db::query]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
}

// An adapter on a field of self or of a typed variable unwraps that
// field: self.inner.unwrap().run(), req.inner.context()?.run().
func TestRustAdaptersOnFieldsOfSelfAndVariablesUnwrapTheField(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::go",
			unit.Call{Kind: unit.MethodCall, Field: "inner", Name: "unwrap", Adapts: 2},
			unit.Call{Kind: unit.MethodCall, From: 1, Name: "run"},
			unit.Call{Kind: unit.MethodCall, Type: "Request", Field: "inner", Name: "context", Adapts: 1},
			unit.Call{Kind: unit.MethodCall, From: 3, Result: 1, Name: "stop"}),
		fn("a/src/inner.rs", "Inner::run"),
		fn("a/src/inner.rs", "Inner::stop"),
		fn("a/src/other.rs", "Other::run"),
		fn("a/src/other.rs", "Other::stop"),
	}
	in := Inputs{
		Crates:    map[string]string{"a": "a"},
		RustFiles: []string{"a/src/server.rs"},
		Fields: map[string]map[string]FieldType{"a": {
			"Server.inner":  {Type: "Option", Unwrapped: "Inner", File: "a/src/server.rs"},
			"Request.inner": {Type: "Result", Unwrapped: "Inner", File: "a/src/server.rs"},
		}},
	}
	if got, want := callees(Build(in, units), "a/src/server.rs Server::go"), "[a/src/inner.rs Inner::run a/src/inner.rs Inner::stop]"; got != want {
		t.Errorf("Server::go calls %s, want %s", got, want)
	}
}

// impl is a method of a trait's implementation.
func impl(file, name, trait string) unit.Unit {
	u := fn(file, name)
	u.Trait = trait
	return u
}

// A value of a trait type (dyn Store, impl Store, S: Store) calls the
// method of the trait's only implementation, or the trait's default when
// that implementation leaves it; with several implementations which one
// runs is not known, and no other method of the name is guessed.
func TestRustCallsThroughTraitsReachTheirOnlyImplementation(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			typed("dyn crate::store::Store", "get"), // one implementation
			typed("dyn Store", "describe"),          // a default it leaves
			typed("dyn Codec", "encode"),            // two implementations
			typed("dyn Codec", "flush")),            // the trait's own default, but neither leaves it
		fn("a/src/store.rs", "Store::describe"),
		impl("a/src/pg.rs", "PgStore::get", "Store"),
		fn("a/src/cache.rs", "Cache::get"),
		impl("a/src/json.rs", "Json::encode", "Codec"),
		impl("a/src/json.rs", "Json::flush", "Codec"),
		impl("a/src/bin.rs", "Bin::encode", "Codec"),
		impl("a/src/bin.rs", "Bin::flush", "Codec"),
		fn("a/src/codec.rs", "Codec::flush"),
		fn("a/src/other.rs", "Other::encode"),
	}
	in := Inputs{Impls: []unit.Impl{
		{File: "a/src/pg.rs", Trait: "Store", Type: "PgStore"},
		{File: "a/src/json.rs", Trait: "Codec", Type: "Json"},
		{File: "a/src/bin.rs", Trait: "Codec", Type: "Bin"},
	}}
	g := Build(in, units)
	if got, want := callees(g, "a/src/server.rs Server::run"), "[a/src/pg.rs PgStore::get a/src/store.rs Store::describe]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(g.ByName[0]), "[false false]"; got != want {
		t.Errorf("ByName = %s, want %s", got, want)
	}
}

// A concrete type named like a trait is no trait: its own methods. A
// trait's implementations are those in its crate or crates importing
// from it; another crate's trait of the name is another trait.
func TestRustTraitCallsKeepToTheTrait(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run",
			typed("crate::model::Store", "get"), // the struct
			typed("dyn Store", "put")),          // the trait
		fn("a/src/model.rs", "Store::get"),
		impl("a/src/pg.rs", "PgStore::get", "Store"),
		impl("a/src/pg.rs", "PgStore::put", "Store"),
		impl("c/src/disk.rs", "Disk::put", "Store"), // c's own trait Store
	}
	in := Inputs{
		Crates:    map[string]string{"a": "a", "c": "c"},
		RustFiles: []string{"a/src/server.rs", "a/src/model.rs", "a/src/pg.rs", "c/src/disk.rs"},
		Impls: []unit.Impl{
			{File: "a/src/pg.rs", Trait: "Store", Type: "PgStore"},
			{File: "c/src/disk.rs", Trait: "Store", Type: "Disk"},
		},
	}
	if got, want := callees(Build(in, units), "a/src/server.rs Server::run"), "[a/src/model.rs Store::get a/src/pg.rs PgStore::put]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
}

// A method neither the trait nor its implementations declare is a
// supertrait's (Derived: Base): guessed by its name, as untyped calls are.
func TestRustTraitCallsToOtherTraitsMethodsAreGuessed(t *testing.T) {
	units := []unit.Unit{
		fn("a/src/server.rs", "Server::run", typed("dyn Derived", "base")),
		impl("a/src/concrete.rs", "Concrete::derived", "Derived"),
		impl("a/src/concrete.rs", "Concrete::base", "Base"),
	}
	in := Inputs{Impls: []unit.Impl{
		{File: "a/src/concrete.rs", Trait: "Derived", Type: "Concrete"},
		{File: "a/src/concrete.rs", Trait: "Base", Type: "Concrete"},
	}}
	g := Build(in, units)
	if got, want := callees(g, "a/src/server.rs Server::run"), "[a/src/concrete.rs Concrete::base]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(g.ByName[0]), "[true]"; got != want {
		t.Errorf("ByName = %s, want %s: found by its name", got, want)
	}
}

// An implementation written with the trait's crate path (impl
// api::Store for Disk, no use) is one of that crate's trait's.
func TestRustTraitImplsWrittenWithTheCratePathCount(t *testing.T) {
	units := []unit.Unit{
		fn("api/src/server.rs", "Server::run", typed("dyn Store", "put")),
		impl("api/src/mem.rs", "Mem::put", "Store"),
		impl("disk/src/disk.rs", "Disk::put", "Store"),
	}
	in := Inputs{
		Crates: map[string]string{"api": "api", "disk": "disk"},
		Impls: []unit.Impl{
			{File: "api/src/mem.rs", Trait: "Store", TraitPath: "Store", Type: "Mem"},
			{File: "disk/src/disk.rs", Trait: "Store", TraitPath: "api::Store", Type: "Disk"},
		},
	}
	if got := callees(Build(in, units), "api/src/server.rs Server::run"); got != "[]" {
		t.Errorf("Server::run calls %s, want nothing: two implementations", got)
	}
}

// svc's manifest roots its library at lib/core.rs instead of src/lib.rs.
var movedLibrary = Libraries{Roots: map[string]string{"svc/lib/core.rs": "svc"}}

func TestALibraryMovedOutOfSrcKeepsItsCrateAndModules(t *testing.T) {
	for file, want := range map[string]string{
		"svc/lib/core.rs":         "svc",
		"svc/lib/store/client.rs": "svc",
		"svc/src/main.rs":         "svc",
		"other/src/lib.rs":        "other",
	} {
		if got := movedLibrary.CrateOf(file); got != want {
			t.Errorf("CrateOf(%s) = %s, want %s", file, got, want)
		}
	}
	for file, want := range map[string]string{
		"svc/lib/core.rs":         "",
		"svc/lib/config.rs":       "config",
		"svc/lib/store/mod.rs":    "store",
		"svc/lib/store/client.rs": "store::client",
		"other/src/a/b.rs":        "a::b",
	} {
		if got, ok := movedLibrary.ModuleOf(file); !ok || got != want {
			t.Errorf("ModuleOf(%s) = %q, %v, want %q", file, got, ok, want)
		}
	}
}

func TestCratePathsResolveInALibraryMovedOutOfSrc(t *testing.T) {
	g := Build(Inputs{Libraries: movedLibrary}, []unit.Unit{
		fn("svc/lib/core.rs", "version"),
		fn("svc/lib/config.rs", "load"),
		fn("other/src/config.rs", "load"),
		fn("svc/lib/store/client.rs", "open", path("crate::config", "load"), path("crate", "version")),
	})
	if got, want := callees(g, "svc/lib/store/client.rs open"), "[svc/lib/config.rs load svc/lib/core.rs version]"; got != want {
		t.Errorf("open calls %s, want %s", got, want)
	}
}

// A library rooted at its package's top (core.rs) claims only its own
// files: not the package's other Cargo targets, nor a nested package.
func TestALibraryRootedAtItsPackageTopClaimsOnlyItsFiles(t *testing.T) {
	l := Libraries{
		Roots:    map[string]string{"svc/core.rs": "svc", "core.rs": "."},
		Packages: map[string]bool{".": true, "svc": true, "svc/member": true, "crates/x": true},
	}
	for file, want := range map[string]bool{
		"svc/core.rs":           true,
		"svc/store.rs":          true,
		"svc/store/client.rs":   true,
		"svc/src/main.rs":       false,
		"svc/src/cli.rs":        false,
		"svc/src/bin/tool.rs":   false,
		"svc/build.rs":          false,
		"svc/examples/demo.rs":  false,
		"svc/tests/api.rs":      false,
		"svc/benches/b.rs":      false,
		"svc/member/src/lib.rs": false,
		"crates/x/src/lib.rs":   false,
		"store.rs":              true,
		"src/main.rs":           false,
	} {
		if _, _, _, got := l.Locate(file); got != want {
			t.Errorf("Locate(%s) found = %v, want %v", file, got, want)
		}
	}
	if got := l.CrateOf("svc/member/src/lib.rs"); got != "svc/member" {
		t.Errorf("CrateOf(nested member) = %s, want its own crate", got)
	}
}

// A library its manifest roots inside a target directory (autoexamples =
// false, path = "examples/lib.rs") owns that directory: the configured
// root wins over the target-directory rules.
func TestALibraryRootedInATargetDirectoryOwnsIt(t *testing.T) {
	l := Libraries{
		Roots:    map[string]string{"svc/examples/lib.rs": "svc"},
		Packages: map[string]bool{"svc": true},
	}
	for file, want := range map[string]bool{
		"svc/examples/lib.rs":         true,
		"svc/examples/store.rs":       true,
		"svc/examples/store/inner.rs": true,
		"svc/tests/api.rs":            false,
	} {
		if _, _, _, got := l.Locate(file); got != want {
			t.Errorf("Locate(%s) found = %v, want %v", file, got, want)
		}
	}
}
