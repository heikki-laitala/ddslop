package rust

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func mustDecls(t *testing.T, src string) unit.Decls {
	t.Helper()
	_, decls := testutil.Parse(t, Parse, "lib.rs", src)
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
	got := testutil.Format(decls.Structs, func(ty unit.Struct) string {
		return fmt.Sprintf("%s %s:%d-%d %s test=%v", ty.Name, ty.File, ty.StartLine, ty.EndLine, fieldList(ty.Fields), ty.Test)
	})
	want := []string{
		"Budget lib.rs:1-5 id:String,limit:Option<Vec<u8>>,label:&'static str test=false",
		"Nested lib.rs:11-11 a:u32 test=false",
		"Fixture lib.rs:20-20 a:u32 test=true",
	}
	testutil.Lines(t, "types", got, want)
	if decls.Structs[0].Nodes == 0 {
		t.Error("a type's size in nodes is not recorded")
	}
}

func TestParseReturnsTheSameUnitsAsUnits(t *testing.T) {
	src := "fn a() {}\nstruct S { x: u32 }\nimpl S { fn b(&self) {} }\n"
	units, _ := testutil.Parse(t, Parse, "lib.rs", src)
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
	got := testutil.Format(decls.Consts, func(c unit.Const) string {
		return fmt.Sprintf("%s=%s %s:%d literal=%v macro=%v", c.Name, c.Value, c.File, c.Line, c.Literal, c.Macro)
	})
	want := []string{
		"MAX_MODELS=64 lib.rs:1 literal=false macro=false",
		"TIMEOUT=Duration::from_secs(5) lib.rs:2 literal=false macro=false",
		`CODE="ledger unavailable" lib.rs:3 literal=true macro=false`,
		`CONTRACT=include_str!("openapi.json") lib.rs:4 literal=false macro=true`,
		"NESTED=3 lib.rs:15 literal=false macro=false",
	}
	testutil.Lines(t, "constants", got, want)
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
	got := testutil.Format(units, func(u unit.Unit) string {
		return fmt.Sprintf("%s public=%v external=%v", u.Name, u.Public, u.External)
	})
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
	testutil.Lines(t, "units", got, want)
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
	got := testutil.Format(units, func(u unit.Unit) string {
		if u.Forwards != "" {
			return u.Name + " -> " + u.Forwards
		}
		return ""
	})
	want := []string{
		"visible -> in_namespace",
		"decide -> super::decide",
		"Store::stored -> self.stored_record",
	}
	testutil.Lines(t, "wrappers", got, want)
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
		impls = append(impls, fmt.Sprintf("%s (%s) for %s %d-%d blanket=%v test=%v", im.Trait, im.TraitPath, im.Type, im.StartLine, im.EndLine, im.Blanket, im.Test))
	}
	wantTraits := "DirectoryStore 1-3 public=true\nIo 4-4 public=false"
	wantImpls := "DirectoryStore (DirectoryStore) for DirectorySession 11-13 blanket=false test=false\n" +
		"Io (Io) for T 14-14 blanket=true test=false\n" +
		"Write (io::Write) for Buffer 15-15 blanket=false test=false\n" +
		"DirectoryStore (super::DirectoryStore) for Fake 20-20 blanket=false test=true"
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
	got := testutil.Format(mustDecls(t, src).Structs, func(s unit.Struct) string {
		return fmt.Sprintf("%s %v", s.Name, s.Converts)
	})
	testutil.Lines(t, "conversions", got, []string{
		"Port [AuthoredPort Port]",
		"Plain [Raw]",
		"Other []",
	})
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
	got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
		if s := u.Scope; s != nil {
			return fmt.Sprintf("%s %s..%s %d-%d", u.Name, s.Opens, s.Closes, s.StartLine, s.EndLine)
		}
		return ""
	})
	// finalize consumes a hasher, but it computes, not closes: not a scope.
	testutil.Lines(t, "scopes", got, []string{
		"reserve transaction..commit 3-6",
		"dropped acquire..drop 15-17",
	})
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
	testutil.Lines(t, "string matches", got, []string{`interpret ["committed" "refused" "replayed"] 2-7`})
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
	testutil.Lines(t, "enums", got, []string{
		"NotSwept 1-6 [HistoryUnavailable(String) Store(StoreFault) Gone{at:u64} Empty] test=false",
		"Fixture 10-10 [A B] test=true",
	})
}

