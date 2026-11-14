// Package rust extracts comparable units from Rust source code.
package rust

import (
	"errors"
	"maps"
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

// grammar describes Rust to the shared flattening (see syntax.Grammar),
// for one function: the type its impl is for (Self), the type parameters
// in scope, and the variables whose type the code states, by name, as
// their declarations are visited.
type grammar struct {
	self    string
	generic map[string]string
	imports map[string]string
	vars    map[string]syntax.Operand
	// entries holds the variables as each open if found them, by the if's
	// span, for its else.
	entries *map[[2]uint]map[string]syntax.Operand
	// calls maps a call's syntax node (its byte range) to its index among
	// the unit's calls, and chains a method call's index to the call its
	// operand is, recorded after it (Store::open()?.get()).
	calls  map[[2]uint]int
	chains map[int]produced
}

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
func (g grammar) Visit(s *syntax.Shaper, n *sitter.Node) func() {
	if form := errorForm(n, s.Src); form.Form != "" {
		s.Forms = append(s.Forms, form)
	}
	switch n.Kind() {
	case "call_expression":
		g.recordCall(s, n)
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
		// The binding takes effect after its value: let x = x.parse()
		// calls parse on the old x.
		return func() { g.let(n, s.Src) }
	case "closure_expression":
		s.Closures++
		restore := g.scope()
		if params := n.ChildByFieldName("parameters"); params != nil {
			g.forget(params, s.Src)
		}
		return func() { s.Closures--; restore() }
	case "match_arm":
		restore := g.scope()
		if pattern := n.ChildByFieldName("pattern"); pattern != nil {
			var value *sitter.Node
			if block := n.Parent(); block != nil && block.Parent() != nil {
				value = block.Parent().ChildByFieldName("value") // match value { arm }
			}
			g.bind(pattern, value, s.Src)
		}
		return restore
	case "block":
		// A for's pattern is in scope in its body alone, after the
		// iterator. A block's use declarations hold throughout it.
		restore := g.scope()
		if p := n.Parent(); p != nil && p.Kind() == "for_expression" && sameNode(p.ChildByFieldName("body"), n) {
			if pattern := p.ChildByFieldName("pattern"); pattern != nil {
				g.forget(pattern, s.Src)
			}
		}
		local := usesIn(n, s.Src)
		if len(local) == 0 {
			return restore
		}
		outer := maps.Clone(g.imports)
		maps.Copy(g.imports, local)
		return func() {
			restore()
			clear(g.imports)
			maps.Copy(g.imports, outer)
		}
	case "if_expression", "while_expression":
		// An if let's or while let's bindings hold from right after their
		// value, through the chain's later operands and the body, until
		// the if or while ends; an else starts from the variables as the
		// if found them.
		(*g.entries)[span(n)] = maps.Clone(g.vars)
		restore := g.scope()
		return func() { delete(*g.entries, span(n)); restore() }
	case "let_condition":
		return func() {
			if pattern := n.ChildByFieldName("pattern"); pattern != nil {
				g.bind(pattern, n.ChildByFieldName("value"), s.Src)
			}
		}
	case "else_clause":
		if entry, ok := (*g.entries)[span(n.Parent())]; ok && n.Parent() != nil {
			clear(g.vars)
			maps.Copy(g.vars, entry)
		}
	}
	return nil
}

// span identifies a syntax node by its byte range.
func span(n *sitter.Node) [2]uint {
	if n == nil {
		return [2]uint{}
	}
	return [2]uint{n.StartByte(), n.EndByte()}
}

// sameNode reports whether a and b are the same syntax node.
func sameNode(a, b *sitter.Node) bool {
	return a != nil && b != nil && span(a) == span(b)
}

// scope opens a lexical scope: the bindings made in it are undone by the
// function it returns. Rust assignments keep a variable's type, so the
// variables as they were are exactly what holds after it.
func (g grammar) scope() func() {
	outer := maps.Clone(g.vars)
	return func() {
		clear(g.vars)
		maps.Copy(g.vars, outer)
	}
}

// produced is a call's result: the call's syntax node and the result's
// position (see unit.Unit.Results).
type produced struct {
	call   [2]uint
	result int
}

// let records the variable a let declares: its type when stated (let c:
// Cache) or built by a struct literal (Ledger { .. }), or the call whose
// result it holds (let s = Store::new()). Any other binding shadows the
// name with a value of unknown type.
func (g grammar) let(n *sitter.Node, src []byte) {
	pattern := n.ChildByFieldName("pattern")
	if pattern == nil {
		return
	}
	if pattern.Kind() != "identifier" {
		g.bind(pattern, n.ChildByFieldName("value"), src) // let Some(x) = find() else { .. }
		return
	}
	var op syntax.Operand
	if t := n.ChildByFieldName("type"); t != nil {
		op.Type = g.typeOf(t.Utf8Text(src))
	} else if v := n.ChildByFieldName("value"); v != nil {
		op = g.valueOf(v, src)
	}
	g.set(pattern.Utf8Text(src), op)
}

// valueOf returns what a value states: a struct literal's type, or the
// call it is, looking through .await and ? (which takes result 1).
func (g grammar) valueOf(v *sitter.Node, src []byte) syntax.Operand {
	if v.Kind() == "struct_expression" {
		if name := v.ChildByFieldName("name"); name != nil {
			return syntax.Operand{Type: g.typeOf(name.Utf8Text(src))}
		}
		return syntax.Operand{}
	}
	if p, ok := callResult(v); ok {
		if k, ok := g.calls[p.call]; ok {
			return syntax.Operand{From: k + 1, Result: p.result}
		}
	}
	return syntax.Operand{}
}

// callResult returns the call a value is, through .await and ?, and the
// result position (see unit.Unit.Results): 1 more after ?, which takes
// the value out of a Result or Option, and 2 more after .await. A result
// adapter (map_err, unwrap) is a call like any other here, marked as one
// (unit.Call.Adapts) for the call graph to look through.
func callResult(v *sitter.Node) (produced, bool) {
	result := 0
	for v != nil {
		switch v.Kind() {
		case "try_expression":
			result |= 1
			v = v.NamedChild(0)
		case "await_expression":
			result |= 2
			v = v.NamedChild(0)
		case "parenthesized_expression":
			v = v.NamedChild(0)
		case "call_expression":
			return produced{call: span(v), result: result}, true
		default:
			return produced{}, false
		}
	}
	return produced{}, false
}

// bind records the variables a pattern binds from a value: Some(x) or
// Ok(x) binds what a ? leaves of a call's result, or of a variable
// holding one (if let Some(x) = find()); any other binding is unknown.
// A value unwrapped already (find()?) has no position for unwrapping it
// again.
func (g grammar) bind(pattern, value *sitter.Node, src []byte) {
	// The value is read where the pattern is not yet bound:
	// if let Some(value) = value.
	var op syntax.Operand
	if value != nil {
		op = g.valueOf(value, src)
		if value.Kind() == "identifier" {
			op = g.vars[value.Utf8Text(src)]
		}
	}
	g.forget(pattern, src)
	if pattern.Kind() == "match_pattern" && pattern.NamedChildCount() > 0 {
		pattern = pattern.NamedChild(0) // an arm's, before any guard
	}
	if pattern.Kind() != "tuple_struct_pattern" || pattern.NamedChildCount() != 2 || value == nil {
		return
	}
	variant, inner := pattern.ChildByFieldName("type"), pattern.NamedChild(1)
	// An imported variant of the name (use crate::MyValue::Some) is
	// another enum's.
	if variant == nil || inner.Kind() != "identifier" || !unwrapsTo[variant.Utf8Text(src)] || g.imports[variant.Utf8Text(src)] != "" {
		return
	}
	if op.From > 0 && op.Result&1 == 0 {
		g.set(inner.Utf8Text(src), syntax.Operand{From: op.From, Result: op.Result | 1})
	}
}

// unwrapsTo are the variants a ? takes the value out of.
var unwrapsTo = map[string]bool{"Some": true, "Ok": true}

// set records what a variable holds, or forgets it when unknown.
func (g grammar) set(name string, op syntax.Operand) {
	if op == (syntax.Operand{}) {
		delete(g.vars, name)
		return
	}
	g.vars[name] = op
}

// forget drops the variables a pattern binds: lower-case names, which
// are bindings rather than variants or constants (Some(x), Point { y }).
func (g grammar) forget(pattern *sitter.Node, src []byte) {
	switch pattern.Kind() {
	case "identifier", "shorthand_field_identifier":
		if name := pattern.Utf8Text(src); !isUpper(name) {
			delete(g.vars, name)
		}
		return
	case "scoped_identifier", "scoped_type_identifier":
		return // a path, such as Kind::Small, binds nothing
	}
	for i := uint(0); i < pattern.NamedChildCount(); i++ {
		g.forget(pattern.NamedChild(i), src)
	}
}

// results returns a function's result types (see unit.Unit.Results): the
// type as returned, and the type a ? leaves of it, inside a Result or an
// Option (the returned type itself otherwise). An async fn returns a
// future, of no type here, and declares the types its .await and .await?
// give, at positions 2 and 3. No return type gives none.
func (g grammar) results(fn *sitter.Node, src []byte) []string {
	ret := fn.ChildByFieldName("return_type")
	if ret == nil {
		return nil
	}
	if isAsync(fn, src) {
		return append([]string{"", ""}, g.returned(ret, src)...)
	}
	return g.returned(ret, src)
}

// isAsync reports whether a function is an async fn.
func isAsync(fn *sitter.Node, src []byte) bool {
	for i := uint(0); i < fn.NamedChildCount(); i++ {
		if c := fn.NamedChild(i); c.Kind() == "function_modifiers" && strings.Contains(c.Utf8Text(src), "async") {
			return true
		}
	}
	return false
}

// returned returns a return type and what a ? leaves of it.
func (g grammar) returned(ret *sitter.Node, src []byte) []string {
	written := strings.TrimSpace(ret.Utf8Text(src))
	return []string{g.typeOf(written), unwrapped(written, g.typeOf)}
}

// unwrapped returns the type ? or unwrap() leaves of a value of the
// written type, as typeOf writes it: the type inside a Result or an
// Option, the type itself otherwise.
func unwrapped(written string, typeOf func(string) string) string {
	written = strings.TrimSpace(written)
	switch unit.SimpleName(written) {
	case "Result", "Option":
		if open := strings.Index(written, "<"); open >= 0 && strings.HasSuffix(written, ">") {
			return typeOf(firstTypeArgument(written[open+1 : len(written)-1]))
		}
		return ""
	}
	return typeOf(written)
}

// typeOf returns the type a method call on a value of the written type
// reaches, as a simple name (see rustType).
func (g grammar) typeOf(written string) string {
	return rustType(written, g.self, g.generic, g.imports)
}

// wrappers are the smart pointers a method call sees through: x.m() on
// an Arc<T> calls T's m.
var wrappers = map[string]bool{"Box": true, "Arc": true, "Rc": true}

// lifetime matches a lifetime at the start of a reference's type.
var lifetime = regexp.MustCompile(`^'\w+\s*`)

// rustType returns the path of the type whose methods a value of the
// written type has, without type arguments, or "": references and mut
// are stripped, Box, Arc and Rc looked through, Self is the impl's type
// (self), a name the file imports is its import path (Client from use
// reqwest::Client is reqwest::Client), a trait object (dyn, impl) or type
// parameter (generic) is its trait's path, when one trait gives its
// methods (see traitOf), and tuples, slices, function pointers and
// lower-case (primitive) types give "".
func rustType(written, self string, generic map[string]string, imports map[string]string) string {
	t := strings.TrimSpace(written)
	for {
		switch {
		case strings.HasPrefix(t, "&"):
			t = strings.TrimSpace(t[1:])
		case strings.HasPrefix(t, "'"):
			t = lifetime.ReplaceAllString(t, "")
		case strings.HasPrefix(t, "mut "):
			t = strings.TrimSpace(t[4:])
		default:
			name := unit.SimpleName(t)
			open := strings.Index(t, "<")
			if !wrappers[name] || open < 0 || !strings.HasSuffix(t, ">") {
				return typePath(t, self, generic, imports)
			}
			t = strings.TrimSpace(firstTypeArgument(t[open+1 : len(t)-1]))
		}
	}
}

// typePath returns a type's path as rustType writes it.
func typePath(t, self string, generic map[string]string, imports map[string]string) string {
	for _, trait := range []string{"dyn ", "impl "} {
		if rest, ok := strings.CutPrefix(t, trait); ok {
			return traitPath(traitOf(rest), self, imports)
		}
	}
	for _, other := range []string{"(", "[", "fn", "*"} {
		if strings.HasPrefix(t, other) {
			return ""
		}
	}
	if i := strings.Index(t, "<"); i >= 0 {
		t = t[:i]
	}
	if strings.Contains(t, "(") {
		return "" // Fn(u8) -> u8
	}
	path := strings.ReplaceAll(t, " ", "")
	if bound, ok := generic[path]; ok {
		return traitPath(bound, self, imports)
	}
	switch name := unit.SimpleName(path); {
	case path == "Self":
		return self
	case name == "" || !isUpper(name):
		return ""
	case !strings.Contains(path, "::") && imports[path] != "":
		return imports[path]
	}
	// An imported module at the root: m::Client after use crate::model as m.
	if root, rest, ok := strings.Cut(path, "::"); ok && imports[root] != "" {
		return imports[root] + "::" + rest
	}
	return path
}

// traitPath returns a trait's path as rustType writes a trait type:
// marked "dyn " to tell it from a concrete type of the name.
func traitPath(trait, self string, imports map[string]string) string {
	if path := typePath(trait, self, nil, imports); path != "" {
		return "dyn " + path
	}
	return ""
}

// markers are the standard traits a bound adds without giving the value
// methods code calls on it.
var markers = map[string]bool{"Send": true, "Sync": true, "Sized": true, "Unpin": true, "Copy": true, "Clone": true,
	"Debug": true, "Default": true, "Display": true, "Eq": true, "PartialEq": true, "Hash": true, "Ord": true, "PartialOrd": true}

// traitOf returns the trait among bounds (Store + Send + 'static) whose
// methods a value of them has: the only one that is no marker, lifetime
// or ?Sized, or "" when none is or several are.
func traitOf(bounds string) string {
	trait := ""
	for _, b := range splitTopLevel(bounds, '+') {
		b = strings.TrimSpace(b)
		name := b
		if i := strings.Index(name, "<"); i >= 0 {
			name = name[:i]
		}
		if b == "" || strings.HasPrefix(b, "'") || strings.HasPrefix(b, "?") || markers[unit.SimpleName(name)] {
			continue
		}
		if trait != "" {
			return ""
		}
		trait = b
	}
	return trait
}

// splitTopLevel splits a list at each sep outside brackets.
func splitTopLevel(list string, sep rune) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range list {
		switch r {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 && (r != '>' || i == 0 || list[i-1] != '-') { // not the -> of Fn() -> T
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, list[start:i])
				start = i + 1
			}
		}
	}
	return append(out, list[start:])
}

