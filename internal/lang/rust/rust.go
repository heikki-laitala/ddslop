// Package rust extracts comparable units from Rust source code.
package rust

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	rustgrammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

var language = sitter.NewLanguage(rustgrammar.Language())

// grammar describes Rust to the shared flattening (see syntax.Grammar).
type grammar struct{}

var rustGrammar grammar

// Prefix is empty for Rust, the first language: its symbols, and so its
// shapes and fingerprints, predate other languages.
func (grammar) Prefix() string { return "" }

func (grammar) Leaf(kind string) (unit.LeafKind, bool) {
	k, ok := leafKinds[kind]
	return k, ok
}

func (grammar) Declares(parent string, parentDeclares bool, field, child string) bool {
	return childDeclares(parent, parentDeclares, field, child)
}

// Binds: in Rust patterns, capitalised names are variants or constants.
func (grammar) Binds(name string) bool { return !isUpper(name) }

func (grammar) Statements(kind string) bool { return kind == "block" }

// Visit records calls, matches on strings and early returns (a return or
// a let-else outside closures).
func (grammar) Visit(s *syntax.Shaper, n *sitter.Node) func() {
	switch n.Kind() {
	case "call_expression":
		recordCall(s, n)
	case "match_expression":
		recordMatch(s, n)
	case "return_expression":
		if s.Closures == 0 {
			s.Exits++
		}
	case "let_declaration":
		if s.Closures == 0 && n.ChildByFieldName("alternative") != nil {
			s.Exits++ // let-else
		}
	case "closure_expression":
		s.Closures++
		return func() { s.Closures-- }
	}
	return nil
}

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

// Units parses Rust source and returns one unit per function or method, in
// source order. Functions containing syntax errors are skipped.
func Units(path string, src []byte) ([]unit.Unit, error) {
	units, _, err := Parse(path, src)
	return units, err
}

// Parse parses Rust source once and returns its units (see Units) and its
// other declarations, in source order. Declarations inside functions are
// not seen.
func Parse(path string, src []byte) ([]unit.Unit, unit.Decls, error) {
	var units []unit.Unit
	var decls unit.Decls
	err := syntax.Parse(language, src, func(root *sitter.Node) {
		e := extractor{path: path, src: src}
		e.walk(root, scope{}, IsTestPath(path), nil)
		units, decls = e.units, e.decls
	})
	return units, decls, err
}

// Traits returns the names of the traits a file declares, in source order.
func Traits(src []byte) []string {
	_, decls, _ := Parse("", src)
	var names []string
	for _, t := range decls.Traits {
		names = append(names, t.Name)
	}
	return names
}

// Imports maps each name a file brings into scope with use to the path it
// refers to: "use a::b::{c, d as e}" gives c -> a::b::c and e -> a::b::d.
// Wildcard imports and imports inside #[cfg(test)] modules are skipped.
func Imports(src []byte) map[string]string {
	out := map[string]string{}
	_ = syntax.Parse(language, src, func(root *sitter.Node) {
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Kind() == "use_declaration" {
				if arg := n.ChildByFieldName("argument"); arg != nil {
					collectUse(arg, "", src, out)
				}
				return
			}
			var attrs []string
			for i := uint(0); i < n.ChildCount(); i++ {
				child := n.Child(i)
				switch {
				case child.Kind() == "attribute_item":
					attrs = append(attrs, child.Utf8Text(src))
					continue
				case child.IsExtra():
					continue
				case child.Kind() == "mod_item" && slices.ContainsFunc(attrs, isCfgTest):
					// test modules' imports are not dependencies of the code
				default:
					walk(child)
				}
				attrs = nil
			}
		}
		walk(root)
	})
	return out
}