func TestUnitsRecordTheirParameters(t *testing.T) {
	src := `impl Store {
    fn write(&mut self, db: &Db, mut deadline:   Duration, (a, b): (u8, u8)) {}
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "Store::write").Params, func(p unit.Param) string {
		return p.Name + ": " + p.Type
	})
	// self is no parameter of the group kind, and a tuple pattern has no
	// single name.
	if want := "[db: &Db deadline: Duration]"; fmt.Sprint(got) != want {
		t.Errorf("params = %v, want %s", got, want)
	}
}

// A scope opens with the call before an error conversion or with type
// arguments, not with the conversion.
func TestAScopeOpensWithTheCallBeforeItsConversionOrTypeArguments(t *testing.T) {
	for name, src := range map[string]string{
		"error conversion": `async fn attempt(&mut self) -> Result<(), Attempt> {
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
`,
		"type arguments": `async fn attempt(&mut self) -> Result<(), Attempt> {
    let transaction = self.client.transaction().await.map_err::<Attempt, _>(retry)?;
    write(&transaction).await?;
    transaction.commit().await.map_err(retry)
}
fn leased(pool: &Pool) {
    let lease = pool.acquire::<Lease>();
    use_it(&lease);
    lease.release();
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
				if s := u.Scope; s != nil {
					return u.Name + " " + s.Opens + ".." + s.Closes
				}
				return ""
			})
			testutil.Lines(t, "scopes", got, []string{
				"attempt transaction..commit",
				"leased acquire..release",
			})
		})
	}
}

// forms returns a unit's forms as "line concern form", in order.
func forms(u unit.Unit) []string {
	var out []string
	for _, f := range u.Forms {
		out = append(out, fmt.Sprintf("%d %s %s", f.Line, f.Concern, f.Form))
	}
	return out
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
	got := forms(byName(t, mustUnits(t, src), "load"))
	want := []string{
		"2 error-wrapping map_err",
		"3 error-checks variant",
		"4 error-checks message",
		"4 error-values variant",
		"7 error-checks variant",
		"8 error-values variant",
		"10 error-wrapping context",
		"11 error-checks message",
		"12 error-values variant",
		"14 error-wrapping map_err",
	}
	testutil.Lines(t, "forms", got, want)
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
	got := forms(byName(t, mustUnits(t, src), "check"))
	want := []string{
		"2 error-checks variant",
		"5 error-checks variant",
		"8 error-wrapping context",
		"9 error-wrapping context",
		"10 error-wrapping context",
	}
	testutil.Lines(t, "forms", got, want)
}

func TestUnitsRecordHowTheyMakeErrors(t *testing.T) {
	src := `fn parse(s: &str, e: Fault) -> Result<u8, Fault> {
    if s.is_empty() {
        return Err(Fault::Empty);
    }
    if s.len() > 9 {
        return Err(crate::Fault::Long { len: s.len() });
    }
    if s == "x" {
        return Err(Fault::Bad(s.to_owned()));
    }
    if s == "y" {
        return Err(format!("bad {s}"));
    }
    if s == "z" {
        return Err("bad".to_string());
    }
    if s == "w" {
        return Err(String::from("bad"));
    }
    if s == "v" {
        bail!("bad {s}");
    }
    if s == "u" {
        return Err(anyhow::anyhow!("bad"));
    }
    if s == "t" {
        return Err(e);
    }
    if s == "q" {
        return Err(Fault::new(s));
    }
    if s == "p" {
        return Result::Err(Fault::Empty);
    }
    if s == "o" {
        return core::result::Result::Err(r#"bad"#.to_string());
    }
    if s == "n" {
        return Err(std::string::String::from("bad"));
    }
    Ok(0)
}
`
	got := forms(byName(t, mustUnits(t, src), "parse"))
	want := []string{
		"3 error-values variant",
		"6 error-values variant",
		"9 error-values variant",
		"12 error-values string",
		"15 error-values string",
		"18 error-values string",
		"21 error-values anyhow",
		"24 error-values anyhow",
		"33 error-values variant",
		"36 error-values string",
		"39 error-values string",
	}
	testutil.Lines(t, "forms", got, want)
}

