// Package unit defines the language-neutral view of code that every
// detector works on. Language adapters produce units; detectors compare them.
package unit

import "strings"

// Unit is one comparable piece of code, such as a function or method.
type Unit struct {
	File      string
	Name      string
	StartLine int // 1-based, inclusive
	EndLine   int // 1-based, inclusive
	// Shape is a hash of the unit's syntax tree with identifiers, types and
	// literals blanked out. Units with equal shapes are exact or renamed copies.
	Shape uint64
	// Nodes is the number of syntax nodes in the unit, used as its size.
	Nodes int
	// Test is true for test code: test functions, functions in test
	// modules and functions in test files.
	Test bool
	// Module is the inline module the unit is declared in within its file
	// (outer::inner for mod outer { mod inner { .. } }), "" for none. Rust
	// only.
	Module string
	// TestCase is true for a test the test runner calls (#[test], TestXxx);
	// other test code is helpers, fakes and fixtures.
	TestCase bool
	// Ignored is true when a ddslop:ignore comment marks the unit: it is
	// left out of the analysis.
	Ignored bool
	// Leaves are the blanked-out tokens in pre-order. Units with equal shapes
	// have leaves that line up one to one, which is how differences are found.
	Leaves []Leaf
	// Seq is the syntax tree flattened in pre-order: one symbol per node
	// kind, a Close symbol after each node's children, and LeafSymbol(kind)
	// for each blanked-out token, in the same order as Leaves. Equal shapes
	// have equal sequences; similar code has similar sequences.
	Seq []uint32
	// SeqLines holds the source line of each symbol in Seq.
	SeqLines []int
	// Signature is the MinHash signature of Seq, used to find near-miss
	// candidates. It is computed when the unit is indexed; empty means not
	// computed yet.
	Signature []uint64
	// Blocks lists, for every block in the unit (the body and nested
	// blocks, in pre-order), its statements in order. Copied blocks inside
	// larger functions are found as repeated runs of statements.
	Blocks [][]Statement
	// Trait is the trait an enclosing impl block implements, if any.
	Trait string
	// Calls are the unit's call sites, in source order.
	Calls []Call
	// Public is true when the unit is visible outside its package or crate
	// (Rust: plain pub).
	Public bool
	// External is true when code outside the analysed source calls the
	// unit: a program's main, or a function whose attributes export it or
	// register it, such as #[no_mangle], #[tokio::main] or #[get("/")].
	External bool
	// Dispatched is true when the unit may be called through an interface
	// without its name being written at the call: Go methods, which satisfy
	// interfaces implicitly. Rust trait methods are marked by Trait.
	Dispatched bool
	// Forwards is set when the unit only passes its parameters on, in
	// order and unchanged, to another function: the callee as written,
	// such as "load", "super::load" or "self.load".
	Forwards string
	// Scope is set when the unit opens a value at its start and closes it
	// at its end, such as a transaction it begins and commits.
	Scope *Scope
	// StringMatches are the unit's matches with at least MinStringArms
	// string literals among their arm patterns.
	StringMatches []StringMatch
	// Params are the unit's named parameters in order, without self and
	// without patterns that bind several names.
	Params []Param
	// Types are the unit's parameter and result types, named or not,
	// such as "(context.Context, string) (int, error)" (Go).
	Types string
	// Results are the unit's result types, written as Call.Type writes an
	// operand's type ("" where that has none, such as error). In Go, one
	// per result. In Rust, the type as returned and what a ? leaves of it,
	// then, for an async fn, what .await and .await? give.
	Results []string
	// Forms are the forms the unit uses for concerns its language's
	// adapter knows, such as how it checks errors, in source order.
	Forms []Form
}

// The concerns adapters record forms for.
const (
	ErrorChecks   = "error-checks"   // how code tells one error from another
	ErrorWrapping = "error-wrapping" // how code adds context to an error it passes on
	ErrorValues   = "error-values"   // how code makes a new error
)

// Form is one occurrence of a form for a concern: "error-checks" done
// with "errors.Is/As" at a line.
type Form struct {
	Concern, Form string
	Line          int
}

// Param is one parameter: its name and its type as written, without
// whitespace between symbols.
type Param struct {
	Name, Type string
}