// collectUse records the names one use tree imports, prefix being the path
// of the enclosing list.
func collectUse(n *sitter.Node, prefix string, src []byte, out map[string]string) {
	join := func(path string) string {
		if prefix == "" {
			return path
		}
		return prefix + "::" + path
	}
	switch n.Kind() {
	case "identifier", "scoped_identifier", "crate", "super":
		path := join(n.Utf8Text(src))
		out[lastSegment(path)] = path
	case "self":
		if prefix != "" {
			out[lastSegment(prefix)] = prefix // use a::b::{self} imports b
		}
	case "use_as_clause":
		path, alias := n.ChildByFieldName("path"), n.ChildByFieldName("alias")
		if path != nil && alias != nil {
			out[alias.Utf8Text(src)] = join(path.Utf8Text(src))
		}
	case "scoped_use_list":
		inner := prefix
		if path := n.ChildByFieldName("path"); path != nil {
			inner = join(path.Utf8Text(src))
		}
		if list := n.ChildByFieldName("list"); list != nil {
			collectUse(list, inner, src, out)
		}
	case "use_list":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			collectUse(n.NamedChild(i), prefix, src, out)
		}
	}
}

// lastSegment returns the part of a path after its last "::".
func lastSegment(path string) string {
	if i := strings.LastIndex(path, "::"); i >= 0 {
		return path[i+2:]
	}
	return path
}

type extractor struct {
	path  string
	src   []byte
	units []unit.Unit
	decls unit.Decls
}

// walk finds function items, tracking the enclosing impl or trait name and
// whether the code is test code. Attributes are siblings that come just
// before the item they apply to; own are those of n.
// scope is what encloses a function: the type or trait owning it and,
// inside a trait impl, the trait implemented.
type scope struct {
	owner, trait string
}

func (e *extractor) walk(n *sitter.Node, sc scope, test bool, own []string) {
	switch n.Kind() {
	case "function_item":
		e.addFunction(n, sc, test, own)
		return
	case "struct_item":
		e.addTypeName(n)
		e.addStruct(n, test, own)
		return
	case "enum_item":
		e.addTypeName(n)
		e.addEnum(n, test)
		return
	case "union_item", "type_item":
		e.addTypeName(n)
		return
	case "const_item", "static_item":
		if sc.owner == "" {
			e.addConst(n, test)
		}
		return
	case "impl_item":
		if t := n.ChildByFieldName("type"); t != nil {
			sc.owner = t.Utf8Text(e.src)
		}
		sc.trait = ""
		if t := n.ChildByFieldName("trait"); t != nil {
			sc.trait = unit.SimpleName(t.Utf8Text(e.src))
			e.addImpl(n, sc, test)
		}
	case "trait_item":
		if t := n.ChildByFieldName("name"); t != nil {
			sc.owner = t.Utf8Text(e.src)
			e.decls.Traits = append(e.decls.Traits, unit.Trait{
				File: e.path, Name: sc.owner, StartLine: syntax.Line(n.StartPosition()), EndLine: syntax.Line(n.EndPosition()),
				Nodes: syntax.CountNodes(n), Public: isPublic(n, e.src), Test: test,
			})
		}
		sc.trait = ""
	}
	var attrs []string
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child.Kind() == "attribute_item" {
			attrs = append(attrs, child.Utf8Text(e.src))
			continue
		}
		if child.IsExtra() {
			continue // comments between attributes and their item
		}
		childTest := test
		switch child.Kind() {
		case "function_item":
			childTest = test || slices.ContainsFunc(attrs, isTestAttr)
		case "mod_item":
			childTest = test || slices.ContainsFunc(attrs, isCfgTest)
		}
		e.walk(child, sc, childTest, attrs)
		attrs = nil
	}
}

// isTestAttr reports whether an attribute marks a test function, such as
// #[test], #[tokio::test(...)], #[rstest] or #[test_case(...)].
func isTestAttr(attr string) bool {
	name := attrName(attr)
	return name == "test" || name == "rstest" || name == "test_case"
}

