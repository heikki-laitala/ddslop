package rust

import (
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/source"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// reachOf finds how far a file's words reach (see unit.Reach).
func reachOf(root *sitter.Node, file syntax.Source) unit.Reach {
	reach := unit.Reach{Exported: exportedWords(root, file.Src), AllOptional: file.Test || underCfg(root, file.Src)}
	if !reach.AllOptional {
		reach.Optional = map[string]int{}
		addComments(reach.Optional, root, file.Src)
	}
	eachItem(root, file.Src, func(item *sitter.Node, attrs []string) bool {
		cfg := slices.ContainsFunc(attrs, isCfg)
		if name := item.ChildByFieldName("name"); cfg && name != nil && item.Kind() == "mod_item" &&
			item.ChildByFieldName("body") == nil && item.Parent().Kind() == "source_file" {
			reach.CfgModules = append(reach.CfgModules, name.Utf8Text(file.Src))
		}
		if reach.AllOptional {
			return false
		}
		if cfg || item.Kind() == "function_item" && slices.ContainsFunc(attrs, isTestAttr) {
			addWords(reach.Optional, item.Utf8Text(file.Src))
			return false
		}
		return true
	})
	return reach
}

// exportedWords counts the words of the code that code outside the crate
// compiles or reaches through a file: the code blocks of its doc comments
// (doc tests) and its #[macro_export] macros, which other crates compile,
// and its pub uses, which re-export what they name.
func exportedWords(root *sitter.Node, src []byte) map[string]int {
	words := map[string]int{}
	addDocTests(words, root, src)
	eachItem(root, src, func(item *sitter.Node, attrs []string) bool {
		exported := item.Kind() == "macro_definition" && slices.ContainsFunc(attrs, func(a string) bool { return attrName(a) == "macro_export" })
		if exported || item.Kind() == "use_declaration" && isPublic(item, src) {
			addWords(words, item.Utf8Text(src))
		}
		return true
	})
	return words
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
