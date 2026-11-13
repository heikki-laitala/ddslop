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
			path("Self", "finish"), path("utils", "log"), path("Missing", "nothing")),
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
	in := Inputs{Fields: map[string]map[string]string{
		"api": {"Server.store": "example.com/app/internal/store.Store"},
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
	g := Build(Inputs{Fields: map[string]map[string]string{"a": {"Server.db": "Database"}}}, units)
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
		fn("a/src/server.rs", "Server::run", typed("Client", "send")),
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
		fn("a/src/server.rs", "Server::run", typed("model::Client", "send"), typed("reqwest::Client", "close")),
		fn("a/src/model/client.rs", "Client::send"),
		fn("a/src/model/client.rs", "Client::close"),
	}
	if got, want := callees(Build(Inputs{Crates: map[string]string{"a": "a"}}, units), "a/src/server.rs Server::run"), "[a/src/model/client.rs Client::send]"; got != want {
		t.Errorf("Server::run calls %s, want %s", got, want)
	}
}
