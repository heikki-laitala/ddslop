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
	var refs []unit.Reference
	_ = syntax.Parse(language, src, func(root *sitter.Node) {
		ignored, _ := syntax.Ignored(root, src)
		r := referrer{src: src, ignored: ignored}
		r.walk(root, at{test: IsTestPath(path)})
		refs = r.refs
	})
	return refs
}

type referrer struct {
	src     []byte
	ignored map[int]bool // lines ddslop:ignore markers apply to
	refs    []unit.Reference
}

// at is what holds where a path is written: the inline module, the type
// or trait whose items enclose it, the item, and whether that is test
// code or marked ddslop:ignore.
type at struct {
	module, owner, item string
	test, ignored       bool
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
		r.pathFrom(n, c)
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
		c.item = "use"
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

// pathFrom records the path that root (crate, super or self) begins, if
// it begins one: self is also a method's receiver, and crate a
// visibility.
func (r *referrer) pathFrom(root *sitter.Node, c at) {
	segments := []string{root.Kind()}
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
			r.add(segments, root, c, leaf)
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
		root := tok.Kind()
		if root != "crate" && root != "super" && root != "self" {
			continue
		}
		if i > 0 && (toks[i-1].Kind() == "$" || toks[i-1].Kind() == "::") {
			continue
		}
		segments := []string{root}
		for j := i + 1; j+1 < len(toks) && toks[j].Kind() == "::"; j += 2 {
			next := toks[j+1]
			segments = append(segments, next.Utf8Text(r.src))
			if next.Kind() != "super" {
				r.add(segments, tok, c, j+2 >= len(toks) || toks[j+2].Kind() != "::")
				break
			}
		}
	}
}

func (r *referrer) add(segments []string, n *sitter.Node, c at, leaf bool) {
	line := syntax.Line(n.StartPosition())
	r.refs = append(r.refs, unit.Reference{
		Path: strings.Join(segments, "::"), Line: line, Module: c.module, Item: c.item, Leaf: leaf,
		Test: c.test, Ignored: c.ignored || r.ignored[line],
	})
}

// joinPath appends name to a :: path.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "::" + name
}
