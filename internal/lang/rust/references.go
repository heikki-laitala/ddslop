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
		r.walk(root, "", IsTestPath(path), false)
		refs = r.refs
	})
	return refs
}

type referrer struct {
	src     []byte
	ignored map[int]bool // lines ddslop:ignore markers apply to
	refs    []unit.Reference
}

// walk visits n, module being the inline module it is in and test and
// ignored what holds for its enclosing code. A marker applies to the
// code starting on its line, and so to every path inside it.
func (r *referrer) walk(n *sitter.Node, module string, test, ignored bool) {
	ignored = ignored || r.ignored[syntax.Line(n.StartPosition())]
	switch n.Kind() {
	case "visibility_modifier":
		return
	case "crate", "super", "self":
		r.pathFrom(n, module, test, ignored)
		return
	case "token_tree":
		r.tokens(n, module, test, ignored)
		return
	case "mod_item":
		if name := n.ChildByFieldName("name"); name != nil && n.ChildByFieldName("body") != nil {
			module = joinPath(module, name.Utf8Text(r.src))
		}
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
		childTest := test || slices.ContainsFunc(attrs, isCfgTest) ||
			child.Kind() == "function_item" && slices.ContainsFunc(attrs, isTestAttr)
		r.walk(child, module, childTest, ignored)
		attrs = nil
	}
}

// pathFrom records the path that root (crate, super or self) begins, if
// it begins one: self is also a method's receiver, and crate a
// visibility.
func (r *referrer) pathFrom(root *sitter.Node, module string, test, ignored bool) {
	segments := []string{root.Kind()}
	cur := root
	for {
		parent := cur.Parent()
		if parent == nil || !isPathOf(parent, cur) {
			return
		}
		if parent.Kind() == "scoped_use_list" {
			// use crate::{a::X, b::Y} names a and b
			r.useList(parent, segments, module, test, ignored)
			return
		}
		name := parent.ChildByFieldName("name")
		if name == nil {
			return
		}
		segments = append(segments, name.Utf8Text(r.src))
		if name.Kind() != "super" {
			r.add(segments, root, module, test, ignored)
			return
		}
		cur = parent
	}
}

// isPathOf reports whether child is parent's path: the a::b of a::b::c,
// or the prefix of a use list.
func isPathOf(parent, child *sitter.Node) bool {
	switch parent.Kind() {
	case "scoped_identifier", "scoped_type_identifier", "scoped_use_list":
		path := parent.ChildByFieldName("path")
		return path != nil && sameNode(path, child)
	}
	return false
}

// useList records the module each item of a use list rooted at prefix
// names: its first segment.
func (r *referrer) useList(list *sitter.Node, prefix []string, module string, test, ignored bool) {
	items := list.ChildByFieldName("list")
	if items == nil {
		return
	}
	for i := uint(0); i < items.NamedChildCount(); i++ {
		item := items.NamedChild(i)
		if first := firstSegment(item, r.src); first != "" {
			r.add(append(slices.Clone(prefix), first), item, module, test, ignored)
		}
	}
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
func (r *referrer) tokens(tree *sitter.Node, module string, test, ignored bool) {
	var toks []*sitter.Node
	for i := uint(0); i < tree.ChildCount(); i++ {
		toks = append(toks, tree.Child(i))
	}
	for i, tok := range toks {
		if tok.Kind() == "token_tree" {
			r.tokens(tok, module, test, ignored || r.ignored[syntax.Line(tok.StartPosition())])
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
				r.add(segments, tok, module, test, ignored || r.ignored[syntax.Line(tok.StartPosition())])
				break
			}
		}
	}
}

func (r *referrer) add(segments []string, at *sitter.Node, module string, test, ignored bool) {
	line := syntax.Line(at.StartPosition())
	r.refs = append(r.refs, unit.Reference{
		Path: strings.Join(segments, "::"), Line: line, Module: module,
		Test: test, Ignored: ignored || r.ignored[line],
	})
}

// joinPath appends name to a :: path.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "::" + name
}