func TestBinariesRecordNoErrorValues(t *testing.T) {
	src := `fn args(s: &str) -> Result<u8, String> {
    if s.is_empty() {
        return Err(format!("usage: {s}"));
    }
    s.parse().map_err(|_| Fault::Number)
}
`
	for _, path := range []string{"src/main.rs", "svc/src/bin/tool.rs", "svc/src/bin/tool/args.rs"} {
		units, _ := testutil.Parse(t, Parse, path, src)
		got := forms(byName(t, units, "args"))
		if want := "5 error-wrapping map_err"; strings.Join(got, "\n") != want {
			t.Errorf("%s: forms = %v, want %s", path, got, want)
		}
	}
}

func TestAMessageCheckInAMatchesGuardIsRecorded(t *testing.T) {
	src := `fn busy(r: Result<u8, Fault>) -> bool {
    matches!(r, Err(e) if e.to_string().contains("busy"))
}
`
	got := forms(byName(t, mustUnits(t, src), "busy"))
	testutil.Lines(t, "forms", got, []string{"2 error-checks message"})
}

// Only an Err in the pattern of matches! is a variant check, its path
// maybe qualified: not one in a literal, a comment or the value matched.
func TestOnlyAnErrInTheMatchesPatternIsAVariantCheck(t *testing.T) {
	src := `fn quoted(s: &str) -> bool {
    matches!(s, "Err(Fault::Gone)" | "e.to_string() == x") // Err(Fault::Gone)
}
fn commented(r: Result<u8, Fault>) -> bool {
    matches!(r, /* Err(Fault::Gone) */ Ok(_))
}
fn real(r: Result<u8, Fault>) -> bool {
    matches!(r, Err(crate::Fault::Gone))
}
fn value_side(r: Option<Result<u8, Fault>>) -> bool {
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
	testutil.Lines(t, "forms", got, []string{"real variant", "qualified variant"})
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
	got := testutil.Format(byName(t, mustUnits(t, src), "gone").Forms, func(f unit.Form) string {
		return fmt.Sprintf("%d %s", f.Line, f.Form)
	})
	testutil.Lines(t, "forms", got, []string{
		"3 variant",
		"4 variant",
	})
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

// A value of a trait type (dyn, impl, a type parameter's bound) calls
// the trait's methods: its type is the trait's path, marked "dyn " to
// tell it from a concrete type of the name. Marker traits (Send,
// 'static) say nothing of the methods; a parameter of two traits is
// ambiguous.
func TestMethodCallsThroughTraitsRecordTheTrait(t *testing.T) {
	src := `use crate::store::Store;

fn run<S: Send + Store, U, V: Store + Saver>(a: Arc<dyn Store + Send + Sync>, b: impl Saver + 'static, c: &mut S, d: U, e: V, f: Box<dyn Fn(u8) -> u8>)
where
    U: Clone + Saver,
{
    a.get();
    b.save();
    c.get();
    d.save();
    e.get();
    f.call();
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "run").Calls, func(c unit.Call) string {
		return fmt.Sprintf("%s type=%q", c.Name, c.Type)
	})
	want := []string{
		`get type="dyn crate::store::Store"`,
		`save type="dyn Saver"`,
		`get type="dyn crate::store::Store"`,
		`save type="dyn Saver"`,
		`get type=""`,
		`call type=""`,
	}
	testutil.Lines(t, "calls", got, want)
}

