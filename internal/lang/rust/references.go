package rust

import (
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// References returns the paths a file writes that name a module of its
// crate (see unit.Reference), in source order: in code, in types, in use
// declarations and in macro arguments. A visibility such as
// pub(in crate::a) is no dependency, and is left out.
func References(path string, src []byte) []unit.Reference {
	return ReferencesAs(syntax.Source{Path: path, Src: src, Test: IsTestPath(path)})
}

// ReferencesAs is References with whether the file is test code by its
// place given (see ParseAs).
func ReferencesAs(file syntax.Source) []unit.Reference {
	var refs []unit.Reference
	_ = syntax.Parse(language, file.Src, func(root *sitter.Node) {
		ignored, _ := syntax.Ignored(root, file.Src)
		r := referrer{src: file.Src, ignored: ignored, aliases: aliasesIn(root, file.Src)}
		r.walk(root, at{test: file.Test})
		refs = r.refs
	})
	return refs
}

type referrer struct {
	src     []byte
	ignored map[int]bool // lines ddslop:ignore markers apply to
	// aliases are the names declarations bind in their scopes: a
	// root-only path's (use crate as root), or another's, which hides one.
	aliases []alias
	refs    []unit.Reference
}

// at is what holds where a path is written: the inline module, the type
// or trait whose items enclose it, the item, and whether that is test
// code or marked ddslop:ignore.
type at struct {
	module, owner, item string
	test, ignored       bool
	// inUse is true in a use declaration, whose path imports every
	// namespace of its name: no value.
	inUse bool
	// relative is true for a path beginning with a name the file does not
	// bind (see unit.Reference.Relative).
	relative bool
}

// walk visits n. A marker applies to the code starting on its line, and
// so to every path inside it.
func (r *referrer) walk(n *sitter.Node, c at) {
	c.ignored = c.ignored || r.ignored[syntax.Line(n.StartPosition())]
	name := func() string {
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(r.src)
		}
		return ""
	}
	switch n.Kind() {
	case "visibility_modifier":
		return
	case "crate", "super", "self":
		r.pathFrom(n, []string{n.Kind()}, c)
		return
	case "identifier":
		// the start of a path, or what use root::* and use root as r import
		if parent := n.Parent(); parent == nil || !isPathOf(parent, n) && parent.Kind() != "use_wildcard" {
			return
		}
		name := n.Utf8Text(r.src)
		if segments, bound := r.aliasAt(name, c.module, n); bound {
			if segments != nil { // root::foo::Y, with use crate as root in scope
				r.pathFrom(n, slices.Clone(segments), c)
			}
			return
		}
		if inUseList(n) {
			return // a::X in use crate::{a::X} is the list's, not a name of its own
		}
		// a name this file does not bind: an alias of the crate declared
		// at its root (extern crate self as root) may be
		c.relative = true
		r.pathFrom(n, []string{name}, c)
		return
	case "extern_crate_declaration":
		if name, alias := n.ChildByFieldName("name"), n.ChildByFieldName("alias"); name != nil && alias != nil && name.Utf8Text(r.src) == "self" &&
			n.Parent() != nil && n.Parent().Kind() == "source_file" { // the crate root's own, not a block's
			r.refs = append(r.refs, unit.Reference{Path: "crate", Line: syntax.Line(n.StartPosition()), Item: "extern crate",
				Alias: alias.Utf8Text(r.src), Test: c.test, Ignored: c.ignored})
		}
		return
	case "token_tree":
		r.tokens(n, c)
		return
	case "mod_item":
		if n.ChildByFieldName("body") != nil {
			c.module, c.owner = joinPath(c.module, name()), ""
		}
	case "impl_item":
		if t := n.ChildByFieldName("type"); t != nil {
			c.owner = t.Utf8Text(r.src)
			c.item = c.owner
		}
	case "trait_item":
		c.owner = name()
		c.item = c.owner
	case "function_item":
		c.item = joinPath(c.owner, name())
	case "const_item", "static_item", "struct_item", "enum_item", "union_item", "type_item":
		c.item = name()
	case "use_declaration":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if n.NamedChild(i).Kind() == "visibility_modifier" {
				return // pub use re-exports, as a crate's facade does: no dependency
			}
		}
		c.item, c.inUse = "use", true
	}
	var attrs []string
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child.Kind() == "attribute_item" {
			attrs = append(attrs, child.Utf8Text(r.src))
			continue
		}
		if child.IsExtra() {
			continue
		}
		childAt := c
		childAt.test = c.test || slices.ContainsFunc(attrs, isCfgTest) ||
			child.Kind() == "function_item" && slices.ContainsFunc(attrs, isTestAttr)
		r.walk(child, childAt)
		attrs = nil
	}
}

