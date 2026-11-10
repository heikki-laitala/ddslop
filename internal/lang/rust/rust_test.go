package rust

import (
	"fmt"
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
		{Kind: unit.Identifier, Text: "a", Line: 1, Declares: true},
		{Kind: unit.Identifier, Text: "x", Line: 1, Declares: true},
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

func TestDeclaringIdentifiersAreMarked(t *testing.T) {
	src := `fn run(input: Vec<u32>, cfg: &Config) -> u32 {
    let total = 0;
    let (lo, hi) = cfg.bounds();
    for item in input.iter() {
        if let Some(v) = check(item) {
            total += v;
        }
    }
    match cfg.mode {
        Mode::Fast(speed) if speed > lo => hi,
        _ => total,
    }
    input.iter().map(|elem| elem + 1).sum::<u32>()
}
`
	f := byName(t, mustUnits(t, src), "run")

	declared := map[string]bool{}
	used := map[string]bool{}
	for _, l := range f.Leaves {
		if l.Kind != unit.Identifier {
			continue
		}
		if l.Declares {
			declared[l.Text] = true
		} else {
			used[l.Text] = true
		}
	}

	for _, name := range []string{"run", "input", "cfg", "total", "lo", "hi", "item", "v", "speed", "elem"} {
		if !declared[name] {
			t.Errorf("%q should be marked as declared", name)
		}
	}
	for _, name := range []string{"bounds", "check", "Some", "mode", "iter", "map", "sum", "Mode", "Fast"} {
		if declared[name] {
			t.Errorf("%q is used, not declared, but was marked as declared", name)
		}
		if !used[name] {
			t.Errorf("%q should appear as a used identifier", name)
		}
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

func TestTraitDefaultMethodsAreNamedAfterTheTrait(t *testing.T) {
	units := mustUnits(t, `trait Greeter {
    fn greet(&self) -> String { String::from("hi") }
}
`)
	if got := names(units); len(got) != 1 || got[0] != "Greeter::greet" {
		t.Errorf("names = %v, want [Greeter::greet]", got)
	}
}

func TestSeqFlattensStructureWithLeavesInOrder(t *testing.T) {
	src := `fn a(x: u32) -> u32 {
    x + 1
}

fn b(y: i64) -> i64 {
    y + 2
}

fn c(x: u32) -> u32 {
    log(x);
    x + 1
}
`
	units := mustUnits(t, src)
	a, b, c := byName(t, units, "a"), byName(t, units, "b"), byName(t, units, "c")

	if fmt.Sprint(a.Seq) != fmt.Sprint(b.Seq) {
		t.Errorf("renamed copies have different sequences:\n%v\n%v", a.Seq, b.Seq)
	}
	if len(c.Seq) <= len(a.Seq) {
		t.Errorf("function with an extra statement has a sequence of %d symbols, want more than %d", len(c.Seq), len(a.Seq))
	}
	if len(a.SeqLines) != len(a.Seq) {
		t.Fatalf("SeqLines has %d entries for %d symbols", len(a.SeqLines), len(a.Seq))
	}
	if a.SeqLines[0] != a.StartLine || a.SeqLines[len(a.SeqLines)-1] != a.EndLine {
		t.Errorf("SeqLines run %d..%d, want %d..%d", a.SeqLines[0], a.SeqLines[len(a.SeqLines)-1], a.StartLine, a.EndLine)
	}

	// Leaf symbols appear in Seq in the same order and kinds as Leaves.
	var leafKinds []uint32
	opens, closes := 0, 0
	for _, sym := range a.Seq {
		switch {
		case unit.IsLeaf(sym):
			leafKinds = append(leafKinds, sym)
		case sym == unit.Close:
			closes++
		default:
			opens++
		}
	}
	if len(leafKinds) != len(a.Leaves) {
		t.Fatalf("Seq has %d leaf symbols, unit has %d leaves", len(leafKinds), len(a.Leaves))
	}
	for i, l := range a.Leaves {
		if leafKinds[i] != unit.LeafSymbol(l.Kind) {
			t.Errorf("leaf symbol %d = %d, want %d", i, leafKinds[i], unit.LeafSymbol(l.Kind))
		}
	}
	if opens != closes || opens+len(a.Leaves) != a.Nodes {
		t.Errorf("opens=%d closes=%d leaves=%d nodes=%d: every non-leaf node needs one open and one close", opens, closes, len(a.Leaves), a.Nodes)
	}
}
