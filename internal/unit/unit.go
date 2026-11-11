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
	// Forwards is set when the unit only passes its parameters on, in
	// order and unchanged, to another function: the callee as written,
	// such as "load", "super::load" or "self.load".
	Forwards string
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
}

// Statement is one statement of a block, as positions in its unit.
type Statement struct {
	SeqStart, SeqEnd   int // the statement's symbols are Seq[SeqStart:SeqEnd]
	LeafStart, LeafEnd int // its leaves are Leaves[LeafStart:LeafEnd]
	StartLine, EndLine int
	Nodes              int
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
	Structs []Struct
	Consts  []Const
	Traits  []Trait
	Impls   []Impl
	// TypeNames are the names of every type the file declares: structs of
	// any kind, enums, unions and type aliases.
	TypeNames []string
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
	// Converts are the types the struct is deserialised from or serialised
	// into, as simple names (#[serde(try_from = "X", from = "Y", into = "Z")]):
	// a struct validating another is a deliberate second type, not a copy.
	Converts []string
}

// Field is one named field of a struct, with its type as written, without
// whitespace.
type Field struct {
	Name, Type string
	Line       int
}
