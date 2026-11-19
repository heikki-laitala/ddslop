package golang

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func mustUnits(t *testing.T, path, src string) []unit.Unit {
	t.Helper()
	units, _ := testutil.Parse(t, Parse, path, src)
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
	got := testutil.Format(mustUnits(t, "store/store.go", store), func(u unit.Unit) string {
		return fmt.Sprintf("%s %d-%d test=%v", u.Name, u.StartLine, u.EndLine, u.Test)
	})
	// Methods are named after their receiver type, without pointer or type
	// arguments; function literals are part of their function; a function
	// with a syntax error is skipped.
	testutil.Lines(t, "units", got, []string{
		"Store.Load 4-14 test=false",
		"open 16-18 test=false",
	})
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
	_, decls := testutil.Parse(t, Parse, "store/store.go", src)
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
		"Low=:12 literal=false", // iota and implicit values: nothing to compare
		"High=:13 literal=false",
	}
	testutil.Lines(t, "constants", consts, wantConsts)
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
	testutil.Lines(t, "structs", structs, []string{"Budget 18-22 [ID:string Name:string Limit:*int64 Mutex:sync.Mutex]"})
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
	units, _ := testutil.Parse(t, Parse, "store/store.go", src)
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
	units, _ := testutil.Parse(t, Parse, "store/store.go", src)
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
	testutil.Lines(t, "string", matches, []string{`Store.interpret ["committed" "refused" "replayed"] 4-11`})
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

func TestUnitsRecordTheirErrorForms(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want []string
	}{
		"checks and wrapping": {
			src: `package store

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

func load(path string) error {
	err := open(path)
	if errors.Is(err, ErrMissing) {
		return nil
	}
	if err == io.EOF || err != nil && err.Error() == "closed" {
		return fmt.Errorf("load %s: %w", path, err)
	}
	if strings.Contains(err.Error(), "busy") {
		return fmt.Errorf("load %s: %v", path, err)
	}
	if err != nil {
		return fmt.Errorf("load %s: bad path", path)
	}
	return nil
}
`,
			want: []string{
				"12 error-checks errors.Is/As",
				"15 error-checks ==",
				"15 error-checks message",
				"16 error-wrapping %w",
				"18 error-checks message",
				"19 error-wrapping %v",
			},
		},
		"a string compared is no error, but err may be a string-backed error": {
			src: `package store

func load(stderr, lastErr string, err Err) bool {
	if stderr == "" || lastErr != ` + "`none`" + ` {
		return false
	}
	return err == ErrMissing || err == "closed"
}
`,
			want: []string{
				"7 error-checks ==",
				"7 error-checks ==",
			},
		},
		"import paths and format directives": {
			src: `package store

import (
	stderrors "errors"
	format "fmt"
)

func load(err error) error {
	if stderrors.Is(err, ErrMissing) {
		return nil
	}
	if err != nil {
		return format.Errorf("literal %%w: %v", err)
	}
	if err != nil {
		return format.Errorf("load: %[1]w", err)
	}
	return format.Errorf("load: %+v", err)
}
`,
			want: []string{
				"9 error-checks errors.Is/As",
				"13 error-wrapping %v",
				"16 error-wrapping %w",
				"18 error-wrapping %v",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := testutil.Format(byName(t, mustUnits(t, "store/s.go", tc.src), "load").Forms, func(f unit.Form) string {
				return fmt.Sprintf("%d %s %s", f.Line, f.Concern, f.Form)
			})
			testutil.Lines(t, "forms", got, tc.want)
		})
	}
}

// Call formats for TestCallsRecordTheTypeOfTheirOperand.
func methodType(c unit.Call) string {
	if c.Kind != unit.MethodCall {
		return ""
	}
	return fmt.Sprintf("%d %s qualifier=%q type=%q field=%q", c.Line, c.Name, c.Qualifier, c.Type, c.Field)
}

