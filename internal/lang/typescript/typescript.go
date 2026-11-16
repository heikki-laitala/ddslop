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
// returns are the unit's, and imports the names its file imports, to
// tell calls apart.
type grammar struct {
	fn      [2]uint
	imports map[string]imported
}

// Prefix keeps TypeScript's symbols apart from other languages'
// alike-named kinds.
func (grammar) Prefix() string { return "ts:" }

func (grammar) Leaves() map[string]unit.LeafKind { return leafKinds }

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

// Visit records calls and counts early returns outside nested functions.
func (g grammar) Visit(s *syntax.Shaper, n *sitter.Node) func() {
	switch {
	case n.Kind() == "call_expression":
		if c, ok := g.call(n, s.Src); ok {
			s.Calls = append(s.Calls, c)
		}
	case n.Kind() == "switch_statement":
		s.RecordStringMatch(n, caseStrings(n, s.Src))
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
// method, and constant or class field holding a function, in source order,
// and its module-level declarations: interfaces and object types as
// structs, enums and unions of string literals as enums, and constants.
// Declarations containing syntax errors are skipped, and functions nested
// in functions are part of them.
func Parse(file string, src []byte) ([]unit.Unit, unit.Decls, error) {
	var marked error
	p := &parser{file: file, src: src, test: IsTestPath(file)}
	err := syntax.Parse(languageOf(file), src, func(root *sitter.Node) {
		p.imports = imports(root, src)
		for i := uint(0); i < root.NamedChildCount(); i++ {
			n, exported := root.NamedChild(i), false
			if d := n.ChildByFieldName("declaration"); n.Kind() == "export_statement" && d != nil {
				n, exported = d, true
			}
			if !n.HasError() {
				p.declaration(n, exported)
			}
		}
		p.markExported(root)
		marked = syntax.MarkIgnored(root, src, p.units, &p.decls)
	})
	return p.units, p.decls, errors.Join(err, marked)
}

// markExported makes public the units an export clause (export { load })
// or a default export of a name (export default save) lists; a clause
// re-exporting another module's names (export { x } from "./y") lists
// none of the file's.
func (p *parser) markExported(root *sitter.Node) {
	names := map[string]bool{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		if n.Kind() != "export_statement" || n.ChildByFieldName("declaration") != nil || n.ChildByFieldName("source") != nil {
			continue
		}
		if v := n.ChildByFieldName("value"); v != nil && v.Kind() == "identifier" {
			names[v.Utf8Text(p.src)] = true
		}
		for j := uint(0); j < n.NamedChildCount(); j++ {
			clause := n.NamedChild(j)
			if clause.Kind() != "export_clause" {
				continue
			}
			for k := uint(0); k < clause.NamedChildCount(); k++ {
				if name := clause.NamedChild(k).ChildByFieldName("name"); name != nil {
					names[name.Utf8Text(p.src)] = true
				}
			}
		}
	}
	for i := range p.units {
		p.units[i].Public = p.units[i].Public || names[p.units[i].Name]
	}
}

// languageOf picks the grammar for a file: TSX for .tsx files, which
// may hold JSX.
func languageOf(file string) *sitter.Language {
	if strings.HasSuffix(file, ".tsx") {
		return tsx
	}
	return typescript
}

// parser collects a file's units and declarations.
type parser struct {
	file    string
	src     []byte
	test    bool
	imports map[string]imported
	units   []unit.Unit
	decls   unit.Decls
}

// declaration records the units and declarations of a top-level
// declaration.
func (p *parser) declaration(n *sitter.Node, exported bool) {
	switch n.Kind() {
	case "function_declaration", "generator_function_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			p.units = append(p.units, p.unit(name.Utf8Text(p.src), n, n, exported))
		}
	case "lexical_declaration", "variable_declaration":
		kind := n.ChildByFieldName("kind")
		constant := kind != nil && kind.Kind() == "const"
		for i := uint(0); i < n.NamedChildCount(); i++ {
			d := n.NamedChild(i)
			name, value := d.ChildByFieldName("name"), d.ChildByFieldName("value")
			switch {
			case d.Kind() != "variable_declarator" || name == nil || name.Kind() != "identifier" || value == nil:
			case isFunction(unwrapped(value)):
				p.units = append(p.units, p.unit(name.Utf8Text(p.src), d, unwrapped(value), exported))
			case constant && isConstant(value) && (!exported || !p.routeOption(name.Utf8Text(p.src))):
				p.constant(name, value)
			}
		}
	case "class_declaration", "abstract_class_declaration":
		p.members(n, exported)
	case "interface_declaration":
		p.structure(n, n.ChildByFieldName("body"))
	case "type_alias_declaration":
		switch value := n.ChildByFieldName("value"); {
		case value == nil:
		case value.Kind() == "object_type":
			p.structure(n, value)
		case value.Kind() == "union_type":
			p.union(n, value)
		}
	case "enum_declaration":
		p.enum(n)
	}
}

// routeOptions are the names under which a Next.js page, layout or route
// file exports its route's options.
var routeOptions = map[string]bool{
	"dynamic": true, "dynamicParams": true, "revalidate": true, "fetchCache": true,
	"runtime": true, "preferredRegion": true, "maxDuration": true,
}

// routeOption reports whether a constant the file exports is a Next.js
// route option: a copy the framework asks every route for.
func (p *parser) routeOption(name string) bool {
	base := path.Base(filepath.ToSlash(p.file))
	switch strings.TrimSuffix(base, path.Ext(base)) {
	case "page", "layout", "route":
		return routeOptions[name]
	}
	return false
}

// unwrapped returns the value a type assertion or check, or parentheses,
// wrap ({} as Cache, (x => f(x)) satisfies Handler), else the value.
func unwrapped(value *sitter.Node) *sitter.Node {
	for value.NamedChildCount() > 0 && (value.Kind() == "as_expression" || value.Kind() == "satisfies_expression" || value.Kind() == "parenthesized_expression") {
		value = value.NamedChild(0)
	}
	return value
}

// isConstant reports whether a const's value is a constant: made of
// literals and operators only, and no empty object or array, which is
// state to fill (const cache = {}, or {} as Cache).
func isConstant(value *sitter.Node) bool {
	value = unwrapped(value)
	empty := (value.Kind() == "object" || value.Kind() == "array") && value.NamedChildCount() == 0
	return !empty && literalsOnly(value)
}

// literalsOnly reports whether a value is made of literals and operators
// only (5 * 60 * 1000, { retries: 3 } as const): a value that names
// anything, such as another constant, or calls anything is computed at
// run time, and a function is code.
func literalsOnly(n *sitter.Node) bool {
	switch n.Kind() {
	case "identifier", "shorthand_property_identifier", "this", "super", "meta_property", "template_substitution",
		"call_expression", "new_expression":
		return false
	}
	if functionKinds[n.Kind()] {
		return false
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if !literalsOnly(n.NamedChild(i)) {
			return false
		}
	}
	return true
}

// constant records a module-level constant: a string literal as written,
// any other value without whitespace.
func (p *parser) constant(name, value *sitter.Node) {
	literal := value.Kind() == "string"
	text := value.Utf8Text(p.src)
	if !literal {
		text = syntax.Compact(text)
	}
	p.decls.Consts = append(p.decls.Consts, unit.Const{
		File: p.file, Name: name.Utf8Text(p.src), Value: text, Line: syntax.Line(name.StartPosition()),
		Nodes: syntax.CountNodes(value), Literal: literal, Test: p.test,
	})
}

// structure records an interface or object type as a struct of its
// properties, each with its type as written, an optional one's as
// T|undefined; methods are left out. An interface's bases are a field
// named extends (extends:Base,Other<T>): they are part of its type.
func (p *parser) structure(decl, body *sitter.Node) {
	name := decl.ChildByFieldName("name")
	if name == nil || body == nil {
		return
	}
	s := unit.Struct{
		File: p.file, Name: name.Utf8Text(p.src),
		StartLine: syntax.Line(decl.StartPosition()), EndLine: syntax.Line(decl.EndPosition()),
		Nodes: syntax.CountNodes(decl), Test: p.test,
	}
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		if bases := decl.NamedChild(i); bases.Kind() == "extends_type_clause" {
			s.Fields = append(s.Fields, unit.Field{Name: "extends", Type: typeList(bases, p.src), Line: syntax.Line(bases.StartPosition())})
		}
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		prop := body.NamedChild(i)
		field, typ := prop.ChildByFieldName("name"), prop.ChildByFieldName("type")
		if prop.Kind() != "property_signature" || field == nil || typ == nil || typ.NamedChildCount() == 0 {
			continue
		}
		t := syntax.Compact(typ.NamedChild(0).Utf8Text(p.src))
		if optional(prop) && !strings.HasSuffix(t, "|undefined") {
			t += "|undefined"
		}
		s.Fields = append(s.Fields, unit.Field{Name: field.Utf8Text(p.src), Type: t, Line: syntax.Line(prop.StartPosition())})
	}
	p.decls.Structs = append(p.decls.Structs, s)
}

