package flow

import (
	"fmt"
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
	g := Build(nil, []unit.Unit{
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
	g := Build(nil, []unit.Unit{
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
	g := Build(nil, []unit.Unit{
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
	g := Build(nil, units)

	if got := fmt.Sprint(g.Entries(map[string]bool{"LedgerApi": true})); got != "[0]" {
		t.Errorf("entries = %s, want [0]", got)
	}
}

func TestSliceIsBreadthFirstDepthLimitedAndSkipsTests(t *testing.T) {
	g := Build(nil, []unit.Unit{
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
	g := Build(nil, []unit.Unit{
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
	g := Build(nil, []unit.Unit{
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
	g := Build(nil, []unit.Unit{
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
	g := Build(imports, []unit.Unit{
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
	g := Build(nil, []unit.Unit{
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
