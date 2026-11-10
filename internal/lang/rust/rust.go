// Package rust extracts comparable units from Rust source code.
package rust

import (
	"errors"
	"hash/fnv"
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	rustgrammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"

	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

var language = sitter.NewLanguage(rustgrammar.Language())

// leafKinds maps tree-sitter node kinds that are blanked out to their
// leaf kind. Literal kinds are taken whole, without descending into them.
var leafKinds = map[string]unit.LeafKind{
	"identifier":                 unit.Identifier,
	"field_identifier":           unit.Identifier,
	"shorthand_field_identifier": unit.Identifier,
	"type_identifier":            unit.Type,
	"primitive_type":             unit.Type,
	"string_literal":             unit.Literal,
	"raw_string_literal":         unit.Literal,
	"char_literal":               unit.Literal,
	"integer_literal":            unit.Literal,
	"float_literal":              unit.Literal,
	"boolean_literal":            unit.Literal,
}

// Units parses Rust source and returns one unit per function or method, in
// source order. Functions containing syntax errors are skipped.
func Units(path string, src []byte) ([]unit.Unit, error) {
	var units []unit.Unit
	err := parse(src, func(root *sitter.Node) {
		e := extractor{path: path, src: src}
		e.walk(root, scope{}, IsTestPath(path))
		units = e.units
	})
	return units, err
}

// Traits returns the names of the traits a file declares, in source order.
func Traits(src []byte) []string {
	var names []string
	_ = parse(src, func(root *sitter.Node) {
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Kind() == "trait_item" {
				if name := n.ChildByFieldName("name"); name != nil {
					names = append(names, name.Utf8Text(src))
				}
			}
			for i := uint(0); i < n.ChildCount(); i++ {
				walk(n.Child(i))
			}
		}
		walk(root)
	})
	return names
}

// Imports maps each name a file brings into scope with use to the path it
// refers to: "use a::b::{c, d as e}" gives c -> a::b::c and e -> a::b::d.
// Wildcard imports and imports inside #[cfg(test)] modules are skipped.
func Imports(src []byte) map[string]string {
	out := map[string]string{}
	_ = parse(src, func(root *sitter.Node) {
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Kind() == "use_declaration" {
				if arg := n.ChildByFieldName("argument"); arg != nil {
					collectUse(arg, "", src, out)
				}
				return
			}
			var attrs []string
			for i := uint(0); i < n.ChildCount(); i++ {
				child := n.Child(i)
				switch {
				case child.Kind() == "attribute_item":
					attrs = append(attrs, child.Utf8Text(src))
					continue
				case child.IsExtra():
					continue
				case child.Kind() == "mod_item" && slices.ContainsFunc(attrs, isCfgTest):
					// test modules' imports are not dependencies of the code
				default:
					walk(child)
				}
				attrs = nil
			}
		}
		walk(root)
	})
	return out
}

// collectUse records the names one use tree imports, prefix being the path
// of the enclosing list.
func collectUse(n *sitter.Node, prefix string, src []byte, out map[string]string) {
	join := func(path string) string {
		if prefix == "" {
			return path
		}
		return prefix + "::" + path
	}
	switch n.Kind() {
	case "identifier", "scoped_identifier", "crate", "super":
		path := join(n.Utf8Text(src))
		out[lastSegment(path)] = path
	case "self":
		if prefix != "" {
			out[lastSegment(prefix)] = prefix // use a::b::{self} imports b
		}
	case "use_as_clause":
		path, alias := n.ChildByFieldName("path"), n.ChildByFieldName("alias")
		if path != nil && alias != nil {
			out[alias.Utf8Text(src)] = join(path.Utf8Text(src))
		}
	case "scoped_use_list":
		inner := prefix
		if path := n.ChildByFieldName("path"); path != nil {
			inner = join(path.Utf8Text(src))
		}
		if list := n.ChildByFieldName("list"); list != nil {
			collectUse(list, inner, src, out)
		}
	case "use_list":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			collectUse(n.NamedChild(i), prefix, src, out)
		}
	}
}

// lastSegment returns the part of a path after its last "::".
func lastSegment(path string) string {
	if i := strings.LastIndex(path, "::"); i >= 0 {
		return path[i+2:]
	}
	return path
}

// parse parses Rust source and calls use with the root of its syntax tree.
func parse(src []byte, use func(root *sitter.Node)) error {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return err
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return errors.New("rust: parser returned no tree")
	}
	defer tree.Close()
	use(tree.RootNode())
	return nil
}

type extractor struct {
	path  string
	src   []byte
	units []unit.Unit
}

// walk finds function items, tracking the enclosing impl or trait name and
// whether the code is test code. Attributes are siblings that come just
// before the item they apply to.
// scope is what encloses a function: the type or trait owning it and,
// inside a trait impl, the trait implemented.
type scope struct {
	owner, trait string
}

