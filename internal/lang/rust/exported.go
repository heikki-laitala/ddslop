package rust

import (
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/source"
)

// ExportedWords counts the words of the code a file shows to code outside
// its crate: the code blocks of its doc comments (doc tests) and its
// #[macro_export] macros, which other crates compile, and its pub uses,
// which re-export what they name.
func ExportedWords(src []byte) map[string]int {
	words := map[string]int{}
	_ = syntax.Parse(language, src, func(root *sitter.Node) {
		addDocTests(words, root, src)
		eachItem(root, src, func(item *sitter.Node, attrs []string) bool {
			exported := item.Kind() == "macro_definition" && slices.ContainsFunc(attrs, func(a string) bool { return attrName(a) == "macro_export" })
			if exported || item.Kind() == "use_declaration" && isPublic(item, src) {
				addWords(words, item.Utf8Text(src))
			}
			return true
		})
	})
	return words
}

// OptionalWords counts the words a file writes outside the code every
// build of its crate compiles: in comments, in test functions, in items
// under a #[cfg] (#[cfg(test)], #[cfg(target_os = "macos")]), or all of
// them in a test file or one under a #![cfg]. A comment inside such an
// item counts twice.
func OptionalWords(file syntax.Source) map[string]int {
	if file.Test {
		return source.Words(file.Src)
	}
	words := map[string]int{}
	_ = syntax.Parse(language, file.Src, func(root *sitter.Node) {
		if underCfg(root, file.Src) {
			words = source.Words(file.Src)
			return
		}
		addComments(words, root, file.Src)
		eachItem(root, file.Src, func(item *sitter.Node, attrs []string) bool {
			if slices.ContainsFunc(attrs, isCfg) || item.Kind() == "function_item" && slices.ContainsFunc(attrs, isTestAttr) {
				addWords(words, item.Utf8Text(file.Src))
				return false
			}
			return true
		})
	})
	return words
}

// CfgModules returns the names of the modules a file declares out of line
// under a #[cfg] ("#[cfg(target_os = "macos")] mod real;"): their files
// are compiled in some builds only.
// ddslop:ignore TestModules shares the shape, with the attribute of test builds
func CfgModules(src []byte) []string {
	return modulesUnder(src, isCfg)
}

func isCfg(attr string) bool { return attrName(attr) == "cfg" }

// underCfg reports whether a file's inner attributes (#![cfg(...)])
// compile it in some builds only.
func underCfg(root *sitter.Node, src []byte) bool {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(i)
		if child.Kind() == "inner_attribute_item" && isCfg("#"+strings.TrimPrefix(child.Utf8Text(src), "#!")) {
			return true
		}
	}
	return false
}

// addComments adds the words of the comments under n.
func addComments(words map[string]int, n *sitter.Node, src []byte) {
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if kind := child.Kind(); kind == "line_comment" || kind == "block_comment" {
			addWords(words, child.Utf8Text(src))
			continue
		}
		addComments(words, child, src)
	}
}

// eachItem calls visit with each node under n but attributes and comments,
// with the attributes written above it, and descends into the nodes visit
// returns true for.
func eachItem(n *sitter.Node, src []byte, visit func(item *sitter.Node, attrs []string) bool) {
	var attrs []string
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		switch {
		case child.Kind() == "attribute_item":
			attrs = append(attrs, child.Utf8Text(src))
			continue
		case child.IsExtra():
			continue
		}
		if visit(child, attrs) {
			eachItem(child, src, visit)
		}
		attrs = nil
	}
}

// addDocTests adds the words of the code blocks in the doc comments under
// root. A block ends with its doc comment, closed or not.
func addDocTests(words map[string]int, root *sitter.Node, src []byte) {
	fenced, lastRow := false, uint(0)
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		for i := uint(0); i < n.ChildCount(); i++ {
			child := n.Child(i)
			if child.Kind() != "line_comment" {
				visit(child)
				continue
			}
			text, doc := docText(child.Utf8Text(src))
			row := child.StartPosition().Row
			if !doc || row != lastRow+1 {
				fenced = false
			}
			if !doc {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(text), "```") {
				fenced = !fenced
			} else if fenced {
				addWords(words, text)
			}
			lastRow = row
		}
	}
	visit(root)
}

func addWords(words map[string]int, text string) {
	for w, n := range source.Words([]byte(text)) {
		words[w] += n
	}
}

// docText returns the text of a doc comment line (/// or //!) after its
// marker, and whether the line is one.
func docText(comment string) (string, bool) {
	if strings.HasPrefix(comment, "////") {
		return "", false
	}
	for _, marker := range []string{"///", "//!"} {
		if text, ok := strings.CutPrefix(comment, marker); ok {
			return text, true
		}
	}
	return "", false
}