func callType(c unit.Call) string {
	return fmt.Sprintf("%d %s kind=%d qualifier=%q type=%q", c.Line, c.Name, c.Kind, c.Qualifier, c.Type)
}

func TestCallsRecordTheTypeOfTheirOperand(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		line func(unit.Call) string
		want []string
	}{
		"method calls record the type the code states": {
			src: `package api

import (
	"strings"

	st "example.com/app/internal/store"
)

func (s *Server) Get(db *st.DB, q Query, names []string, n int) {
	db.Load()
	q.Run()
	var b strings.Builder
	b.WriteString("x")
	c := &Cache{}
	c.Fill()
	d := st.Index{}
	d.Find()
	s.store.Save()
	s.Flush()
	names[0].Trim()
	n.String()
	c = other()
	c.Fill()
	unknown.Do()
}
`,
			line: methodType,
			want: []string{
				`10 Load qualifier="" type="example.com/app/internal/store.DB" field=""`,
				`11 Run qualifier="" type="Query" field=""`,
				`13 WriteString qualifier="" type="strings.Builder" field=""`,
				`15 Fill qualifier="" type="Cache" field=""`,
				`17 Find qualifier="" type="example.com/app/internal/store.Index" field=""`,
				`18 Save qualifier="" type="" field="store"`,
				`19 Flush qualifier="self" type="" field=""`,
				`20 Trim qualifier="" type="" field=""`,
				`21 String qualifier="" type="" field=""`,
				`23 Fill qualifier="" type="" field=""`,
				`24 Do qualifier="" type="" field=""`,
			},
		},
		"operand types keep to their scope": {
			src: `package api

func run(db *DB, items []Item) {
	{
		db := &Other{}
		db.Save()
	}
	db.Save()
	if db := (&Other{}); true {
		db.Save()
	}
	db.Save()
	go func(db *Remote) {
		db.Save()
	}(nil)
	db.Save()
	for _, db := range items {
		db.Save()
	}
	db.Save()
}
`,
			line: methodType,
			want: []string{
				`6 Save qualifier="" type="Other" field=""`,
				`8 Save qualifier="" type="DB" field=""`,
				`10 Save qualifier="" type="" field=""`,
				`12 Save qualifier="" type="DB" field=""`,
				`14 Save qualifier="" type="Remote" field=""`,
				`16 Save qualifier="" type="DB" field=""`,
				`18 Save qualifier="" type="Item" field=""`,
				`20 Save qualifier="" type="DB" field=""`,
			},
		},
		"elements of a typed slice or array take their type": {
			src: `package api

import st "example.com/app/internal/store"

func run(units []st.Unit, rows [4]*Row, byName map[string]Unit, grid [][]Unit, rest ...Item) {
	for _, u := range units {
		u.ViaInterface()
	}
	for i := range units {
		i.Next()
	}
	units[0].ViaInterface()
	rows[1].Load()
	rest[0].Fill()
	byName["x"].Fill()
	grid[0][1].Fill()
	r := rows[2]
	r.Load()
	units[0].Store.Save()
	var more []Item
	for _, m := range more {
		m.Fill()
	}
}
`,
			line: methodType,
			want: []string{
				`7 ViaInterface qualifier="" type="example.com/app/internal/store.Unit" field=""`,
				`10 Next qualifier="" type="" field=""`,
				`12 ViaInterface qualifier="" type="example.com/app/internal/store.Unit" field=""`,
				`13 Load qualifier="" type="Row" field=""`,
				`14 Fill qualifier="" type="Item" field=""`,
				`15 Fill qualifier="" type="" field=""`,
				`16 Fill qualifier="" type="" field=""`,
				`18 Load qualifier="" type="Row" field=""`,
				`19 Save qualifier="" type="example.com/app/internal/store.Unit" field="Store"`,
				`22 Fill qualifier="" type="Item" field=""`,
			},
		},
		"an assignment's right side sees the type before it": {
			src: `package api

func run(x *DB) {
	x = x.Reset()
	x.Close()
	y := &Cache{}
	y = y.Clone()
	y.Fill()
}
`,
			line: methodType,
			want: []string{
				`4 Reset qualifier="" type="DB" field=""`,
				`5 Close qualifier="" type="" field=""`,
				`7 Clone qualifier="" type="Cache" field=""`,
				`8 Fill qualifier="" type="" field=""`,
			},
		},
		"assignments in a nested scope leave the outer type unknown": {
			src: `package api

func run(ok bool, items []*Other) {
	x := &DB{}
	{
		x = &Other{}
	}
	x.Save()
	y := &DB{}
	if ok {
		y = &Other{}
	}
	y.Save()
	z := &DB{}
	for _, z = range items {
	}
	z.Save()
	w := &DB{}
	{
		w := &Other{}
		w.Save()
	}
	w.Save()
}
`,
			line: methodType,
			want: []string{
				`8 Save qualifier="" type="" field=""`,
				`13 Save qualifier="" type="" field=""`,
				`17 Save qualifier="" type="" field=""`,
				`21 Save qualifier="" type="Other" field=""`,
				`23 Save qualifier="" type="DB" field=""`,
			},
		},
		"each branch starts from the types before it": {
			src: `package api

func run(ok bool, n int) {
	x := &DB{}
	if ok {
		x = &Other{}
	} else {
		x.Save()
	}
	x.Save()
	y := &DB{}
	switch n {
	case 1:
		y = &Other{}
	case 2:
		y.Save()
	}
	y.Save()
	z := &DB{}
	if ok {
		{
			z = &Other{}
		}
		z.Save()
	} else {
		z.Save()
	}
}
`,
			line: methodType,
			want: []string{
				`8 Save qualifier="" type="DB" field=""`,
				`10 Save qualifier="" type="" field=""`,
				`16 Save qualifier="" type="DB" field=""`,
				`18 Save qualifier="" type="" field=""`,
				`24 Save qualifier="" type="" field=""`,
				`26 Save qualifier="" type="DB" field=""`,
			},
		},
		"a shadowed receiver is an ordinary variable": {
			src: `package api

func (s *Server) Run() {
	s.store.Save()
	s.Flush()
	{
		s := &Other{}
		s.store.Save()
		s.Flush()
	}
	go func(s *Remote) {
		s.Flush()
	}(nil)
	s.Flush()
	var db = &DB{}
	db.Load()
}
`,
			line: methodType,
			want: []string{
				`4 Save qualifier="" type="" field="store"`,
				`5 Flush qualifier="self" type="" field=""`,
				`8 Save qualifier="" type="Other" field="store"`,
				`9 Flush qualifier="" type="Other" field=""`,
				`12 Flush qualifier="" type="Remote" field=""`,
				`14 Flush qualifier="self" type="" field=""`,
				`16 Load qualifier="" type="DB" field=""`,
			},
		},
		"field chains start from the receiver or a typed variable": {
			src: `package api

import "example.com/app/flow"

func (s *Server) Get(in flow.Inputs) {
	s.cfg.store.Save()
	in.Libraries.CrateOf()
	db := s.store
	db.Save()
	libraries := in.Libraries
	libraries.Locate()
	unknown.Libraries.Locate()
	flow.Default.Locate()
}
`,
			line: methodType,
			want: []string{
				`6 Save qualifier="" type="" field="cfg.store"`,
				`7 CrateOf qualifier="" type="example.com/app/flow.Inputs" field="Libraries"`,
				`9 Save qualifier="" type="" field="store"`,
				`11 Locate qualifier="" type="example.com/app/flow.Inputs" field="Libraries"`,
				`12 Locate qualifier="" type="" field=""`,
				`13 Locate qualifier="" type="" field=""`,
			},
		},
		"bare calls by a variable's name in scope are local": {
			src: `package api

func run(n *Node, src []byte, check func() bool) {
	if params := params(n, src); len(params) > 0 {
		params(n, src)
	}
	params(n, src)
	for _, x := range n.Items {
		shared := shared(x)
		shared(x)
	}
	shared(n)
	check()
	walk := func() { helper() }
	walk()
	var visit func()
	visit = func() { visit() }
	run(n, src, check)
}
`,
			line: func(c unit.Call) string {
				if c.Kind != unit.FreeCall {
					return ""
				}
				return fmt.Sprintf("%d %s local=%v", c.Line, c.Name, c.Local)
			},
			want: []string{
				`4 params local=false`,
				`4 len local=false`,
				`5 params local=true`,
				`7 params local=false`,
				`9 shared local=false`,
				`10 shared local=true`,
				`12 shared local=false`,
				`13 check local=true`,
				`14 helper local=false`,
				`15 walk local=true`,
				`17 visit local=true`,
				`18 run local=false`,
			},
		},
		"variables shadow imports and named results are typed": {
			src: `package api

import "encoding/json"

func run(json *Codec) (db *DB) {
	json.Marshal()
	db.Save()
	return
}

func plain() {
	json.Marshal()
	{
		json := &Codec{}
		json.Marshal()
	}
}
`,
			line: callType,
			want: []string{
				fmt.Sprintf(`6 Marshal kind=%d qualifier="" type="Codec"`, unit.MethodCall),
				fmt.Sprintf(`7 Save kind=%d qualifier="" type="DB"`, unit.MethodCall),
				fmt.Sprintf(`12 Marshal kind=%d qualifier="json" type=""`, unit.PathCall),
				fmt.Sprintf(`15 Marshal kind=%d qualifier="" type="Codec"`, unit.MethodCall),
			},
		},
		"select receives and literal results declare variables": {
			src: `package api

import "encoding/json"

func (s *Server) run(ch chan *Codec) {
	select {
	case json := <-ch:
		json.Marshal()
	case s := <-ch:
		s.Flush()
	}
	_ = func() (db *DB) {
		db.Save()
		return
	}
}
`,
			line: callType,
			want: []string{
				fmt.Sprintf(`8 Marshal kind=%d qualifier="" type=""`, unit.MethodCall),
				fmt.Sprintf(`10 Flush kind=%d qualifier="" type=""`, unit.MethodCall),
				fmt.Sprintf(`13 Save kind=%d qualifier="" type="DB"`, unit.MethodCall),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []unit.Call
			for _, u := range mustUnits(t, "api/run.go", tc.src) {
				calls = append(calls, u.Calls...)
			}
			testutil.Lines(t, "calls", testutil.Format(calls, tc.line), tc.want)
		})
	}
}