func TestMethodCallsRecordTheTypeTheCodeStates(t *testing.T) {
	src := `impl Server {
    fn run<T: Saver>(&self, store: &mut Store, shared: Arc<Cache>, me: &Self, any: T, d: &dyn Saver, n: u32) {
        store.save();
        shared.fill();
        me.ping();
        any.save();
        d.save();
        n.count();
        self.db.query();
        self.flush();
        let c: Cache = make();
        c.fill();
        let l = Ledger { id: 1 };
        l.post();
        {
            let store = other();
            store.save();
        }
        store.save();
        match load() {
            Some(store) => store.save(),
            None => {}
        }
        if let Some(shared) = find() {
            shared.fill();
        }
        let f = |store| store.save();
        store.save();
    }
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "Server::run").Calls, func(c unit.Call) string {
		if c.Kind == unit.MethodCall {
			return fmt.Sprintf("%d %s type=%q field=%q self=%v", c.Line, c.Name, c.Type, c.Field, c.Qualifier == "self")
		}
		return ""
	})
	want := []string{
		`3 save type="Store" field="" self=false`,
		`4 fill type="Cache" field="" self=false`,
		`5 ping type="Server" field="" self=false`,
		`6 save type="dyn Saver" field="" self=false`,
		`7 save type="dyn Saver" field="" self=false`,
		`8 count type="" field="" self=false`,
		`9 query type="" field="db" self=false`,
		`10 flush type="" field="" self=true`,
		`12 fill type="Cache" field="" self=false`,
		`14 post type="Ledger" field="" self=false`,
		`17 save type="" field="" self=false`,
		`19 save type="Store" field="" self=false`,
		`21 save type="" field="" self=false`,
		`25 fill type="" field="" self=false`,
		`27 save type="" field="" self=false`,
		`28 save type="Store" field="" self=false`,
	}
	testutil.Lines(t, "method calls", got, want)
}

func TestStructFieldTypesAreRecordedForRust(t *testing.T) {
	src := `struct Server<T> {
    db: Arc<Database>,
    cache: &'static Cache,
    names: Vec<String>,
    inner: T,
    pub store: crate::store::Store,
}
struct Pair(Left, Right);
`
	got := testutil.Format(mustDecls(t, src).FieldTypes, func(f unit.FieldType) string {
		return fmt.Sprintf("%s.%s %s", f.Struct, f.Field, f.Type)
	})
	// Vec is recorded too: the graph finds no Vec method in the repository
	// and falls back to guessing by name, as without a type. A field of no
	// type (inner: T) is recorded as unknown, so that a struct of the same
	// name elsewhere cannot type it.
	want := []string{"Server.db Database", "Server.cache Cache", "Server.names Vec", "Server.inner ", "Server.store crate::store::Store"}
	testutil.Lines(t, "field types", got, want)
}

// A field's unwrapped type is what ? or unwrap() leaves of it: inside an
// Option or Result, the type itself otherwise.
func TestStructFieldTypesRecordWhatUnwrappingLeaves(t *testing.T) {
	src := `struct Server {
    db: Option<Arc<Database>>,
    cache: Result<Cache, Error>,
    store: Store,
}
`
	got := testutil.Format(mustDecls(t, src).FieldTypes, func(f unit.FieldType) string {
		return fmt.Sprintf("%s %s %s", f.Field, f.Type, f.Unwrapped)
	})
	want := []string{"db Option Database", "cache Result Cache", "store Store Store"}
	testutil.Lines(t, "field types", got, want)
}

func TestRustBindingsTakeEffectWhereTheyAreInScope(t *testing.T) {
	src := `fn run(store: &Store, items: Vec<Item>) {
    for store in store.items() {
        store.save();
    }
    if let Some(store) = store.load() {
        store.save();
    } else {
        store.save();
    }
    while let Some(store) = store.next() {
        store.save();
    }
    let mut cache: Cache = make();
    cache.fill();
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "run").Calls, func(c unit.Call) string {
		return fmt.Sprintf("%d %s %q", c.Line, c.Name, c.Type)
	})
	want := []string{
		`2 items "Store"`, `3 save ""`,
		`5 load "Store"`, `6 save ""`, `8 save "Store"`,
		`10 next "Store"`, `11 save ""`,
		`13 make ""`, `14 fill "Cache"`,
	}
	testutil.Lines(t, "calls", got, want)
}