// firstTypeArgument returns the first of comma-separated type arguments,
// leaving commas inside nested brackets alone.
func firstTypeArgument(args string) string {
	depth := 0
	for i, r := range args {
		switch r {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				return args[:i]
			}
		}
	}
	return args
}

// IdiomaticForms names the idiomatic form of each concern the adapter
// knows, which the map suggests where a codebase has no convention. Error
// wrapping and error values have none: map_err to a typed error and
// anyhow's context, and errors made from variants and with anyhow, are
// both idiomatic, for libraries and applications.
var IdiomaticForms = map[string]string{unit.ErrorChecks: "variant"}

// errorForm returns the error form a node is, if any: a pattern
// Err(Path::Variant ..), or one inside matches!, checks an error's
// variant; comparing an error's to_string() (==, contains, starts_with,
// ends_with) checks its message; map_err converts an error, and anyhow's
// context (given a message) or with_context (given a closure) wraps one.
// All are recognised from syntax alone, so ? with a From conversion,
// which looks like any other ?, is no form.
func errorForm(n *sitter.Node, src []byte) unit.Form {
	form := unit.Form{Line: syntax.Line(n.StartPosition())}
	switch n.Kind() {
	case "tuple_struct_pattern":
		typ := n.ChildByFieldName("type")
		if typ != nil && lastSegment(typ.Utf8Text(src)) == "Err" && n.NamedChildCount() > 1 && isVariantPath(n.NamedChild(1).Utf8Text(src)) {
			form.Concern, form.Form = unit.ErrorChecks, "variant"
		}
	case "macro_invocation":
		m := n.ChildByFieldName("macro")
		if m == nil {
			break
		}
		switch lastSegment(m.Utf8Text(src)) {
		case "bail", "anyhow":
			form.Concern, form.Form = unit.ErrorValues, "anyhow"
		case "matches":
			// A macro's arguments are an opaque token tree: read matches!'s
			// pattern, after the value, from its tokens.
			pattern, guard := matchesPattern(n, src)
			if p := errPattern.FindStringSubmatch(pattern); p != nil && isVariantPath(strings.ReplaceAll(p[1], " ", "")) {
				form.Concern, form.Form = unit.ErrorChecks, "variant"
			} else if messageCheck.MatchString(guard) {
				form.Concern, form.Form = unit.ErrorChecks, "message"
			}
		}
	case "binary_expression":
		op := n.ChildByFieldName("operator")
		left, right := n.ChildByFieldName("left"), n.ChildByFieldName("right")
		if op != nil && (op.Kind() == "==" || op.Kind() == "!=") && left != nil && right != nil &&
			(isErrorMessage(left, src) || isErrorMessage(right, src)) {
			form.Concern, form.Form = unit.ErrorChecks, "message"
		}
	case "call_expression":
		callee := calleeOf(n)
		if callee != nil && (callee.Kind() == "identifier" || callee.Kind() == "scoped_identifier") && lastSegment(callee.Utf8Text(src)) == "Err" {
			if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() == 1 {
				if f := errorValue(args.NamedChild(0), src); f != "" {
					form.Concern, form.Form = unit.ErrorValues, f
				}
			}
			break
		}
		if callee == nil || callee.Kind() != "field_expression" {
			break
		}
		field, value := callee.ChildByFieldName("field"), callee.ChildByFieldName("value")
		if field == nil || value == nil {
			break
		}
		switch field.Utf8Text(src) {
		case "map_err":
			form.Concern, form.Form = unit.ErrorWrapping, "map_err"
		case "context", "with_context":
			if anyhowContext(field.Utf8Text(src), n) {
				form.Concern, form.Form = unit.ErrorWrapping, "context"
			}
		case "contains", "starts_with", "ends_with":
			if isErrorMessage(value, src) {
				form.Concern, form.Form = unit.ErrorChecks, "message"
			}
		}
	}
	return form
}