// pathFrom records the path that root (crate, super, self, or an alias of
// one, whose segments start segments) begins, if it begins one: self is
// also a method's receiver, and crate a visibility.
func (r *referrer) pathFrom(root *sitter.Node, segments []string, c at) {
	cur := root
	for {
		parent := cur.Parent()
		if parent != nil && parent.Kind() == "use_as_clause" && parent.Parent() != nil && parent.Parent().Kind() == "use_list" {
			return // a::{self as b} names the list's prefix, which useList records
		}
		if parent != nil && (parent.Kind() == "use_wildcard" || parent.Kind() == "use_as_clause" && isPathOf(parent, cur)) {
			// use crate::* imports that module's items, and use crate as
			// root names it
			r.add(segments, root, c, false)
			return
		}
		if parent == nil || !isPathOf(parent, cur) {
			return
		}
		if parent.Kind() == "scoped_use_list" {
			// use crate::{a::X, b::Y} names a and b
			r.useList(parent, segments, c)
			return
		}
		name := parent.ChildByFieldName("name")
		if name == nil {
			return
		}
		segments = append(segments, name.Utf8Text(r.src))
		if name.Kind() != "super" {
			outer := parent.Parent()
			// a::b::C and a::b::* enter b; a::b and a::b as c end there
			leaf := outer == nil || outer.Kind() == "use_as_clause" ||
				outer.Kind() != "use_wildcard" && !isPathOf(outer, parent)
			r.addMacro(segments, root, c, leaf, outer != nil && outer.Kind() == "macro_invocation")
			return
		}
		cur = parent
	}
}

// isPathOf reports whether child is parent's path: the a::b of a::b::c,
// or the prefix of a use list.
func isPathOf(parent, child *sitter.Node) bool {
	switch parent.Kind() {
	case "scoped_identifier", "scoped_type_identifier", "scoped_use_list", "use_as_clause":
		path := parent.ChildByFieldName("path")
		return path != nil && sameNode(path, child)
	}
	return false
}

// useList records the module each item of a use list rooted at prefix
// names: its first segment.
func (r *referrer) useList(list *sitter.Node, prefix []string, c at) {
	items := list.ChildByFieldName("list")
	if items == nil {
		return
	}
	for i := uint(0); i < items.NamedChildCount(); i++ {
		item := items.NamedChild(i)
		if first := firstSegment(item, r.src); first != "" {
			r.add(append(slices.Clone(prefix), first), item, c, endsAtFirst(item))
		} else if namesPrefix(item) {
			r.add(prefix, item, c, false)
		}
	}
}

// endsAtFirst reports whether a use list entry is its first segment
// alone: foo, or foo as bar.
func endsAtFirst(item *sitter.Node) bool {
	if item.Kind() == "use_as_clause" {
		item = item.ChildByFieldName("path")
	}
	return item != nil && item.Kind() == "identifier"
}

// namesPrefix reports whether a use list entry imports its list's prefix
// itself: self, self as x, or *.
func namesPrefix(item *sitter.Node) bool {
	switch item.Kind() {
	case "self":
		return true
	case "use_as_clause":
		path := item.ChildByFieldName("path")
		return path != nil && path.Kind() == "self"
	case "use_wildcard":
		return item.NamedChildCount() == 0
	}
	return false
}

// firstSegment returns the first name of a use tree: a of a::b, a::b as c
// or a::{b, c}; "" for self and wildcards.
func firstSegment(n *sitter.Node, src []byte) string {
	var inner *sitter.Node
	switch n.Kind() {
	case "identifier":
		return n.Utf8Text(src)
	case "scoped_identifier", "scoped_use_list", "use_as_clause":
		inner = n.ChildByFieldName("path")
	case "use_wildcard":
		inner = n.NamedChild(0)
	}
	if inner == nil {
		return ""
	}
	return firstSegment(inner, src)
}

// tokens records the paths written in a macro's arguments, which the
// grammar leaves as tokens: crate::a::b reads crate, ::, a. $crate in a
// macro definition is a template, not a path of this module.
func (r *referrer) tokens(tree *sitter.Node, c at) {
	var toks []*sitter.Node
	for i := uint(0); i < tree.ChildCount(); i++ {
		toks = append(toks, tree.Child(i))
	}
	for i, tok := range toks {
		if tok.Kind() == "token_tree" {
			inner := c
			inner.ignored = c.ignored || r.ignored[syntax.Line(tok.StartPosition())]
			r.tokens(tok, inner)
			continue
		}
		var segments []string
		relative := false
		switch tok.Kind() {
		case "crate", "super", "self":
			segments = []string{tok.Kind()}
		case "identifier":
			if bound, ok := r.aliasAt(tok.Utf8Text(r.src), c.module, tok); ok {
				segments = slices.Clone(bound)
			} else if i+1 < len(toks) && toks[i+1].Kind() == "::" {
				// a name the file does not bind, as in a structured path
				segments, relative = []string{tok.Utf8Text(r.src)}, true
			}
		}
		if segments == nil || i > 0 && (toks[i-1].Kind() == "$" || toks[i-1].Kind() == "::") {
			continue
		}
		for j := i + 1; j+1 < len(toks) && toks[j].Kind() == "::"; j += 2 {
			next := toks[j+1]
			segments = append(segments, next.Utf8Text(r.src))
			if next.Kind() != "super" {
				after := ""
				if j+2 < len(toks) {
					after = toks[j+2].Kind()
				}
				tc := c
				tc.relative = relative
				r.addMacro(segments, tok, tc, after != "::", after == "!")
				break
			}
		}
	}
}