func (e *extractor) walk(n *sitter.Node, sc scope, test bool) {
	switch n.Kind() {
	case "function_item":
		e.addFunction(n, sc, test)
		return
	case "impl_item":
		if t := n.ChildByFieldName("type"); t != nil {
			sc.owner = t.Utf8Text(e.src)
		}
		sc.trait = ""
		if t := n.ChildByFieldName("trait"); t != nil {
			sc.trait = unit.SimpleName(t.Utf8Text(e.src))
		}
	case "trait_item":
		if t := n.ChildByFieldName("name"); t != nil {
			sc.owner = t.Utf8Text(e.src)
		}
		sc.trait = ""
	}
	var attrs []string
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child.Kind() == "attribute_item" {
			attrs = append(attrs, child.Utf8Text(e.src))
			continue
		}
		if child.IsExtra() {
			continue // comments between attributes and their item
		}
		childTest := test
		switch child.Kind() {
		case "function_item":
			childTest = test || slices.ContainsFunc(attrs, isTestAttr)
		case "mod_item":
			childTest = test || slices.ContainsFunc(attrs, isCfgTest)
		}
		e.walk(child, sc, childTest)
		attrs = nil
	}
}

// isTestAttr reports whether an attribute marks a test function, such as
// #[test], #[tokio::test(...)], #[rstest] or #[test_case(...)].
func isTestAttr(attr string) bool {
	path := strings.TrimPrefix(attr, "#[")
	if i := strings.IndexAny(path, "(]="); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSpace(path)
	if i := strings.LastIndex(path, "::"); i >= 0 {
		path = path[i+2:]
	}
	return path == "test" || path == "rstest" || path == "test_case"
}

// isCfgTest reports whether an attribute limits an item to test builds.
func isCfgTest(attr string) bool {
	compact := strings.Join(strings.Fields(attr), "")
	return compact == "#[cfg(test)]" || strings.HasPrefix(compact, "#[cfg(all(test,")
}

// IsTestPath reports whether a file holds tests by Cargo and Rust
// convention: under a tests/ or benches/ directory, or a tests.rs module.
func IsTestPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, dir := range parts[:len(parts)-1] {
		if dir == "tests" || dir == "benches" {
			return true
		}
	}
	return parts[len(parts)-1] == "tests.rs"
}

func (e *extractor) addFunction(n *sitter.Node, sc scope, test bool) {
	owner := sc.owner
	if n.HasError() {
		return
	}
	name := ""
	if id := n.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(e.src)
	}
	if owner != "" {
		name = owner + "::" + name
	}
	s := shaper{src: e.src}
	s.visit(n, false)
	e.units = append(e.units, unit.Unit{
		File:      e.path,
		Name:      name,
		StartLine: line(n.StartPosition()),
		EndLine:   line(n.EndPosition()),
		Shape:     similarity.HashSymbols(s.seq),
		Nodes:     s.nodes,
		Test:      test,
		Leaves:    s.leaves,
		Seq:       s.seq,
		SeqLines:  s.lines,
		Blocks:    s.blocks,
		Trait:     sc.trait,
		Calls:     s.calls,
	})
}

// shaper flattens a subtree into a symbol sequence and collects its
// blanked-out leaves.
type shaper struct {
	src    []byte
	nodes  int
	leaves []unit.Leaf
	seq    []uint32
	lines  []int
	blocks [][]unit.Statement
	calls  []unit.Call
}

// recordCall notes a call expression by the shape of its function.
func (s *shaper) recordCall(n *sitter.Node) {
	fn := n.ChildByFieldName("function")
	for fn != nil && fn.Kind() == "generic_function" {
		fn = fn.ChildByFieldName("function") // turbofish: f::<T>(...)
	}
	if fn == nil {
		return
	}
	c := unit.Call{Line: line(n.StartPosition())}
	switch fn.Kind() {
	case "identifier":
		c.Kind, c.Name = unit.FreeCall, fn.Utf8Text(s.src)
	case "scoped_identifier":
		c.Kind = unit.PathCall
		if name := fn.ChildByFieldName("name"); name != nil {
			c.Name = name.Utf8Text(s.src)
		}
		if path := fn.ChildByFieldName("path"); path != nil {
			c.Qualifier = path.Utf8Text(s.src)
		}
	case "field_expression":
		c.Kind = unit.MethodCall
		if field := fn.ChildByFieldName("field"); field != nil {
			c.Name = field.Utf8Text(s.src)
		}
		if value := fn.ChildByFieldName("value"); value != nil && value.Kind() == "self" {
			c.Qualifier = "self"
		}
	default:
		return // closures and other callees are not resolved by name
	}
	s.calls = append(s.calls, c)
}

