package syntax

import (
	"bytes"
	"fmt"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// IgnoreMarker starts a comment that leaves code out of the analysis:
// "// ddslop:ignore <reason>". The reason is required, so suppressions stay
// reviewable.
const IgnoreMarker = "ddslop:ignore"

// MarkIgnored marks the units, structs, enums, constants and literal
// leaves that start on a line a ddslop:ignore comment applies to (see
// Ignored).
func MarkIgnored(root *sitter.Node, src []byte, units []unit.Unit, decls *unit.Decls) error {
	lines, err := Ignored(root, src)
	if err != nil || len(lines) == 0 {
		return err
	}
	for i := range units {
		u := &units[i]
		u.Ignored = lines[u.StartLine]
		for j := range u.Leaves {
			u.Leaves[j].Ignored = u.Leaves[j].Kind == unit.Literal && lines[u.Leaves[j].Line]
		}
	}
	for i := range decls.Structs {
		decls.Structs[i].Ignored = lines[decls.Structs[i].StartLine]
	}
	for i := range decls.Enums {
		decls.Enums[i].Ignored = lines[decls.Enums[i].StartLine]
	}
	for i := range decls.Consts {
		decls.Consts[i].Ignored = lines[decls.Consts[i].Line]
	}
	return nil
}

// Ignored returns the lines ddslop:ignore comments in the syntax tree at
// root apply to. A marker after code applies to its own line; a marker on
// a line of its own applies to the first line below it that is neither a
// comment nor an attribute (a blank line ends the search: the marker
// applies to nothing). Comments come from the syntax tree, so a marker
// inside a string is no marker.
func Ignored(root *sitter.Node, src []byte) (map[int]bool, error) {
	if !bytes.Contains(src, []byte(IgnoreMarker)) {
		return nil, nil
	}
	comments := collectComments(root, src)
	lines := strings.Split(string(src), "\n")
	ownLine := map[int]bool{} // lines holding only comments
	for _, c := range comments {
		if !c.trailing {
			for l := c.start; l <= c.end; l++ {
				ownLine[l] = true
			}
		}
	}
	ignored := map[int]bool{}
	for _, c := range comments {
		reason, ok := ignoreReason(c.text)
		if !ok {
			continue
		}
		if reason == "" {
			return nil, fmt.Errorf("line %d: %s needs a reason (// %s <why the duplication is intended>)", c.start, IgnoreMarker, IgnoreMarker)
		}
		if c.trailing {
			ignored[c.start] = true
		} else if l := target(lines, ownLine, c.end+1); l > 0 {
			ignored[l] = true
		}
	}
	return ignored, nil
}

type comment struct {
	text       string
	start, end int
	// trailing is set when code precedes the comment on its line.
	trailing bool
}

func collectComments(n *sitter.Node, src []byte) []comment {
	if n.IsExtra() && strings.Contains(n.Kind(), "comment") {
		start := n.StartPosition()
		lineStart := int(n.StartByte()) - int(start.Column) //nolint:gosec // byte offsets of a source file fit in int
		before := src[lineStart:n.StartByte()]
		end := Line(n.EndPosition())
		if n.EndPosition().Column == 0 {
			end-- // a Rust line comment's node takes in its newline
		}
		return []comment{{
			text:     n.Utf8Text(src),
			start:    Line(start),
			end:      end,
			trailing: len(bytes.TrimSpace(before)) > 0,
		}}
	}
	var out []comment
	for i := uint(0); i < n.ChildCount(); i++ {
		out = append(out, collectComments(n.Child(i), src)...)
	}
	return out
}

// ignoreReason returns the reason of a ddslop:ignore comment, and whether
// the comment is one.
func ignoreReason(text string) (string, bool) {
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, "*/")
	text = strings.TrimLeft(text, "/*!")
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), IgnoreMarker)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// target returns the first line from line on (1-based) that is neither a
// comment nor part of an attribute, or 0 when a blank line or the end of
// the file comes first.
func target(lines []string, comment map[int]bool, line int) int {
	inAttribute := false
	for ; line <= len(lines); line++ {
		text := strings.TrimSpace(lines[line-1])
		switch {
		case comment[line]:
		case inAttribute || strings.HasPrefix(text, "#["):
			inAttribute = !strings.HasSuffix(text, "]")
		case text == "":
			return 0
		default:
			return line
		}
	}
	return 0
}
