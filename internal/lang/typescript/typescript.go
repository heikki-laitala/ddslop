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
	"github.com/heikki-laitala/ddslop/internal/source"
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
	// vars maps the names the function binds (its parameters and
	// variables, and its nested functions' and blocks' while they are
	// open) to the type the code states for them, as Call.Type writes it
	// (see parser.typeOf), or "" for none: a bound name shadows an import.
	vars map[string]string
	// otherThis counts the open nested functions, but arrow functions,
	// that have a this of their own: in them this is no class instance.
	otherThis *int
	p         *parser
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
	case n.Kind() == "variable_declarator":
		// const repo: Repo = make(), const cache = new Cache(); a variable
		// of no stated type, or a destructured one, shadows any earlier one's
		name := n.ChildByFieldName("name")
		if typ := g.p.declaredType(n); name != nil && name.Kind() == "identifier" && typ != "" {
			g.vars[name.Utf8Text(s.Src)] = typ
		} else {
			g.bind(name, s.Src)
		}
	case n.Kind() == "for_in_statement" || n.Kind() == "catch_clause":
		restore := syntax.Scope(g.vars) // the loop's or catch's own variable
		g.bind(n.ChildByFieldName("left"), s.Src)
		g.bind(n.ChildByFieldName("parameter"), s.Src)
		return restore
	case n.Kind() == "statement_block":
		return syntax.Scope(g.vars) // a block's variables are its own
	case n.Kind() == "switch_statement":
		s.RecordStringMatch(n, caseStrings(n, s.Src))
	case n.Kind() == "return_statement":
		if s.Closures == 0 {
			s.Exits++
		}
	case functionKinds[n.Kind()] && syntax.Span(n) != g.fn:
		// a nested function's parameters shadow the outer names, in it
		restore := syntax.Scope(g.vars)
		g.bind(n.ChildByFieldName("parameter"), s.Src)
		eachParam(n, func(p, _, _ *sitter.Node) { g.bind(p.ChildByFieldName("pattern"), s.Src) })
		ownThis := n.Kind() != "arrow_function"
		if ownThis {
			*g.otherThis++
		}
		s.Closures++
		return func() {
			s.Closures--
			if ownThis {
				*g.otherThis--
			}
			restore()
		}
	}
	return nil
}

// bind records the names a binding (a name, or a destructuring pattern)
// introduces, of no stated type: they shadow any earlier variable or
// import of the name in the open scope.
// ddslop:ignore the Rust adapter's forget over TypeScript's node kinds; each changes with its own grammar
func (g grammar) bind(binding *sitter.Node, src []byte) {
	if binding == nil {
		return
	}
	switch binding.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		g.vars[binding.Utf8Text(src)] = ""
	case "assignment_pattern", "object_assignment_pattern":
		g.bind(binding.ChildByFieldName("left"), src) // a default value binds nothing
	case "pair_pattern":
		g.bind(binding.ChildByFieldName("value"), src) // nor does a key
	default:
		for i := uint(0); i < binding.NamedChildCount(); i++ {
			g.bind(binding.NamedChild(i), src)
		}
	}
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
		p.decls.WholeImports = wholeImports(root, src)
		for i := uint(0); i < root.NamedChildCount(); i++ {
			n, exported, byDefault := root.NamedChild(i), false, false
			if d := n.ChildByFieldName("declaration"); n.Kind() == "export_statement" && d != nil {
				// A default class is called from outside, its methods by name.
				n, exported, byDefault = d, true, hasChild(n, "default") && d.Kind() != "class_declaration" && d.Kind() != "abstract_class_declaration"
			}
			if n.HasError() {
				continue
			}
			if source := n.ChildByFieldName("source"); n.Kind() == "import_statement" && source != nil && !typesOnly(n) {
				p.decls.Imports = append(p.decls.Imports, unit.Import{
					Module: strings.Trim(source.Utf8Text(src), `"'`), Line: syntax.Line(n.StartPosition()), Test: p.test,
				})
			}
			from := len(p.units)
			p.declaration(n, exported)
			for j := from; j < len(p.units); j++ {
				p.units[j].External = byDefault
			}
		}
		p.decls.Reach.Optional = map[string]int{}
		p.markExported(root)
		addNoUses(p.decls.Reach.Optional, root, src)
		marked = syntax.MarkIgnored(root, src, p.units, &p.decls)
	})
	return p.units, p.decls, errors.Join(err, marked)
}