// typeList writes the types a clause lists, as written, comma-separated.
func typeList(clause *sitter.Node, src []byte) string {
	types := make([]string, clause.NamedChildCount())
	for i := range types {
		types[i] = syntax.Compact(clause.NamedChild(uint(i)).Utf8Text(src))
	}
	return strings.Join(types, ",")
}

// optional reports whether a property is optional (a?: T): its value may
// be undefined.
func optional(prop *sitter.Node) bool {
	for i := uint(0); i < prop.ChildCount(); i++ {
		if prop.Child(i).Kind() == "?" {
			return true
		}
	}
	return false
}

// enum records an enum, a variant's initialiser as its payload ("=10").
func (p *parser) enum(decl *sitter.Node) {
	body := decl.ChildByFieldName("body")
	if body == nil {
		return
	}
	var variants []unit.Variant
	for i := uint(0); i < body.NamedChildCount(); i++ {
		switch v := body.NamedChild(i); v.Kind() {
		case "property_identifier":
			variants = append(variants, unit.Variant{Name: v.Utf8Text(p.src)})
		case "string":
			variants = append(variants, unit.Variant{Name: quoted(v.Utf8Text(p.src))})
		case "enum_assignment":
			name, value := v.ChildByFieldName("name"), v.ChildByFieldName("value")
			if name == nil || value == nil {
				continue
			}
			payload := syntax.Compact(value.Utf8Text(p.src))
			if value.Kind() == "string" {
				payload = quoted(value.Utf8Text(p.src))
			}
			variant := name.Utf8Text(p.src)
			if name.Kind() == "string" {
				variant = quoted(variant)
			}
			variants = append(variants, unit.Variant{Name: variant, Payload: "=" + payload})
		}
	}
	p.addEnum(decl, variants)
}

