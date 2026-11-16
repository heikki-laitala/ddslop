package typescript

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/testutil"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// unitNamed parses src and returns its unit of that name.
func unitNamed(t *testing.T, path, src, name string) unit.Unit {
	t.Helper()
	units, _ := testutil.Parse(t, Parse, path, src)
	i := slices.IndexFunc(units, func(u unit.Unit) bool { return u.Name == name })
	if i < 0 {
		t.Fatalf("no unit %q in %s", name, path)
	}
	return units[i]
}

const store = `import { Db } from "./db";

export async function loadRow(db: Db, id: number): Promise<Row | null> {
  const { name, email: mail = "", ...rest } = await db.query("SELECT * FROM rows WHERE id = $1", [id]);
  if (!name) {
    return null;
  }
  for (const [k, v] of Object.entries(rest)) {
    use(k, v);
  }
  const done = (row: Row) => { return row; };
  return done(new Row(name, mail));
}

export class Store<T> extends Base {
  private cache = new Map<string, T>();

  constructor(private readonly db: Db) {
    super();
  }

  async load(id: string): Promise<T> {
    return this.db.get(id);
  }

  handle = (event: Event) => {
    this.cache.clear();
  };
}

export const fetchRow = async (id: string) => {
  return api.get(id);
};

const double = function (x: number) { return x * 2; }, limit = 3;

function broken( {
}
`