func (r *referrer) add(segments []string, n *sitter.Node, c at, leaf bool) {
	r.addMacro(segments, n, c, leaf, false)
}

// addMacro records a path, macro telling whether it is a macro's.
func (r *referrer) addMacro(segments []string, n *sitter.Node, c at, leaf, macro bool) {
	line := syntax.Line(n.StartPosition())
	r.refs = append(r.refs, unit.Reference{
		Path: strings.Join(segments, "::"), Line: line, Module: c.module, Item: c.item,
		Leaf: leaf && !c.inUse, Macro: macro, Relative: c.relative,
		Test: c.test, Ignored: c.ignored || r.ignored[line],
	})
}

// inUseList reports whether n is inside a use list's entry, whose paths
// continue the list's prefix.
func inUseList(n *sitter.Node) bool {
	for p := n.Parent(); p != nil && p.Kind() != "use_declaration"; p = p.Parent() {
		if p.Kind() == "use_list" {
			return true
		}
	}
	return false
}

// alias is a name a declaration binds within a scope: a block, a module's
// body or the file. segments is the root-only path it names (use crate as
// root), or nil for any other path, which hides an alias of the name from
// an outer scope.
type alias struct {
	name, module string
	segments     []string
	start, end   uint
}

// aliasesIn returns the names use and extern crate declarations bind, with
// the scope each holds in: the inline module and the byte range of the
// block, module body or file declaring it.
func aliasesIn(root *sitter.Node, src []byte) []alias {
	var out []alias
	var walk func(n *sitter.Node, module string)
	walk = func(n *sitter.Node, module string) {
		scope := n.Parent()
		for scope != nil && scope.Kind() != "block" && scope.Kind() != "declaration_list" && scope.Kind() != "source_file" {
			scope = scope.Parent()
		}
		bind := func(name string, segments []string) {
			if scope != nil {
				out = append(out, alias{name: name, module: module, segments: segments, start: scope.StartByte(), end: scope.EndByte()})
			}
		}
		switch n.Kind() {
		case "mod_item":
			if name := n.ChildByFieldName("name"); name != nil {
				// foo::Y enters the module foo, and a module root hides an
				// alias root
				bind(name.Utf8Text(src), []string{"self", name.Utf8Text(src)})
				if n.ChildByFieldName("body") != nil {
					module = joinPath(module, name.Utf8Text(src))
				}
			}
		case "struct_item", "enum_item", "union_item", "trait_item", "type_item":
			if name := n.ChildByFieldName("name"); name != nil {
				bind(name.Utf8Text(src), nil)
			}
		case "type_parameters":
			// a generic parameter hides an alias of its name in its item
			if item := n.Parent(); item != nil {
				for i := uint(0); i < n.NamedChildCount(); i++ {
					p := n.NamedChild(i)
					if name := p.ChildByFieldName("name"); name != nil && (p.Kind() == "type_parameter" || p.Kind() == "const_parameter") {
						out = append(out, alias{name: name.Utf8Text(src), module: module, start: item.StartByte(), end: item.EndByte()})
					}
				}
			}
			return
		case "use_declaration":
			if arg := n.ChildByFieldName("argument"); arg != nil {
				imported := map[string]string{}
				collectUse(arg, "", src, imported)
				for name, path := range imported {
					bind(name, rootSegments(strings.TrimSuffix(path, "::self")))
				}
			}
			return
		case "extern_crate_declaration":
			if name, alias := n.ChildByFieldName("name"), n.ChildByFieldName("alias"); name != nil && alias != nil {
				var segments []string
				if name.Utf8Text(src) == "self" {
					segments = []string{"crate"}
				}
				bind(alias.Utf8Text(src), segments)
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), module)
		}
	}
	walk(root, "")
	return out
}

// aliasAt returns what name is bound to where n is written, in module: the
// root-only path of the innermost declaration binding it in scope, nil
// for a binding of another path, and false when nothing binds it.
func (r *referrer) aliasAt(name, module string, n *sitter.Node) ([]string, bool) {
	var found *alias
	for i, a := range r.aliases {
		if a.name == name && a.module == module && a.start <= n.StartByte() && n.EndByte() <= a.end &&
			(found == nil || a.end-a.start < found.end-found.start) {
			found = &r.aliases[i]
		}
	}
	if found == nil {
		return nil, false
	}
	return found.segments, true
}

// rootSegments returns the segments of a root-only path written as text,
// crate, self, super or super::super, or nil for any other.
func rootSegments(path string) []string {
	segments := strings.Split(path, "::")
	switch segments[0] {
	case "crate", "self", "super":
	default:
		return nil
	}
	for _, s := range segments[1:] {
		if s != "super" {
			return nil
		}
	}
	return segments
}

// joinPath appends name to a :: path.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "::" + name
}
