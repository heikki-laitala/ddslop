package rust

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func mustDecls(t *testing.T, path, src string) unit.Decls {
	t.Helper()
	_, decls, err := Parse(path, []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return decls
}

func fieldList(fields []unit.Field) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f.Name + ":" + f.Type
	}
	return strings.Join(parts, ",")
}

func TestParseListsStructsWithNamedFields(t *testing.T) {
	src := `pub struct Budget {
    pub id: String,
    limit: Option<Vec< u8 >>,
}

struct Unit;
struct Pair(u32, u32);

mod inner {
    struct Nested { a: u32 }
}

fn f() {
    struct Local { a: u32 }
}

#[cfg(test)]
mod tests {
    struct Fixture { a: u32 }
}
`
	decls := mustDecls(t, "lib.rs", src)
	var got []string
	for _, ty := range decls.Structs {
		got = append(got, fmt.Sprintf("%s %s:%d-%d %s test=%v", ty.Name, ty.File, ty.StartLine, ty.EndLine, fieldList(ty.Fields), ty.Test))
	}
	want := []string{
		"Budget lib.rs:1-4 id:String,limit:Option<Vec<u8>> test=false",
		"Nested lib.rs:10-10 a:u32 test=false",
		"Fixture lib.rs:19-19 a:u32 test=true",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("types:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if decls.Structs[0].Nodes == 0 {
		t.Error("a type's size in nodes is not recorded")
	}
}

func TestParseReturnsTheSameUnitsAsUnits(t *testing.T) {
	src := "fn a() {}\nstruct S { x: u32 }\nimpl S { fn b(&self) {} }\n"
	units, _, err := Parse("lib.rs", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(names(units)); got != "[a S::b]" {
		t.Errorf("Parse units = %s, want [a S::b]", got)
	}
}

func TestParseListsModuleLevelConstants(t *testing.T) {
	src := `pub const MAX_MODELS: usize = 64;
static TIMEOUT: Duration = Duration::from_secs( 5 );
const CODE: &str = "ledger unavailable";
const CONTRACT: &str = include_str!("openapi.json");

impl Store {
    const ASSOCIATED: u32 = 1;
}

fn f() {
    const LOCAL: u32 = 2;
}

mod inner {
    const NESTED: u32 = 3;
}
`
	decls := mustDecls(t, "lib.rs", src)
	var got []string
	for _, c := range decls.Consts {
		got = append(got, fmt.Sprintf("%s=%s %s:%d literal=%v macro=%v", c.Name, c.Value, c.File, c.Line, c.Literal, c.Macro))
	}
	want := []string{
		"MAX_MODELS=64 lib.rs:1 literal=false macro=false",
		"TIMEOUT=Duration::from_secs(5) lib.rs:2 literal=false macro=false",
		`CODE="ledger unavailable" lib.rs:3 literal=true macro=false`,
		`CONTRACT=include_str!("openapi.json") lib.rs:4 literal=false macro=true`,
		"NESTED=3 lib.rs:15 literal=false macro=false",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("constants:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
