package typescript

import (
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// IdiomaticForms names the idiomatic form of each concern the adapter
// knows, which the map suggests where a codebase has no convention: an
// error checked by its class or a stable field, and an error passed on as
// the cause of a new one (ES2022).
var IdiomaticForms = map[string]string{unit.ErrorChecks: "instanceof/code", unit.ErrorWrapping: "cause"}

// errorForm returns the error form a node is, if any. An error is checked
// by its class (err instanceof NotFoundError) or a stable field compared
// (err.code === "ENOENT", a DOMException's err.name === "AbortError"),
// one form as Go's errors.Is and errors.As are, or by its message, compared, searched
// (err.message.includes) or matched (/x/.test(err.message)); a new error,
// of a class named like one, wraps another as its { cause } or spells it
// into its message (err.message, String(err), err in a template or a +).
// isError says which names hold errors.
func errorForm(n *sitter.Node, src []byte, isError func(name string) bool) unit.Form {
	form := unit.Form{Line: syntax.Line(n.StartPosition())}
	errorField := func(n *sitter.Node, fields ...string) bool {
		if n == nil || n.Kind() != "member_expression" {
			return false
		}
		object, property := n.ChildByFieldName("object"), n.ChildByFieldName("property")
		return object != nil && property != nil && isErrorName(object, src, isError) && slices.Contains(fields, property.Utf8Text(src))
	}
	switch n.Kind() {
	case "binary_expression":
		op, left, right := n.ChildByFieldName("operator"), n.ChildByFieldName("left"), n.ChildByFieldName("right")
		if op == nil || left == nil || right == nil {
			break
		}
		switch op.Kind() {
		case "instanceof":
			if isErrorName(left, src, isError) {
				form.Concern, form.Form = unit.ErrorChecks, "instanceof/code"
			}
		case "==", "===", "!=", "!==":
			switch {
			case errorField(left, "code", "name") || errorField(right, "code", "name"):
				form.Concern, form.Form = unit.ErrorChecks, "instanceof/code"
			case errorField(left, "message") || errorField(right, "message"):
				form.Concern, form.Form = unit.ErrorChecks, "message"
			}
		}
	case "call_expression":
		callee, args := n.ChildByFieldName("function"), n.ChildByFieldName("arguments")
		if callee == nil || callee.Kind() != "member_expression" || args == nil {
			break
		}
		method := callee.ChildByFieldName("property").Utf8Text(src)
		switch {
		case slices.Contains([]string{"includes", "startsWith", "endsWith", "match", "indexOf", "search"}, method) &&
			errorField(callee.ChildByFieldName("object"), "message"):
			form.Concern, form.Form = unit.ErrorChecks, "message"
		case method == "test" && args.NamedChildCount() > 0 && errorField(args.NamedChild(0), "message"):
			form.Concern, form.Form = unit.ErrorChecks, "message"
		}
	case "new_expression":
		constructor, args := n.ChildByFieldName("constructor"), n.ChildByFieldName("arguments")
		if constructor == nil || args == nil || !strings.HasSuffix(constructor.Utf8Text(src), "Error") {
			break
		}
		for i := uint(0); i < args.NamedChildCount(); i++ {
			if arg := args.NamedChild(i); hasCause(arg, src) {
				form.Concern, form.Form = unit.ErrorWrapping, "cause"
				return form
			}
		}
		if spellsError(args, src, isError) {
			form.Concern, form.Form = unit.ErrorWrapping, "message"
		}
	}
	return form
}

// isErrorName reports whether n is a name holding an error.
func isErrorName(n *sitter.Node, src []byte, isError func(name string) bool) bool {
	return n != nil && n.Kind() == "identifier" && isError(n.Utf8Text(src))
}

// hasCause reports whether n is an object literal with a cause property.
func hasCause(n *sitter.Node, src []byte) bool {
	if n.Kind() != "object" {
		return false
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		p := n.NamedChild(i)
		key := p
		if p.Kind() == "pair" {
			key = p.ChildByFieldName("key")
		}
		if key != nil && (key.Kind() == "property_identifier" || key.Kind() == "shorthand_property_identifier") && key.Utf8Text(src) == "cause" {
			return true
		}
	}
	return false
}

// spellsError reports whether an error's text goes into n: its message,
// String(err), err.toString(), or err itself in a template or a +.
func spellsError(n *sitter.Node, src []byte, isError func(name string) bool) bool {
	switch n.Kind() {
	case "member_expression":
		if p := n.ChildByFieldName("property"); p != nil && p.Utf8Text(src) == "message" && isErrorName(n.ChildByFieldName("object"), src, isError) {
			return true
		}
	case "call_expression":
		callee, args := n.ChildByFieldName("function"), n.ChildByFieldName("arguments")
		if callee != nil && callee.Utf8Text(src) == "String" && args != nil && args.NamedChildCount() == 1 && isErrorName(args.NamedChild(0), src, isError) {
			return true
		}
		if callee != nil && callee.Kind() == "member_expression" && callee.ChildByFieldName("property").Utf8Text(src) == "toString" &&
			isErrorName(callee.ChildByFieldName("object"), src, isError) {
			return true
		}
	case "template_substitution":
		if n.NamedChildCount() == 1 && isErrorName(n.NamedChild(0), src, isError) {
			return true
		}
	case "binary_expression":
		if op := n.ChildByFieldName("operator"); op != nil && op.Kind() == "+" &&
			(isErrorName(n.ChildByFieldName("left"), src, isError) || isErrorName(n.ChildByFieldName("right"), src, isError)) {
			return true
		}
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if spellsError(n.NamedChild(i), src, isError) {
			return true
		}
	}
	return false
}