// markExported makes public the units and constants an export clause
// (export { load }) or a default export of a name (export default save)
// lists; a clause re-exporting another module's names (export { x } from
// "./y") lists none of the file's. A default export and a renamed one
// (export { load as fetch }) are External: other files know the name by
// another. A clause's words are no use of the names (see unit.Reach).
// A route option a clause exports is no constant, as when exported
// directly (see routeOption).
func (p *parser) markExported(root *sitter.Node) {
	names, external := map[string]bool{}, map[string]bool{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		if n.Kind() != "export_statement" || n.ChildByFieldName("declaration") != nil || n.ChildByFieldName("source") != nil {
			continue
		}
		if v := n.ChildByFieldName("value"); v != nil && v.Kind() == "identifier" {
			name := v.Utf8Text(p.src)
			names[name], external[name] = true, true
		}
		for j := uint(0); j < n.NamedChildCount(); j++ {
			clause := n.NamedChild(j)
			if clause.Kind() != "export_clause" {
				continue
			}
			for k := uint(0); k < clause.NamedChildCount(); k++ {
				spec := clause.NamedChild(k)
				if spec.Kind() != "export_specifier" {
					continue // a comment, whose words addNoUses adds
				}
				source.AddWords(p.decls.Reach.Optional, spec.Utf8Text(p.src))
				if name := spec.ChildByFieldName("name"); name != nil {
					names[name.Utf8Text(p.src)] = true
					if spec.ChildByFieldName("alias") != nil {
						external[name.Utf8Text(p.src)] = true
					}
				}
			}
		}
	}
	for i := range p.units {
		u := &p.units[i]
		u.Public = u.Public || names[u.Name]
		u.External = u.External || external[u.Name] || (u.Public && frameworkExports[u.Name])
	}
	consts := p.decls.Consts[:0]
	for _, c := range p.decls.Consts {
		c.Public = c.Public || names[c.Name]
		if c.Public && p.routeOption(c.Name) {
			continue // the route's config, as an export const of it is
		}
		c.External = c.External || external[c.Name] || (c.Public && frameworkExports[c.Name])
		consts = append(consts, c)
	}
	p.decls.Consts = consts
}

// addNoUses adds the words under n that use no name: those of comments
// and string literals (a template string's text, not its substitutions)
// and the names of signatures (an overload, an interface's method),
// which declare one.
func addNoUses(words map[string]int, n *sitter.Node, src []byte) {
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		switch child.Kind() {
		case "comment", "string", "string_fragment":
			source.AddWords(words, child.Utf8Text(src))
			continue
		case "function_signature", "method_signature", "abstract_method_signature":
			// a computed name uses its words: [KEY]() uses KEY
			if name := child.ChildByFieldName("name"); name != nil && strings.HasSuffix(name.Kind(), "identifier") {
				source.AddWords(words, name.Utf8Text(src))
			}
		}
		addNoUses(words, child, src)
	}
}