// errorValue returns the form of the error an Err(..) holds: a variant
// (Fault::Busy, Fault::Write(e), Fault::Long { .. }), or a string
// (format!, a literal, .to_string(), .into() or .to_owned() on one, or
// String::from). Anything else, such as an error passed on or made by a
// constructor, is no form; anyhow! is recorded as the macro itself.
func errorValue(n *sitter.Node, src []byte) string {
	switch n.Kind() {
	case "string_literal", "raw_string_literal":
		return "string"
	case "macro_invocation":
		if m := n.ChildByFieldName("macro"); m != nil && lastSegment(m.Utf8Text(src)) == "format" {
			return "string"
		}
	case "call_expression":
		callee := calleeOf(n)
		switch {
		case callee == nil:
		case callee.Kind() == "field_expression":
			field, value := callee.ChildByFieldName("field"), callee.ChildByFieldName("value")
			if field != nil && value != nil && (value.Kind() == "string_literal" || value.Kind() == "raw_string_literal") {
				switch field.Utf8Text(src) {
				case "to_string", "into", "to_owned":
					return "string"
				}
			}
		case callee.Utf8Text(src) == "String::from" || strings.HasSuffix(callee.Utf8Text(src), "::String::from"):
			return "string"
		case isVariantPath(callee.Utf8Text(src)):
			return "variant"
		}
	case "scoped_identifier", "struct_expression":
		if isVariantPath(n.Utf8Text(src)) {
			return "variant"
		}
	}
	return ""
}

