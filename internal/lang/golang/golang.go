// Package golang extracts comparable units from Go source code.
package golang

import (
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	gogrammar "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

var language = sitter.NewLanguage(gogrammar.Language())

// leafKinds maps tree-sitter node kinds that are blanked out to their
// leaf kind. Literal kinds are taken whole.
var leafKinds = map[string]unit.LeafKind{
	"identifier":                 unit.Identifier,
	"field_identifier":           unit.Identifier,
	"package_identifier":         unit.Identifier,
	"label_name":                 unit.Identifier,
	"type_identifier":            unit.Type,
	"interpreted_string_literal": unit.Literal,
	"raw_string_literal":         unit.Literal,
	"rune_literal":               unit.Literal,
	"int_literal":                unit.Literal,
	"float_literal":              unit.Literal,
	"imaginary_literal":          unit.Literal,
	"nil":                        unit.Literal,
	"true":                       unit.Literal,
	"false":                      unit.Literal,
	"iota":                       unit.Literal,
}

// grammar describes Go to the shared flattening (see syntax.Grammar).
type grammar struct{}

// Prefix keeps Go's symbols apart from other languages' alike-named kinds.
func (grammar) Prefix() string { return "go:" }

func (grammar) Leaf(kind string) (unit.LeafKind, bool) {
	k, ok := leafKinds[kind]
	return k, ok
}

// Declares: the function's name, parameter names, and the names
// short variable declarations, var and const specs and range clauses
// introduce. Types and values in those positions declare nothing.
func (grammar) Declares(parent string, parentDeclares bool, field, _ string) bool {
	switch parent {
	case "function_declaration", "method_declaration", "parameter_declaration",
		"variadic_parameter_declaration", "var_spec", "const_spec":
		return field == "name"
	case "short_var_declaration", "range_clause", "receive_statement":
		return field == "left"
	case "type_switch_statement":
		return field == "alias"
	case "expression_list":
		return parentDeclares
	}
	return false
}

// Binds: every Go identifier in a declaring position binds a name.
func (grammar) Binds(string) bool { return true }

func (grammar) Statements(kind string) bool { return kind == "statement_list" }

// Visit counts early returns outside function literals.
func (grammar) Visit(s *syntax.Shaper, n *sitter.Node) func() {
	switch n.Kind() {
	case "return_statement":
		if s.Closures == 0 {
			s.Exits++
		}
	case "func_literal":
		s.Closures++
		return func() { s.Closures-- }
	}
	return nil
}

// Units parses Go source and returns one unit per function or method, in
// source order. Functions containing syntax errors are skipped.
func Units(path string, src []byte) ([]unit.Unit, error) {
	var units []unit.Unit
	test := IsTestPath(path)
	err := syntax.Parse(language, src, func(root *sitter.Node) {
		for i := uint(0); i < root.NamedChildCount(); i++ {
			n := root.NamedChild(i)
			if (n.Kind() != "function_declaration" && n.Kind() != "method_declaration") || n.HasError() {
				continue
			}
			name := n.ChildByFieldName("name")
			if name == nil {
				continue
			}
			full := name.Utf8Text(src)
			if owner := receiverType(n, src); owner != "" {
				full = owner + "." + full
			}
			s := syntax.Flatten(n, src, grammar{})
			units = append(units, unit.Unit{
				File:      path,
				Name:      full,
				StartLine: syntax.Line(n.StartPosition()),
				EndLine:   syntax.Line(n.EndPosition()),
				Shape:     similarity.HashSymbols(s.Seq),
				Nodes:     s.Nodes,
				Test:      test,
				Leaves:    s.Leaves,
				Seq:       s.Seq,
				SeqLines:  s.Lines,
				Blocks:    s.Blocks,
			})
		}
	})
	return units, err
}

// receiverType returns a method's receiver type name, without pointer or
// type arguments: Store for (s *Store[T]); "" for a function.
func receiverType(n *sitter.Node, src []byte) string {
	recv := n.ChildByFieldName("receiver")
	if recv == nil || recv.NamedChildCount() == 0 {
		return ""
	}
	t := recv.NamedChild(0).ChildByFieldName("type")
	for t != nil && (t.Kind() == "pointer_type" || t.Kind() == "generic_type") {
		if t.Kind() == "pointer_type" {
			t = t.NamedChild(0)
		} else {
			t = t.ChildByFieldName("type")
		}
	}
	if t == nil {
		return ""
	}
	return t.Utf8Text(src)
}

// IsTestPath reports whether a file is test code by Go convention: a
// _test.go file, or a file under a testdata directory, which the go tool
// never builds.
func IsTestPath(path string) bool {
	slashed := filepath.ToSlash(path)
	return strings.HasSuffix(slashed, "_test.go") || strings.HasPrefix(slashed, "testdata/") || strings.Contains(slashed, "/testdata/")
}
