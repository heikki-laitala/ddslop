// Package typescript extracts comparable units from TypeScript source code.
package typescript

import (
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgrammar "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

var (
	typescript = sitter.NewLanguage(tsgrammar.LanguageTypescript())
	tsx        = sitter.NewLanguage(tsgrammar.LanguageTSX())
)

// Extensions are the file extensions of TypeScript source.
var Extensions = []string{".ts", ".tsx", ".mts", ".cts"}

// leafKinds maps tree-sitter node kinds that are blanked out to their
// leaf kind. Literal kinds are taken whole.
var leafKinds = map[string]unit.LeafKind{
	"identifier":                            unit.Identifier,
	"property_identifier":                   unit.Identifier,
	"private_property_identifier":           unit.Identifier,
	"shorthand_property_identifier":         unit.Identifier,
	"shorthand_property_identifier_pattern": unit.Identifier,
	"statement_identifier":                  unit.Identifier,
	"type_identifier":                       unit.Type,
	"predefined_type":                       unit.Type,
	"string":                                unit.Literal,
	"template_string":                       unit.Literal,
	"number":                                unit.Literal,
	"regex":                                 unit.Literal,
	"jsx_text":                              unit.Literal,
	"true":                                  unit.Literal,
	"false":                                 unit.Literal,
	"null":                                  unit.Literal,
	"undefined":                             unit.Literal,
}

// functionKinds are the node kinds holding a function's own body: a
// return inside one nested in a unit leaves only that function.
var functionKinds = map[string]bool{
	"function_declaration": true, "generator_function_declaration": true, "function_expression": true,
	"generator_function": true, "arrow_function": true, "method_definition": true,
}

// grammar describes TypeScript to the shared flattening (see
// syntax.Grammar) for one unit: fn is the unit's own function, whose
// returns are the unit's.
type grammar struct {
	fn [2]uint
}

// Prefix keeps TypeScript's symbols apart from other languages'
// alike-named kinds.
func (grammar) Prefix() string { return "ts:" }

func (grammar) Leaf(kind string) (unit.LeafKind, bool) {
	k, ok := leafKinds[kind]
	return k, ok
}

// Declares: the names of functions, methods, classes, variables,
// parameters, catch clauses and for-of loops, through destructuring
// patterns; a pattern's keys and default values declare nothing.
func (grammar) Declares(parent string, parentDeclares bool, field, _ string) bool {
	switch parent {
	case "function_declaration", "generator_function_declaration", "function_expression", "generator_function",
		"class_declaration", "method_definition", "public_field_definition", "variable_declarator":
		return field == "name"
	case "required_parameter", "optional_parameter":
		return field == "pattern"
	case "arrow_function", "catch_clause":
		return field == "parameter"
	case "for_in_statement":
		return field == "left"
	case "pair_pattern":
		return parentDeclares && field == "value"
	case "assignment_pattern", "object_assignment_pattern":
		return parentDeclares && field == "left"
	case "object_pattern", "array_pattern", "rest_pattern":
		return parentDeclares
	}
	return false
}

// Binds: every TypeScript identifier in a declaring position binds a name.
func (grammar) Binds(string) bool { return true }

func (grammar) Statements(kind string) bool { return kind == "statement_block" }

// Visit counts early returns outside nested functions.
func (g grammar) Visit(s *syntax.Shaper, n *sitter.Node) func() {
	switch {
	case n.Kind() == "return_statement":
		if s.Closures == 0 {
			s.Exits++
		}
	case functionKinds[n.Kind()] && syntax.Span(n) != g.fn:
		s.Closures++
		return func() { s.Closures-- }
	}
	return nil
}

// Parse parses TypeScript source and returns one unit per function,
// method, and constant or class field holding a function, in source order.
// Declarations containing syntax errors are skipped, and functions nested
// in functions are part of them.
func Parse(file string, src []byte) ([]unit.Unit, unit.Decls, error) {
	var units []unit.Unit
	var decls unit.Decls
	var marked error
	p := parser{file: file, src: src, test: IsTestPath(file)}
	err := syntax.Parse(languageOf(file), src, func(root *sitter.Node) {
		for i := uint(0); i < root.NamedChildCount(); i++ {
			n, exported := root.NamedChild(i), false
			if d := n.ChildByFieldName("declaration"); n.Kind() == "export_statement" && d != nil {
				n, exported = d, true
			}
			if !n.HasError() {
				units = append(units, p.declaration(n, exported)...)
			}
		}
		marked = syntax.MarkIgnored(root, src, units, &decls)
	})
	return units, decls, errors.Join(err, marked)
}

// languageOf picks the grammar for a file: TSX for .tsx files, which
// may hold JSX.
func languageOf(file string) *sitter.Language {
	if strings.HasSuffix(file, ".tsx") {
		return tsx
	}
	return typescript
}

// parser holds what every unit of a file shares.
type parser struct {
	file string
	src  []byte
	test bool
}

// declaration returns the units of a top-level declaration.
func (p parser) declaration(n *sitter.Node, exported bool) []unit.Unit {
	var out []unit.Unit
	switch n.Kind() {
	case "function_declaration", "generator_function_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			out = append(out, p.unit(name.Utf8Text(p.src), n, n, exported))
		}
	case "lexical_declaration", "variable_declaration":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			d := n.NamedChild(i)
			name, value := d.ChildByFieldName("name"), d.ChildByFieldName("value")
			if d.Kind() == "variable_declarator" && name != nil && name.Kind() == "identifier" && isFunction(value) {
				out = append(out, p.unit(name.Utf8Text(p.src), d, value, exported))
			}
		}
	case "class_declaration", "abstract_class_declaration":
		out = p.members(n, exported)
	}
	return out
}