// errPattern finds an Err(Path ..) pattern in a macro's text.
var errPattern = regexp.MustCompile(`\bErr \( ([A-Za-z_][\w: ]*)`)

// messageCheck finds a comparison of an error's message in a macro's
// text, as isErrorMessage does in parsed code.
var messageCheck = regexp.MustCompile(`\b(e|err|error|\w+_err|\w+_error) \. to_string \( \) (==|!=|\. (contains|starts_with|ends_with) \()`)

// matchesPattern returns the tokens of a matches! invocation's pattern
// and of its guard: after the value's top-level comma, split at a
// top-level if.
func matchesPattern(n *sitter.Node, src []byte) (pattern, guard string) {
	tokens := strings.Fields(tokenText(n, src))
	depth, start := 0, -1
	for i, t := range tokens {
		switch {
		case t == "(" || t == "[" || t == "{":
			depth++
		case t == ")" || t == "]" || t == "}":
			depth--
		case t == "," && depth == 1 && start < 0: // inside the invocation's own parentheses
			start = i + 1
		case t == "if" && depth == 1 && start >= 0:
			return strings.Join(tokens[start:i], " "), strings.Join(tokens[i+1:], " ")
		}
	}
	if start < 0 {
		return "", ""
	}
	return strings.Join(tokens[start:], " "), ""
}