func TestRustTypesKeepTheirPathQualifiedByImports(t *testing.T) {
	src := `use reqwest::Client;
use crate::store::{Store, Cache as Kept};

struct Server {
    http: Client,
    store: Store,
    other: other_crate::Thing,
}

fn run(c: Client, s: Store, k: Kept, local: Ledger, q: crate::queue::Queue) {
    c.send();
    s.save();
    k.fill();
    local.post();
    q.push_back();
}
`
	units, decls := testutil.Parse(t, Parse, "a/src/lib.rs", src)
	got := testutil.Format(byName(t, units, "run").Calls, func(c unit.Call) string {
		return fmt.Sprintf("%s %s", c.Name, c.Type)
	})
	for _, f := range decls.FieldTypes {
		got = append(got, fmt.Sprintf("field %s %s", f.Field, f.Type))
	}
	want := []string{
		"send reqwest::Client", "save crate::store::Store", "fill crate::store::Cache", "post Ledger", "push_back crate::queue::Queue",
		"field http reqwest::Client", "field store crate::store::Store", "field other other_crate::Thing",
	}
	testutil.Lines(t, "types", got, want)
}

func TestLetChainBindingsApplyToLaterConditions(t *testing.T) {
	src := `fn run(store: &Store) {
    if let Some(store) = load() && store.ready() {
        store.save();
    } else {
        store.save();
    }
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "run").Calls, func(c unit.Call) string {
		return fmt.Sprintf("%d %s %q", c.Line, c.Name, c.Type)
	})
	want := []string{`2 load ""`, `2 ready ""`, `3 save ""`, `5 save "Store"`}
	testutil.Lines(t, "calls", got, want)
}

func TestRustImportsQualifyOnlyWhereTheyAreVisible(t *testing.T) {
	src := `use crate::store::Store;

fn local(c: Client) {
    use reqwest::Client;
    c.send();
    let r: Client = make();
    r.send();
}

fn other(c: Client, s: Store) {
    c.send();
    s.save();
}

mod inner {
    use crate::cache::Cache;
    fn run(c: Cache, s: Store) {
        c.fill();
        s.save();
    }
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		for _, c := range u.Calls {
			got = append(got, fmt.Sprintf("%s %s %s", u.Name, c.Name, c.Type))
		}
	}
	// A use in a function's body is not in scope in its signature: the
	// parameter is the module's Client, the let's reqwest's.
	want := []string{
		"local send Client", "local make ", "local send reqwest::Client",
		"other send Client", "other save crate::store::Store",
		"run fill crate::cache::Cache", "run save Store",
	}
	testutil.Lines(t, "calls", got, want)
}

