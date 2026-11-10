// Package unit defines the language-neutral view of code that every
// detector works on. Language adapters produce units; detectors compare them.
package unit

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
}

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