// isCfgTest reports whether an attribute limits an item to test builds.
func isCfgTest(attr string) bool {
	compact := strings.Join(strings.Fields(attr), "")
	return compact == "#[cfg(test)]" || strings.HasPrefix(compact, "#[cfg(all(test,")
}

// IsTestPath reports whether a file holds tests by Cargo and Rust
// convention: under a tests/ or benches/ directory, or a tests.rs module.
func IsTestPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, dir := range parts[:len(parts)-1] {
		if dir == "tests" || dir == "benches" {
			return true
		}
	}
	return parts[len(parts)-1] == "tests.rs"
}

func (e *extractor) addFunction(n *sitter.Node, sc scope, test bool, attrs []string) {
	owner := sc.owner
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
	s := syntax.Flatten(n, e.src, rustGrammar)
	e.units = append(e.units, unit.Unit{
		File:      e.path,
		Name:      name,
		StartLine: syntax.Line(n.StartPosition()),
		EndLine:   syntax.Line(n.EndPosition()),
		Shape:     similarity.HashSymbols(s.Seq),
		Nodes:     s.Nodes,
		Test:      test,
		Leaves:    s.Leaves,
		Seq:       s.Seq,
		SeqLines:  s.Lines,
		Blocks:    s.Blocks,
		Trait:     sc.trait,
		Calls:     s.Calls,
		Public:    isPublic(n, e.src),
		External:  (owner == "" && name == "main") || slices.ContainsFunc(attrs, isExternalAttr),
		Forwards:  forwards(n, e.src),
		Scope:     scopeOf(n, e.src),

		StringMatches: s.Matches,
		Params:        params(n, e.src),
	})
}

// params returns a function's named parameters, self and destructuring
// patterns left out.
func params(fn *sitter.Node, src []byte) []unit.Param {
	list := fn.ChildByFieldName("parameters")
	if list == nil {
		return nil
	}
	var out []unit.Param
	for i := uint(0); i < list.NamedChildCount(); i++ {
		p := list.NamedChild(i)
		pat, ty := p.ChildByFieldName("pattern"), p.ChildByFieldName("type")
		if p.Kind() != "parameter" || pat == nil || ty == nil {
			continue
		}
		if pat.Kind() == "mut_pattern" {
			pat = pat.NamedChild(0)
		}
		if pat != nil && pat.Kind() == "identifier" {
			out = append(out, unit.Param{Name: pat.Utf8Text(src), Type: syntax.Compact(ty.Utf8Text(src))})
		}
	}
	return out
}

// closers are the methods that end a value's use: committing or rolling
// back a transaction, closing or releasing a connection, finishing work.
var closers = map[string]bool{
	"commit": true, "rollback": true, "close": true, "release": true,
	"finish": true, "shutdown": true, "abort": true,
}

// scopeEdge is how many statements at each end of a body are searched for
// a scope's opening and closing.
const scopeEdge = 3

// scopeOf finds a value one of a function's first statements binds from a
// call (let mut tx = db.transaction().await?) and one of its last closes
// (tx.commit() or drop(tx)).
func scopeOf(fn *sitter.Node, src []byte) *unit.Scope {
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var stmts []*sitter.Node
	for i := uint(0); i < body.NamedChildCount(); i++ {
		if c := body.NamedChild(i); !c.IsExtra() {
			stmts = append(stmts, c)
		}
	}
	for i := 0; i < len(stmts) && i < scopeEdge; i++ {
		name, opens := binding(stmts[i], src)
		if name == "" {
			continue
		}
		for j := len(stmts) - 1; j > i && j >= len(stmts)-scopeEdge; j-- {
			if closes := closing(stmts[j], name, src); closes != "" {
				return &unit.Scope{
					Opens: opens, Closes: closes,
					StartLine: syntax.Line(stmts[i].StartPosition()), EndLine: syntax.Line(stmts[j].EndPosition()),
					Nodes: syntax.CountNodes(stmts[i]) + syntax.CountNodes(stmts[j]),
				}
			}
		}
	}
	return nil
}