func TestImportedModulesExpandAtTheRootOfATypePath(t *testing.T) {
	src := `use crate::model as m;
use crate::store;

fn run(c: m::Client, s: store::Store, o: other::Thing) {
    c.send();
    s.save();
    o.go();
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "run").Calls, func(c unit.Call) string {
		return fmt.Sprintf("%s %s", c.Name, c.Type)
	})
	want := []string{"send crate::model::Client", "save crate::store::Store", "go other::Thing"}
	testutil.Lines(t, "calls", got, want)
}

func TestUseDeclarationsInNestedBlocksQualifyThere(t *testing.T) {
	src := `fn run() {
    {
        use reqwest::Client;
        let c: Client = make();
        c.send();
    }
    let l: Client = make();
    l.send();
}
`
	got := testutil.Format(byName(t, mustUnits(t, src), "run").Calls, func(c unit.Call) string {
		if c.Kind == unit.MethodCall {
			return fmt.Sprintf("%d %s", c.Line, c.Type)
		}
		return ""
	})
	testutil.Lines(t, "calls", got, []string{
		"5 reqwest::Client",
		"8 Client",
	})
}

func TestCfgTestStructsAreTestCode(t *testing.T) {
	src := `#[cfg(test)]
struct Server {
    db: FakeDb,
}

struct Client {
    http: Http,
}
`
	d := mustDecls(t, src)
	got := testutil.Format(d.Structs, func(s unit.Struct) string {
		return fmt.Sprintf("struct %s test=%v", s.Name, s.Test)
	})
	for _, f := range d.FieldTypes {
		got = append(got, fmt.Sprintf("field %s.%s test=%v", f.Struct, f.Field, f.Test))
	}
	testutil.Lines(t, "got", got, []string{
		"struct Server test=true",
		"struct Client test=false",
		"field Server.db test=true",
		"field Client.http test=false",
	})
}

func TestRustUnitsRecordTheirResultTypes(t *testing.T) {
	src := `use crate::db::Db;

impl Store {
    fn new() -> Self { Store }
    fn open() -> Result<Store, Error> { todo!() }
    async fn connect() -> anyhow::Result<Db> { todo!() }
    fn find(&self) -> Option<&Item> { None }
    fn count(&self) -> usize { 0 }
    fn nothing(&self) {}
}
`
	got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
		return fmt.Sprintf("%s %q", u.Name, u.Results)
	})
	want := []string{
		`Store::new ["Store" "Store"]`,
		`Store::open ["Result" "Store"]`,
		// async: the future as returned, then what .await and .await? give
		`Store::connect ["" "" "anyhow::Result" "crate::db::Db"]`,
		`Store::find ["Option" "Item"]`,
		`Store::count ["" ""]`,
		`Store::nothing []`,
	}
	testutil.Lines(t, "results", got, want)
}

// producedCall describes a call by the call producing its operand, and
// the operand's type, field path and result adapter where it has them.
func producedCall(calls []unit.Call, c unit.Call) string {
	line := fmt.Sprintf("%d %s from=%s", c.Line, c.Name, testutil.Producer(calls, c))
	if c.Type != "" {
		line += fmt.Sprintf(" type=%q", c.Type)
	}
	if c.Field != "" {
		line += fmt.Sprintf(" field=%q", c.Field)
	}
	if c.Adapts != 0 {
		line += fmt.Sprintf(" adapts=%d", c.Adapts)
	}
	return line
}

func TestMethodCallsRecordTheCallProducingTheirOperand(t *testing.T) {
	for name, tc := range map[string]struct {
		src, unit string
		want      []string
	}{
		"lets, awaits and ?": {
			src: `async fn run() -> Result<(), Error> {
    let s = Store::new();
    s.save();
    let db = Db::connect().await?;
    db.query();
    Store::open()?.get();
    let n = s.count();
    n.add();
    Ok(())
}
`,
			unit: "run",
			want: []string{
				"2 new from=-",
				"3 save from=new#0",
				"4 connect from=-",
				"5 query from=connect#3",
				"6 get from=open#1",
				"6 open from=-",
				"7 count from=new#0",
				"8 add from=count#0",
				"9 Ok from=-",
			},
		},
		// tree-sitter parses let mut x with mut beside the pattern, not as
		// a mut_pattern around it: a mutable binding records its producer
		// too.
		"mutable lets": {
			src: `async fn run() -> Result<(), Error> {
    let mut db = Db::connect().await?;
    db.query();
    Ok(())
}
`,
			unit: "run",
			want: []string{
				"2 connect from=-",
				"3 query from=connect#3",
				"4 Ok from=-",
			},
		},
		// An async fn's call gives a future until awaited: make().map(..)
		// is the future's map, not the output's.
		"unawaited async calls are not their output": {
			src: `async fn make() -> Widget { Widget }

async fn run() {
    make().map(|w| w);
    let f = make();
    f.spin();
    make().await.spin();
}
`,
			unit: "run",
			want: []string{
				"4 map from=make#0",
				"4 make from=-",
				"5 make from=-",
				"6 spin from=make#0",
				"7 spin from=make#2",
				"7 make from=-",
			},
		},
		// map_err, context, unwrap and expect are recorded as result
		// adapters (unwrap and expect as unwrapping): whether one is a
		// library's, to look through, or the repository's own method is
		// the call graph's to tell.
		"result adapters are marked": {
			src: `async fn run() -> anyhow::Result<()> {
    let db = Db::connect().await.context("connect")?;
    db.query();
    let s = Store::open().unwrap();
    s.get();
    Ok(())
}
`,
			unit: "run",
			want: []string{
				"2 context from=connect#2 adapts=1",
				"2 connect from=-",
				"3 query from=context#1",
				"4 unwrap from=open#0 adapts=2",
				"4 open from=-",
				"5 get from=unwrap#0",
				"6 Ok from=-",
			},
		},
		"parenthesised values keep their producer": {
			src: `async fn run() -> anyhow::Result<()> {
    let db = (Db::connect().await?);
    db.query();
    (Store::open()?).get();
    Ok(())
}
`,
			unit: "run",
			want: []string{
				"2 connect from=-",
				"3 query from=connect#3",
				"4 get from=open#1",
				"4 open from=-",
				"5 Ok from=-",
			},
		},
		// A Some(x) or Ok(x) pattern binds what unwrapping its value
		// leaves: of a call, its result after a ?; anything else it binds
		// is unknown.
		"unwrapping patterns bind the call's unwrapped result": {
			src: `fn run() {
    if let Some(a) = find() {
        a.save();
    }
    while let Ok(b) = next().await {
        b.save();
    }
    let Some(c) = find() else { return };
    c.save();
    match load() {
        Ok(d) => d.save(),
        Err(e) => e.save(),
    }
    let found = find();
    if let Some(f) = found {
        f.save();
    }
    if let Some(g) = find()? {
        g.save();
    }
    if let Some((h, _)) = find() {
        h.save();
    }
    {
        use crate::MyValue::Some;
        if let Some(i) = find() {
            i.save();
        }
    }
    let value = find();
    if let Some(value) = value {
        value.save();
    }
}
`,
			unit: "run",
			want: []string{
				"2 find from=-",
				"3 save from=find#1",
				"5 next from=-",
				"6 save from=next#3",
				"8 find from=-",
				"9 save from=find#1",
				"10 load from=-",
				"11 save from=load#1",
				"12 save from=-", // the error: not what ? leaves
				"14 find from=-",
				"16 save from=find#1",
				"18 find from=-",
				"19 save from=-", // unwrapped twice: no position says
				"21 find from=-",
				"22 save from=-", // a tuple inside
				"26 find from=-",
				"27 save from=-", // an imported variant named Some
				"30 find from=-",
				"32 save from=find#1", // the outer value, shadowed only inside
			},
		},
		// A method called through fields records the field path from
		// where the chain starts: self, a typed variable, or a call's
		// result.
		"field chains record the path": {
			src: `impl Server {
    fn run(&self, req: &Request) {
        self.ctx.db.query();
        req.store.get();
        make().inner.run();
        self.db.ping();
        make().inner?.run();
        self.ctx?.db.query();
        req.store?.get();
    }
}
`,
			unit: "Server::run",
			want: []string{
				`3 query from=- field="ctx.db"`,
				`4 get from=- type="Request" field="store"`,
				`5 run from=make#0 field="inner"`,
				"5 make from=-",
				`6 ping from=- field="db"`,
				// A ? on a field unwraps it ("?"): what is inside its Option.
				`7 run from=make#0 field="inner?"`,
				"7 make from=-",
				`8 query from=- field="ctx?.db"`,
				`9 get from=- type="Request" field="store?"`,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := byName(t, mustUnits(t, tc.src), tc.unit).Calls
			got := testutil.Format(calls, func(c unit.Call) string { return producedCall(calls, c) })
			testutil.Lines(t, "calls", got, tc.want)
		})
	}
}

