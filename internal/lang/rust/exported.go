package rust

import (
	"regexp"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/source"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Reach is unit.Reach of a file the parser does not analyse, such as a
// generated one.
func Reach(file syntax.Source) unit.Reach {
	var reach unit.Reach
	_ = syntax.Parse(language, file.Src, func(root *sitter.Node) { reach = reachOf(root, file) })
	return reach
}

// reachOf finds how far a file's words reach (see unit.Reach).
func reachOf(root *sitter.Node, file syntax.Source) unit.Reach {
	reach := unit.Reach{AllOptional: file.Test || underCfg(root, file.Src)}
	addExported(root, file.Src, &reach)
	if !reach.AllOptional {
		reach.Optional = map[string]int{}
		addCommentsAndStrings(reach.Optional, root, file.Src)
	}
	eachItem(root, file.Src, func(item *sitter.Node, attrs []string) bool {
		cfg := slices.ContainsFunc(attrs, isCfg)
		outOfLine := item.Kind() == "mod_item" && item.ChildByFieldName("body") == nil && item.Parent().Kind() == "source_file"
		if name := item.ChildByFieldName("name"); (cfg || slices.ContainsFunc(attrs, isCfgPath)) && name != nil && outOfLine {
			reach.CfgModules = append(reach.CfgModules, moduleEntry(name.Utf8Text(file.Src), attrs))
		}
		if name := item.ChildByFieldName("name"); name != nil && outOfLine {
			if entry := moduleEntry(name.Utf8Text(file.Src), attrs); entry != name.Utf8Text(file.Src) {
				if reach.PathModules == nil {
					reach.PathModules = map[string]string{}
				}
				reach.PathModules[name.Utf8Text(file.Src)] = entry
			}
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

// globs returns the modules a use declaration re-exports whole, each by
// the last name of its path: shapes for pub use shapes::* and pub use
// crate::{shapes::*}.
func globs(n *sitter.Node, src []byte) []string {
	var out []string
	if n.Kind() == "use_wildcard" {
		path := strings.TrimSuffix(strings.TrimSpace(n.Utf8Text(src)), "*")
		path = strings.TrimSuffix(strings.TrimSpace(path), "::")
		if last := path[strings.LastIndex(path, ":")+1:]; last != "" {
			out = append(out, last)
		}
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		out = append(out, globs(n.NamedChild(i), src)...)
	}
	return out
}

// addExported sets reach.Exported, the words of the code that code
// outside the crate compiles or reaches through a file: its doc tests and
// #[macro_export] macros, which other crates compile, and its pub uses,
// which re-export what they name. It also lists the files the file's doc
// attributes include (reach.DocFiles, reach.PackageDocFiles).
func addExported(root *sitter.Node, src []byte, reach *unit.Reach) {
	words := map[string]int{}
	addDocTests(words, root, src)
	eachItem(root, src, func(item *sitter.Node, attrs []string) bool {
		if item.Kind() == "inner_attribute_item" {
			attrs = []string{item.Utf8Text(src)}
		}
		for _, attr := range attrs {
			if file, ok := docIncludeOf(attr, docInclude); ok {
				reach.DocFiles = append(reach.DocFiles, file)
			} else if file, ok := docIncludeOf(attr, packageDocInclude); ok {
				reach.PackageDocFiles = append(reach.PackageDocFiles, file)
			} else if isDocValue(attr) {
				addWords(words, attr) // its text may hold a doc test
			}
		}
		exported := item.Kind() == "macro_definition" && slices.ContainsFunc(attrs, func(a string) bool { return attrName(a) == "macro_export" })
		if exported || item.Kind() == "use_declaration" && isPublic(item, src) {
			addWords(words, item.Utf8Text(src))
		}
		if item.Kind() == "use_declaration" && isPublic(item, src) {
			reach.Globs = append(reach.Globs, globs(item, src)...)
		}
		return true
	})
	reach.Exported = words
}

var (
	docValue   = regexp.MustCompile(`^#!?\[\s*doc\s*=`)
	docInclude = regexp.MustCompile(`^#!?\[\s*doc\s*=\s*include_str!\s*\(\s*"([^"]*)"\s*\)\s*\]$`)
	// packageDocInclude includes a file of the package's directory.
	packageDocInclude = regexp.MustCompile(`^#!?\[\s*doc\s*=\s*include_str!\s*\(\s*concat!\s*\(\s*env!\s*\(\s*"CARGO_MANIFEST_DIR"\s*\)\s*,\s*"/?([^"]*)"\s*\)\s*\)\s*\]$`)
)

// isDocValue reports whether an attribute sets documentation text
// (#[doc = "..."]).
func isDocValue(attr string) bool { return docValue.MatchString(attr) }

// docIncludeOf returns the file a doc attribute includes, if it is one
// include matches (#![doc = include_str!("../README.md")]).
func docIncludeOf(attr string, include *regexp.Regexp) (string, bool) {
	m := include.FindStringSubmatch(attr)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func isCfg(attr string) bool { return attrName(attr) == "cfg" }

// isCfgPath reports whether an attribute places a module in another file
// in some builds only: #[cfg_attr(unix, path = "unix.rs")].
func isCfgPath(attr string) bool {
	return attrName(attr) == "cfg_attr" && strings.Contains(attr, "path")
}

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

// addCommentsAndStrings adds the words of the comments and string
// literals under n: no code there uses a name.
func addCommentsAndStrings(words map[string]int, n *sitter.Node, src []byte) {
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		switch child.Kind() {
		case "line_comment", "block_comment", "string_literal", "raw_string_literal":
			addWords(words, child.Utf8Text(src))
			continue
		}
		addCommentsAndStrings(words, child, src)
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
// root: in each run of /// or //! lines, and in each /** */ or /*! */
// block.
func addDocTests(words map[string]int, root *sitter.Node, src []byte) {
	var lines []string
	marker, lastRow := "", uint(0)
	flush := func() {
		addCodeBlocks(words, lines)
		lines = nil
	}
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		for i := uint(0); i < n.ChildCount(); i++ {
			child := n.Child(i)
			switch child.Kind() {
			case "line_comment":
				row := child.StartPosition().Row
				m, line := docLine(strings.TrimRight(child.Utf8Text(src), "\r\n"))
				if m == "" || m != marker || row != lastRow+1 {
					flush()
				}
				if m != "" {
					lines = append(lines, line)
					marker, lastRow = m, row
				}
			case "block_comment":
				flush()
				addCodeBlocks(words, blockDocLines(child.Utf8Text(src)))
			default:
				visit(child)
			}
		}
	}
	visit(root)
	flush()
}

// DocTestWords counts the words of the code blocks of a Markdown file,
// such as a README a crate includes as its documentation: rustdoc runs
// them as doc tests.
func DocTestWords(markdown []byte) map[string]int {
	words := map[string]int{}
	addCodeBlocks(words, strings.Split(string(markdown), "\n"))
	return words
}

// addCodeBlocks adds the words of the code blocks in documentation text,
// as rustdoc reads it: fenced (``` or ~~~) or indented by four spaces
// more than the text's common indentation.
func addCodeBlocks(words map[string]int, lines []string) {
	indent := -1
	for _, line := range lines {
		if trimmed := strings.TrimLeft(line, " \t"); trimmed != "" && (indent < 0 || len(line)-len(trimmed) < indent) {
			indent = len(line) - len(trimmed)
		}
	}
	fenced := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fenced = !fenced
		case fenced:
			addWords(words, line)
		case trimmed != "" && (strings.HasPrefix(line[indent:], "    ") || strings.HasPrefix(line[indent:], "\t")):
			addWords(words, line)
		}
	}
}

func addWords(words map[string]int, text string) {
	for w, n := range source.Words([]byte(text)) {
		words[w] += n
	}
}

// docLine returns the marker of a doc comment line (/// or //!) and its
// text after the marker; no marker for any other comment.
func docLine(comment string) (marker, text string) {
	if strings.HasPrefix(comment, "////") {
		return "", ""
	}
	for _, m := range []string{"///", "//!"} {
		if text, ok := strings.CutPrefix(comment, m); ok {
			return m, text
		}
	}
	return "", ""
}

// blockDocLines returns the lines of a block doc comment (/** */ or
// /*! */) without its delimiters, and without the asterisk starting each
// line when every line has one; nil for any other block comment.
func blockDocLines(comment string) []string {
	if len(comment) < 5 || !strings.HasPrefix(comment, "/**") && !strings.HasPrefix(comment, "/*!") ||
		strings.HasPrefix(comment, "/***") || !strings.HasSuffix(comment, "*/") {
		return nil
	}
	lines := strings.Split(comment[3:len(comment)-2], "\n")
	starred := true
	for _, line := range lines[1:] {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "*") {
			starred = false
		}
	}
	if starred {
		for i, line := range lines[1:] {
			lines[i+1] = strings.TrimPrefix(strings.TrimLeft(line, " \t"), "*")
		}
	}
	return lines
}
