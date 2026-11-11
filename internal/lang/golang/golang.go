// Package golang extracts comparable units from Go source code.
package golang

import (
	"path/filepath"
	"slices"
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

// grammar describes Go to the shared flattening (see syntax.Grammar) for
// one function: its receiver's name, if it is a method, and the file's
// imported package names, to tell calls apart.
type grammar struct {
	receiver string
	packages map[string]bool
}

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

// Visit records calls and counts early returns outside function literals.
func (g grammar) Visit(s *syntax.Shaper, n *sitter.Node) func() {
	switch n.Kind() {
	case "call_expression":
		if c, ok := g.call(n, s.Src); ok {
			s.Calls = append(s.Calls, c)
		}
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

// call classifies a call by its callee: f(x) is a free call, a selector
// on the receiver (s.load) a call on self, one on an imported package name
// (json.Marshal) a path call, and any other selector a method call.
func (g grammar) call(n *sitter.Node, src []byte) (unit.Call, bool) {
	fn := n.ChildByFieldName("function")
	c := unit.Call{Line: syntax.Line(n.StartPosition())}
	switch {
	case fn == nil:
		return c, false
	case fn.Kind() == "identifier":
		c.Kind, c.Name = unit.FreeCall, fn.Utf8Text(src)
	case fn.Kind() == "selector_expression":
		operand, field := fn.ChildByFieldName("operand"), fn.ChildByFieldName("field")
		if operand == nil || field == nil {
			return c, false
		}
		c.Kind, c.Name = unit.MethodCall, field.Utf8Text(src)
		if operand.Kind() == "identifier" {
			switch name := operand.Utf8Text(src); {
			case name == g.receiver:
				c.Qualifier = "self"
			case g.packages[name]:
				c.Kind, c.Qualifier = unit.PathCall, name
			}
		}
	default:
		return c, false // function values, conversions
	}
	return c, true
}

// Parse parses Go source and returns one unit per function or method, in
// source order, and its package-level declarations: structs and constants.
// Functions containing syntax errors are skipped, and declarations inside
// functions are not seen.
func Parse(path string, src []byte) ([]unit.Unit, unit.Decls, error) {
	var units []unit.Unit
	var decls unit.Decls
	test := IsTestPath(path)
	err := syntax.Parse(language, src, func(root *sitter.Node) {
		packages := imports(root, src)
		for i := uint(0); i < root.NamedChildCount(); i++ {
			n := root.NamedChild(i)
			if n.HasError() {
				continue
			}
			switch n.Kind() {
			case "package_clause":
				if name := n.NamedChild(0); name != nil {
					decls.Package = name.Utf8Text(src)
				}
			case "function_declaration", "method_declaration":
				if u, ok := function(n, path, src, test, packages); ok {
					units = append(units, u)
				}
			case "const_declaration":
				decls.Consts = append(decls.Consts, constants(n, path, src, test)...)
			case "type_declaration":
				decls.Structs = append(decls.Structs, structs(n, path, src, test)...)
			}
		}
	})
	return units, decls, err
}

// function makes a unit of a function or method declaration.
func function(n *sitter.Node, path string, src []byte, test bool, packages map[string]bool) (unit.Unit, bool) {
	name := n.ChildByFieldName("name")
	if name == nil {
		return unit.Unit{}, false
	}
	short := name.Utf8Text(src)
	owner := receiverType(n, src)
	full := short
	if owner != "" {
		full = owner + "." + short
	}
	s := syntax.Flatten(n, src, grammar{receiver: receiverName(n, src), packages: packages})
	return unit.Unit{
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
		Calls:     s.Calls,
		Forwards:  forwards(n, src),
		// Exported names are public; methods may satisfy an interface
		// without being named; main and init are called by the runtime.
		Public:     short != "" && short[0] >= 'A' && short[0] <= 'Z',
		Dispatched: owner != "",
		External:   owner == "" && (short == "main" || short == "init"),
	}, true
}

// fieldNodes returns n's named children under a field name, in order: a
// const spec or struct field can declare several names. The commas between
// them carry the field name too, but are not named nodes.
func fieldNodes(n *sitter.Node, field string) []*sitter.Node {
	var out []*sitter.Node
	for i := uint(0); i < n.ChildCount(); i++ {
		if c := n.Child(i); c.IsNamed() && n.FieldNameForChild(uint32(i)) == field {
			out = append(out, c)
		}
	}
	return out
}

// constants returns a const declaration's constants with written values,
// each name paired with its value by position; iota and implicit values
// have nothing to compare.
func constants(n *sitter.Node, path string, src []byte, test bool) []unit.Const {
	var out []unit.Const
	for i := uint(0); i < n.NamedChildCount(); i++ {
		spec := n.NamedChild(i)
		values := spec.ChildByFieldName("value")
		if spec.Kind() != "const_spec" || values == nil {
			continue
		}
		for j, name := range fieldNodes(spec, "name") {
			if j >= int(values.NamedChildCount()) { //nolint:gosec // a child count fits in int
				break
			}
			v := values.NamedChild(uint(j))
			if v.Kind() == "iota" {
				continue
			}
			literal := v.Kind() == "interpreted_string_literal" || v.Kind() == "raw_string_literal"
			text := v.Utf8Text(src)
			if !literal {
				text = syntax.Compact(text)
			}
			out = append(out, unit.Const{
				File: path, Name: name.Utf8Text(src), Value: text, Line: syntax.Line(spec.StartPosition()),
				Nodes: syntax.CountNodes(v), Literal: literal, Test: test,
			})
		}
	}
	return out
}

// structs returns a type declaration's struct types. An embedded field is
// named after its type, as Go names it.
func structs(n *sitter.Node, path string, src []byte, test bool) []unit.Struct {
	var out []unit.Struct
	for i := uint(0); i < n.NamedChildCount(); i++ {
		spec := n.NamedChild(i)
		name, typ := spec.ChildByFieldName("name"), spec.ChildByFieldName("type")
		if spec.Kind() != "type_spec" || name == nil || typ == nil || typ.Kind() != "struct_type" {
			continue
		}
		s := unit.Struct{
			File: path, Name: name.Utf8Text(src),
			StartLine: syntax.Line(spec.StartPosition()), EndLine: syntax.Line(spec.EndPosition()),
			Nodes: syntax.CountNodes(spec), Test: test,
		}
		list := typ.NamedChild(0)
		for j := uint(0); list != nil && j < list.NamedChildCount(); j++ {
			f := list.NamedChild(j)
			ftype := f.ChildByFieldName("type")
			if f.Kind() != "field_declaration" || ftype == nil {
				continue
			}
			t := syntax.Compact(ftype.Utf8Text(src))
			names := fieldNodes(f, "name")
			if len(names) == 0 { // embedded
				s.Fields = append(s.Fields, unit.Field{Name: embeddedName(t), Type: t, Line: syntax.Line(f.StartPosition())})
			}
			for _, fn := range names {
				s.Fields = append(s.Fields, unit.Field{Name: fn.Utf8Text(src), Type: t, Line: syntax.Line(f.StartPosition())})
			}
		}
		out = append(out, s)
	}
	return out
}

// embeddedName is the field name Go gives an embedded type: *pkg.Mutex
// is Mutex.
func embeddedName(typ string) string {
	typ = strings.TrimPrefix(typ, "*")
	if i := strings.LastIndex(typ, "."); i >= 0 {
		typ = typ[i+1:]
	}
	if i := strings.Index(typ, "["); i >= 0 {
		typ = typ[:i]
	}
	return typ
}

// imports returns the names a file's imports bind: an alias, or the
// path's last element ("encoding/json" is json; a major version suffix
// such as /v3 is skipped).
func imports(root *sitter.Node, src []byte) map[string]bool {
	names := map[string]bool{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Kind() == "import_spec" {
			path := n.ChildByFieldName("path")
			if alias := n.ChildByFieldName("name"); alias != nil {
				names[alias.Utf8Text(src)] = true
			} else if path != nil {
				parts := strings.Split(strings.Trim(path.Utf8Text(src), "\"`"), "/")
				last := parts[len(parts)-1]
				if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
					last = parts[len(parts)-2]
				}
				names[last] = true
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		if n := root.NamedChild(i); n.Kind() == "import_declaration" {
			walk(n)
		}
	}
	return names
}

// receiverName returns a method's receiver variable (s in (s *Store)), or
// "" for a function or an unnamed receiver.
func receiverName(n *sitter.Node, src []byte) string {
	recv := n.ChildByFieldName("receiver")
	if recv == nil || recv.NamedChildCount() == 0 {
		return ""
	}
	if name := recv.NamedChild(0).ChildByFieldName("name"); name != nil {
		return name.Utf8Text(src)
	}
	return ""
}

// forwards returns the callee when a function's whole body is one call,
// returned or not, passing the function's parameters on in order, each as
// is or by address: func f(a, b) { return g(a, &b) }. Calls on fields
// (s.inner.Get(x)) do not count, and nor do functions without parameters
// or with a variadic one.
func forwards(fn *sitter.Node, src []byte) string {
	params, body := fn.ChildByFieldName("parameters"), fn.ChildByFieldName("body")
	if params == nil || body == nil || body.NamedChildCount() != 1 {
		return ""
	}
	var names []string
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		if p.Kind() != "parameter_declaration" {
			return "" // variadic
		}
		for _, name := range fieldNodes(p, "name") {
			names = append(names, name.Utf8Text(src))
		}
	}
	stmts := body.NamedChild(0)
	if len(names) == 0 || stmts.Kind() != "statement_list" || stmts.NamedChildCount() != 1 {
		return ""
	}
	expr := stmts.NamedChild(0)
	switch expr.Kind() {
	case "return_statement":
		list := expr.NamedChild(0)
		if list == nil || list.NamedChildCount() != 1 {
			return ""
		}
		expr = list.NamedChild(0)
	case "expression_statement":
		expr = expr.NamedChild(0)
	}
	if expr == nil || expr.Kind() != "call_expression" {
		return ""
	}
	callee, args := expr.ChildByFieldName("function"), expr.ChildByFieldName("arguments")
	if callee == nil || !syntax.PassesOn(args, names, src, addressOf) {
		return ""
	}
	if callee.Kind() == "selector_expression" {
		if op := callee.ChildByFieldName("operand"); op == nil || op.Kind() != "identifier" {
			return "" // delegation to a field
		}
	} else if callee.Kind() != "identifier" {
		return ""
	}
	return callee.Utf8Text(src)
}

// addressOf returns x for an argument &x, else nil.
func addressOf(n *sitter.Node, src []byte) *sitter.Node {
	if op := n.ChildByFieldName("operator"); n.Kind() == "unary_expression" && op != nil && op.Utf8Text(src) == "&" {
		return n.ChildByFieldName("operand")
	}
	return nil
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

// Internal reports whether a Go package can only be used by the analysed
// code: it sits under an internal directory, which the go tool forbids
// importing from outside its parent, or it is package main, which nothing
// imports.
func Internal(path, pkg string) bool {
	if pkg == "main" {
		return true
	}
	parts := strings.Split(filepath.ToSlash(filepath.Dir(path)), "/")
	return slices.Contains(parts, "internal")
}