// binding returns the name a let statement binds and the name of the
// function or method whose call produced the value.
func binding(stmt *sitter.Node, src []byte) (name, opens string) {
	if stmt.Kind() != "let_declaration" {
		return "", ""
	}
	pat, value := stmt.ChildByFieldName("pattern"), stmt.ChildByFieldName("value")
	if pat != nil && pat.Kind() == "mut_pattern" {
		pat = pat.NamedChild(0)
	}
	for value != nil && (value.Kind() == "try_expression" || value.Kind() == "await_expression") {
		value = value.NamedChild(0)
	}
	if pat == nil || pat.Kind() != "identifier" || value == nil || value.Kind() != "call_expression" {
		return "", ""
	}
	callee := value.ChildByFieldName("function")
	switch {
	case callee == nil:
		return "", ""
	case callee.Kind() == "field_expression":
		if f := callee.ChildByFieldName("field"); f != nil {
			return pat.Utf8Text(src), f.Utf8Text(src)
		}
	case callee.Kind() == "identifier" || callee.Kind() == "scoped_identifier":
		return pat.Utf8Text(src), lastSegment(callee.Utf8Text(src))
	}
	return "", ""
}

// closing returns the closing method a statement calls on name, or "drop"
// for drop(name), or "".
func closing(stmt *sitter.Node, name string, src []byte) string {
	found := ""
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != "" {
			return
		}
		if n.Kind() == "call_expression" {
			callee, args := n.ChildByFieldName("function"), n.ChildByFieldName("arguments")
			switch {
			case callee != nil && callee.Kind() == "field_expression":
				v, f := callee.ChildByFieldName("value"), callee.ChildByFieldName("field")
				if v != nil && f != nil && v.Utf8Text(src) == name && closers[f.Utf8Text(src)] {
					found = f.Utf8Text(src)
					return
				}
			case callee != nil && callee.Utf8Text(src) == "drop" && args != nil && args.NamedChildCount() == 1 &&
				args.NamedChild(0).Utf8Text(src) == name:
				found = "drop"
				return
			}
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(stmt)
	return found
}

// forwards returns the callee when a function's body is one call (possibly
// awaited) passing the function's parameters on in order, each as is or
// borrowed: fn f(a, b) { g(a, &b) }. Constructors (Self(x), Some(x)) and
// calls on fields (self.inner.get(x)) do not count, and nor do functions
// without parameters besides self.
func forwards(fn *sitter.Node, src []byte) string {
	params, body := fn.ChildByFieldName("parameters"), fn.ChildByFieldName("body")
	if params == nil || body == nil || body.NamedChildCount() != 1 {
		return ""
	}
	var names []string
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		switch p.Kind() {
		case "self_parameter", "line_comment", "block_comment":
			continue
		case "parameter":
			if pat := p.ChildByFieldName("pattern"); pat != nil && pat.Kind() == "identifier" {
				names = append(names, pat.Utf8Text(src))
				continue
			}
		}
		return "" // patterns, variadics: not a plain forward
	}
	expr := body.NamedChild(0)
	for expr.Kind() == "expression_statement" || expr.Kind() == "await_expression" {
		expr = expr.NamedChild(0)
	}
	if len(names) == 0 || expr.Kind() != "call_expression" {
		return ""
	}
	callee, args := expr.ChildByFieldName("function"), expr.ChildByFieldName("arguments")
	if callee == nil || !syntax.PassesOn(args, names, src, borrowed) {
		return ""
	}
	switch callee.Kind() {
	case "identifier", "scoped_identifier":
		if isUpper(lastSegment(callee.Utf8Text(src))) {
			return "" // a constructor
		}
	case "field_expression":
		if v := callee.ChildByFieldName("value"); v == nil || v.Kind() != "self" {
			return "" // delegation to a field
		}
	default:
		return ""
	}
	return callee.Utf8Text(src)
}