// tokenText returns a macro's tokens separated by single spaces, leaving
// out literals and comments, whose text may look like code.
func tokenText(n *sitter.Node, src []byte) string {
	var tokens []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Kind() {
		case "string_literal", "raw_string_literal", "char_literal", "line_comment", "block_comment":
			return
		}
		if n.ChildCount() == 0 {
			tokens = append(tokens, n.Utf8Text(src))
			return
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(n)
	return strings.Join(tokens, " ")
}

// isVariantPath reports whether a pattern names an enum variant by path:
// its last segment is upper case (Fault::Gone, crate::errors::Fault::Gone,
// Fault::Store(_)), unlike a binding (e) or a bare name.
func isVariantPath(pattern string) bool {
	if i := strings.IndexAny(pattern, "({ "); i >= 0 {
		pattern = pattern[:i]
	}
	i := strings.LastIndex(pattern, "::")
	return i >= 0 && isUpper(pattern[i+2:])
}

// anyhowContext reports whether a context or with_context call wraps an
// error as anyhow does: context with a message (a string or format!),
// with_context with a closure. Other APIs share the names, such as
// OpenTelemetry's with_context(cx), and take a value.
func anyhowContext(method string, call *sitter.Node) bool {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() != 1 {
		return false
	}
	switch arg := args.NamedChild(0).Kind(); method {
	case "context":
		return arg == "string_literal" || arg == "raw_string_literal" || arg == "macro_invocation"
	default:
		return arg == "closure_expression"
	}
}

// isErrorMessage reports whether n is to_string() called on an error
// value: one named e, err or error, or ending in _err or _error, possibly
// a field (row.err).
func isErrorMessage(n *sitter.Node, src []byte) bool {
	if n.Kind() != "call_expression" {
		return false
	}
	callee := calleeOf(n)
	if callee == nil || callee.Kind() != "field_expression" {
		return false
	}
	field, value := callee.ChildByFieldName("field"), callee.ChildByFieldName("value")
	if field == nil || value == nil || field.Utf8Text(src) != "to_string" {
		return false
	}
	if value.Kind() == "field_expression" {
		value = value.ChildByFieldName("field")
	}
	if value == nil || (value.Kind() != "identifier" && value.Kind() != "field_identifier") {
		return false
	}
	name := value.Utf8Text(src)
	return name == "e" || name == "err" || name == "error" || strings.HasSuffix(name, "_err") || strings.HasSuffix(name, "_error")
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
	return ParseAs(syntax.Source{Path: path, Src: src, Test: IsTestPath(path)})
}

// ParseAs is Parse with whether the file is test code by its place (see
// IsTestPath) given: a library a manifest roots under tests/ is not.
func ParseAs(file syntax.Source) ([]unit.Unit, unit.Decls, error) {
	path, src := file.Path, file.Src
	var units []unit.Unit
	var decls unit.Decls
	var marked error
	err := syntax.Parse(language, src, func(root *sitter.Node) {
		e := extractor{path: path, src: src, uses: map[[2]uint]map[string]string{}}
		e.walk(root, scope{}, file.Test, nil)
		units, decls = e.units, e.decls
		marked = syntax.MarkIgnored(root, src, units, &decls)
	})
	if isBinaryPath(path) {
		for i := range units {
			units[i].Forms = slices.DeleteFunc(units[i].Forms, func(f unit.Form) bool { return f.Concern == unit.ErrorValues })
		}
	}
	return units, decls, errors.Join(err, marked)
}

// isBinaryPath reports whether a file belongs to a binary by Cargo's
// layout: src/main.rs, or under src/bin/. A binary's errors are printed
// rather than matched by callers, so how it makes them is no error-values
// form: measured, the CLIs of lns-inference-router return usage messages
// as strings in a workspace that otherwise makes errors from variants.
func isBinaryPath(path string) bool {
	path = filepath.ToSlash(path)
	return path == "src/main.rs" || strings.HasSuffix(path, "/src/main.rs") ||
		strings.HasPrefix(path, "src/bin/") || strings.Contains(path, "/src/bin/")
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
	var out map[string]string
	_ = syntax.Parse(language, src, func(root *sitter.Node) { out = importsOf(root, src) })
	return out
}

// importsOf is Imports for a parsed file's root.
func importsOf(root *sitter.Node, src []byte) map[string]string {
	out := map[string]string{}
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
	// uses caches each module's use imports (see visibleImports), by the
	// span of the module's item list.
	uses map[[2]uint]map[string]string
}

// visibleImports returns the use imports an item sees: those of the
// module it is declared in, the file's top level or an inline mod's
// body. A module does not see its parent's imports.
func (e *extractor) visibleImports(item *sitter.Node) map[string]string {
	module := item.Parent()
	for module != nil && module.Kind() != "source_file" && module.Kind() != "declaration_list" {
		module = module.Parent()
	}
	// An impl's or trait's item list is not a module: keep climbing to
	// the mod or file holding it.
	for module != nil && module.Kind() == "declaration_list" && module.Parent() != nil && module.Parent().Kind() != "mod_item" {
		module = module.Parent().Parent()
		for module != nil && module.Kind() != "source_file" && module.Kind() != "declaration_list" {
			module = module.Parent()
		}
	}
	if module == nil {
		return nil
	}
	key := span(module)
	if _, ok := e.uses[key]; !ok {
		e.uses[key] = usesIn(module, e.src)
	}
	return e.uses[key]
}

// usesIn returns the imports of the use declarations directly in a
// module's item list or a block, as Imports gives them.
func usesIn(container *sitter.Node, src []byte) map[string]string {
	out := map[string]string{}
	for i := uint(0); i < container.NamedChildCount(); i++ {
		if n := container.NamedChild(i); n.Kind() == "use_declaration" {
			if arg := n.ChildByFieldName("argument"); arg != nil {
				collectUse(arg, "", src, out)
			}
		}
	}
	return out
}

// walk finds function items, tracking the enclosing impl or trait name and
// whether the code is test code. Attributes are siblings that come just
// before the item they apply to; own are those of n.
// scope is what encloses a function: the type or trait owning it and,
// inside a trait impl, the trait implemented.
type scope struct {
	owner, trait string
	// module is the inline module path within the file (outer::inner).
	module string
}

func (e *extractor) walk(n *sitter.Node, sc scope, test bool, own []string) {
	switch n.Kind() {
	case "function_item":
		e.addFunction(n, sc, test, own)
		return
	case "struct_item":
		e.addTypeName(n)
		e.addStruct(n, sc, test, own)
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
	case "mod_item":
		// mod x { .. } opens an inline module; mod x; names a file.
		if name := n.ChildByFieldName("name"); name != nil && n.ChildByFieldName("body") != nil {
			if sc.module != "" {
				sc.module += "::"
			}
			sc.module += name.Utf8Text(e.src)
		}
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
			childTest = test || slices.ContainsFunc(attrs, isTestAttr) || slices.ContainsFunc(attrs, isCfgTest)
		case "mod_item", "impl_item":
			childTest = test || slices.ContainsFunc(attrs, isCfgTest)
		}
		e.walk(child, sc, childTest, attrs)
		attrs = nil
	}
}

