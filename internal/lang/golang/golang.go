// Package golang extracts comparable units from Go source code.
package golang

import (
	"errors"
	"maps"
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
	packages map[string]string // imported name -> import path
	// vars maps the function's variables whose type the code states to
	// that type (see typeOf), as declarations are visited.
	vars map[string]string
	// scopes are the open lexical scopes, innermost last.
	scopes *[]*varScope
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
			if form := errorCallForm(c, g.packages[c.Qualifier], n, s.Src); form.Form != "" {
				s.Forms = append(s.Forms, form)
			}
		}
	case "binary_expression":
		if form := errorComparison(n, s.Src); form.Form != "" {
			s.Forms = append(s.Forms, form)
		}
	// Declarations take effect after their values, whose calls still see
	// the names as they were: x = x.Reset() calls Reset on the old x.
	case "var_spec":
		return func() {
			t, values := n.ChildByFieldName("type"), n.ChildByFieldName("value")
			names := fieldNodes(n, "name")
			for i, name := range names {
				typ := ""
				switch {
				case t != nil:
					typ = typeOf(syntax.Compact(t.Utf8Text(s.Src)), g.packages)
				case values != nil && values.NamedChildCount() == uint(len(names)):
					typ = g.literalType(values.NamedChild(uint(i)), s.Src) // var x = T{}
				}
				g.declare(name.Utf8Text(s.Src), typ, true)
			}
		}
	case "short_var_declaration", "assignment_statement", "range_clause", "receive_statement":
		return func() { g.assign(n, s.Src) }
	case "expression_switch_statement":
		var values []string
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if c := n.NamedChild(i); c.Kind() == "expression_case" {
				for _, v := range fieldNodes(c, "value") {
					for j := uint(0); j < v.NamedChildCount(); j++ {
						if lit := v.NamedChild(j); lit.Kind() == "interpreted_string_literal" || lit.Kind() == "raw_string_literal" {
							values = append(values, lit.Utf8Text(s.Src))
						}
					}
				}
			}
		}
		s.RecordStringMatch(n, values)
		return g.scope(false)
	case "return_statement":
		if s.Closures == 0 {
			s.Exits++
		}
	case "func_literal":
		s.Closures++
		restore := g.scope(false)
		eachOwn(n, s.Src, func(name, typ string) { g.declare(name, typeOf(typ, g.packages), true) })
		return func() { s.Closures--; restore() }
	case "block":
		// An if's body or else is a branch; other blocks run in sequence.
		parent := n.Parent()
		return g.scope(parent != nil && parent.Kind() == "if_statement")
	case "if_statement", "for_statement", "type_switch_statement", "select_statement":
		return g.scope(false)
	case "expression_case", "type_case", "communication_case", "default_case":
		return g.scope(true)
	}
	return nil
}

// isReceiver reports whether name is the method's receiver here, not a
// variable shadowing it.
func (g grammar) isReceiver(name string) bool {
	return g.receiver != "" && name == g.receiver && !g.isVariable(name)
}

// isVariable reports whether an open scope, the function's parameters
// and results included, declares a variable of that name, which shadows
// an imported package or the receiver.
func (g grammar) isVariable(name string) bool {
	for _, s := range *g.scopes {
		if s.declared[name] {
			return true
		}
	}
	return false
}

// varScope is a lexical scope: the variables declared in it and the
// outer ones assigned in it or in the scopes it holds.
type varScope struct {
	declared, assigned map[string]bool
}

// scope opens a lexical scope, closed by the function it returns. The
// variables declared in it are then forgotten and the ones they shadow
// restored. An outer variable assigned in it becomes unknown, as the
// scope may not have run (an if's body, a loop), except when the scope
// is a branch: the next branch (else, the next case) starts from the
// types before it, and the variable becomes unknown when the if or
// switch holding the branches ends.
func (g grammar) scope(branch bool) func() {
	outer := maps.Clone(g.vars)
	s := &varScope{declared: map[string]bool{}, assigned: map[string]bool{}}
	*g.scopes = append(*g.scopes, s)
	return func() {
		*g.scopes = (*g.scopes)[:len(*g.scopes)-1]
		restore := func(name string) {
			if typ, ok := outer[name]; ok {
				g.vars[name] = typ
			} else {
				delete(g.vars, name)
			}
		}
		for name := range s.declared {
			restore(name)
		}
		for name := range s.assigned {
			if s.declared[name] {
				continue
			}
			if n := len(*g.scopes); n > 0 {
				(*g.scopes)[n-1].assigned[name] = true
			}
			if branch {
				restore(name)
			} else {
				delete(g.vars, name)
			}
		}
	}
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
		switch operand.Kind() {
		case "identifier":
			switch name := operand.Utf8Text(src); {
			case g.isReceiver(name):
				c.Qualifier = "self"
			case g.packages[name] != "" && !g.isVariable(name):
				c.Kind, c.Qualifier = unit.PathCall, name
			default:
				c.Type = g.vars[name]
			}
		case "selector_expression":
			// s.store.Save(): a method of the receiver's field.
			if v, f := operand.ChildByFieldName("operand"), operand.ChildByFieldName("field"); v != nil && f != nil &&
				v.Kind() == "identifier" && g.isReceiver(v.Utf8Text(src)) {
				c.Field = f.Utf8Text(src)
			}
		}
	default:
		return c, false // function values, conversions
	}
	return c, true
}

