package rust

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
)

// Syntax is a Rust file's syntax tree: each of its methods reads one
// thing from it, so one parse serves them all (see WithSyntax).
type Syntax struct {
	root *sitter.Node
	file syntax.Source
}

// WithSyntax parses a file once and calls use with its syntax tree, which
// is freed when use returns.
func WithSyntax(file syntax.Source, use func(s Syntax)) error {
	return syntax.Parse(language, file.Src, func(root *sitter.Node) { use(Syntax{root, file}) })
}

// read parses src and returns what get reads from its syntax tree.
func read[T any](src []byte, get func(Syntax) T) T {
	var out T
	_ = WithSyntax(syntax.Source{Src: src}, func(s Syntax) { out = get(s) })
	return out
}