// wholeImports returns the modules a file imports whole, in source order:
// by a namespace import (import * as schema from "./schema") or a
// dynamic one (import("./routes")).
func wholeImports(root *sitter.Node, src []byte) []string {
	var out []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch fn, args := n.ChildByFieldName("function"), n.ChildByFieldName("arguments"); {
		case n.Kind() == "import_statement" && n.ChildByFieldName("source") != nil && hasNamespaceImport(n):
			out = append(out, strings.Trim(n.ChildByFieldName("source").Utf8Text(src), `"'`))
		case n.Kind() == "call_expression" && fn != nil && fn.Kind() == "import" && args != nil && args.NamedChildCount() > 0 && args.NamedChild(0).Kind() == "string":
			out = append(out, strings.Trim(args.NamedChild(0).Utf8Text(src), `"'`))
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// typesOnly reports whether an import statement imports types only
// (import type { A }, import { type A, type B }): no value of the module
// is used. Compiling removes the first form; the second, under
// verbatimModuleSyntax, keeps an empty import that loads the module, but
// counts as types only here too.
func typesOnly(n *sitter.Node) bool {
	if hasChild(n, "type") {
		return true
	}
	clause := clauseOf(n)
	if clause == nil || clause.NamedChildCount() != 1 || clause.NamedChild(0).Kind() != "named_imports" {
		return false // import "./x", or a default or namespace import
	}
	names := clause.NamedChild(0)
	specifiers := 0
	for i := uint(0); i < names.NamedChildCount(); i++ {
		if s := names.NamedChild(i); s.Kind() == "import_specifier" {
			if !hasChild(s, "type") {
				return false
			}
			specifiers++
		}
	}
	return specifiers > 0 // a comment is no name
}

// hasNamespaceImport reports whether an import statement imports its
// module whole (import * as schema).
func hasNamespaceImport(n *sitter.Node) bool {
	clause := clauseOf(n)
	return clause != nil && hasChild(clause, "namespace_import")
}

// clauseOf returns an import statement's clause, the names it imports,
// comments before it aside; nil for import "./x".
func clauseOf(n *sitter.Node) *sitter.Node {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Kind() == "import_clause" {
			return c
		}
	}
	return nil
}

// hasChild reports whether a node has a child of a kind, named or not:
// a token (async, default, ?) or a node (accessibility_modifier).
func hasChild(n *sitter.Node, kind string) bool {
	for i := uint(0); i < n.ChildCount(); i++ {
		if n.Child(i).Kind() == kind {
			return true
		}
	}
	return false
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
			if makesObject(n.ChildByFieldName("body")) {
				p.typedAs(n, name.Utf8Text(p.src), n.ChildByFieldName("return_type"))
			}
		}
	case "lexical_declaration", "variable_declaration":
		kind := n.ChildByFieldName("kind")
		constant := kind != nil && kind.Kind() == "const"
		declarators := syntax.CountKind(n, "variable_declarator")
		for i := uint(0); i < n.NamedChildCount(); i++ {
			d := n.NamedChild(i)
			name, value := d.ChildByFieldName("name"), d.ChildByFieldName("value")
			if name != nil && value != nil && unwrapped(value).Kind() == "object" {
				p.typedAs(d, name.Utf8Text(p.src), d.ChildByFieldName("type"))
			}
			switch {
			case d.Kind() != "variable_declarator" || name == nil || name.Kind() != "identifier" || value == nil:
			case isFunction(unwrapped(value)):
				p.units = append(p.units, p.unit(name.Utf8Text(p.src), d, unwrapped(value), exported))
				if fn := unwrapped(value); makesObject(fn.ChildByFieldName("body")) {
					p.typedAs(d, name.Utf8Text(p.src), fn.ChildByFieldName("return_type"))
				}
			case constant && isConstant(value) && (!exported || !p.routeOption(name.Utf8Text(p.src))):
				p.constant(n, name, value, exported, declarators > 1)
			}
		}
	case "class_declaration", "abstract_class_declaration":
		p.members(n, exported)
		p.implementations(n)
	case "interface_declaration":
		p.structure(n, n.ChildByFieldName("body"))
		p.contract(n, exported)
		p.bases(n)
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

// frameworkExports are the names under which Next.js calls a module's
// exported functions: a page's data functions, a route's handlers, the
// app router's metadata and static parameters, middleware (proxy since
// Next.js 16) and instrumentation; and Remix's and SvelteKit's route
// modules' data functions. They also name the constants frameworks read:
// Next.js metadata, viewport, config and image metadata (alt, size,
// contentType), SvelteKit's and Astro's page options (prerender, ssr,
// csr, trailingSlash) and Remix's handle. Unlike routeOptions, they
// count in any file: a function or constant of such a name elsewhere is
// only missed as unused.
var frameworkExports = map[string]bool{
	"metadata": true, "viewport": true, "config": true, "alt": true, "size": true, "contentType": true,
	"prerender": true, "ssr": true, "csr": true, "trailingSlash": true, "handle": true,
	"getStaticProps": true, "getStaticPaths": true, "getServerSideProps": true,
	"generateMetadata": true, "generateStaticParams": true, "generateViewport": true,
	"generateImageMetadata": true, "generateSitemaps": true, "middleware": true, "proxy": true,
	"register": true, "onRequestError": true,
	"loader": true, "action": true, "clientLoader": true, "clientAction": true, "load": true,
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
}

// routeOptions are the names under which a Next.js page, layout, route
// or metadata file (opengraph-image, sitemap and the like, special route
// handlers) exports its route's options.
var routeOptions = map[string]bool{
	"dynamic": true, "dynamicParams": true, "revalidate": true, "fetchCache": true,
	"runtime": true, "preferredRegion": true, "maxDuration": true,
}

// routeOption reports whether a constant the file exports is a Next.js
// route option: a copy the framework asks every route for.
func (p *parser) routeOption(name string) bool {
	base := path.Base(filepath.ToSlash(p.file))
	switch strings.TrimSuffix(base, path.Ext(base)) {
	case "page", "layout", "route", "opengraph-image", "twitter-image", "icon", "apple-icon", "sitemap", "robots", "manifest":
		return routeOptions[name]
	}
	return false
}

// makesObject reports whether a function body returns an object literal,
// as a factory implementing an interface does; one returning an instance
// of a class leaves the class the implementation.
func makesObject(body *sitter.Node) bool {
	if body == nil {
		return false
	}
	if body.Kind() != "statement_block" {
		return unwrapped(body).Kind() == "object" // () => ({ ... })
	}
	return returnsObject(body)
}

// returnsObject reports whether a return in n, outside nested functions,
// returns an object literal.
func returnsObject(n *sitter.Node) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		switch {
		case isFunction(c):
		case c.Kind() == "return_statement":
			if c.NamedChildCount() > 0 && unwrapped(c.NamedChild(0)).Kind() == "object" {
				return true
			}
		case returnsObject(c):
			return true
		}
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
// any other value without whitespace. It is inseparable when its
// declaration declares others too (const a = 1, b = 2); otherwise its
// lines start at the declaration's (export const\n  X = 1).
func (p *parser) constant(decl, name, value *sitter.Node, exported, inseparable bool) {
	literal := value.Kind() == "string"
	text := value.Utf8Text(p.src)
	if !literal {
		text = syntax.Compact(text)
	}
	p.decls.Consts = append(p.decls.Consts, unit.Const{
		File: p.file, Name: name.Utf8Text(p.src), Value: text, Line: syntax.Line(name.StartPosition()), EndLine: syntax.Line(value.EndPosition()),
		Nodes: syntax.CountNodes(value), Literal: literal, Public: exported, Inseparable: inseparable,
		Test: p.test,
	})
	if !inseparable {
		p.decls.Consts[len(p.decls.Consts)-1].Line = syntax.Line(decl.StartPosition())
	}
}

// structure records an interface or object type as a struct of its
// properties, each with its type as written, an optional one's as
// T|undefined; methods are left out. An interface's bases are a field
// named extends (extends:Base,Other<T>): they are part of its type. Each
// property's named type is recorded too, as a class field's is.
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
		p.addFieldType(s.Name, field, p.typeOf(typ.NamedChild(0)))
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
func optional(prop *sitter.Node) bool { return hasChild(prop, "?") }

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

// ddslop:ignore fills unit.Enum as constant fills unit.Const; only a data model change could merge them
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
		p.fieldTypes(name.Utf8Text(p.src), m)
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

// fieldTypes records the type of a class member that is a field: a typed
// field (clock: Clock), one set with new (cache = new Cache()), or a
// constructor's parameter property (constructor(private repo: Repo)).
func (p *parser) fieldTypes(class string, m *sitter.Node) {
	switch name := m.ChildByFieldName("name"); {
	case m.Kind() == "public_field_definition":
		p.addFieldType(class, name, p.declaredType(m))
	case m.Kind() == "method_definition" && name != nil && name.Utf8Text(p.src) == "constructor":
		eachParam(m, func(param, field, typ *sitter.Node) {
			if hasChild(param, "accessibility_modifier") || hasChild(param, "readonly") {
				p.addFieldType(class, field, p.typeOf(typ))
			}
		})
	}
}

// addFieldType records that a class's, interface's or object type's
// field has a type, as Call.Type writes it, when it has one.
func (p *parser) addFieldType(owner string, field *sitter.Node, typ string) {
	if field != nil && typ != "" {
		p.decls.FieldTypes = append(p.decls.FieldTypes, unit.FieldType{Struct: owner, Field: field.Utf8Text(p.src), Type: typ, File: p.file, Test: p.test})
	}
}

// declaredType returns the type a variable or field declaration states:
// its annotation's (x: Repo), or the class its value makes (new Repo()).
func (p *parser) declaredType(n *sitter.Node) string {
	if annotation := n.ChildByFieldName("type"); annotation != nil {
		return p.typeOf(annotation.NamedChild(0))
	}
	if value := n.ChildByFieldName("value"); value != nil && unwrapped(value).Kind() == "new_expression" {
		return p.typeOf(unwrapped(value).ChildByFieldName("constructor"))
	}
	return ""
}

// typeOf returns a named type (Repo, Cache<T>) as Call.Type writes it,
// without type arguments: an imported one qualified by the module it is
// imported from and its exported name (../store/users.UserRepo), a local
// one bare; "" for any other type (string[], A | B).
func (p *parser) typeOf(n *sitter.Node) string {
	if n != nil && n.Kind() == "generic_type" {
		n = n.ChildByFieldName("name")
	}
	if n == nil || (n.Kind() != "type_identifier" && n.Kind() != "identifier") {
		return ""
	}
	name := n.Utf8Text(p.src)
	if im, ok := p.imports[name]; ok && im.name != "" {
		return im.module + "." + im.name
	}
	return name
}

// contract records an interface declaring methods as a trait, the
// contract classes implement; one of properties only is a data shape.
// ddslop:ignore fills unit.Trait as addEnum fills unit.Enum; only a data model change could merge them
func (p *parser) contract(decl *sitter.Node, exported bool) {
	name, body := decl.ChildByFieldName("name"), decl.ChildByFieldName("body")
	if name == nil || body == nil {
		return
	}
	var methods []string
	for i := uint(0); i < body.NamedChildCount(); i++ {
		if m := body.NamedChild(i); m.Kind() == "method_signature" && m.ChildByFieldName("name") != nil {
			methods = append(methods, m.ChildByFieldName("name").Utf8Text(p.src))
		}
	}
	if len(methods) == 0 {
		return
	}
	p.decls.Traits = append(p.decls.Traits, unit.Trait{
		File: p.file, Name: name.Utf8Text(p.src),
		StartLine: syntax.Line(decl.StartPosition()), EndLine: syntax.Line(decl.EndPosition()),
		Nodes: syntax.CountNodes(decl), Public: exported, Test: p.test, Methods: methods,
	})
}

// implementations records a class's name, and each interface it
// implements (class Repo implements IRepo) as an impl of that trait.
func (p *parser) implementations(class *sitter.Node) {
	name := class.ChildByFieldName("name")
	if name == nil {
		return
	}
	p.decls.TypeNames = append(p.decls.TypeNames, name.Utf8Text(p.src))
	for i := uint(0); i < class.NamedChildCount(); i++ {
		if heritage := class.NamedChild(i); heritage.Kind() == "class_heritage" {
			p.implementedIn(class, heritage, "implements_clause", name.Utf8Text(p.src))
		}
	}
}

// typedAs records an object typed as an interface (const mem: IRepo =
// {...}), or a function whose result is typed as one (a factory, (): IRepo
// => ({...})), as implementing it: the interface has an implementation
// besides its classes. annotation is the declared type, if any.
func (p *parser) typedAs(decl *sitter.Node, name string, annotation *sitter.Node) {
	if annotation != nil {
		p.implementation(decl, name, annotation.NamedChild(0))
	}
}

// bases records each interface an interface extends as implemented by
// it: every class implementing it implements those too, so they are no
// single class's.
func (p *parser) bases(decl *sitter.Node) {
	if name := decl.ChildByFieldName("name"); name != nil {
		p.implementedIn(decl, decl, "extends_type_clause", name.Utf8Text(p.src))
	}
}

// implementedIn records the declaration decl, named name, as implementing
// each interface listed by the clauses of a kind among holder's children.
func (p *parser) implementedIn(decl, holder *sitter.Node, clause, name string) {
	for i := uint(0); i < holder.NamedChildCount(); i++ {
		c := holder.NamedChild(i)
		for j := uint(0); c.Kind() == clause && j < c.NamedChildCount(); j++ {
			p.implementation(decl, name, c.NamedChild(j))
		}
	}
}

// implementation records that a class, or an interface extending it,
// implements the interface typ.
func (p *parser) implementation(decl *sitter.Node, name string, typ *sitter.Node) {
	path := p.typeOf(typ)
	if path == "" {
		return
	}
	p.decls.Impls = append(p.decls.Impls, unit.Impl{
		File: p.file, Trait: path[strings.LastIndex(path, ".")+1:], TraitPath: path, Type: name,
		StartLine: syntax.Line(decl.StartPosition()), EndLine: syntax.Line(decl.EndPosition()), Test: p.test,
	})
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
	vars := map[string]string{}
	if param := fn.ChildByFieldName("parameter"); param != nil {
		vars[param.Utf8Text(p.src)] = "" // x => f(x)
	}
	otherThis := 0
	g := grammar{fn: syntax.Span(fn), imports: p.imports, vars: vars, otherThis: &otherThis, p: p}
	eachParam(fn, func(param, name, typ *sitter.Node) {
		if name != nil {
			vars[name.Utf8Text(p.src)] = p.typeOf(typ)
		} else {
			g.bind(param.ChildByFieldName("pattern"), p.src)
		}
	})
	s := syntax.Flatten(n, p.src, g)
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
// __tests__ directory; or a Storybook .stories file, whose exports
// Storybook loads.
func IsTestPath(file string) bool {
	slashed := filepath.ToSlash(file)
	base := path.Base(slashed)
	stem := strings.TrimSuffix(base, path.Ext(base))
	return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") || strings.HasSuffix(stem, ".stories") ||
		slices.Contains(strings.Split(path.Dir(slashed), "/"), "__tests__")
}