// IdiomaticForms names the idiomatic form of each concern the adapter
// knows, which the map suggests where a codebase has no convention.
var IdiomaticForms = map[string]string{unit.ErrorChecks: "errors.Is/As", unit.ErrorWrapping: "%w"}

// errorCallForm returns the error form a call is, if any: errors.Is or
// errors.As checks an error, comparing err.Error() inside strings.Contains
// and its kin checks its message, and fmt.Errorf wraps one with %w or
// formats it with %v or %s.
func errorCallForm(c unit.Call, pkg string, n *sitter.Node, src []byte) unit.Form {
	form := unit.Form{Line: c.Line}
	args := n.ChildByFieldName("arguments")
	if c.Kind != unit.PathCall || args == nil {
		return form
	}
	switch {
	case pkg == "errors" && (c.Name == "Is" || c.Name == "As"):
		form.Concern, form.Form = unit.ErrorChecks, "errors.Is/As"
	case pkg == "strings" && (c.Name == "Contains" || c.Name == "HasPrefix" || c.Name == "HasSuffix" || c.Name == "EqualFold"):
		for i := uint(0); i < args.NamedChildCount(); i++ {
			if isErrorMessage(args.NamedChild(i), src) {
				form.Concern, form.Form = unit.ErrorChecks, "message"
			}
		}
	case pkg == "fmt" && c.Name == "Errorf" && args.NamedChildCount() > 1:
		verbs := formatVerbs(args.NamedChild(0).Utf8Text(src))
		formatsError := false
		for i := uint(1); i < args.NamedChildCount(); i++ {
			formatsError = formatsError || isErrorValue(args.NamedChild(i), src)
		}
		switch {
		case strings.ContainsRune(verbs, 'w'):
			form.Concern, form.Form = unit.ErrorWrapping, "%w"
		case formatsError && strings.ContainsAny(verbs, "vs"):
			form.Concern, form.Form = unit.ErrorWrapping, "%v"
		}
	}
	return form
}

// declare records a variable's type, or "" when unknown, in the current
// scope: declared there (var, :=), or assigned there (=).
func (g grammar) declare(name, typ string, declared bool) {
	if n := len(*g.scopes); n > 0 {
		in := (*g.scopes)[n-1].assigned
		if declared {
			in = (*g.scopes)[n-1].declared
		}
		in[name] = true
	}
	g.set(name, typ)
}

// set records a variable's type, or forgets the variable when its type is
// unknown ("").
func (g grammar) set(name, typ string) {
	if typ == "" {
		delete(g.vars, name)
		return
	}
	g.vars[name] = typ
}

// assign updates the variables an assignment, short declaration or range
// clause sets: one set from a composite literal (T{}, &T{}) takes its type,
// any other is forgotten.
func (g grammar) assign(n *sitter.Node, src []byte) {
	left, right := n.ChildByFieldName("left"), n.ChildByFieldName("right")
	if left == nil {
		return
	}
	// := declares; = assigns, also in a range clause.
	declares := n.Kind() == "short_var_declaration"
	for i := uint(0); i < n.ChildCount(); i++ {
		declares = declares || n.Child(i).Kind() == ":="
	}
	for i := uint(0); i < left.NamedChildCount(); i++ {
		name := left.NamedChild(i)
		if name.Kind() != "identifier" {
			continue
		}
		typ := ""
		if n.Kind() != "range_clause" && right != nil && right.NamedChildCount() == left.NamedChildCount() {
			typ = g.literalType(right.NamedChild(i), src)
		}
		g.declare(name.Utf8Text(src), typ, declares)
	}
}

// literalType returns the type of a composite literal value (T{}, &T{})
// as typeOf writes it, or "" for any other value.
func (g grammar) literalType(value *sitter.Node, src []byte) string {
	if value.Kind() == "unary_expression" && value.NamedChildCount() == 1 {
		value = value.NamedChild(0) // &T{}
	}
	if t := value.ChildByFieldName("type"); value.Kind() == "composite_literal" && t != nil {
		return typeOf(syntax.Compact(t.Utf8Text(src)), g.packages)
	}
	return ""
}