func TestParseRecordsTheParameterTypesOfFunctionTypes(t *testing.T) {
	src := `package cli

type command func(args []string, stdout, stderr io.Writer) int

type (
	handler func(context.Context, *Request) error
	options struct{ verbose bool }
)

type visit = func(path string)
`
	_, decls := testutil.Parse(t, Parse, "cli/cli.go", src)
	// Named or not, each parameter and result counts by its type, a
	// Context too; an alias declares a function type as well.
	if want := "[{cli/cli.go ([]string, io.Writer, io.Writer) (int) false} {cli/cli.go (context.Context, *Request) (error) false} {cli/cli.go (string) () false}]"; fmt.Sprint(decls.FuncTypes) != want {
		t.Errorf("FuncTypes = %v, want %s", decls.FuncTypes, want)
	}
}

func TestUnitsRecordTheirSignatures(t *testing.T) {
	src := `package store

func none() {}

func one() error { return nil }

func named(ctx context.Context, a, b string) (n, m int, err error) { return 0, 0, nil }

func variadic(a int, rest ...string) {}
`
	got := unitLines(t, "store/store.go", src, func(u unit.Unit) string { return u.Name + " " + u.Types })
	if want := "[none () () one () (error) named (context.Context, string, string) (int, int, error) variadic (int, ...string) ()]"; fmt.Sprint(got) != want {
		t.Errorf("signatures = %v, want %s", got, want)
	}
}