func (s *shaper) emit(sym uint32, line int) {
	s.seq = append(s.seq, sym)
	s.lines = append(s.lines, line)
}

// kindSymbol maps a node kind to a Seq symbol. The top bit keeps it apart
// from unit.Close and the leaf symbols.
func kindSymbol(kind string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(kind))
	return h.Sum32() | 1<<31
}

func (s *shaper) visit(n *sitter.Node, declares bool) {
	if n.IsExtra() {
		return // comments
	}
	s.nodes++
	if kind, ok := leafKinds[n.Kind()]; ok {
		s.emit(unit.LeafSymbol(kind), line(n.StartPosition()))
		text := n.Utf8Text(s.src)
		s.leaves = append(s.leaves, unit.Leaf{
			Kind:     kind,
			Text:     text,
			Line:     line(n.StartPosition()),
			Declares: declares && kind == unit.Identifier && !isUpper(text),
		})
		return
	}
	s.emit(kindSymbol(n.Kind()), line(n.StartPosition()))
	if n.Kind() == "call_expression" {
		s.recordCall(n)
	}
	block := -1
	if n.Kind() == "block" {
		block = len(s.blocks)
		s.blocks = append(s.blocks, nil) // reserve the slot: blocks stay in pre-order
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		field := n.FieldNameForChild(uint32(i))
		statement := block >= 0 && !child.IsExtra() && child.Kind() != "{" && child.Kind() != "}"
		st := unit.Statement{SeqStart: len(s.seq), LeafStart: len(s.leaves), Nodes: s.nodes}
		s.visit(child, childDeclares(n.Kind(), declares, field, child.Kind()))
		if statement {
			st.SeqEnd, st.LeafEnd, st.Nodes = len(s.seq), len(s.leaves), s.nodes-st.Nodes
			st.StartLine, st.EndLine = line(child.StartPosition()), line(child.EndPosition())
			s.blocks[block] = append(s.blocks[block], st)
		}
	}
	s.emit(unit.Close, line(n.EndPosition()))
}

// childDeclares reports whether a child sits in a position that introduces
// names: the function name, parameter and binding patterns. Paths and
// struct field names inside patterns never introduce names.
func childDeclares(parent string, parentDeclares bool, field, child string) bool {
	switch parent {
	case "function_item":
		return field == "name"
	case "parameter", "let_declaration", "for_expression", "let_condition":
		return field == "pattern"
	case "closure_parameters":
		return true
	case "match_pattern":
		return field != "condition"
	case "tuple_struct_pattern", "struct_pattern":
		return parentDeclares && field != "type"
	case "field_pattern":
		return parentDeclares && (field != "name" || child == "shorthand_field_identifier")
	case "scoped_identifier", "scoped_type_identifier":
		return false
	}
	return parentDeclares
}

// isUpper reports whether a name starts with an upper-case ASCII letter. In
// Rust patterns such names are enum variants or constants, not bindings.
func isUpper(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

// line converts a tree-sitter position to a 1-based line number.
func line(p sitter.Point) int {
	return int(p.Row) + 1 //nolint:gosec // a row count always fits in int
}

// IsGenerated reports whether a file is marked as generated in its leading
// comment block: a comment containing "@generated" or "do not edit", or
// starting with "generated by" or "code generated". Only the comments before
// the first line of code count, so generators that mention these phrases in
// their own code are not mistaken for generated files.
func IsGenerated(src []byte) bool {
	for raw := range strings.Lines(string(src)) {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		comment, ok := commentText(text)
		if !ok {
			return false
		}
		lower := strings.ToLower(comment)
		if strings.Contains(lower, "@generated") || strings.Contains(lower, "do not edit") ||
			strings.HasPrefix(lower, "generated by") || strings.HasPrefix(lower, "code generated") {
			return true
		}
	}
	return false
}

// commentText strips comment markers from a line, reporting whether the
// line is a comment at all.
func commentText(line string) (string, bool) {
	for _, prefix := range []string{"//!", "///", "//", "/*!", "/**", "/*", "*"} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(strings.TrimSuffix(rest, "*/")), true
		}
	}
	return "", false
}

// CrateName returns the name Rust code uses for a crate, from its
// Cargo.toml: the [lib] name if set, otherwise the [package] name, with
// dashes as underscores. It returns "" when there is no package.
func CrateName(cargoToml []byte) string {
	section := ""
	names := map[string]string{} // section -> name
	for line := range strings.Lines(string(cargoToml)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "name" || (section != "package" && section != "lib") {
			continue
		}
		names[section] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	name := names["lib"]
	if name == "" {
		name = names["package"]
	}
	return strings.ReplaceAll(name, "-", "_")
}
