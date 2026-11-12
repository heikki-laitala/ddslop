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

func TestStatementsRecordWhetherTheyReturnEarly(t *testing.T) {
	src := `fn handle(cx: &Cx) -> Result<Response> {
    trace::tag_event(EVENT);
    let user = current_user(cx).await.signed_in().ok_or_unauthorized()?;
    if !user.admin {
        return Err(Forbidden);
    }
    let Some(org) = visited(cx, &user) else {
        return not_found();
    };
    let names = org.members.iter().map(|m| m.name.parse::<Name>()).collect::<Result<Vec<_>>>();
    render(org, names)
}
`
	var got []bool
	for _, st := range byName(t, mustUnits(t, src), "handle").Blocks[0] {
		got = append(got, st.Exits)
	}
	// Only an explicit return or a let-else counts: a ? propagates errors,
	// as setup code does too, and the one in the closure leaves the
	// closure, not the function.
	if want := "[false false true true false false]"; fmt.Sprint(got) != want {
		t.Errorf("exits = %v, want %s", got, want)
	}
}

func TestUnitsRecordAValueOpenedAtTheStartAndClosedAtTheEnd(t *testing.T) {
	src := `async fn reserve(db: &Db) -> Result<()> {
    let mut db = db.clone();
    let mut tx = db.transaction().await?;
    lock(&mut tx).await?;
    write(&mut tx).await?;
    tx.commit().await?;
    Ok(())
}
fn hashed(data: &[u8]) -> Digest {
    let mut digest = Sha256::new();
    digest.update(data);
    digest.finalize()
}
fn dropped(pool: &Pool) {
    let lease = pool.acquire();
    use_it(&lease);
    drop(lease);
}
fn closed_in_the_middle(db: &Db) {
    let tx = db.begin();
    tx.commit();
    one();
    two();
    three();
    four();
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		if s := u.Scope; s != nil {
			got = append(got, fmt.Sprintf("%s %s..%s %d-%d", u.Name, s.Opens, s.Closes, s.StartLine, s.EndLine))
		}
	}
	// finalize consumes a hasher, but it computes, not closes: not a scope.
	want := "reserve transaction..commit 3-6\ndropped acquire..drop 15-17"
	if strings.Join(got, "\n") != want {
		t.Errorf("scopes:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestUnitsRecordMatchesOnStringLiterals(t *testing.T) {
	src := `fn interpret(status: &str) -> Outcome {
    match status {
        "committed" => Outcome::Done,
        "replayed" | "refused" => Outcome::Seen,
        other if other.starts_with("x") => Outcome::Odd,
        _ => Outcome::Unknown,
    }
}
fn small(kind: &str) -> u8 {
    match kind {
        "a" => 1,
        "b" => 2,
        _ => 0,
    }
}
fn numbers(n: u8) -> &'static str {
    match n {
        1 => "one",
        2 => "two",
        _ => "many",
    }
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		for _, m := range u.StringMatches {
			got = append(got, fmt.Sprintf("%s %v %d-%d", u.Name, m.Values, m.StartLine, m.EndLine))
		}
	}
	if want := `interpret ["committed" "refused" "replayed"] 2-7`; strings.Join(got, "\n") != want {
		t.Errorf("string matches:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestParseListsEnumsWithTheirVariants(t *testing.T) {
	src := `pub enum NotSwept {
    HistoryUnavailable(String),
    Store(StoreFault),
    Gone { at:   u64 },
    Empty,
}

#[cfg(test)]
mod tests {
    enum Fixture { A, B }
}
`
	var got []string
	for _, e := range mustDecls(t, src).Enums {
		var vs []string
		for _, v := range e.Variants {
			vs = append(vs, v.Name+v.Payload)
		}
		got = append(got, fmt.Sprintf("%s %d-%d %v test=%v", e.Name, e.StartLine, e.EndLine, vs, e.Test))
	}
	want := "NotSwept 1-6 [HistoryUnavailable(String) Store(StoreFault) Gone{at:u64} Empty] test=false\nFixture 10-10 [A B] test=true"
	if strings.Join(got, "\n") != want {
		t.Errorf("enums:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestUnitsRecordTheirParameters(t *testing.T) {
	src := `impl Store {
    fn write(&mut self, db: &Db, mut deadline:   Duration, (a, b): (u8, u8)) {}
}
`
	var got []string
	for _, p := range byName(t, mustUnits(t, src), "Store::write").Params {
		got = append(got, p.Name+": "+p.Type)
	}
	// self is no parameter of the group kind, and a tuple pattern has no
	// single name.
	if want := "[db: &Db deadline: Duration]"; fmt.Sprint(got) != want {
		t.Errorf("params = %v, want %s", got, want)
	}
}

func TestAScopeOpensWithTheCallBeforeItsErrorConversion(t *testing.T) {
	src := `async fn attempt(&mut self) -> Result<(), Attempt> {
    let transaction = self.client.transaction().await.map_err(retry)?;
    write(&transaction).await?;
    transaction.commit().await.map_err(retry)
}
fn leased(pool: &Pool) -> anyhow::Result<()> {
    let lease = pool.acquire().context("no lease")?;
    use_it(&lease);
    lease.release();
    Ok(())
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		if s := u.Scope; s != nil {
			got = append(got, u.Name+" "+s.Opens+".."+s.Closes)
		}
	}
	if want := "attempt transaction..commit\nleased acquire..release"; strings.Join(got, "\n") != want {
		t.Errorf("scopes:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestAScopeOpensThroughCallsWithTypeArguments(t *testing.T) {
	src := `async fn attempt(&mut self) -> Result<(), Attempt> {
    let transaction = self.client.transaction().await.map_err::<Attempt, _>(retry)?;
    write(&transaction).await?;
    transaction.commit().await.map_err(retry)
}
fn leased(pool: &Pool) {
    let lease = pool.acquire::<Lease>();
    use_it(&lease);
    lease.release();
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		if s := u.Scope; s != nil {
			got = append(got, u.Name+" "+s.Opens+".."+s.Closes)
		}
	}
	if want := "attempt transaction..commit\nleased acquire..release"; strings.Join(got, "\n") != want {
		t.Errorf("scopes:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestUnitsRecordTheirErrorForms(t *testing.T) {
	src := `fn load(store: &Store) -> Result<Row, Fault> {
    match store.get().map_err(Fault::Store) {
        Err(StoreError::Missing) => return Ok(Row::default()),
        Err(e) if e.to_string().contains("busy") => return Err(Fault::Busy),
        other => other,
    }?;
    if let Err(Fault::Store(_)) = store.ping() {
        return Err(Fault::Down);
    }
    let row = store.read().with_context(|| "read")?;
    if row.err.to_string() == "stale" {
        return Err(Fault::Stale);
    }
    store.write(&row).map_err(|e| Fault::Write(e))?;
    Ok(row)
}
`
	var got []string
	for _, f := range byName(t, mustUnits(t, src), "load").Forms {
		got = append(got, fmt.Sprintf("%d %s %s", f.Line, f.Concern, f.Form))
	}
	want := []string{
		"2 error-wrapping map_err",
		"3 error-checks variant",
		"4 error-checks message",
		"7 error-checks variant",
		"10 error-wrapping context",
		"11 error-checks message",
		"14 error-wrapping map_err",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("forms:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestErrorFormsInMacrosQualifiedPathsAndAnyhowContext(t *testing.T) {
	src := `fn check(r: Result<u8, Fault>, cx: Context) -> Result<u8, Fault> {
    if matches!(r, Err(Fault::Gone)) {
        return Ok(0);
    }
    if let Err(crate::errors::Fault::Busy) = r {
        return Ok(1);
    }
    let a = load().context("load")?;
    let b = load().context(format!("load {a}"))?;
    let c = load().with_context(|| "load")?;
    let d = serve().with_context(cx).await;
    Ok(a + b + c + d)
}
`
	var got []string
	for _, f := range byName(t, mustUnits(t, src), "check").Forms {
		got = append(got, fmt.Sprintf("%d %s %s", f.Line, f.Concern, f.Form))
	}
	want := []string{
		"2 error-checks variant",
		"5 error-checks variant",
		"8 error-wrapping context",
		"9 error-wrapping context",
		"10 error-wrapping context",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("forms:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAMessageCheckInAMatchesGuardIsRecorded(t *testing.T) {
	src := `fn busy(r: Result<u8, Fault>) -> bool {
    matches!(r, Err(e) if e.to_string().contains("busy"))
}
`
	var got []string
	for _, f := range byName(t, mustUnits(t, src), "busy").Forms {
		got = append(got, fmt.Sprintf("%d %s %s", f.Line, f.Concern, f.Form))
	}
	if want := "2 error-checks message"; strings.Join(got, "\n") != want {
		t.Errorf("forms = %v, want %s", got, want)
	}
}

func TestLiteralsAndCommentsInMatchesAreNoErrorChecks(t *testing.T) {
	src := `fn quoted(s: &str) -> bool {
    matches!(s, "Err(Fault::Gone)" | "e.to_string() == x") // Err(Fault::Gone)
}
fn commented(r: Result<u8, Fault>) -> bool {
    matches!(r, /* Err(Fault::Gone) */ Ok(_))
}
fn real(r: Result<u8, Fault>) -> bool {
    matches!(r, Err(crate::Fault::Gone))
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		for _, f := range u.Forms {
			got = append(got, u.Name+" "+f.Form)
		}
	}
	if want := "real variant"; strings.Join(got, "\n") != want {
		t.Errorf("forms = %v, want only %s", got, want)
	}
}

func TestQualifiedErrPatternsAreVariantChecks(t *testing.T) {
	src := `fn gone(r: Result<u8, Fault>) -> bool {
    match r {
        Result::Err(Fault::Gone) => true,
        core::result::Result::Err(Fault::Busy) => true,
        _ => false,
    }
}
`
	var got []string
	for _, f := range byName(t, mustUnits(t, src), "gone").Forms {
		got = append(got, fmt.Sprintf("%d %s", f.Line, f.Form))
	}
	if want := "3 variant\n4 variant"; strings.Join(got, "\n") != want {
		t.Errorf("forms = %v, want variant checks on lines 3 and 4", got)
	}
}

func TestOnlyTheMatchesPatternIsSearchedAndItsPathMayBeQualified(t *testing.T) {
	src := `fn value_side(r: Option<Result<u8, Fault>>) -> bool {
    matches!(Some(Err(Fault::Gone)), Some(Ok(_)))
}
fn qualified(r: Result<u8, Fault>) -> bool {
    core::matches!(r, Err(Fault::Gone))
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		for _, f := range u.Forms {
			got = append(got, u.Name+" "+f.Form)
		}
	}
	if want := "qualified variant"; strings.Join(got, "\n") != want {
		t.Errorf("forms = %v, want only %s", got, want)
	}
}

func TestAnErrInAMatchesGuardIsNoVariantCheck(t *testing.T) {
	src := `fn guarded(r: Result<u8, Fault>) -> bool {
    matches!(r, Ok(v) if predicate(Err(Fault::Gone)))
}
`
	if forms := byName(t, mustUnits(t, src), "guarded").Forms; len(forms) != 0 {
		t.Errorf("forms = %v, want none: the pattern matches Ok", forms)
	}
}
