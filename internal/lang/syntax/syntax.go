// Package syntax turns tree-sitter syntax trees into units' structure: the
// flattened sequence of node kinds, the blanked-out leaves, blocks of
// statements and per-language facts. Language adapters describe their
// grammar with a Grammar and share the rest.
package syntax

import (
	"errors"
	"hash/fnv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Grammar is what flattening needs to know about a language.
type Grammar interface {
	// Prefix is prepended to node kinds before they become symbols, so
	// languages naming kinds alike (identifier, block) keep apart symbols.
	Prefix() string
	// Leaf returns the leaf kind of a node kind that is blanked out, taken
	// whole without descending into it.
	Leaf(kind string) (unit.LeafKind, bool)
	// Declares reports whether a child sits in a position that introduces
	// names, given its parent's kind, whether the parent does, and the
	// child's field name and kind.
	Declares(parent string, parentDeclares bool, field, child string) bool
	// Binds reports whether an identifier in a declaring position binds a
	// name (in Rust patterns, capitalised names are variants instead).
	Binds(name string) bool
	// Statements reports whether a node kind holds a block's statements as
	// its children (brace tokens and comments aside).
	Statements(kind string) bool
	// Visit is called on every node that is not a leaf, before its
	// children, to record language facts in the shaper. It returns a
	// function to run after the children, or nil.
	Visit(s *Shaper, n *sitter.Node) (after func())
}

// Shaper accumulates a subtree's structure while it is flattened.
type Shaper struct {
	Src    []byte
	Nodes  int
	Leaves []unit.Leaf
	Seq    []uint32
	Lines  []int
	Blocks [][]unit.Statement
	// Calls and Matches are recorded by the grammar's Visit.
	Calls   []unit.Call
	Matches []unit.StringMatch
	// Exits counts the early returns seen so far, and Closures how deep in
	// closures the walk is: a return in a closure leaves the closure. The
	// grammar's Visit maintains both.
	Exits, Closures int

	grammar Grammar
}

// Flatten walks the subtree at n.
func Flatten(n *sitter.Node, src []byte, g Grammar) *Shaper {
	s := &Shaper{Src: src, grammar: g}
	s.visit(n, false)
	return s
}

func (s *Shaper) emit(sym uint32, line int) {
	s.Seq = append(s.Seq, sym)
	s.Lines = append(s.Lines, line)
}

// KindSymbol maps a node kind to a Seq symbol. The top bit keeps it apart
// from unit.Close and the leaf symbols.
func KindSymbol(kind string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(kind))
	return h.Sum32() | 1<<31
}

func (s *Shaper) visit(n *sitter.Node, declares bool) {
	if n.IsExtra() {
		return // comments
	}
	s.Nodes++
	if kind, ok := s.grammar.Leaf(n.Kind()); ok {
		s.emit(unit.LeafSymbol(kind), Line(n.StartPosition()))
		text := n.Utf8Text(s.Src)
		s.Leaves = append(s.Leaves, unit.Leaf{
			Kind:     kind,
			Text:     text,
			Line:     Line(n.StartPosition()),
			Declares: declares && kind == unit.Identifier && s.grammar.Binds(text),
		})
		return
	}
	s.emit(KindSymbol(s.grammar.Prefix()+n.Kind()), Line(n.StartPosition()))
	if after := s.grammar.Visit(s, n); after != nil {
		defer after()
	}
	block := -1
	if s.grammar.Statements(n.Kind()) {
		block = len(s.Blocks)
		s.Blocks = append(s.Blocks, nil) // reserve the slot: blocks stay in pre-order
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		field := n.FieldNameForChild(uint32(i))
		statement := block >= 0 && !child.IsExtra() && child.Kind() != "{" && child.Kind() != "}"
		st := unit.Statement{SeqStart: len(s.Seq), LeafStart: len(s.Leaves), Nodes: s.Nodes}
		exits := s.Exits
		s.visit(child, s.grammar.Declares(n.Kind(), declares, field, child.Kind()))
		if statement {
			st.SeqEnd, st.LeafEnd, st.Nodes = len(s.Seq), len(s.Leaves), s.Nodes-st.Nodes
			st.StartLine, st.EndLine = Line(child.StartPosition()), Line(child.EndPosition())
			st.Exits = s.Exits > exits
			s.Blocks[block] = append(s.Blocks[block], st)
		}
	}
	s.emit(unit.Close, Line(n.EndPosition()))
}

// Line converts a tree-sitter position to a 1-based line number.
func Line(p sitter.Point) int {
	return int(p.Row) + 1 //nolint:gosec // a row count always fits in int
}

// CountNodes counts the syntax nodes of a subtree, comments left out.
func CountNodes(n *sitter.Node) int {
	if n.IsExtra() {
		return 0
	}
	count := 1
	for i := uint(0); i < n.ChildCount(); i++ {
		count += CountNodes(n.Child(i))
	}
	return count
}

// Parse parses source with a language and calls use with the root of its
// syntax tree.
func Parse(language *sitter.Language, src []byte, use func(root *sitter.Node)) error {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return err
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return errors.New("parser returned no tree")
	}
	defer tree.Close()
	use(tree.RootNode())
	return nil
}

// Compact removes whitespace from code, keeping one space where it
// separates two words: "Option<Vec< u8 >>" becomes "Option<Vec<u8>>" and
// "&'static   str" becomes "&'static str".
func Compact(code string) string {
	var b strings.Builder
	for i, part := range strings.Fields(code) {
		if i > 0 && isWordEnd(b.String()) && isWordStart(part) {
			b.WriteByte(' ')
		}
		b.WriteString(part)
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isWordEnd(s string) bool   { return s != "" && isWordByte(s[len(s)-1]) }
func isWordStart(s string) bool { return s != "" && isWordByte(s[0]) }

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

// PassesOn reports whether a call's argument list is exactly names, in
// order, each written as is or behind a borrow that borrow unwraps (&x),
// returning nil when an argument is no borrow: the rule for a wrapper that
// only forwards its parameters.
func PassesOn(args *sitter.Node, names []string, src []byte, borrow func(n *sitter.Node, src []byte) *sitter.Node) bool {
	if args == nil || args.NamedChildCount() != uint(len(names)) {
		return false
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		a := args.NamedChild(i)
		if inner := borrow(a, src); inner != nil {
			a = inner
		}
		if a.Kind() != "identifier" || a.Utf8Text(src) != names[i] {
			return false
		}
	}
	return true
}