// A struct field's type is qualified by the import of its package: by its
// alias, or the name Go's tools assume for it (go-redis/v9 is redis,
// yaml.v3 is yaml, go-sqlite3 is sqlite3).
func TestStructFieldTypesAreQualifiedByTheFileImports(t *testing.T) {
	src := `package api

import (
	st "example.com/app/internal/store"
	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
	"github.com/mattn/go-sqlite3"
)

type Server struct {
	store *st.Store
	cache Cache
	names []string
	st.Logger
	rdb  *redis.Client
	conf yaml.Node
	conn sqlite3.SQLiteConn
}
`
	_, decls := testutil.Parse(t, Parse, "api/server.go", src)
	got := testutil.Format(decls.FieldTypes, func(f unit.FieldType) string {
		return fmt.Sprintf("%s.%s %s", f.Struct, f.Field, f.Type)
	})
	want := []string{
		"Server.store example.com/app/internal/store.Store",
		"Server.cache Cache",
		"Server.Logger example.com/app/internal/store.Logger",
		"Server.rdb github.com/redis/go-redis/v9.Client",
		"Server.conf gopkg.in/yaml.v3.Node",
		"Server.conn github.com/mattn/go-sqlite3.SQLiteConn",
	}
	testutil.Lines(t, "field types", got, want)
}