// isTestAttr reports whether an attribute marks a test function, such as
// #[test], #[tokio::test(...)], #[rstest], #[test_case(...)], #[bench], or
// a runner's own named after test (#[wasm_bindgen_test]).
func isTestAttr(attr string) bool {
	name := attrName(attr)
	return name == "test" || strings.HasSuffix(name, "_test") || name == "rstest" || name == "test_case" || name == "bench"
}

// isCfgTest reports whether an attribute limits an item to test builds.
func isCfgTest(attr string) bool {
	compact := strings.Join(strings.Fields(attr), "")
	return compact == "#[cfg(test)]" || strings.HasPrefix(compact, "#[cfg(all(test,")
}

// inBenches reports whether a file is in a benches/ directory, whose
// functions a benchmark harness registers (Criterion's criterion_group!):
// all of them are test cases.
func inBenches(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	return slices.Contains(parts[:len(parts)-1], "benches")
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
	// The signature sees its module's imports; each block of the body also
	// those its own use declarations add (see Visit).
	g := grammar{self: unit.SimpleName(owner), generic: typeParameters(n, e.src), imports: maps.Clone(e.visibleImports(n)),
		vars: map[string]syntax.Operand{}, entries: &map[[2]uint]map[string]syntax.Operand{},
		calls: map[[2]uint]int{}, chains: map[int]produced{}}
	if impl := enclosingImpl(n); impl != nil {
		maps.Copy(g.generic, typeParameters(impl, e.src))
	}
	if params := n.ChildByFieldName("parameters"); params != nil {
		for i := uint(0); i < params.NamedChildCount(); i++ {
			p := params.NamedChild(i)
			pattern, typ := p.ChildByFieldName("pattern"), p.ChildByFieldName("type")
			if pattern != nil && pattern.Kind() == "mut_pattern" && pattern.NamedChildCount() > 0 {
				pattern = pattern.NamedChild(pattern.NamedChildCount() - 1)
			}
			if p.Kind() == "parameter" && pattern != nil && typ != nil && pattern.Kind() == "identifier" {
				g.set(pattern.Utf8Text(e.src), syntax.Operand{Type: g.typeOf(typ.Utf8Text(e.src))})
			}
		}
	}
	if g.imports == nil {
		g.imports = map[string]string{}
	}
	results := g.results(n, e.src)
	s := syntax.Flatten(n, e.src, g)
	for k, p := range g.chains {
		if j, ok := g.calls[p.call]; ok {
			s.Calls[k].From, s.Calls[k].Result = j+1, p.result
		}
	}
	e.units = append(e.units, unit.Unit{
		File:      e.path,
		Name:      name,
		StartLine: syntax.Line(n.StartPosition()),
		EndLine:   syntax.Line(n.EndPosition()),
		Shape:     similarity.HashSymbols(s.Seq),
		Nodes:     s.Nodes,
		Test:      test,
		Module:    sc.module,
		TestCase:  slices.ContainsFunc(attrs, isTestAttr) || inBenches(e.path),
		Leaves:    s.Leaves,
		Seq:       s.Seq,
		SeqLines:  s.Lines,
		Blocks:    s.Blocks,
		Trait:     sc.trait,
		Calls:     s.Calls,
		Results:   results,
		Public:    isPublic(n, e.src),
		External:  (owner == "" && name == "main") || slices.ContainsFunc(attrs, isExternalAttr),
		Forwards:  forwards(n, e.src),
		Scope:     scopeOf(n, e.src),

		StringMatches: s.Matches,
		Params:        params(n, e.src),
		Forms:         s.Forms,
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
	value = producer(value, src)
	if pat == nil || pat.Kind() != "identifier" || value == nil || value.Kind() != "call_expression" {
		return "", ""
	}
	callee := calleeOf(value)
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

// resultAdapters convert a call's error or unwrap its result: the value
// comes from the call before them.
var resultAdapters = map[string]bool{
	"map_err": true, "context": true, "with_context": true, "unwrap": true, "expect": true,
}

// unwrapping are the resultAdapters that take the value out of a Result
// or Option, as ? does.
var unwrapping = map[string]bool{"unwrap": true, "expect": true}

// producer returns the call that produced a bound value, looking through
// ?, .await and resultAdapters (db.transaction().await.map_err(retry)? is
// produced by transaction).
func producer(value *sitter.Node, src []byte) *sitter.Node {
	for value != nil {
		switch value.Kind() {
		case "try_expression", "await_expression":
			value = value.NamedChild(0)
			continue
		case "call_expression":
			callee := calleeOf(value)
			if callee != nil && callee.Kind() == "field_expression" {
				if f := callee.ChildByFieldName("field"); f != nil && resultAdapters[f.Utf8Text(src)] {
					value = callee.ChildByFieldName("value")
					continue
				}
			}
		}
		return value
	}
	return nil
}

// calleeOf returns what a call expression calls, without its type
// arguments: pool.acquire::<Lease>() calls pool.acquire.
func calleeOf(call *sitter.Node) *sitter.Node {
	callee := call.ChildByFieldName("function")
	if callee != nil && callee.Kind() == "generic_function" {
		return callee.ChildByFieldName("function")
	}
	return callee
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
// ddslop:ignore the Go adapter's algorithm over Rust's node kinds; one table-driven home on a third language
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
// ddslop:ignore fills unit.Enum as addStruct fills unit.Struct; only a data model change could merge them
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
		File: e.path, Trait: sc.trait, TraitPath: traitPathOf(n.ChildByFieldName("trait"), e.src), Type: ty,
		StartLine: syntax.Line(n.StartPosition()), EndLine: syntax.Line(n.EndPosition()),
		Blanket: blanket, Test: test,
	})
}

// traitPathOf returns a trait's path as written, without type arguments.
func traitPathOf(trait *sitter.Node, src []byte) string {
	if trait == nil {
		return ""
	}
	path, _, _ := strings.Cut(syntax.Compact(trait.Utf8Text(src)), "<")
	return path
}

// addStruct records a struct with named fields; unit and tuple structs are
// left out.
func (e *extractor) addStruct(n *sitter.Node, sc scope, test bool, attrs []string) {
	test = test || slices.ContainsFunc(attrs, isCfgTest) // #[cfg(test)] struct
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
	generic := typeParameters(n, e.src)
	eachMember(body, "field_declaration", func(f, fname *sitter.Node) {
		if ftype := f.ChildByFieldName("type"); ftype != nil {
			ty.Fields = append(ty.Fields, unit.Field{
				Name: fname.Utf8Text(e.src),
				Type: syntax.Compact(ftype.Utf8Text(e.src)),
				Line: syntax.Line(f.StartPosition()),
			})
			// A field of no type is recorded too, as unknown (""), so that a
			// struct of the same name in another module cannot type it.
			typeOf := func(written string) string { return rustType(written, ty.Name, generic, e.visibleImports(n)) }
			e.decls.FieldTypes = append(e.decls.FieldTypes, unit.FieldType{Struct: ty.Name, Field: fname.Utf8Text(e.src),
				Type: typeOf(ftype.Utf8Text(e.src)), Unwrapped: unwrapped(ftype.Utf8Text(e.src), typeOf),
				File: e.path, Module: sc.module, Test: test})
		}
	})
	e.decls.Structs = append(e.decls.Structs, ty)
}

// typeParameters returns the type parameters a function, impl or struct
// declares (<T: Saver, U>), leaving out lifetimes, each with the trait
// its bounds there and in the where clause give it (see traitOf), or "".
func typeParameters(n *sitter.Node, src []byte) map[string]string {
	bounds := map[string][]string{}
	add := func(name, bound *sitter.Node) {
		if name == nil {
			return
		}
		b := bounds[name.Utf8Text(src)]
		if bound != nil {
			b = append(b, strings.TrimPrefix(strings.TrimSpace(bound.Utf8Text(src)), ":")) // : Send + Store
		}
		bounds[name.Utf8Text(src)] = b
	}
	list := n.ChildByFieldName("type_parameters")
	for i := uint(0); list != nil && i < list.NamedChildCount(); i++ {
		p := list.NamedChild(i)
		switch {
		case p.Kind() == "type_identifier":
			add(p, nil)
		case p.ChildByFieldName("name") != nil:
			add(p.ChildByFieldName("name"), p.ChildByFieldName("bounds"))
		case p.ChildByFieldName("left") != nil:
			add(p.ChildByFieldName("left"), p.ChildByFieldName("bounds"))
		}
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if w := n.NamedChild(i); w.Kind() == "where_clause" {
			for j := uint(0); j < w.NamedChildCount(); j++ {
				left := w.NamedChild(j).ChildByFieldName("left")
				if left == nil {
					continue
				}
				if _, declared := bounds[left.Utf8Text(src)]; declared {
					add(left, w.NamedChild(j).ChildByFieldName("bounds"))
				}
			}
		}
	}
	out := map[string]string{}
	for name, b := range bounds {
		out[name] = traitOf(strings.Join(b, "+"))
	}
	return out
}

// enclosingImpl returns the impl block a function item is declared in,
// or nil.
func enclosingImpl(fn *sitter.Node) *sitter.Node {
	for p := fn.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "impl_item":
			return p
		case "function_item", "source_file", "mod_item":
			return nil
		}
	}
	return nil
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
func (g grammar) recordCall(s *syntax.Shaper, n *sitter.Node) {
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
			// An imported root is written as its import's path, by the
			// imports visible here: helpers::log after use crate::util as
			// helpers is crate::util::log.
			if root, rest, found := strings.Cut(c.Qualifier, "::"); g.imports[root] != "" {
				c.Qualifier = g.imports[root]
				if found {
					c.Qualifier += "::" + rest
				}
			}
		}
	case "field_expression":
		c.Kind = unit.MethodCall
		if field := fn.ChildByFieldName("field"); field != nil {
			c.Name = field.Utf8Text(s.Src)
		}
		switch value := fn.ChildByFieldName("value"); {
		case value == nil:
		case value.Kind() == "self":
			c.Qualifier = "self"
		case value.Kind() == "identifier":
			op := g.vars[value.Utf8Text(s.Src)]
			c.Type, c.From, c.Result = op.Type, op.From, op.Result
		case value.Kind() == "field_expression" || triedField(value):
			// self.ctx.db.query(), req.store.get(), make().inner.run(): the
			// fields followed from where the chain starts, one a ? unwraps
			// marked "?" (make().inner?.run()).
			var fields []string
			base, unwrap := value, ""
			for base != nil && (base.Kind() == "field_expression" || triedField(base)) {
				if triedField(base) {
					base, unwrap = base.NamedChild(0), "?"
					continue
				}
				f := base.ChildByFieldName("field")
				if f == nil {
					break
				}
				fields = append([]string{f.Utf8Text(s.Src) + unwrap}, fields...)
				base, unwrap = base.ChildByFieldName("value"), ""
			}
			switch {
			case base == nil || base.Kind() == "field_expression":
				fields = nil
			case base.Kind() == "self":
			case base.Kind() == "identifier":
				op := g.vars[base.Utf8Text(s.Src)]
				if op == (syntax.Operand{}) {
					fields = nil // an untyped start types nothing
				}
				c.Type, c.From, c.Result = op.Type, op.From, op.Result
			default:
				if p, ok := callResult(base); ok {
					g.chains[len(s.Calls)] = p // recorded after this call
				} else {
					fields = nil
				}
			}
			c.Field = strings.Join(fields, ".")
		}
	default:
		return // closures and other callees are not resolved by name
	}
	if c.Kind == unit.MethodCall {
		switch {
		case unwrapping[c.Name]:
			c.Adapts = 2
		case resultAdapters[c.Name]:
			c.Adapts = 1
		}
		if p, ok := callResult(fn.ChildByFieldName("value")); ok {
			g.chains[len(s.Calls)] = p // recorded after this call
		}
	}
	g.calls[span(n)] = len(s.Calls)
	s.Calls = append(s.Calls, c)
}

