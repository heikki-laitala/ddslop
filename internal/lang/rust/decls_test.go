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