// borrowed returns x for an argument &x or &mut x, else nil.
func borrowed(n *sitter.Node, _ []byte) *sitter.Node {
	if n.Kind() == "reference_expression" {
		return n.ChildByFieldName("value")
	}
	return nil
}

// isPublic reports whether an item is declared plain pub, visible outside
// its crate; pub(crate) and narrower are not.
func isPublic(n *sitter.Node, src []byte) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); c.Kind() == "visibility_modifier" {
			return c.Utf8Text(src) == "pub"
		}
	}
	return false
}

// internalAttrs are attributes that never make a function callable from
// outside the source: lints, inlining, documentation, conditional
// compilation and tracing.
var internalAttrs = map[string]bool{
	"allow": true, "expect": true, "warn": true, "deny": true, "forbid": true,
	"inline": true, "must_use": true, "cold": true, "track_caller": true,
	"doc": true, "deprecated": true, "cfg": true, "cfg_attr": true,
	"instrument": true, "skip": true, // tracing::instrument, rustfmt::skip
}

// isExternalAttr reports whether an attribute may make a function called
// from outside the source (#[no_mangle], #[tokio::main], route macros):
// anything but test attributes and internalAttrs.
func isExternalAttr(attr string) bool {
	return !isTestAttr(attr) && !internalAttrs[attrName(attr)]
}

// attrName returns the last segment of an attribute's path: "main" for
// #[tokio::main(flavor = "current_thread")].
func attrName(attr string) string {
	path := strings.TrimPrefix(attr, "#[")
	if i := strings.IndexAny(path, "(]="); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSpace(path)
	if i := strings.LastIndex(path, "::"); i >= 0 {
		path = path[i+2:]
	}
	return path
}

// addTypeName records the name of a declared type.
func (e *extractor) addTypeName(n *sitter.Node) {
	if name := n.ChildByFieldName("name"); name != nil {
		e.decls.TypeNames = append(e.decls.TypeNames, name.Utf8Text(e.src))
	}
}

// addEnum records an enum and its variants.
func (e *extractor) addEnum(n *sitter.Node, test bool) {
	name, body := n.ChildByFieldName("name"), n.ChildByFieldName("body")
	if name == nil || body == nil || n.HasError() {
		return
	}
	en := unit.Enum{
		File: e.path, Name: name.Utf8Text(e.src),
		StartLine: syntax.Line(n.StartPosition()), EndLine: syntax.Line(n.EndPosition()),
		Nodes: syntax.CountNodes(n), Test: test,
	}
	eachMember(body, "enum_variant", func(v, vname *sitter.Node) {
		variant := unit.Variant{Name: vname.Utf8Text(e.src)}
		if p := v.ChildByFieldName("body"); p != nil {
			variant.Payload = syntax.Compact(p.Utf8Text(e.src))
		}
		en.Variants = append(en.Variants, variant)
	})
	e.decls.Enums = append(e.decls.Enums, en)
}

// eachMember calls each with the named children of body of a kind (enum
// variants, struct fields) that have a name, and that name.
func eachMember(body *sitter.Node, kind string, each func(member, name *sitter.Node)) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		m := body.NamedChild(i)
		if name := m.ChildByFieldName("name"); m.Kind() == kind && name != nil {
			each(m, name)
		}
	}
}

// addImpl records a trait impl; sc holds the trait implemented.
func (e *extractor) addImpl(n *sitter.Node, sc scope, test bool) {
	tn := n.ChildByFieldName("type")
	for tn != nil && tn.Kind() == "reference_type" {
		tn = tn.ChildByFieldName("type") // &'a mut Buffer<'a>
	}
	if tn == nil {
		return
	}
	ty := unit.SimpleName(tn.Utf8Text(e.src))
	blanket := false
	if params := n.ChildByFieldName("type_parameters"); params != nil {
		for i := uint(0); i < params.NamedChildCount(); i++ {
			if name := params.NamedChild(i).ChildByFieldName("name"); name != nil && name.Utf8Text(e.src) == ty {
				blanket = true
			}
		}
	}
	e.decls.Impls = append(e.decls.Impls, unit.Impl{
		File: e.path, Trait: sc.trait, Type: ty,
		StartLine: syntax.Line(n.StartPosition()), EndLine: syntax.Line(n.EndPosition()),
		Blanket: blanket, Test: test,
	})
}