func TestFieldTypesOfTestFilesAreMarked(t *testing.T) {
	_, decls := testutil.Parse(t, Parse, "api/server_test.go", "package api_test\n\ntype Server struct{ store Fake }\n")
	if len(decls.FieldTypes) != 1 || !decls.FieldTypes[0].Test {
		t.Errorf("field types = %+v, want one marked as test code", decls.FieldTypes)
	}
}

func TestModulePathReadsGoMod(t *testing.T) {
	for gomod, want := range map[string]string{
		"module example.com/app\n\ngo 1.25\n":         "example.com/app",
		"// comment\nmodule \"example.com/q\" // x\n": "example.com/q",
		"go 1.25\n": "",
	} {
		if got := ModulePath([]byte(gomod)); got != want {
			t.Errorf("ModulePath(%q) = %q, want %q", gomod, got, want)
		}
	}
}

func TestTypeParametersAndTypeSwitchAliasesAreNotConcreteTypes(t *testing.T) {
	src := `package api

import "encoding/json"

type Box[T any] struct {
	v T
	c Cache
}

func run[T any](x T, c Cache) {
	x.Save()
	c.Fill()
}

func (s *Set[E]) Add(x E) {
	x.Save()
}

func (s *Server) kind(v any) {
	switch json := v.(type) {
	case *Codec:
		json.Marshal()
	}
	switch s := v.(type) {
	default:
		s.Flush()
	}
}
`
	units, decls := testutil.Parse(t, Parse, "api/run.go", src)
	var got []string
	for _, u := range units {
		for _, c := range u.Calls {
			got = append(got, fmt.Sprintf("%d kind=%d qualifier=%q type=%q", c.Line, c.Kind, c.Qualifier, c.Type))
		}
	}
	for _, f := range decls.FieldTypes {
		got = append(got, fmt.Sprintf("field %s.%s %s", f.Struct, f.Field, f.Type))
	}
	want := []string{
		fmt.Sprintf(`11 kind=%d qualifier="" type=""`, unit.MethodCall),
		fmt.Sprintf(`12 kind=%d qualifier="" type="Cache"`, unit.MethodCall),
		fmt.Sprintf(`16 kind=%d qualifier="" type=""`, unit.MethodCall),
		fmt.Sprintf(`22 kind=%d qualifier="" type=""`, unit.MethodCall),
		fmt.Sprintf(`26 kind=%d qualifier="" type=""`, unit.MethodCall),
		"field Box.c Cache",
	}
	testutil.Lines(t, "calls", got, want)
}

