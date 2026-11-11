package golang

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func mustUnits(t *testing.T, path, src string) []unit.Unit {
	t.Helper()
	units, _, err := Parse(path, []byte(src))
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
	t.Fatalf("no unit %q", name)
	return unit.Unit{}
}

const store = `package store

// Load reads a row.
func (s *Store[T]) Load(ctx context.Context, id int) (*Row, error) {
	row, err := s.db.Query(ctx, "SELECT * FROM rows WHERE id = $1", id)
	if err != nil {
		return nil, err
	}
	for i, v := range row.Values {
		use(i, v)
	}
	done := func() error { return nil }
	return row, done()
}

func open(path string) *Store[int] {
	return &Store[int]{path: path}
}

func broken( {
}
`

func TestUnitsExtractsFunctionsAndMethods(t *testing.T) {
	var got []string
	for _, u := range mustUnits(t, "store/store.go", store) {
		got = append(got, fmt.Sprintf("%s %d-%d test=%v", u.Name, u.StartLine, u.EndLine, u.Test))
	}
	// Methods are named after their receiver type, without pointer or type
	// arguments; function literals are part of their function; a function
	// with a syntax error is skipped.
	want := "Store.Load 4-14 test=false\nopen 16-18 test=false"
	if strings.Join(got, "\n") != want {
		t.Errorf("units:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestTestFilesAndTestdataAreTestCode(t *testing.T) {
	for path, want := range map[string]bool{
		"store/store.go":            false,
		"store/store_test.go":       true,
		"store/testdata/sample.go":  true,
		"testdata/golden/sample.go": true,
	} {
		units := mustUnits(t, path, "package p\n\nfunc f() {}\n")
		if len(units) != 1 || units[0].Test != want {
			t.Errorf("%s: test = %v, want %v", path, units[0].Test, want)
		}
	}
}

func TestRenamedCopiesHaveTheSameShape(t *testing.T) {
	a := byName(t, mustUnits(t, "a.go", store), "Store.Load")
	renamed := strings.NewReplacer("Load", "Fetch", "row", "rec", `"SELECT * FROM rows WHERE id = $1"`, `"SELECT 1"`, "Store", "Repo").Replace(store)
	b := byName(t, mustUnits(t, "b.go", renamed), "Repo.Fetch")
	if a.Shape != b.Shape || len(a.Leaves) != len(b.Leaves) {
		t.Errorf("shapes differ (%x, %x) or leaves do not line up (%d, %d)", a.Shape, b.Shape, len(a.Leaves), len(b.Leaves))
	}
	if a.Nodes == 0 || len(a.Seq) == 0 || a.Signature != nil {
		t.Errorf("nodes %d, seq %d; want both set (the signature is computed when indexing)", a.Nodes, len(a.Seq))
	}
}

func TestDeclaringNamesAreMarked(t *testing.T) {
	u := byName(t, mustUnits(t, "a.go", store), "Store.Load")
	declared := map[string]bool{}
	for _, l := range u.Leaves {
		if l.Declares {
			declared[l.Text] = true
		}
	}
	for _, name := range []string{"Load", "s", "ctx", "id", "row", "err", "i", "v", "done"} {
		if !declared[name] {
			t.Errorf("%s is not marked as declared (declared: %v)", name, declared)
		}
	}
	for _, name := range []string{"Query", "use", "Values", "Store"} {
		if declared[name] {
			t.Errorf("%s is marked as declared, but is only used", name)
		}
	}
}

func TestBlocksListStatementsAndEarlyReturns(t *testing.T) {
	u := byName(t, mustUnits(t, "a.go", store), "Store.Load")
	body := u.Blocks[0]
	var got []string
	for _, st := range body {
		got = append(got, fmt.Sprintf("%d-%d exits=%v", st.StartLine, st.EndLine, st.Exits))
	}
	// The if returns early; the closure's return leaves the closure only;
	// the final return is a return too.
	want := "5-5 exits=false 6-8 exits=true 9-11 exits=false 12-12 exits=false 13-13 exits=true"
	if strings.Join(got, " ") != want {
		t.Errorf("body statements = %s\nwant %s", strings.Join(got, " "), want)
	}
}

// Go and Rust name some node kinds alike (identifier, block,
// call_expression); their structure symbols must still differ, so a Go
// function is never a near miss of a Rust one.
func TestGoSymbolsAreApartFromOtherLanguages(t *testing.T) {
	u := byName(t, mustUnits(t, "a.go", store), "Store.Load")
	shared := map[uint32]string{}
	for _, kind := range []string{"block", "identifier", "call_expression", "if_statement", "return_statement"} {
		shared[syntax.KindSymbol(kind)] = kind
	}
	for _, sym := range u.Seq {
		if kind, ok := shared[sym]; ok {
			t.Fatalf("a Go %s has the same symbol as an unprefixed %s", kind, kind)
		}
	}
}

func TestParseListsStructsAndPackageConstants(t *testing.T) {
	src := `package store

const MaxGrantedModels = 64

const (
	CodeUnavailable = "ledger_unavailable"
	Timeout         = 5 * time.Second
	first, second   = "a", "b"
)

const (
	Low Level = iota
	High
)

var mutable = "not a constant"

type Budget struct {
	ID, Name   string
	Limit      *int64 ` + "`json:\"limit\"`" + `
	sync.Mutex
}

type Alias = Budget

func f() {
	const local = 1
	type inner struct{ a int }
}
`
	_, decls, err := Parse("store/store.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var consts []string
	for _, c := range decls.Consts {
		consts = append(consts, fmt.Sprintf("%s=%s:%d literal=%v", c.Name, c.Value, c.Line, c.Literal))
	}
	wantConsts := []string{
		"MaxGrantedModels=64:3 literal=false",
		`CodeUnavailable="ledger_unavailable":6 literal=true`,
		"Timeout=5*time.Second:7 literal=false",
		`first="a":8 literal=true`,
		`second="b":8 literal=true`,
	}
	if strings.Join(consts, "\n") != strings.Join(wantConsts, "\n") {
		t.Errorf("constants:\n%s\nwant:\n%s", strings.Join(consts, "\n"), strings.Join(wantConsts, "\n"))
	}
	var structs []string
	for _, s := range decls.Structs {
		var fields []string
		for _, f := range s.Fields {
			fields = append(fields, f.Name+":"+f.Type)
		}
		structs = append(structs, fmt.Sprintf("%s %d-%d %v", s.Name, s.StartLine, s.EndLine, fields))
	}
	// Each name of a multi-name field is a field; an embedded field is
	// named after its type.
	if want := "Budget 18-22 [ID:string Name:string Limit:*int64 Mutex:sync.Mutex]"; strings.Join(structs, "\n") != want {
		t.Errorf("structs:\n%s\nwant:\n%s", strings.Join(structs, "\n"), want)
	}
}

func TestUnitsRecordCallsAndWhatAWrapperForwardsTo(t *testing.T) {
	src := `package store

import (
	"encoding/json"
	auth "example.com/app/internal/auth"
)

func (s *Store) Visible(principal string, tokenID int) bool {
	return s.inNamespace(principal, tokenID)
}

func Decode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func Check(token string) error {
	return auth.Verify(token)
}

func load(path string) *Store {
	return open(path)
}

func (s *Store) Delegate(x int) int {
	return s.inner.Get(x)
}

func reordered(a, b int) int {
	return add(b, a)
}

func logged(x int) int {
	log(x)
	return add(x)
}
`
	units, _, err := Parse("store/store.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var forwards, calls []string
	for _, u := range units {
		if u.Forwards != "" {
			forwards = append(forwards, u.Name+" -> "+u.Forwards)
		}
		for _, c := range u.Calls {
			calls = append(calls, fmt.Sprintf("%s:%d:%s:%s", u.Name, c.Kind, c.Qualifier, c.Name))
		}
	}
	wantForwards := "Store.Visible -> s.inNamespace\nDecode -> json.Unmarshal\nCheck -> auth.Verify\nload -> open"
	if strings.Join(forwards, "\n") != wantForwards {
		t.Errorf("wrappers:\n%s\nwant:\n%s", strings.Join(forwards, "\n"), wantForwards)
	}
	// A call on the receiver is a self call; one through a package name is
	// a path call; any other selector is a method call.
	for _, want := range []string{
		fmt.Sprintf("Store.Visible:%d:self:inNamespace", unit.MethodCall),
		fmt.Sprintf("Decode:%d:json:Unmarshal", unit.PathCall),
		fmt.Sprintf("Check:%d:auth:Verify", unit.PathCall),
		fmt.Sprintf("load:%d::open", unit.FreeCall),
		fmt.Sprintf("Store.Delegate:%d::Get", unit.MethodCall),
	} {
		if !slices.Contains(calls, want) {
			t.Errorf("calls %v lack %s", calls, want)
		}
	}
}

func TestUnitsRecordParametersAndSwitchesOnStrings(t *testing.T) {
	src := `package store

func (s *Store) interpret(ctx context.Context, a, b int, status string) int {
	switch status {
	case "committed":
		return 1
	case "replayed", "refused":
		return 2
	default:
		return 0
	}
}

func small(kind string) int {
	switch kind {
	case "a", "b":
		return 1
	}
	return 0
}
`
	units, _, err := Parse("store/store.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	u := byName(t, units, "Store.interpret")
	var params []string
	for _, p := range u.Params {
		params = append(params, p.Name+": "+p.Type)
	}
	// The receiver is no parameter; a, b int declares two; a Context is
	// never grouped (Go passes it explicitly, never in a struct).
	if want := "[a: int b: int status: string]"; fmt.Sprint(params) != want {
		t.Errorf("params = %v, want %s", params, want)
	}
	var matches []string
	for _, x := range units {
		for _, m := range x.StringMatches {
			matches = append(matches, fmt.Sprintf("%s %v %d-%d", x.Name, m.Values, m.StartLine, m.EndLine))
		}
	}
	if want := `Store.interpret ["committed" "refused" "replayed"] 4-11`; strings.Join(matches, "\n") != want {
		t.Errorf("string switches:\n%s\nwant:\n%s", strings.Join(matches, "\n"), want)
	}
}

func TestInterfaceMethodsListsTheMethodsOfDeclaredInterfaces(t *testing.T) {
	src := `// Code generated by oapi-codegen. DO NOT EDIT.
package oapi

type ServerInterface interface {
	// (GET /health)
	GetHealth(w http.ResponseWriter, r *http.Request)
	GetDocument(w http.ResponseWriter, r *http.Request, id DocID)
	io.Closer
}

type Params struct{ Limit int }
`
	if got := fmt.Sprint(InterfaceMethods([]byte(src))); got != "[GetHealth GetDocument]" {
		t.Errorf("InterfaceMethods = %s, want [GetHealth GetDocument]", got)
	}
}