func TestAnAsyncFunctionsResultsStartWithItsFuture(t *testing.T) {
	src := "async fn make() -> Widget { Widget }\n"
	if got := fmt.Sprintf("%q", byName(t, mustUnits(t, src), "make").Results); got != `["" "" "Widget" "Widget"]` {
		t.Errorf("make's results = %s", got)
	}
}

// A path call's imported root is written as its import's path, by the
// imports visible at the call: an alias in one function does not reach
// another.
func TestPathCallsExpandTheirVisibleImportedRoot(t *testing.T) {
	src := `use crate::util as tools;

fn a() {
    use tokio::fs as helpers;
    helpers::read();
    tools::log();
}

fn b() {
    helpers::log();
}
`
	var got []string
	for _, u := range mustUnits(t, src) {
		for _, c := range u.Calls {
			got = append(got, fmt.Sprintf("%s %s::%s", u.Name, c.Qualifier, c.Name))
		}
	}
	testutil.Lines(t, "calls", got, []string{
		"a tokio::fs::read",
		"a crate::util::log",
		"b helpers::log",
	})
}

// #[cfg(test)] on a function or an impl block limits it to test builds,
// as on a module: test code.
func TestCfgTestFunctionsAndImplsAreTestCode(t *testing.T) {
	src := `pub fn real() {}

#[cfg(test)]
pub fn fake_store() {}

#[cfg(test)]
impl Store {
    pub fn fake() -> Self { Store }
}

impl Store {
    pub fn real_one() {}
}
`
	got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
		return fmt.Sprintf("%s test=%v", u.Name, u.Test)
	})
	testutil.Lines(t, "units", got, []string{
		"real test=false",
		"fake_store test=true",
		"Store::fake test=true",
		"Store::real_one test=false",
	})
}

