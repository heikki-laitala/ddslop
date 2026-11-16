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