// addStruct records a struct with named fields; unit and tuple structs are
// left out.
func (e *extractor) addStruct(n *sitter.Node, test bool, attrs []string) {
	name, body := n.ChildByFieldName("name"), n.ChildByFieldName("body")
	if name == nil || body == nil || body.Kind() != "field_declaration_list" || n.HasError() {
		return
	}
	ty := unit.Struct{
		File:      e.path,
		Name:      name.Utf8Text(e.src),
		StartLine: syntax.Line(n.StartPosition()),
		EndLine:   syntax.Line(n.EndPosition()),
		Nodes:     syntax.CountNodes(n),
		Test:      test,
		Converts:  serdeConversions(attrs),
	}
	eachMember(body, "field_declaration", func(f, fname *sitter.Node) {
		if ftype := f.ChildByFieldName("type"); ftype != nil {
			ty.Fields = append(ty.Fields, unit.Field{
				Name: fname.Utf8Text(e.src),
				Type: syntax.Compact(ftype.Utf8Text(e.src)),
				Line: syntax.Line(f.StartPosition()),
			})
		}
	})
	e.decls.Structs = append(e.decls.Structs, ty)
}

// addConst records a constant or static item.
func (e *extractor) addConst(n *sitter.Node, test bool) {
	name, value := n.ChildByFieldName("name"), n.ChildByFieldName("value")
	if name == nil || value == nil || n.HasError() {
		return
	}
	literal := value.Kind() == "string_literal"
	text := value.Utf8Text(e.src)
	if !literal {
		text = syntax.Compact(text)
	}
	e.decls.Consts = append(e.decls.Consts, unit.Const{
		File:    e.path,
		Name:    name.Utf8Text(e.src),
		Value:   text,
		Line:    syntax.Line(n.StartPosition()),
		Nodes:   syntax.CountNodes(value),
		Literal: literal,
		Macro:   value.Kind() == "macro_invocation",
		Test:    test,
	})
}

// serdeConversion matches a serde conversion setting inside an attribute.
var serdeConversion = regexp.MustCompile(`\b(?:try_from|from|into)\s*=\s*"([^"]+)"`)

// serdeConversions returns the types serde attributes convert a type from
// or into, as simple names.
func serdeConversions(attrs []string) []string {
	var names []string
	for _, a := range attrs {
		if attrName(a) != "serde" {
			continue
		}
		for _, m := range serdeConversion.FindAllStringSubmatch(a, -1) {
			names = append(names, unit.SimpleName(m[1]))
		}
	}
	return names
}

// recordCall notes a call expression by the shape of its function.
func recordCall(s *syntax.Shaper, n *sitter.Node) {
	fn := n.ChildByFieldName("function")
	for fn != nil && fn.Kind() == "generic_function" {
		fn = fn.ChildByFieldName("function") // turbofish: f::<T>(...)
	}
	if fn == nil {
		return
	}
	c := unit.Call{Line: syntax.Line(n.StartPosition())}
	switch fn.Kind() {
	case "identifier":
		c.Kind, c.Name = unit.FreeCall, fn.Utf8Text(s.Src)
	case "scoped_identifier":
		c.Kind = unit.PathCall
		if name := fn.ChildByFieldName("name"); name != nil {
			c.Name = name.Utf8Text(s.Src)
		}
		if path := fn.ChildByFieldName("path"); path != nil {
			c.Qualifier = path.Utf8Text(s.Src)
		}
	case "field_expression":
		c.Kind = unit.MethodCall
		if field := fn.ChildByFieldName("field"); field != nil {
			c.Name = field.Utf8Text(s.Src)
		}
		if value := fn.ChildByFieldName("value"); value != nil && value.Kind() == "self" {
			c.Qualifier = "self"
		}
	default:
		return // closures and other callees are not resolved by name
	}
	s.Calls = append(s.Calls, c)
}

