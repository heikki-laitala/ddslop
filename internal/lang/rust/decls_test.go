package rust

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func mustDecls(t *testing.T, src string) unit.Decls {
	t.Helper()
	_, decls, err := Parse("lib.rs", []byte(src))
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
    label: &'static   str,
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
	decls := mustDecls(t, src)
	var got []string
	for _, ty := range decls.Structs {
		got = append(got, fmt.Sprintf("%s %s:%d-%d %s test=%v", ty.Name, ty.File, ty.StartLine, ty.EndLine, fieldList(ty.Fields), ty.Test))
	}
	want := []string{
		"Budget lib.rs:1-5 id:String,limit:Option<Vec<u8>>,label:&'static str test=false",
		"Nested lib.rs:11-11 a:u32 test=false",
		"Fixture lib.rs:20-20 a:u32 test=true",
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
	decls := mustDecls(t, src)
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

func TestUnitsRecordVisibilityAndExternalCallers(t *testing.T) {
	src := `pub fn api() {}
pub(crate) fn internal() {}
fn private() {}

#[inline]
#[must_use]
#[allow(dead_code)]
#[tracing::instrument(skip(self))]
#[cfg(feature = "x")]
#[deprecated]
pub fn annotated() {}

#[no_mangle]
pub extern "C" fn exported() {}

#[tokio::main]
async fn main() {}

#[get("/health")]
pub async fn health() {}

pub struct S;
impl S {
    pub fn method(&self) {}
}
`
	units := mustUnits(t, src)
	var got []string
	for _, u := range units {
		got = append(got, fmt.Sprintf("%s public=%v external=%v", u.Name, u.Public, u.External))
	}
	want := []string{
		"api public=true external=false",
		"internal public=false external=false",
		"private public=false external=false",
		"annotated public=true external=false",
		"exported public=true external=true",
		"main public=false external=true",
		"health public=true external=true",
		"S::method public=true external=false",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("units:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestUnitsRecordWhatAWrapperForwardsTo(t *testing.T) {
	src := `fn visible(principal: &str, token_id: u64) -> bool {
    in_namespace(principal, token_id)
}
async fn decide(connection: &mut Conn, mode: Mode) -> Decision {
    super::decide(&mut connection, &mode).await
}
impl Store {
    fn stored(&self, record: Record) -> Op {
        self.stored_record(record)
    }
    fn delegate(&self, x: u32) -> u32 {
        self.inner.get(x)
    }
    fn new(bound: u64) -> Self {
        Self(bound)
    }
    fn reordered(a: u32, b: u32) -> u32 {
        add(b, a)
    }
    fn extra(a: u32) -> u32 {
        add(a, 1)
    }
    fn converts(a: u32) -> u64 {
        widen(a.into())
    }
    fn no_params() -> Clock {
        now()
    }
    fn two_statements(a: u32) -> u32 {
        log(a);
        add(a)
    }
}
`
	units := mustUnits(t, src)
	var got []string
	for _, u := range units {
		if u.Forwards != "" {
			got = append(got, u.Name+" -> "+u.Forwards)
		}
	}
	want := []string{
		"visible -> in_namespace",
		"decide -> super::decide",
		"Store::stored -> self.stored_record",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("wrappers:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseListsTraitsImplsAndTypeNames(t *testing.T) {
	src := `pub trait DirectoryStore {
    fn put(&self);
}
trait Io {}

pub struct DirectorySession { a: u32 }
enum Mode { A }
struct Unit;
type Alias = u32;

impl DirectoryStore for DirectorySession {
    fn put(&self) {}
}
impl<T: Read> Io for T {}
impl<'a> io::Write for &'a mut Buffer<'a> {}
impl DirectorySession {}

#[cfg(test)]
mod tests {
    impl super::DirectoryStore for Fake {}
}
`
	decls := mustDecls(t, src)
	var traits, impls []string
	for _, tr := range decls.Traits {
		traits = append(traits, fmt.Sprintf("%s %d-%d public=%v", tr.Name, tr.StartLine, tr.EndLine, tr.Public))
	}
	for _, im := range decls.Impls {
		impls = append(impls, fmt.Sprintf("%s for %s %d-%d blanket=%v test=%v", im.Trait, im.Type, im.StartLine, im.EndLine, im.Blanket, im.Test))
	}
	wantTraits := "DirectoryStore 1-3 public=true\nIo 4-4 public=false"
	wantImpls := "DirectoryStore for DirectorySession 11-13 blanket=false test=false\n" +
		"Io for T 14-14 blanket=true test=false\n" +
		"Write for Buffer 15-15 blanket=false test=false\n" +
		"DirectoryStore for Fake 20-20 blanket=false test=true"
	if got := strings.Join(traits, "\n"); got != wantTraits {
		t.Errorf("traits:\n%s\nwant:\n%s", got, wantTraits)
	}
	if got := strings.Join(impls, "\n"); got != wantImpls {
		t.Errorf("impls:\n%s\nwant:\n%s", got, wantImpls)
	}
	if got := fmt.Sprint(decls.TypeNames); got != "[DirectorySession Mode Unit Alias]" {
		t.Errorf("type names = %s, want [DirectorySession Mode Unit Alias]", got)
	}
}

func TestParseRecordsTheTypesAStructConvertsWith(t *testing.T) {
	src := `#[derive(Deserialize)]
#[serde(try_from = "AuthoredPort", into = "wire::Port<'static>")]
pub struct Port {
    host: u32,
}

#[serde(from = "Raw")]
struct Plain {
    a: u32,
}

#[serde(rename_all = "camelCase")]
struct Other {
    a: u32,
}
`
	var got []string
	for _, s := range mustDecls(t, src).Structs {
		got = append(got, fmt.Sprintf("%s %v", s.Name, s.Converts))
	}
	if want := "Port [AuthoredPort Port]\nPlain [Raw]\nOther []"; strings.Join(got, "\n") != want {
		t.Errorf("conversions:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}