// union records a union of string literals ("a" | "b") as an enum of
// them; a union of anything else is no enum.
func (p *parser) union(decl, union *sitter.Node) {
	var variants []unit.Variant
	var walk func(n *sitter.Node) bool
	walk = func(n *sitter.Node) bool {
		switch {
		case n.Kind() == "union_type":
			for i := uint(0); i < n.NamedChildCount(); i++ {
				if !walk(n.NamedChild(i)) {
					return false
				}
			}
			return true
		case n.Kind() == "literal_type" && n.NamedChild(0) != nil && n.NamedChild(0).Kind() == "string":
			variants = append(variants, unit.Variant{Name: quoted(n.NamedChild(0).Utf8Text(p.src))})
			return true
		}
		return false
	}
	if walk(union) {
		p.addEnum(decl, variants)
	}
}

// caseStrings returns the string literals a switch's cases compare with,
// in double quotes.
func caseStrings(n *sitter.Node, src []byte) []string {
	var values []string
	body := n.ChildByFieldName("body")
	for i := uint(0); body != nil && i < body.NamedChildCount(); i++ {
		if value := body.NamedChild(i).ChildByFieldName("value"); value != nil && value.Kind() == "string" {
			values = append(values, quoted(value.Utf8Text(src)))
		}
	}
	return values
}

// quoted writes a string literal in double quotes: 'a' and "a" are one
// variant.
func quoted(literal string) string { return `"` + literal[1:len(literal)-1] + `"` }

func (p *parser) addEnum(decl *sitter.Node, variants []unit.Variant) {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	p.decls.Enums = append(p.decls.Enums, unit.Enum{
		File: p.file, Name: name.Utf8Text(p.src),
		StartLine: syntax.Line(decl.StartPosition()), EndLine: syntax.Line(decl.EndPosition()),
		Nodes: syntax.CountNodes(decl), Variants: variants, Test: p.test,
	})
}

// members records a class's methods and fields holding a function, named
// Class.member. A private member is never public.
func (p *parser) members(class *sitter.Node, exported bool) {
	name, body := class.ChildByFieldName("name"), class.ChildByFieldName("body")
	if name == nil || body == nil {
		return
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		m := body.NamedChild(i)
		member, fn := m.ChildByFieldName("name"), m
		if m.Kind() == "public_field_definition" {
			fn = nil
			if value := m.ChildByFieldName("value"); value != nil {
				fn = unwrapped(value)
			}
		}
		if member == nil || member.Kind() == "computed_property_name" || (m.Kind() != "method_definition" && !isFunction(fn)) {
			continue
		}
		u := p.unit(name.Utf8Text(p.src)+"."+member.Utf8Text(p.src), m, fn, exported && !isPrivate(m, member))
		u.Dispatched = true // it may implement an interface, which TypeScript checks by shape
		p.units = append(p.units, u)
	}
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
func (p *parser) unit(name string, n, fn *sitter.Node, public bool) unit.Unit {
	s := syntax.Flatten(n, p.src, grammar{fn: syntax.Span(fn), imports: p.imports})
	ps, _ := params(fn, p.src)
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
		Calls:     s.Calls,
		Forwards:  forwards(fn, p.src),
		Params:    ps,

		StringMatches: s.Matches,
		Public:        public,
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