// triedField reports whether a node is a field a ? unwraps: inner? in
// make().inner?.run().
func triedField(n *sitter.Node) bool {
	return n.Kind() == "try_expression" && n.NamedChildCount() > 0 && n.NamedChild(0).Kind() == "field_expression"
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
	s.RecordStringMatch(n, values)
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
	name := manifestValue(cargoToml, "lib", "name")
	if name == "" {
		name = manifestValue(cargoToml, "package", "name")
	}
	return strings.ReplaceAll(name, "-", "_")
}

// tableHeader returns the table a TOML line opens ([lib], [[bin]]), its
// comment left out, or false for a line that opens none.
func tableHeader(line string) (string, bool) {
	if !strings.HasPrefix(line, "[") {
		return "", false
	}
	header, _, _ := strings.Cut(line, "#")
	return strings.Trim(strings.TrimSpace(header), "[] "), true
}

// tomlString returns a TOML value as written: a quoted string's contents,
// up to its closing quote, or a bare value up to a comment.
func tomlString(value string) string {
	if value != "" && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return value[1 : 1+end]
		}
	}
	bare, _, _ := strings.Cut(value, "#")
	return strings.TrimSpace(bare)
}

// manifestValue returns a string key's value in one section of a
// Cargo.toml, unquoted, or "" when the section does not set it.
func manifestValue(cargoToml []byte, section, key string) string {
	if values := manifestValues(cargoToml, key, section); len(values) > 0 {
		return values[0]
	}
	return ""
}