// recordMatch notes a match whose arm patterns hold at least
// unit.MinStringArms string literals, alone or in or-patterns. Guards are
// not patterns.
func recordMatch(s *syntax.Shaper, n *sitter.Node) {
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	var values []string
	var collect func(p *sitter.Node)
	collect = func(p *sitter.Node) {
		switch p.Kind() {
		case "string_literal", "raw_string_literal":
			values = append(values, p.Utf8Text(s.Src))
		case "or_pattern":
			for i := uint(0); i < p.NamedChildCount(); i++ {
				collect(p.NamedChild(i))
			}
		}
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		arm := body.NamedChild(i)
		if arm.Kind() != "match_arm" {
			continue
		}
		if pat := arm.ChildByFieldName("pattern"); pat != nil && pat.NamedChildCount() > 0 {
			collect(pat.NamedChild(0)) // the pattern, before any guard
		}
	}
	slices.Sort(values)
	values = slices.Compact(values)
	if len(values) >= unit.MinStringArms {
		s.Matches = append(s.Matches, unit.StringMatch{Values: values, StartLine: syntax.Line(n.StartPosition()), EndLine: syntax.Line(n.EndPosition())})
	}
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

// CrateName returns the name Rust code uses for a crate, from its
// Cargo.toml: the [lib] name if set, otherwise the [package] name, with
// dashes as underscores. It returns "" when there is no package.
func CrateName(cargoToml []byte) string {
	section := ""
	names := map[string]string{} // section -> name
	for line := range strings.Lines(string(cargoToml)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "name" || (section != "package" && section != "lib") {
			continue
		}
		names[section] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	name := names["lib"]
	if name == "" {
		name = names["package"]
	}
	return strings.ReplaceAll(name, "-", "_")
}

// TestModules returns the names of the modules a file declares out of
// line under #[cfg(test)] ("#[cfg(test)] mod name;"): their files hold
// test code.
//
// Only declarations at the top level of the file are considered.
func TestModules(src []byte) []string {
	var names []string
	_ = syntax.Parse(language, src, func(root *sitter.Node) {
		var attrs []string
		for i := uint(0); i < root.ChildCount(); i++ {
			child := root.Child(i)
			switch {
			case child.Kind() == "attribute_item":
				attrs = append(attrs, child.Utf8Text(src))
				continue
			case child.IsExtra():
				continue
			case child.Kind() == "mod_item" && child.ChildByFieldName("body") == nil && slices.ContainsFunc(attrs, isCfgTest):
				if name := child.ChildByFieldName("name"); name != nil {
					names = append(names, name.Utf8Text(src))
				}
			}
			attrs = nil
		}
	})
	return names
}

// Unpublished reports whether a crate is kept out of registries, so its
// public items are used only inside the workspace: its manifest says
// publish = false (or []), directly or through publish.workspace = true
// and the workspace manifest's [workspace.package].
func Unpublished(manifest, workspace []byte) bool {
	switch publishValue(manifest, "package") {
	case "false", "[]":
		return true
	case "workspace":
		v := publishValue(workspace, "workspace.package")
		return v == "false" || v == "[]"
	}
	return false
}

// publishValue returns the publish setting of a manifest section, without
// spaces, or "workspace" when the setting is inherited from the workspace.
func publishValue(manifest []byte, want string) string {
	section := ""
	for line := range strings.Lines(string(manifest)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != want {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Join(strings.Fields(value), "")
		switch {
		case key == "publish.workspace" && value == "true", key == "publish" && value == "{workspace=true}":
			return "workspace"
		case key == "publish":
			return value
		}
	}
	return ""
}