// A function in an inline module records that module's path within its
// file.
func TestUnitsRecordTheirInlineModule(t *testing.T) {
	src := `pub fn top() {}

mod outer {
    pub fn a() {}
    pub mod inner {
        pub fn b() {}
    }
    impl Thing {
        pub fn c() {}
    }
}
`
	got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
		return fmt.Sprintf("%s module=%q", u.Name, u.Module)
	})
	testutil.Lines(t, "units", got, []string{
		"top module=\"\"",
		"a module=\"outer\"",
		"b module=\"outer::inner\"",
		"Thing::c module=\"outer\"",
	})
}

// A test case (#[test], #[tokio::test], #[rstest]) is told apart from the
// helpers and fakes around it in test code.
func TestTestCasesAreToldFromTestHelpers(t *testing.T) {
	src := `pub fn real() {}

#[cfg(test)]
mod tests {
    fn store() -> Store { Store }

    #[test]
    fn saves() { store(); }

    #[tokio::test]
    async fn loads() { store(); }
}
`
	got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
		return fmt.Sprintf("%s test=%v case=%v", u.Name, u.Test, u.TestCase)
	})
	testutil.Lines(t, "units", got, []string{
		"real test=false case=false",
		"store test=true case=false",
		"saves test=true case=true",
		"loads test=true case=true",
	})
}

// Cargo's benchmark harness runs #[bench] functions: test cases.
func TestBenchmarksAreTestCases(t *testing.T) {
	src := "#[cfg(test)]\nmod benches {\n    #[bench]\n    fn saving(b: &mut Bencher) {}\n}\n"
	if u := byName(t, mustUnits(t, src), "saving"); !u.Test || !u.TestCase {
		t.Errorf("saving: test=%v case=%v, want a test case", u.Test, u.TestCase)
	}
}

// Test runners name their attribute after test: #[wasm_bindgen_test],
// #[sqlx::test], #[test_log::test]. Each marks a test case.
func TestRunnerAttributesMarkTestCases(t *testing.T) {
	src := `#[wasm_bindgen_test]
fn in_browser() {}

#[sqlx::test]
async fn with_db() {}

#[test_log::test]
fn logged() {}

#[serde(rename = "contest")]
fn not_a_test() {}
`
	got := testutil.Format(mustUnits(t, src), func(u unit.Unit) string {
		return fmt.Sprintf("%s case=%v", u.Name, u.TestCase)
	})
	testutil.Lines(t, "units", got, []string{
		"in_browser case=true",
		"with_db case=true",
		"logged case=true",
		"not_a_test case=false",
	})
}

// Benchmark files register their cases with macros (Criterion's
// criterion_group!), so every function in benches/ is a test case.
func TestFunctionsInBenchesAreTestCases(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "svc/benches/save.rs", "fn bench_save(c: &mut Criterion) {}\ncriterion_group!(benches, bench_save);\n")
	if u := byName(t, units, "bench_save"); !u.Test || !u.TestCase {
		t.Errorf("bench_save: test=%v case=%v, want a test case", u.Test, u.TestCase)
	}
}