// MinStringArms is the fewest string literals a match needs among its arm
// patterns to treat them as a set of kinds.
const MinStringArms = 3

// StringMatch is a match whose arms compare against string literals.
type StringMatch struct {
	// Values are the literals, as written with their quotes, sorted.
	Values             []string
	StartLine, EndLine int
}

// Scope is a value a function opens in one of its first statements and
// closes in one of its last.
type Scope struct {
	// Opens is the name of the function or method that produced the value
	// ("transaction"), and Closes the method that ends it ("commit"), or
	// "drop".
	Opens, Closes      string
	StartLine, EndLine int
	// Nodes is the size of the opening and closing statements.
	Nodes int
}

// CallKind says how a call names its callee.
type CallKind int

// Call kinds.
const (
	FreeCall   CallKind = iota + 1 // foo(...)
	PathCall                       // Type::foo(...), module::foo(...), Self::foo(...)
	MethodCall                     // value.foo(...)
)

// Call is one call site. Calls are resolved to units by name, so they are
// approximate: dynamic dispatch and calls inside macros are not seen.
type Call struct {
	Kind CallKind
	// Qualifier is the path before the name of a path call, as written,
	// or "self" for a method called on self.
	Qualifier string
	Name      string
	Line      int
	// Type is the type of a method call's operand where the code states
	// it (a parameter, var x T, x := T{}), as a Go package's import path
	// and the type name ("example.com/app/store.DB"), or the name alone
	// for the caller's own package. Go only.
	Type string
	// Field is the field a method is called on, followed from the
	// operand: the receiver's (store in s.store.Save(), Go's and Rust's
	// self.store.get()) or, in Rust, a dotted path from the receiver, a
	// typed variable (Type) or a call's result (From): ctx.db in
	// self.ctx.db.query(), store in req.store.get().
	Field string
	// From is 1 + the index, among its unit's calls, of the call whose
	// result the operand is (db.Save() after db, err := open(), or
	// s.Store().Save()), and Result that result's position; From is 0
	// when the operand is no call's result. Go only.
	From, Result int
	// Adapts marks a Rust method named like a result adapter (map_err,
	// context, unwrap, ...): 1 when it keeps its operand's value, 2 when
	// it takes it out as ? does. A library's is looked through to the
	// call it adapts; one the repository defines is a call like any other.
	Adapts int
	// Ignored is true when a ddslop:ignore comment marks the call's line.
	Ignored bool
}

// Statement is one statement of a block, as positions in its unit.
type Statement struct {
	SeqStart, SeqEnd   int // the statement's symbols are Seq[SeqStart:SeqEnd]
	LeafStart, LeafEnd int // its leaves are Leaves[LeafStart:LeafEnd]
	StartLine, EndLine int
	Nodes              int
	// Exits is true when the statement can return early by its own
	// decision: it holds a return or a let-else outside any closure. A ?
	// only propagates an error, as setup code does too.
	Exits bool
}

// Close is the Seq symbol that ends a node's children.
const Close uint32 = 0

// LeafSymbol is the Seq symbol of a blanked-out token of the given kind.
func LeafSymbol(k LeafKind) uint32 { return uint32(k) } //nolint:gosec // leaf kinds are 1 to 3

// IsLeaf reports whether a Seq symbol stands for a blanked-out token.
func IsLeaf(sym uint32) bool { return sym >= uint32(Identifier) && sym <= uint32(Literal) }

// LeafKind says what a blanked-out token is.
type LeafKind int

// Leaf kinds.
const (
	Identifier LeafKind = iota + 1
	Type
	Literal
)

// Leaf is one blanked-out token: its kind, its original text and its line.
type Leaf struct {
	Kind LeafKind
	Text string
	Line int
	// Declares is true when this occurrence introduces a name inside the
	// unit: the unit's own name, a parameter, or a local binding.
	Declares bool
	// Ignored is true when a ddslop:ignore comment marks the leaf's line.
	Ignored bool
}

// SimpleName reduces a type or trait path to its last segment without
// generic arguments: "fmt::Display" and "Endpoint<P>" become "Display" and
// "Endpoint". Calls and owners are matched on simple names.
func SimpleName(path string) string {
	if i := strings.Index(path, "<"); i >= 0 {
		path = path[:i]
	}
	if i := strings.LastIndex(path, "::"); i >= 0 {
		path = path[i+2:]
	}
	return strings.TrimSpace(path)
}

