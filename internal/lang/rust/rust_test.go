package rust

import (
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func mustUnits(t *testing.T, src string) []unit.Unit {
	t.Helper()
	units, err := Units("lib.rs", []byte(src))
	if err != nil {
		t.Fatalf("Units: %v", err)
	}
	return units
}

func byName(t *testing.T, units []unit.Unit, name string) unit.Unit {
	t.Helper()
	for _, u := range units {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no unit named %q in %v", name, names(units))
	return unit.Unit{}
}

func names(units []unit.Unit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.Name
	}
	return out
}

func TestUnitsExtractsFunctionsAndMethods(t *testing.T) {
	src := `use std::fmt;

fn free(a: u32) -> u32 {
    a + 1
}

struct Store;

impl Store {
    fn get(&self, id: u64) -> Option<u64> {
        Some(id)
    }
}

mod inner {
    pub fn nested() {}
}
`
	units := mustUnits(t, src)

	got := names(units)
	want := []string{"free", "Store::get", "nested"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}

	free := byName(t, units, "free")
	if free.File != "lib.rs" || free.StartLine != 3 || free.EndLine != 5 {
		t.Errorf("free location = %s:%d-%d, want lib.rs:3-5", free.File, free.StartLine, free.EndLine)
	}
	get := byName(t, units, "Store::get")
	if get.StartLine != 10 || get.EndLine != 12 {
		t.Errorf("Store::get lines = %d-%d, want 10-12", get.StartLine, get.EndLine)
	}
	if free.Nodes == 0 {
		t.Errorf("free.Nodes = 0, want a positive size")
	}
}

func TestShapeIgnoresNamesValuesTypesAndFormatting(t *testing.T) {
	src := `fn load_user(db: &Db, user_id: u64) -> Option<User> {
    let row = db.get("users", user_id)?;
    Some(User::from(row))
}

// A renamed and reformatted copy with different literal and types.
fn load_order(store: &Store, order_id: i64) -> Option<Order> {
    let r = store.get("orders", order_id)?; // fetch
    Some(Order::from(r))
}

fn load_other(db: &Db, user_id: u64) -> Option<User> {
    let row = db.get("users", user_id)?;
    log(&row);
    Some(User::from(row))
}
`
	units := mustUnits(t, src)
	user := byName(t, units, "load_user")
	order := byName(t, units, "load_order")
	other := byName(t, units, "load_other")

	if user.Shape != order.Shape {
		t.Errorf("renamed copy has a different shape: %x vs %x", user.Shape, order.Shape)
	}
	if user.Shape == other.Shape {
		t.Errorf("function with an extra statement has the same shape %x", user.Shape)
	}
	if user.Nodes != order.Nodes {
		t.Errorf("renamed copy has a different size: %d vs %d", user.Nodes, order.Nodes)
	}
}

func TestLeavesLineUpBetweenRenamedCopies(t *testing.T) {
	src := `fn a(x: u32) -> u32 {
    x + 1
}

fn b(y: i64) -> i64 {
    y + 2
}
`
	units := mustUnits(t, src)
	a := byName(t, units, "a")
	b := byName(t, units, "b")

	want := []unit.Leaf{
		{Kind: unit.Identifier, Text: "a", Line: 1},
		{Kind: unit.Identifier, Text: "x", Line: 1},
		{Kind: unit.Type, Text: "u32", Line: 1},
		{Kind: unit.Type, Text: "u32", Line: 1},
		{Kind: unit.Identifier, Text: "x", Line: 2},
		{Kind: unit.Literal, Text: "1", Line: 2},
	}
	if len(a.Leaves) != len(want) {
		t.Fatalf("a.Leaves = %+v, want %+v", a.Leaves, want)
	}
	for i := range want {
		if a.Leaves[i] != want[i] {
			t.Errorf("a.Leaves[%d] = %+v, want %+v", i, a.Leaves[i], want[i])
		}
	}
	if len(b.Leaves) != len(a.Leaves) {
		t.Fatalf("leaf counts differ: %d vs %d", len(a.Leaves), len(b.Leaves))
	}
	if b.Leaves[5].Text != "2" || b.Leaves[2].Text != "i64" {
		t.Errorf("b leaves not aligned with a: %+v", b.Leaves)
	}
}

func TestStringLiteralIsOneLeaf(t *testing.T) {
	units := mustUnits(t, `fn f() -> &'static str { "a\nb" }`)
	f := byName(t, units, "f")
	last := f.Leaves[len(f.Leaves)-1]
	if last.Kind != unit.Literal || last.Text != `"a\nb"` {
		t.Errorf("last leaf = %+v, want the whole string literal", last)
	}
}

func TestFunctionsWithSyntaxErrorsAreSkipped(t *testing.T) {
	src := `fn good() -> u32 { 1 }

fn broken() -> u32 { let = ; }
`
	units := mustUnits(t, src)
	got := names(units)
	if len(got) != 1 || got[0] != "good" {
		t.Errorf("names = %v, want only [good]", got)
	}
}
