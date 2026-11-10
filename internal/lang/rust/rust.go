// Package rust extracts comparable units from Rust source code.
package rust

import (
	"errors"
	"hash"
	"hash/fnv"

	sitter "github.com/tree-sitter/go-tree-sitter"
	rustgrammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

var language = sitter.NewLanguage(rustgrammar.Language())

// leafKinds maps tree-sitter node kinds that are blanked out to their
// leaf kind. Literal kinds are taken whole, without descending into them.
var leafKinds = map[string]unit.LeafKind{
	"identifier":                 unit.Identifier,
	"field_identifier":           unit.Identifier,
	"shorthand_field_identifier": unit.Identifier,
	"type_identifier":            unit.Type,
	"primitive_type":             unit.Type,
	"string_literal":             unit.Literal,
	"raw_string_literal":         unit.Literal,
	"char_literal":               unit.Literal,
	"integer_literal":            unit.Literal,
	"float_literal":              unit.Literal,
	"boolean_literal":            unit.Literal,
}

// leafMarkers are what a blanked-out leaf contributes to the shape hash.
var leafMarkers = map[unit.LeafKind][]byte{
	unit.Identifier: []byte("#id\x00"),
	unit.Type:       []byte("#type\x00"),
	unit.Literal:    []byte("#lit\x00"),
}

var closeMarker = []byte(")\x00")

// Units parses Rust source and returns one unit per function or method, in
// source order. Functions containing syntax errors are skipped.
func Units(path string, src []byte) ([]unit.Unit, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return nil, err
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, errors.New("rust: parser returned no tree")
	}
	defer tree.Close()

	e := extractor{path: path, src: src}
	e.walk(tree.RootNode(), "")
	return e.units, nil
}

type extractor struct {
	path  string
	src   []byte
	units []unit.Unit
}

// walk finds function items, tracking the enclosing impl or trait name.
func (e *extractor) walk(n *sitter.Node, owner string) {
	switch n.Kind() {
	case "function_item":
		e.addFunction(n, owner)
		return
	case "impl_item", "trait_item":
		field := "type"
		if n.Kind() == "trait_item" {
			field = "name"
		}
		if t := n.ChildByFieldName(field); t != nil {
			owner = t.Utf8Text(e.src)
		}
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		e.walk(n.Child(i), owner)
	}
}

func (e *extractor) addFunction(n *sitter.Node, owner string) {
	if n.HasError() {
		return
	}
	name := ""
	if id := n.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(e.src)
	}
	if owner != "" {
		name = owner + "::" + name
	}
	s := shaper{src: e.src, h: fnv.New64a()}
	s.visit(n, false)
	e.units = append(e.units, unit.Unit{
		File:      e.path,
		Name:      name,
		StartLine: line(n.StartPosition()),
		EndLine:   line(n.EndPosition()),
		Shape:     s.h.Sum64(),
		Nodes:     s.nodes,
		Leaves:    s.leaves,
	})
}

// shaper hashes a subtree's structure and collects its blanked-out leaves.
type shaper struct {
	src    []byte
	h      hash.Hash64
	nodes  int
	leaves []unit.Leaf
}

func (s *shaper) visit(n *sitter.Node, declares bool) {
	if n.IsExtra() {
		return // comments
	}
	s.nodes++
	if kind, ok := leafKinds[n.Kind()]; ok {
		_, _ = s.h.Write(leafMarkers[kind])
		text := n.Utf8Text(s.src)
		s.leaves = append(s.leaves, unit.Leaf{
			Kind:     kind,
			Text:     text,
			Line:     line(n.StartPosition()),
			Declares: declares && kind == unit.Identifier && !isUpper(text),
		})
		return
	}
	_, _ = s.h.Write([]byte(n.Kind()))
	_, _ = s.h.Write([]byte{0})
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		field := n.FieldNameForChild(uint32(i))
		s.visit(child, childDeclares(n.Kind(), declares, field, child.Kind()))
	}
	_, _ = s.h.Write(closeMarker)
}

// childDeclares reports whether a child sits in a position that introduces
// names: the function name, parameter and binding patterns. Paths and
// struct field names inside patterns never introduce names.
func childDeclares(parent string, parentDeclares bool, field, child string) bool {
	switch parent {
	case "function_item":
		return field == "name"
	case "parameter", "let_declaration", "for_expression", "let_condition":
		return field == "pattern"
	case "closure_parameters":
		return true
	case "match_pattern":
		return field != "condition"
	case "tuple_struct_pattern", "struct_pattern":
		return parentDeclares && field != "type"
	case "field_pattern":
		return parentDeclares && (field != "name" || child == "shorthand_field_identifier")
	case "scoped_identifier", "scoped_type_identifier":
		return false
	}
	return parentDeclares
}

// isUpper reports whether a name starts with an upper-case ASCII letter. In
// Rust patterns such names are enum variants or constants, not bindings.
func isUpper(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

// line converts a tree-sitter position to a 1-based line number.
func line(p sitter.Point) int {
	return int(p.Row) + 1 //nolint:gosec // a row count always fits in int
}