// typeOf returns a named type as Call.Type writes it, qualified by the
// file's imports, without pointer or type arguments: *st.DB gives
// "example.com/app/store.DB", Cache[T] gives "Cache". Predeclared types,
// types of unknown packages and other types (slices, maps, functions)
// give "".
func typeOf(typ string, packages map[string]string) string {
	typ = strings.TrimPrefix(typ, "*")
	for _, other := range []string{"[", "map[", "chan", "func(", "interface{", "struct{"} {
		if strings.HasPrefix(typ, other) {
			return ""
		}
	}
	if i := strings.IndexByte(typ, '['); i >= 0 {
		typ = typ[:i]
	}
	if pkg, name, ok := strings.Cut(typ, "."); ok {
		if packages[pkg] == "" {
			return ""
		}
		return packages[pkg] + "." + name
	}
	if predeclared[typ] {
		return ""
	}
	return typ
}

// predeclared are Go's predeclared types.
var predeclared = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "rune": true, "string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
}

// formatVerbs returns the verbs of a format string's directives, in
// order: %+v gives v and %[1]w gives w, while %% is a literal percent.
func formatVerbs(format string) string {
	var verbs strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0123456789.*[]", format[i]) >= 0 {
			i++ // flags, width, precision and argument indexes
		}
		if i < len(format) && format[i] != '%' {
			verbs.WriteByte(format[i])
		}
	}
	return verbs.String()
}

// errorComparison returns the error check an == or != comparison is: on
// err.Error() a check of the message, between an error value and anything
// but nil a check against a sentinel (err == io.EOF).
func errorComparison(n *sitter.Node, src []byte) unit.Form {
	form := unit.Form{Line: syntax.Line(n.StartPosition())}
	op := n.ChildByFieldName("operator")
	left, right := n.ChildByFieldName("left"), n.ChildByFieldName("right")
	if op == nil || left == nil || right == nil || (op.Kind() != "==" && op.Kind() != "!=") {
		return form
	}
	switch {
	case isErrorMessage(left, src) || isErrorMessage(right, src):
		form.Concern, form.Form = unit.ErrorChecks, "message"
	case (isErrorValue(left, src) && right.Kind() != "nil") || (isErrorValue(right, src) && left.Kind() != "nil"):
		form.Concern, form.Form = unit.ErrorChecks, "=="
	}
	return form
}

// isErrorValue reports whether n names an error by Go's naming habit: err,
// or a name ending in err or Err (readErr, ErrMissing).
func isErrorValue(n *sitter.Node, src []byte) bool {
	if n.Kind() != "identifier" {
		return false
	}
	name := n.Utf8Text(src)
	return strings.HasSuffix(name, "err") || strings.HasSuffix(name, "Err")
}

// isErrorMessage reports whether n is a call of Error() on an error value.
func isErrorMessage(n *sitter.Node, src []byte) bool {
	if n.Kind() != "call_expression" {
		return false
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Kind() != "selector_expression" {
		return false
	}
	operand, field := fn.ChildByFieldName("operand"), fn.ChildByFieldName("field")
	return operand != nil && field != nil && field.Utf8Text(src) == "Error" && isErrorValue(operand, src)
}

// Parse parses Go source and returns one unit per function or method, in
// source order, and its package-level declarations: structs and constants.
// Functions containing syntax errors are skipped, and declarations inside
// functions are not seen.
func Parse(path string, src []byte) ([]unit.Unit, unit.Decls, error) {
	var units []unit.Unit
	var decls unit.Decls
	var marked error
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
				for _, st := range structs(n, path, src, test) {
					decls.Structs = append(decls.Structs, st)
					for _, f := range st.Fields {
						if typ := typeOf(f.Type, packages); typ != "" {
							decls.FieldTypes = append(decls.FieldTypes, unit.FieldType{Struct: st.Name, Field: f.Name, Type: typ, Test: test})
						}
					}
				}
				for _, ft := range funcTypes(n, src) {
					ft.File, ft.Test = path, test
					decls.FuncTypes = append(decls.FuncTypes, ft)
				}
			}
		}
		marked = syntax.MarkIgnored(root, src, units, &decls)
	})
	return units, decls, errors.Join(err, marked)
}