func TestParseExtractsFunctionsMethodsAndFunctionConstants(t *testing.T) {
	var got []string
	units, _ := testutil.Parse(t, Parse, "src/store.ts", store)
	for _, u := range units {
		got = append(got, fmt.Sprintf("%s %d-%d public=%v", u.Name, u.StartLine, u.EndLine, u.Public))
	}
	// Methods and function-valued fields are named after their class;
	// arrow functions and function expressions after the constant holding
	// them; nested functions are part of their function; a function with
	// a syntax error is skipped.
	want := strings.Join([]string{
		"loadRow 3-13 public=true",
		"Store.constructor 18-20 public=true",
		"Store.load 22-24 public=true",
		"Store.handle 26-28 public=true",
		"fetchRow 31-33 public=true",
		"double 35-35 public=false",
	}, "\n")
	if strings.Join(got, "\n") != want {
		t.Errorf("units:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestTestFilesAreTestCode(t *testing.T) {
	for path, want := range map[string]bool{
		"src/store.ts":                false,
		"src/store.test.ts":           true,
		"src/store.spec.tsx":          true,
		"src/__tests__/store.ts":      true,
		"__tests__/helpers/render.ts": true,
		"src/latest.ts":               false,
	} {
		units, _ := testutil.Parse(t, Parse, path, "function f() {}\n")
		if len(units) != 1 || units[0].Test != want {
			t.Errorf("%s: units %+v, want one with test = %v", path, units, want)
		}
	}
}

func TestRenamedCopiesHaveTheSameShape(t *testing.T) {
	a := unitNamed(t, "a.ts", store, "loadRow")
	renamed := strings.NewReplacer("loadRow", "fetchItem", "Row", "Item", `"SELECT * FROM rows WHERE id = $1"`, `"SELECT 1"`, "name", "title").Replace(store)
	b := unitNamed(t, "b.ts", renamed, "fetchItem")
	describe := func(u unit.Unit) []string {
		return []string{fmt.Sprintf("shape %x", u.Shape), fmt.Sprintf("%d leaves", len(u.Leaves))}
	}
	testutil.Lines(t, "renamed copy", describe(b), describe(a))
	// The signature is computed when indexing.
	if a.Nodes == 0 || len(a.Seq) == 0 || a.Signature != nil {
		t.Errorf("nodes %d, seq %d, signature %v; want nodes and seq set, no signature", a.Nodes, len(a.Seq), a.Signature)
	}
}

func TestDeclaringNamesAreMarked(t *testing.T) {
	// A pattern's keys (email), callees and types declare nothing.
	declared := testutil.Format(unitNamed(t, "a.ts", store, "loadRow").Leaves, func(l unit.Leaf) string {
		if l.Declares {
			return l.Text
		}
		return ""
	})
	testutil.Lines(t, "declared names", declared, []string{"loadRow", "db", "id", "name", "mail", "rest", "k", "v", "done", "row"})
}

func TestBlocksListStatementsAndEarlyReturns(t *testing.T) {
	body := unitNamed(t, "a.ts", store, "loadRow").Blocks[0]
	// The if returns early; the arrow function's return leaves the arrow
	// function only; the final return is a return too.
	testutil.Lines(t, "body statements", testutil.Format(body, func(st unit.Statement) string {
		return fmt.Sprintf("%d-%d exits=%v", st.StartLine, st.EndLine, st.Exits)
	}), []string{"4-4 exits=false", "5-7 exits=true", "8-10 exits=false", "11-11 exits=false", "12-12 exits=true"})
}

func TestAnArrowFunctionUnitCountsItsOwnReturns(t *testing.T) {
	u := unitNamed(t, "a.ts", store, "fetchRow")
	if len(u.Blocks) == 0 || len(u.Blocks[0]) != 1 || !u.Blocks[0][0].Exits {
		t.Errorf("fetchRow blocks = %+v, want one statement that returns", u.Blocks)
	}
}

func TestTSXFilesParseJSX(t *testing.T) {
	src := `export function Greeting({ name }: Props) {
  return <div className="greeting">Hello, {name}</div>;
}
`
	u := unitNamed(t, "src/Greeting.tsx", src, "Greeting")
	if u.StartLine != 1 || u.EndLine != 3 {
		t.Errorf("Greeting at %d-%d, want 1-3", u.StartLine, u.EndLine)
	}
}

// TypeScript, Go and Rust name some node kinds alike (identifier,
// call_expression); their structure symbols must still differ, so a
// TypeScript function is never a near miss of a Rust or Go one.
func TestTypeScriptSymbolsAreApartFromOtherLanguages(t *testing.T) {
	others := map[uint32]string{}
	for _, kind := range []string{"identifier", "call_expression", "if_statement", "return_statement"} {
		others[syntax.KindSymbol(kind)] = kind
		others[syntax.KindSymbol("go:"+kind)] = "go:" + kind
	}
	shared := testutil.Format(unitNamed(t, "a.ts", store, "loadRow").Seq, func(sym uint32) string { return others[sym] })
	testutil.Lines(t, "symbols of other languages", shared, nil)
}

func TestParseListsTypesEnumsAndModuleConstants(t *testing.T) {
	src := `export const MAX_MODELS = 64;
export const CODE_UNAVAILABLE = "ledger_unavailable";
const RETRY = { attempts: 3, backoff: 100 } as const;
let mutable = "not a constant";
export const load = () => 1;
export const TIMEOUT_MS = 5 * 60 * 1000, root = createRoot(el), DOUBLE = MAX_MODELS * 2;
const SHORTHAND = { foo }, ENV = import.meta.env.X;
const cache: Record<string, string> = {}, items: string[] = [], HANDLERS = { a: () => 1 }, CAST = {} as Cache, SEEN = ([] satisfies string[]), UPPER = "x".toUpperCase(), WORKER = import("./worker"), MADE = new (class {})();

export interface Budget {
  id: string;
  limit?: number;
  readonly spent: number;
  updatedAt: Date;
  refresh(): void;
}
type Point = { x: number; y?: number | undefined };
type Id = string;
export enum Level { Low, High = 10, Name = "name" }
export type Status = "active" | "paused" | "done";
type Mixed = "a" | number;

function f() {
  const local = "inside a function";
}
`
	_, decls, err := Parse("src/budget.ts", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range decls.Consts {
		got = append(got, fmt.Sprintf("const %s = %s line=%d literal=%v", c.Name, c.Value, c.Line, c.Literal))
	}
	for _, s := range decls.Structs {
		var fields []string
		for _, f := range s.Fields {
			fields = append(fields, fmt.Sprintf("%s:%s@%d", f.Name, f.Type, f.Line))
		}
		got = append(got, fmt.Sprintf("struct %s %d-%d [%s]", s.Name, s.StartLine, s.EndLine, strings.Join(fields, " ")))
	}
	for _, e := range decls.Enums {
		var variants []string
		for _, v := range e.Variants {
			variants = append(variants, v.Name+v.Payload)
		}
		got = append(got, fmt.Sprintf("enum %s %d-%d [%s]", e.Name, e.StartLine, e.EndLine, strings.Join(variants, " ")))
	}
	// Interfaces and object types are structs of their properties, an
	// optional one typed as possibly undefined; enums and unions of string
	// literals are enums; module-level constants are constants when their
	// value is made of literals only: a call's result (even
	// "x".toUpperCase()) or another constant's value is computed at run
	// time, an empty object or array is state to fill, and a function is
	// code.
	want := strings.Join([]string{
		"const MAX_MODELS = 64 line=1 literal=false",
		`const CODE_UNAVAILABLE = "ledger_unavailable" line=2 literal=true`,
		"const RETRY = {attempts:3,backoff:100}as const line=3 literal=false",
		"const TIMEOUT_MS = 5*60*1000 line=6 literal=false",
		"struct Budget 10-16 [id:string@11 limit:number|undefined@12 spent:number@13 updatedAt:Date@14]",
		"struct Point 17-17 [x:number@17 y:number|undefined@17]",
		`enum Level 19-19 [Low High=10 Name="name"]`,
		`enum Status 20-20 ["active" "paused" "done"]`,
	}, "\n")
	if strings.Join(got, "\n") != want {
		t.Errorf("declarations:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestDeclarationsOfTestFilesAreMarked(t *testing.T) {
	_, decls, err := Parse("src/budget.test.ts", []byte("const LIMIT = 3;\ninterface Fixture { a: string }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(decls.Consts) != 1 || !decls.Consts[0].Test || len(decls.Structs) != 1 || !decls.Structs[0].Test {
		t.Errorf("decls = %+v, want one test constant and one test struct", decls)
	}
}

// Next.js reads a route's options from constants each page, layout or
// route file exports under fixed names: copies the framework asks for.
func TestNextRouteSegmentOptionsAreNoConstants(t *testing.T) {
	src := "export const dynamic = \"force-dynamic\";\nexport const revalidate = 60;\nexport const PAGE_SIZE = 20;\n"
	for file, want := range map[string]string{
		"src/app/posts/page.tsx":     "PAGE_SIZE",
		"src/app/layout.tsx":         "PAGE_SIZE",
		"src/app/api/users/route.ts": "PAGE_SIZE",
		"src/lib/config.ts":          "dynamic revalidate PAGE_SIZE",
	} {
		_, decls, err := Parse(file, []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range decls.Consts {
			got = append(got, c.Name)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%s: constants %v, want %s", file, got, want)
		}
	}
}

// Quote style is formatting: 'a' and "a" name one variant.
func TestVariantsOfEitherQuoteStyleAreAlike(t *testing.T) {
	_, decls, err := Parse("src/state.ts", []byte(`type A = 'queued' | "done" | "say 'hi'" | '"';
enum B { 'queued', "done" }
enum C { Queued = 'queued', Done = "done" }
enum D { 'x-y' = 1 }
`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range decls.Enums {
		for _, v := range e.Variants {
			got = append(got, e.Name+" "+v.Name+v.Payload)
		}
	}
	if want := `A "queued" A "done" A "say 'hi'" A """ B "queued" B "done" C Queued="queued" C Done="done" D "x-y"=1`; strings.Join(got, " ") != want {
		t.Errorf("variants = %s, want %s", strings.Join(got, " "), want)
	}
}

func TestUnitsRecordCallsAndWhatAWrapperForwardsTo(t *testing.T) {
	src := `import { readFile } from "node:fs/promises";
import { verify as verifyToken } from "../auth/verify.js";
import * as db from "./db";
import open from "./open";

export class Store {
  visible(principal: string, tokenId: number): boolean {
    return this.inNamespace(principal, tokenId);
  }
  delegate(x: number) {
    return this.inner.get(x);
  }
}

export async function read(path: string) {
  return await readFile(path);
}

export function check(token: string) {
  return verifyToken(token);
}

export const load = (path: string) => open(path);

export function query(sql: string, ...args: unknown[]) {
  return db.query(sql, ...args);
}

function reordered(a: number, b: number) {
  return add(b, a);
}

function logged(x: number) {
  log(x);
  return add(x);
}

function pick(items: Item[]) {
  return items.map((item) => helper(item));
}

export function bound(this: Ctx, id: string) {
  return load(id);
}

export const withDefaults = (id: string, opts = {}) => load(id, opts);

export function* steps(x: number) {
  return step(x);
}

export const maybe = (id?: string) => load(id);
`
	units, _, err := Parse("src/store/store.ts", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var forwards, calls []string
	for _, u := range units {
		if u.Forwards != "" {
			forwards = append(forwards, u.Name+" -> "+u.Forwards)
		}
		for _, c := range u.Calls {
			calls = append(calls, fmt.Sprintf("%s:%d:%s:%s:%s", u.Name, c.Kind, c.Qualifier, c.Field, c.Name))
		}
	}
	// A rest parameter, reordered arguments, a second statement or a
	// default value it adds make no wrapper. Nor does an optional
	// parameter, which it passes on although its callers left it out,
	// or an async function or a generator: its callers get a promise or
	// an iterator, not the call's value. A this parameter is no argument
	// to pass on.
	wantForwards := "Store.visible -> this.inNamespace\ncheck -> verifyToken\nload -> open\nbound -> load"
	if strings.Join(forwards, "\n") != wantForwards {
		t.Errorf("wrappers:\n%s\nwant:\n%s", strings.Join(forwards, "\n"), wantForwards)
	}
	// An imported function is a path call qualified by its module as
	// written, under the name it is exported as; one through a namespace
	// import too. A call on this is a self call, one on a field of this a
	// method call on that field, and one on anything else a method call.
	want := []string{
		fmt.Sprintf("Store.visible:%d:self::inNamespace", unit.MethodCall),
		fmt.Sprintf("Store.delegate:%d::inner:get", unit.MethodCall),
		fmt.Sprintf("read:%d:node:fs/promises::readFile", unit.PathCall),
		fmt.Sprintf("check:%d:../auth/verify.js::verify", unit.PathCall),
		fmt.Sprintf("load:%d:./open::open", unit.PathCall),
		fmt.Sprintf("query:%d:./db::query", unit.PathCall),
		fmt.Sprintf("reordered:%d:::add", unit.FreeCall),
		fmt.Sprintf("logged:%d:::log", unit.FreeCall),
		fmt.Sprintf("logged:%d:::add", unit.FreeCall),
		fmt.Sprintf("pick:%d:::map", unit.MethodCall),
		fmt.Sprintf("pick:%d:::helper", unit.FreeCall),
		fmt.Sprintf("bound:%d:::load", unit.FreeCall),
		fmt.Sprintf("withDefaults:%d:::load", unit.FreeCall),
		fmt.Sprintf("steps:%d:::step", unit.FreeCall),
		fmt.Sprintf("maybe:%d:::load", unit.FreeCall),
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

// A class method may implement an interface, which TypeScript checks by
// shape alone: it may be called without its name being written.
func TestClassMembersMayBeCalledThroughAnInterface(t *testing.T) {
	u := unitNamed(t, "a.ts", store, "Store.load")
	if !u.ViaInterface() || unitNamed(t, "a.ts", store, "loadRow").ViaInterface() {
		t.Error("want class members, and only them, callable through an interface")
	}
}

func TestUnitsRecordParametersAndSwitchesOnStrings(t *testing.T) {
	src := `export function interpret(this: Store, a: number, b?: number, status = "new", tags: string[] = [], { id }: Row, ...rest: string[]) {
  switch (status) {
    case "committed":
      return 1;
    case 'replayed':
    case "refused":
      return 2;
    default:
      return 0;
  }
}

const small = (kind: string) => {
  switch (kind) {
    case "a":
    case "b":
      return 1;
  }
  return 0;
};
`
	units, _ := testutil.Parse(t, Parse, "src/store.ts", src)
	if len(units) != 2 {
		t.Fatalf("got %d units, want interpret and small", len(units))
	}
	interpret, small := units[0], units[1]
	// this is no parameter; a type is taken as written, "" when the
	// parameter has none; destructured and rest parameters bind no one
	// name.
	testutil.Lines(t, "params", testutil.Format(interpret.Params, func(p unit.Param) string { return p.Name + ": " + p.Type }),
		[]string{"a: number", "b: number", "status: ", "tags: string[]"})
	// Quote style is formatting: 'replayed' is "replayed".
	testutil.Lines(t, "string switches", testutil.Format(interpret.StringMatches, func(m unit.StringMatch) string {
		return fmt.Sprintf("%v %d-%d", m.Values, m.StartLine, m.EndLine)
	}), []string{`["committed" "refused" "replayed"] 2-10`})
	if len(small.StringMatches) != 0 {
		t.Errorf("small: string switches %v, want none (two strings are too few)", small.StringMatches)
	}
}

// A computed member name ([Symbol.iterator]) names no member code calls
// by name.
func TestComputedMembersAreNoUnits(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "src/bag.ts", `export class Bag {
  *[Symbol.iterator]() { yield this.count(); }
  ["size"]() { return 1; }
  count() { return 0; }
}
`)
	testutil.Lines(t, "units", testutil.Format(units, func(u unit.Unit) string { return u.Name }), []string{"Bag.count"})
}

// An export clause makes the names it lists public, as export before a
// declaration does; one re-exporting another module's names does not.
func TestExportClausesMakeNamesPublic(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "src/api.ts", `function load() {}
function save() {}
const remove = () => {};
function helper() {}
export { load, remove as del };
export default save;
export { helper } from "./other";
`)
	testutil.Lines(t, "units", testutil.Format(units, func(u unit.Unit) string { return fmt.Sprintf("%s public=%v", u.Name, u.Public) }),
		[]string{"load public=true", "save public=true", "remove public=true", "helper public=false"})
}

// An interface's bases are part of its type: two interfaces extending
// different bases differ there, however alike their own properties.
func TestInterfaceBasesAreRecordedAsAField(t *testing.T) {
	_, decls := testutil.Parse(t, Parse, "src/child.ts", "interface Child extends Base, Other<T> {\n  a: string;\n}\n")
	testutil.Lines(t, "structs", testutil.Format(decls.Structs, func(s unit.Struct) string {
		return s.Name + " " + fmt.Sprint(testutil.Format(s.Fields, func(f unit.Field) string { return fmt.Sprintf("%s:%s@%d", f.Name, f.Type, f.Line) }))
	}), []string{"Child [extends:Base,Other<T>@1 a:string@2]"})
}

// A function a type assertion or check wraps (as, satisfies, parentheses)
// is a function all the same.
func TestWrappedFunctionConstantsAreUnits(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "src/handlers.ts", `export const typed = ((req: Req) => work(req)) satisfies Handler;
const cast = (function (x: number) { return x; }) as Fn;
`)
	testutil.Lines(t, "units", testutil.Format(units, func(u unit.Unit) string { return u.Name + " -> " + u.Forwards }),
		[]string{"typed -> work", "cast -> "})
}

// Code outside the analysed source calls a default export, under any
// name, and a framework's functions by their names (Next.js's
// getStaticProps, a route's GET): none is unused when nothing names it.
func TestDefaultAndFrameworkExportsAreCalledFromOutside(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "src/pages/index.tsx", `export default function Page() { return null; }
export async function getStaticProps() { return { props: {} }; }
export async function GET(req: Request) { return new Response(); }
export const generateMetadata = async () => ({});
export function proxy(request: Request) { return request; }
function save() {}
export default save;
export function helper() {}
`)
	testutil.Lines(t, "units", testutil.Format(units, func(u unit.Unit) string { return fmt.Sprintf("%s external=%v", u.Name, u.External) }),
		[]string{"Page external=true", "getStaticProps external=true", "GET external=true", "generateMetadata external=true",
			"proxy external=true", "save external=true", "helper external=false"})
}

// A default-exported class is called from outside; its methods are
// called by name like any other.
func TestOnlyTheDefaultClassItselfIsExternal(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "src/store.ts", "export default class Store {\n  load() { return 1; }\n}\n")
	testutil.Lines(t, "units", testutil.Format(units, func(u unit.Unit) string { return fmt.Sprintf("%s external=%v", u.Name, u.External) }),
		[]string{"Store.load external=false"})
}

// A file importing another whole (import * as schema, import("./user"))
// may use any of its exports without naming it.
func TestImportsOfWholeModulesAreRecorded(t *testing.T) {
	_, decls := testutil.Parse(t, Parse, "src/db/client.ts", `import * as schema from "./schema";
import { open } from "./open";
export async function load() {
  const routes = await import("./routes");
  return [schema, routes, open];
}
`)
	testutil.Lines(t, "whole imports", decls.WholeImports, []string{"./schema", "./routes"})
}

// A class's fields record the types the code states, as Call.Type writes
// them: an imported type qualified by its module, a local one bare.
func TestClassFieldsRecordTheirTypes(t *testing.T) {
	_, decls := testutil.Parse(t, Parse, "src/users/service.ts", `import { UserRepo } from "../store/users";
import type { Mailer } from "@acme/mail";

export class UserService {
  private cache = new Cache<string>();
  clock: Clock;
  private readonly names: string[];
  constructor(private readonly repo: UserRepo, public mailer: Mailer, plain: number, readonly clock2: Clock) {}
}

class Cache<T> {}
`)
	testutil.Lines(t, "field types", testutil.Format(decls.FieldTypes, func(f unit.FieldType) string {
		return fmt.Sprintf("%s.%s %s test=%v", f.Struct, f.Field, f.Type, f.Test)
	}), []string{
		"UserService.cache Cache test=false",
		"UserService.clock Clock test=false",
		"UserService.repo ../store/users.UserRepo test=false",
		"UserService.mailer @acme/mail.Mailer test=false",
		"UserService.clock2 Clock test=false",
	})
}

// A method call records its operand's type where the code states it: a
// typed parameter, or a constant typed or made with new.
func TestMethodCallsRecordTheTypeTheCodeStates(t *testing.T) {
	units, _ := testutil.Parse(t, Parse, "src/users/handler.ts", `import { UserRepo } from "../store/users";

export async function handle(repo: UserRepo, id: string) {
  const cache = new Map<string, User>();
  const mailer: Mailer = makeMailer();
  repo.find(id);
  cache.get(id);
  mailer.send(id);
  other.run();
  {
    const repo = make();
    repo.find(id);
  }
}
`)
	testutil.Lines(t, "calls", testutil.Format(units[0].Calls, func(c unit.Call) string {
		if c.Kind != unit.MethodCall {
			return ""
		}
		return c.Name + " " + c.Type
	}), []string{"find ../store/users.UserRepo", "get Map", "send Mailer", "run ", "find "})
}

// An interface's or object type's properties record their types too, and
// a method call on a typed variable's property records both: the
// variable's type and the property.
func TestCallsThroughATypedVariablesFieldRecordBoth(t *testing.T) {
	units, decls := testutil.Parse(t, Parse, "src/controllers/cluster.ts", `import type { IClusterRepo } from "../repositories/cluster";

export interface ClusterDeps {
  clusters: IClusterRepo;
  limit: number;
}

type Options = { clock: Clock };

export function createController(deps: ClusterDeps) {
  return deps.clusters.findById("1");
}
`)
	testutil.Lines(t, "field types", testutil.Format(decls.FieldTypes, func(f unit.FieldType) string { return f.Struct + "." + f.Field + " " + f.Type }),
		[]string{"ClusterDeps.clusters ../repositories/cluster.IClusterRepo", "Options.clock Clock"})
	testutil.Lines(t, "calls", testutil.Format(units[0].Calls, func(c unit.Call) string { return c.Name + " " + c.Type + " " + c.Field }),
		[]string{"findById ClusterDeps clusters"})
}

// An interface declaring methods is a contract classes implement, as a
// Rust trait is; one only of properties is a data shape, no contract.
func TestInterfacesWithMethodsAndTheirImplementationsAreRecorded(t *testing.T) {
	_, decls := testutil.Parse(t, Parse, "src/store/repo.ts", `import type { Other } from "./other";

export interface IRepo {
  find(id: string): Row;
}

interface Row {
  id: string;
}

export class Repo extends Base implements IRepo, Other<string> {
  find(id: string) { return { id }; }
}

class Plain {}
`)
	testutil.Lines(t, "traits", testutil.Format(decls.Traits, func(tr unit.Trait) string {
		return fmt.Sprintf("%s %d-%d public=%v", tr.Name, tr.StartLine, tr.EndLine, tr.Public)
	}), []string{"IRepo 3-5 public=true"})
	testutil.Lines(t, "impls", testutil.Format(decls.Impls, func(im unit.Impl) string {
		return fmt.Sprintf("%s %s %s %d-%d", im.Type, im.Trait, im.TraitPath, im.StartLine, im.EndLine)
	}), []string{"Repo IRepo IRepo 11-13", "Repo Other ./other.Other 11-13"})
	testutil.Lines(t, "type names", decls.TypeNames, []string{"Repo", "Plain"})
}