// members returns a class's methods and fields holding a function, named
// Class.member. A private member is never public.
func (p parser) members(class *sitter.Node, exported bool) []unit.Unit {
	name, body := class.ChildByFieldName("name"), class.ChildByFieldName("body")
	if name == nil || body == nil {
		return nil
	}
	var out []unit.Unit
	for i := uint(0); i < body.NamedChildCount(); i++ {
		m := body.NamedChild(i)
		member, fn := m.ChildByFieldName("name"), m
		if m.Kind() == "public_field_definition" {
			fn = m.ChildByFieldName("value")
		}
		if member == nil || (m.Kind() != "method_definition" && !isFunction(fn)) {
			continue
		}
		out = append(out, p.unit(name.Utf8Text(p.src)+"."+member.Utf8Text(p.src), m, fn, exported && !isPrivate(m, member)))
	}
	return out
}

// isFunction reports whether a value is a function expression of any kind.
func isFunction(value *sitter.Node) bool {
	return value != nil && functionKinds[value.Kind()]
}

// isPrivate reports whether a class member is private or protected, by
// modifier or by a #name.
func isPrivate(m, name *sitter.Node) bool {
	if name.Kind() == "private_property_identifier" {
		return true
	}
	for i := uint(0); i < m.NamedChildCount(); i++ {
		if c := m.NamedChild(i); c.Kind() == "accessibility_modifier" {
			return c.Child(0) != nil && c.Child(0).Kind() != "public"
		}
	}
	return false
}

// unit makes a unit of the declaration n, whose own function is fn.
func (p parser) unit(name string, n, fn *sitter.Node, public bool) unit.Unit {
	s := syntax.Flatten(n, p.src, grammar{fn: syntax.Span(fn)})
	return unit.Unit{
		File:      p.file,
		Name:      name,
		StartLine: syntax.Line(n.StartPosition()),
		EndLine:   syntax.Line(n.EndPosition()),
		Shape:     similarity.HashSymbols(s.Seq),
		Nodes:     s.Nodes,
		Test:      p.test,
		Leaves:    s.Leaves,
		Seq:       s.Seq,
		SeqLines:  s.Lines,
		Blocks:    s.Blocks,
		Public:    public,
	}
}

// IsTestPath reports whether a file is test code by the conventions of
// TypeScript test runners: a .test or .spec file, or a file under a
// __tests__ directory.
func IsTestPath(file string) bool {
	slashed := filepath.ToSlash(file)
	base := path.Base(slashed)
	stem := strings.TrimSuffix(base, path.Ext(base))
	return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") ||
		slices.Contains(strings.Split(path.Dir(slashed), "/"), "__tests__")
}

// IsDeclarationFile reports whether a file only declares types for code
// elsewhere (.d.ts): it holds no code to compare.
func IsDeclarationFile(file string) bool {
	for _, ext := range Extensions {
		if strings.HasSuffix(file, ".d"+ext) {
			return true
		}
	}
	return false
}
