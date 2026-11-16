package typescript

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// ddslop:ignore shape, not logic: each adapter's tests call its own Parse
func mustUnits(t *testing.T, path, src string) []unit.Unit {
	t.Helper()
	units, _, err := Parse(path, []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return units
}

// unitNamed parses src and returns its unit of that name.
func unitNamed(t *testing.T, path, src, name string) unit.Unit {
	t.Helper()
	units := mustUnits(t, path, src)
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
	for _, u := range mustUnits(t, "src/store.ts", store) {
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
		units := mustUnits(t, path, "function f() {}\n")
		if len(units) != 1 || units[0].Test != want {
			t.Errorf("%s: units %+v, want one with test = %v", path, units, want)
		}
	}
}

func TestRenamedCopiesHaveTheSameShape(t *testing.T) {
	a := unitNamed(t, "a.ts", store, "loadRow")
	renamed := strings.NewReplacer("loadRow", "fetchItem", "Row", "Item", `"SELECT * FROM rows WHERE id = $1"`, `"SELECT 1"`, "name", "title").Replace(store)
	b := unitNamed(t, "b.ts", renamed, "fetchItem")
	if a.Shape != b.Shape || len(a.Leaves) != len(b.Leaves) {
		t.Errorf("shapes differ (%x, %x) or leaves do not line up (%d, %d)", a.Shape, b.Shape, len(a.Leaves), len(b.Leaves))
	}
	if a.Nodes == 0 || len(a.Seq) == 0 || a.Signature != nil {
		t.Errorf("nodes %d, seq %d; want both set (the signature is computed when indexing)", a.Nodes, len(a.Seq))
	}
}

func TestDeclaringNamesAreMarked(t *testing.T) {
	u := unitNamed(t, "a.ts", store, "loadRow")
	declared := map[string]bool{}
	for _, l := range u.Leaves {
		if l.Declares {
			declared[l.Text] = true
		}
	}
	for _, name := range []string{"loadRow", "db", "id", "name", "mail", "rest", "k", "v", "done", "row"} {
		if !declared[name] {
			t.Errorf("%s is not marked as declared (declared: %v)", name, declared)
		}
	}
	for _, name := range []string{"query", "use", "email", "Object", "entries", "Row"} {
		if declared[name] {
			t.Errorf("%s is marked as declared, but is only used", name)
		}
	}
}

func TestBlocksListStatementsAndEarlyReturns(t *testing.T) {
	u := unitNamed(t, "a.ts", store, "loadRow")
	var got []string
	for _, st := range u.Blocks[0] {
		got = append(got, fmt.Sprintf("%d-%d exits=%v", st.StartLine, st.EndLine, st.Exits))
	}
	// The if returns early; the arrow function's return leaves the arrow
	// function only; the final return is a return too.
	want := "4-4 exits=false 5-7 exits=true 8-10 exits=false 11-11 exits=false 12-12 exits=true"
	if strings.Join(got, " ") != want {
		t.Errorf("body statements = %s\nwant %s", strings.Join(got, " "), want)
	}
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
	u := unitNamed(t, "a.ts", store, "loadRow")
	shared := map[uint32]string{}
	for _, kind := range []string{"identifier", "call_expression", "if_statement", "return_statement"} {
		shared[syntax.KindSymbol(kind)] = kind
		shared[syntax.KindSymbol("go:"+kind)] = "go:" + kind
	}
	for _, sym := range u.Seq {
		if kind, ok := shared[sym]; ok {
			t.Fatalf("a TypeScript node has the same symbol as %s", kind)
		}
	}
}

func TestParseListsTypesEnumsAndModuleConstants(t *testing.T) {
	src := `export const MAX_MODELS = 64;
export const CODE_UNAVAILABLE = "ledger_unavailable";
const RETRY = { attempts: 3, backoff: 100 } as const;
let mutable = "not a constant";
export const load = () => 1;
export const TIMEOUT_MS = 5 * 60 * 1000, root = createRoot(el), DOUBLE = MAX_MODELS * 2;
const SHORTHAND = { foo }, ENV = import.meta.env.X;
const cache: Record<string, string> = {}, items: string[] = [], HANDLERS = { a: () => 1 };

export interface Budget {
  id: string;
  limit?: number;
  readonly spent: number;
  updatedAt: Date;
  refresh(): void;
}
type Point = { x: number; y: number };
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
	// Interfaces and object types are structs of their properties; enums
	// and unions of string literals are enums; module-level constants are
	// constants when their value is made of literals only: a call's
	// result or another constant's value is computed at run time, an
	// empty object or array is state to fill, and a function is code.
	want := strings.Join([]string{
		"const MAX_MODELS = 64 line=1 literal=false",
		`const CODE_UNAVAILABLE = "ledger_unavailable" line=2 literal=true`,
		"const RETRY = {attempts:3,backoff:100}as const line=3 literal=false",
		"const TIMEOUT_MS = 5*60*1000 line=6 literal=false",
		"struct Budget 10-16 [id:string@11 limit:number@12 spent:number@13 updatedAt:Date@14]",
		"struct Point 17-17 [x:number@17 y:number@17]",
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
	if want := `A "queued" A "done" A "say 'hi'" A """ B "queued" B "done" C Queued="queued" C Done="done"`; strings.Join(got, " ") != want {
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
	// default value it adds make no wrapper; an awaited call does, and a
	// this parameter is no argument to pass on.
	wantForwards := "Store.visible -> this.inNamespace\nread -> readFile\ncheck -> verifyToken\nload -> open\nbound -> load"
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
	units, _, err := Parse("src/store.ts", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var params []string
	for _, p := range units[0].Params { // interpret
		params = append(params, p.Name+": "+p.Type)
	}
	// this is no parameter; a type is taken as written, "" when the
	// parameter has none; destructured and rest parameters bind no one
	// name.
	if want := "[a: number b: number status:  tags: string[]]"; fmt.Sprint(params) != want {
		t.Errorf("params = %v, want %s", params, want)
	}
	var matches []string
	for _, x := range units {
		for _, m := range x.StringMatches {
			matches = append(matches, fmt.Sprintf("%s %v %d-%d", x.Name, m.Values, m.StartLine, m.EndLine))
		}
	}
	// Quote style is formatting: 'replayed' is "replayed".
	if want := `interpret ["committed" "refused" "replayed"] 2-10`; strings.Join(matches, "\n") != want {
		t.Errorf("string switches:\n%s\nwant:\n%s", strings.Join(matches, "\n"), want)
	}
}