// unitLines parses src as path and lists its units, each as line makes it.
func unitLines(t *testing.T, path, src string, line func(unit.Unit) string) []string {
	t.Helper()
	units, _ := testutil.Parse(t, Parse, path, src)
	return testutil.Format(units, line)
}

func TestUnitsRecordTheirResultTypes(t *testing.T) {
	src := `package api

import st "example.com/app/internal/store"

func open() (*st.DB, error) { return nil, nil }
func one() *Cache { return nil }
func named() (db *st.DB, n int) { return }
func generic[T any]() (T, Cache) { var t T; return t, Cache{} }
func none() {}
`
	got := unitLines(t, "api/open.go", src, func(u unit.Unit) string { return fmt.Sprintf("%s %q", u.Name, u.Results) })
	want := []string{
		`open ["example.com/app/internal/store.DB" ""]`,
		`one ["Cache"]`,
		`named ["example.com/app/internal/store.DB" ""]`,
		`generic ["" "Cache"]`,
		`none []`,
	}
	testutil.Lines(t, "results", got, want)
}

func TestMethodCallsRecordTheCallProducingTheirOperand(t *testing.T) {
	src := `package api

func run(s *Server) {
	db, err := open()
	db.Save()
	err.Error()
	c := one()
	c.Fill()
	s.Store().Save()
	c = &Cache{}
	c.Fill()
	var d = one()
	d.Fill()
	r, _ := check()
	r.Summary.Health.Limit()
	libraries := load().Libraries
	libraries.Locate()
	load().Libraries.Locate()
}
`
	units, _ := testutil.Parse(t, Parse, "api/run.go", src)
	calls := units[0].Calls
	got := testutil.Format(calls, func(c unit.Call) string {
		return fmt.Sprintf("%d %s from=%s field=%q", c.Line, c.Name, testutil.Producer(calls, c), c.Field)
	})
	want := []string{
		`4 open from=- field=""`,
		`5 Save from=open#0 field=""`,
		`6 Error from=open#1 field=""`,
		`7 one from=- field=""`,
		`8 Fill from=one#0 field=""`,
		`9 Save from=Store#0 field=""`,
		`9 Store from=- field=""`,
		`11 Fill from=- field=""`,
		`12 one from=- field=""`,
		`13 Fill from=one#0 field=""`,
		`14 check from=- field=""`,
		`15 Limit from=check#0 field="Summary.Health"`,
		`16 load from=- field=""`,
		`17 Locate from=load#0 field="Libraries"`,
		`18 Locate from=load#0 field="Libraries"`,
		`18 load from=- field=""`,
	}
	testutil.Lines(t, "calls", got, want)
}

// In Go, a test case is a TestXxx, BenchmarkXxx, FuzzXxx or ExampleXxx
// function of a _test.go file; the rest there are helpers.
func TestGoTestCasesAreToldFromTestHelpers(t *testing.T) {
	src := `package store

import "testing"

func TestSave(t *testing.T) { fixture() }
func BenchmarkSave(b *testing.B) {}
func fixture() *Store { return nil }
func Testify() {}
func Testé() {}
`
	got := unitLines(t, "store/store_test.go", src, func(u unit.Unit) string { return fmt.Sprintf("%s case=%v", u.Name, u.TestCase) })
	testutil.Lines(t, "units", got, []string{
		"TestSave case=true",
		"BenchmarkSave case=true",
		"fixture case=false",
		"Testify case=false",
		"Testé case=false",
	})
}

// Imports maps each name a file's imports bind to the import's path: an
// alias, or the package's assumed name.
func TestImportsMapNamesToPaths(t *testing.T) {
	got := Imports([]byte(`package db

import (
	"fmt"
	pg "github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)
`))
	want := map[string]string{"fmt": "fmt", "pg": "github.com/jackc/pgx/v5/pgxpool", "redis": "github.com/redis/go-redis/v9"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Imports = %v, want %v", got, want)
	}
}
