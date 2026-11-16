package typescript

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// imported is where a name a file imports comes from: the module as
// written ("./store", "node:fs/promises") and the name the module exports
// it under, or "" for a namespace import (import * as db).
type imported struct {
	module, name string
}

// imports returns the names a file's imports bind. A default import is
// taken under its local name, which usually is the exported function's.
func imports(root *sitter.Node, src []byte) map[string]imported {
	names := map[string]imported{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		source := n.ChildByFieldName("source")
		if n.Kind() != "import_statement" || source == nil {
			continue
		}
		module := strings.Trim(source.Utf8Text(src), `"'`)
		for j := uint(0); j < n.NamedChildCount(); j++ {
			if clause := n.NamedChild(j); clause.Kind() == "import_clause" {
				importClause(clause, module, src, names)
			}
		}
	}
	return names
}

// importClause adds the names one import clause binds: a default import,
// a namespace import and named imports, aliases understood.
func importClause(clause *sitter.Node, module string, src []byte, names map[string]imported) {
	for i := uint(0); i < clause.NamedChildCount(); i++ {
		switch c := clause.NamedChild(i); c.Kind() {
		case "identifier":
			names[c.Utf8Text(src)] = imported{module: module, name: c.Utf8Text(src)}
		case "namespace_import":
			if c.NamedChild(0) != nil {
				names[c.NamedChild(0).Utf8Text(src)] = imported{module: module}
			}
		case "named_imports":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				spec := c.NamedChild(j)
				name, alias := spec.ChildByFieldName("name"), spec.ChildByFieldName("alias")
				if name == nil {
					continue
				}
				local := name
				if alias != nil {
					local = alias
				}
				names[local.Utf8Text(src)] = imported{module: module, name: name.Utf8Text(src)}
			}
		}
	}
}

// call classifies a call by its callee: an imported function, or one
// through a namespace import (db.query), is a path call qualified by its
// module; any other plain name a free call; a call on this a self call,
// one on a field of this (this.store.get) a method call on that field,
// and any other a method call.
// ddslop:ignore the Go adapter's classification over TypeScript's node kinds; each changes with its own grammar
func (g grammar) call(n *sitter.Node, src []byte) (unit.Call, bool) {
	fn := n.ChildByFieldName("function")
	c := unit.Call{Line: syntax.Line(n.StartPosition())}
	switch {
	case fn == nil:
		return c, false
	case fn.Kind() == "identifier":
		c.Kind, c.Name = unit.FreeCall, fn.Utf8Text(src)
		if im, ok := g.imports[c.Name]; ok && im.name != "" {
			c.Kind, c.Qualifier, c.Name = unit.PathCall, im.module, im.name
		}
	case fn.Kind() == "member_expression":
		object, property := fn.ChildByFieldName("object"), fn.ChildByFieldName("property")
		if object == nil || property == nil {
			return c, false
		}
		c.Kind, c.Name = unit.MethodCall, property.Utf8Text(src)
		switch object.Kind() {
		case "this":
			c.Qualifier = "self"
		case "identifier":
			if im, ok := g.imports[object.Utf8Text(src)]; ok && im.name == "" {
				c.Kind, c.Qualifier = unit.PathCall, im.module
			}
		case "member_expression":
			if this, field := object.ChildByFieldName("object"), object.ChildByFieldName("property"); this != nil && field != nil && this.Kind() == "this" {
				c.Field = field.Utf8Text(src)
			}
		}
	default:
		return c, false // a called call's result, an immediately invoked function
	}
	return c, true
}

// forwards returns the callee when a function's whole body is one call,
// returned or not, passing the function's parameters on in order:
// function f(a, b) { return g(a, b); }, or (a) => g(a). Calls on fields
// (this.inner.get(x)) do not count, and nor do functions without
// parameters or with a rest, destructured or defaulted one (opts = {}),
// nor async functions and generators, whose callers get a promise or an
// iterator, not the call's value.
func forwards(fn *sitter.Node, src []byte) string {
	ps, plain := params(fn, src)
	expr := fn.ChildByFieldName("body")
	if !plain || len(ps) == 0 || expr == nil || asyncOrGenerator(fn) {
		return ""
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	if expr.Kind() == "statement_block" {
		if expr.NamedChildCount() != 1 {
			return ""
		}
		switch stmt := expr.NamedChild(0); stmt.Kind() {
		case "return_statement", "expression_statement":
			expr = stmt.NamedChild(0)
		default:
			return ""
		}
	}
	if expr == nil || expr.Kind() != "call_expression" {
		return ""
	}
	callee := expr.ChildByFieldName("function")
	if callee == nil || !syntax.PassesOn(expr.ChildByFieldName("arguments"), names, src, noBorrow) {
		return ""
	}
	switch callee.Kind() {
	case "identifier":
	case "member_expression":
		if object := callee.ChildByFieldName("object"); object == nil || (object.Kind() != "identifier" && object.Kind() != "this") {
			return "" // delegation to a field
		}
	default:
		return ""
	}
	return callee.Utf8Text(src)
}

// asyncOrGenerator reports whether a function is async or a generator,
// by its async or * token.
func asyncOrGenerator(fn *sitter.Node) bool {
	for i := uint(0); i < fn.ChildCount(); i++ {
		if k := fn.Child(i).Kind(); k == "async" || k == "*" {
			return true
		}
	}
	return false
}

// params returns a function's parameters that bind one name, in order,
// each with its type as written ("" for none), and whether all are plain
// names: false when a rest or destructured parameter was left out, or
// one has a default value (opts = {}), which its callers then need not
// pass. A this parameter is no parameter.
func params(fn *sitter.Node, src []byte) ([]unit.Param, bool) {
	if p := fn.ChildByFieldName("parameter"); p != nil {
		return []unit.Param{{Name: p.Utf8Text(src)}}, true // x => g(x)
	}
	list := fn.ChildByFieldName("parameters")
	if list == nil {
		return nil, false
	}
	var out []unit.Param
	plain := true
	for i := uint(0); i < list.NamedChildCount(); i++ {
		p := list.NamedChild(i)
		pattern, typ := p.ChildByFieldName("pattern"), p.ChildByFieldName("type")
		switch {
		case pattern != nil && pattern.Kind() == "this":
		case (p.Kind() != "required_parameter" && p.Kind() != "optional_parameter") || pattern == nil || pattern.Kind() != "identifier":
			plain = false
		default:
			plain = plain && p.ChildByFieldName("value") == nil
			param := unit.Param{Name: pattern.Utf8Text(src)}
			if typ != nil && typ.NamedChild(0) != nil {
				param.Type = syntax.Compact(typ.NamedChild(0).Utf8Text(src))
			}
			out = append(out, param)
		}
	}
	return out, plain
}

// noBorrow: TypeScript passes arguments as they are.
func noBorrow(*sitter.Node, []byte) *sitter.Node { return nil }