// function makes a unit of a function or method declaration.
func function(n *sitter.Node, path string, src []byte, test bool, packages map[string]string) (unit.Unit, bool) {
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
	// The parameters and named results are the function's own scope.
	vars, own := map[string]string{}, map[string]bool{}
	eachOwn(n, src, func(name, typ string) {
		own[name] = true
		if t := typeOf(typ, packages); t != "" {
			vars[name] = t
		}
	})
	s := syntax.Flatten(n, src, grammar{receiver: receiverName(n, src), packages: packages, vars: vars,
		scopes: &[]*varScope{{declared: own, assigned: map[string]bool{}}}})
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
		Params:    params(n, src),
		Types:     types(n, src),
		Forms:     s.Forms,

		StringMatches: s.Matches,
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
func imports(root *sitter.Node, src []byte) map[string]string {
	names := map[string]string{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Kind() == "import_spec" {
			path := n.ChildByFieldName("path")
			if path == nil {
				return
			}
			imported := strings.Trim(path.Utf8Text(src), "\"`")
			if alias := n.ChildByFieldName("name"); alias != nil {
				names[alias.Utf8Text(src)] = imported
			} else {
				parts := strings.Split(imported, "/")
				last := parts[len(parts)-1]
				if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
					last = parts[len(parts)-2]
				}
				names[last] = imported
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

// params returns a function's parameters that could be grouped, the
// receiver aside: a, b int gives two.
func params(fn *sitter.Node, src []byte) []unit.Param {
	var out []unit.Param
	eachParam(fn.ChildByFieldName("parameters"), src, func(name, typ string) {
		if name != "" && typ != contextType {
			out = append(out, unit.Param{Name: name, Type: typ})
		}
	})
	return out
}

// contextType is never grouped with other parameters: Go passes it
// explicitly and never stores it in a struct.
const contextType = "context.Context"

// types returns the parameter and result types of a function or function
// type, named or not: (ctx context.Context, a, b string) (n int, err
// error) gives "(context.Context, string, string) (int, error)".
func types(fn *sitter.Node, src []byte) string {
	var params, results []string
	eachParam(fn.ChildByFieldName("parameters"), src, func(_, t string) { params = append(params, t) })
	switch r := fn.ChildByFieldName("result"); {
	case r == nil:
	case r.Kind() == "parameter_list":
		eachParam(r, src, func(_, t string) { results = append(results, t) })
	default:
		results = []string{syntax.Compact(r.Utf8Text(src))}
	}
	return "(" + strings.Join(params, ", ") + ") (" + strings.Join(results, ", ") + ")"
}

// funcTypes returns a type declaration's function types, declared or
// aliased (type visit = func(path string)), without file.
func funcTypes(n *sitter.Node, src []byte) []unit.FuncType {
	var out []unit.FuncType
	for i := uint(0); i < n.NamedChildCount(); i++ {
		spec := n.NamedChild(i)
		typ := spec.ChildByFieldName("type")
		if (spec.Kind() == "type_spec" || spec.Kind() == "type_alias") && typ != nil && typ.Kind() == "function_type" {
			out = append(out, unit.FuncType{Types: types(typ, src)})
		}
	}
	return out
}

// eachOwn calls each with the named parameters and named results of a
// function or function literal, with their types as written.
func eachOwn(fn *sitter.Node, src []byte, each func(name, typ string)) {
	for _, list := range []*sitter.Node{fn.ChildByFieldName("parameters"), fn.ChildByFieldName("result")} {
		if list == nil || list.Kind() != "parameter_list" {
			continue
		}
		eachParam(list, src, func(name, typ string) {
			if name != "" {
				each(name, typ)
			}
		})
	}
}

// eachParam calls each with the name ("" when unnamed) and type of every
// entry of a parameter or result list, a, b int giving two; a variadic
// type is ...T, as written.
func eachParam(list *sitter.Node, src []byte, each func(name, typ string)) {
	for i := uint(0); list != nil && i < list.NamedChildCount(); i++ {
		p := list.NamedChild(i)
		typ := p.ChildByFieldName("type")
		if typ == nil {
			continue
		}
		t := syntax.Compact(typ.Utf8Text(src))
		if p.Kind() == "variadic_parameter_declaration" {
			t = "..." + t
		}
		names := fieldNodes(p, "name")
		if len(names) == 0 {
			each("", t)
		}
		for _, name := range names {
			each(name.Utf8Text(src), t)
		}
	}
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

// InterfaceMethods returns the names of the methods the file's interfaces
// declare, in source order: in generated code (a server interface from an
// OpenAPI spec), the entry points of features. Embedded interfaces add
// none.
func InterfaceMethods(src []byte) []string {
	var names []string
	_ = syntax.Parse(language, src, func(root *sitter.Node) {
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Kind() == "method_elem" || n.Kind() == "method_spec" {
				if name := n.ChildByFieldName("name"); name != nil {
					names = append(names, name.Utf8Text(src))
				}
				return
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				walk(n.NamedChild(i))
			}
		}
		walk(root)
	})
	return names
}

// ModulePath returns the module path a go.mod declares, or "".
func ModulePath(gomod []byte) string {
	for line := range strings.Lines(string(gomod)) {
		line, _, _ = strings.Cut(line, "//")
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(path), "\"`")
		}
	}
	return ""
}