// Decls are a file's declarations other than functions.
type Decls struct {
	// Package is the file's package name, for languages that declare one
	// (Go).
	Package string
	Structs []Struct
	Enums   []Enum
	Consts  []Const
	Traits  []Trait
	Impls   []Impl
	// TypeNames are the names of every type the file declares: structs of
	// any kind, enums, unions and type aliases.
	TypeNames []string
	// FuncTypes are the function types the file declares; Go only.
	FuncTypes []FuncType
	// FieldTypes are the types of the file's struct fields, written as
	// Call.Type; fields of other types (slices, maps) are left out. Go
	// only.
	FieldTypes []FieldType
}

// FieldType is the type of one struct field: Server.store is a
// "example.com/app/store.Store".
type FieldType struct {
	Struct, Field, Type string
	// Unwrapped is what ? or unwrap() leaves of a Rust field's value:
	// the type inside an Option or Result, the type itself otherwise.
	Unwrapped string
	// File and Module are where the struct is declared: its file and, in
	// Rust, the inline module within it, where a relative type is read.
	File, Module string
	Test         bool
}

// FuncType is a function type declaration: type command func(args
// []string, out io.Writer) int.
type FuncType struct {
	File string
	// Types are written as a unit's (see Unit.Types).
	Types string
	Test  bool
}

// Trait is a trait declaration.
type Trait struct {
	File               string
	Name               string
	StartLine, EndLine int
	Nodes              int
	// Public is true when the trait is visible outside its crate.
	Public bool
	Test   bool
}

// Impl is an implementation of a trait for a type, both as simple names
// (see SimpleName).
type Impl struct {
	File               string
	Trait, Type        string
	StartLine, EndLine int
	// Blanket is true when the type is one of the impl's own type
	// parameters: impl<T: Read> Io for T.
	Blanket bool
	Test    bool
}

// Const is a constant or static declared at module level, outside any
// function or impl block.
type Const struct {
	File, Name string
	// Value is the initialiser as written: a string literal exactly, any
	// other value without whitespace.
	Value string
	Line  int
	Nodes int
	// Literal is true when the value is one string literal; Value then
	// holds it with its quotes, as code writes it inline.
	Literal bool
	// Macro is true when the value is a macro call, such as include_str!,
	// whose meaning can depend on where it is written.
	Macro bool
	Test  bool
	// Ignored is true when a ddslop:ignore comment marks the constant.
	Ignored bool
}

// Struct is a struct with named fields.
type Struct struct {
	File               string
	Name               string
	StartLine, EndLine int
	// Nodes is the number of syntax nodes in the declaration.
	Nodes  int
	Fields []Field
	Test   bool
	// Ignored is true when a ddslop:ignore comment marks the struct.
	Ignored bool
	// Converts are the types the struct is deserialised from or serialised
	// into, as simple names (#[serde(try_from = "X", from = "Y", into = "Z")]):
	// a struct validating another is a deliberate second type, not a copy.
	Converts []string
}

// Enum is an enum declaration with its variants in order.
type Enum struct {
	File               string
	Name               string
	StartLine, EndLine int
	Nodes              int
	Variants           []Variant
	Test               bool
	// Ignored is true when a ddslop:ignore comment marks the enum.
	Ignored bool
}

// Variant is one enum variant: its name and its payload as written,
// without whitespace between symbols ("(String)", "{at:u64}"), or "".
type Variant struct {
	Name, Payload string
}

// Field is one named field of a struct, with its type as written, without
// whitespace.
type Field struct {
	Name, Type string
	Line       int
}

// At returns where a unit starts.
func (u Unit) At() (file string, line int) { return u.File, u.StartLine }

// At returns where a struct starts.
func (s Struct) At() (file string, line int) { return s.File, s.StartLine }

// At returns where an enum starts.
func (e Enum) At() (file string, line int) { return e.File, e.StartLine }

// At returns where a constant is declared.
func (c Const) At() (file string, line int) { return c.File, c.Line }

// ViaInterface reports whether a unit may be called through a trait or
// interface rather than by name, so a missing call or a dictated signature
// says nothing about it.
func (u Unit) ViaInterface() bool { return u.Trait != "" || u.Dispatched }