// manifestValues returns a string key's values in the given sections of a
// Cargo.toml, unquoted, in manifest order: an array of tables ([[bin]])
// repeats its section.
func manifestValues(cargoToml []byte, key string, sections ...string) []string {
	var out []string
	current := ""
	for line := range strings.Lines(string(cargoToml)) {
		line = strings.TrimSpace(line)
		if header, ok := tableHeader(line); ok {
			current = header
			continue
		}
		k, value, ok := strings.Cut(line, "=")
		if ok && slices.Contains(sections, current) && strings.TrimSpace(k) == key {
			out = append(out, tomlString(strings.TrimSpace(value)))
		}
	}
	return out
}

// DeclaredModules returns the modules a file declares out of line (mod
// name;), at its top level, in source order: each one's name, or the file
// a #[path] attribute places it in, ending in .rs.
func DeclaredModules(src []byte) []string {
	return outOfLineModules(src, func(name string, attrs []string) string {
		for _, attr := range attrs {
			if file, ok := pathAttribute(attr); ok {
				return file
			}
		}
		return name
	})
}

// pathAttribute returns the file a #[path = "..."] attribute names.
func pathAttribute(attr string) (string, bool) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(attr, "#["), "]"))
	key, value, ok := strings.Cut(inner, "=")
	if !ok || strings.TrimSpace(key) != "path" {
		return "", false
	}
	return tomlString(strings.TrimSpace(value)), true
}

// TestModules returns the names of the modules a file declares out of
// line under #[cfg(test)] ("#[cfg(test)] mod name;"): their files hold
// test code.
//
// Only declarations at the top level of the file are considered.
func TestModules(src []byte) []string {
	return outOfLineModules(src, func(name string, attrs []string) string {
		if slices.ContainsFunc(attrs, isCfgTest) {
			return name
		}
		return ""
	})
}

// outOfLineModules returns, for the modules a file declares out of line at
// its top level, what entry makes of each one's name and attributes, ""
// leaving it out.
func outOfLineModules(src []byte, entry func(name string, attrs []string) string) []string {
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
			case child.Kind() == "mod_item" && child.ChildByFieldName("body") == nil:
				if name := child.ChildByFieldName("name"); name != nil {
					if e := entry(name.Utf8Text(src), attrs); e != "" {
						names = append(names, e)
					}
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
		if header, ok := tableHeader(line); ok {
			section = header
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

// TargetPaths returns the paths a Cargo.toml sets for its other targets'
// root files ([[bin]], [[example]], [[test]], [[bench]]), relative to the
// package, in manifest order.
func TargetPaths(cargoToml []byte) []string {
	return manifestValues(cargoToml, "path", "bin", "example", "test", "bench")
}

// LibPath returns the [lib] path a Cargo.toml sets for its library's root
// file, relative to the package, or "" when it keeps Cargo's default.
func LibPath(cargoToml []byte) string {
	return manifestValue(cargoToml, "lib", "path")
}
