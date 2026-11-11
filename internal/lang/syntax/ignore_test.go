package syntax

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	gogrammar "github.com/tree-sitter/tree-sitter-go/bindings/go"
	rustgrammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

var (
	rustLanguage = sitter.NewLanguage(rustgrammar.Language())
	goLanguage   = sitter.NewLanguage(gogrammar.Language())
)

func parseIgnored(language *sitter.Language, src string) (lines map[int]bool, err error) {
	if perr := Parse(language, []byte(src), func(root *sitter.Node) { lines, err = Ignored(root, []byte(src)) }); perr != nil {
		return nil, perr
	}
	return lines, err
}

func ignoredLines(t *testing.T, language *sitter.Language, src string) string {
	t.Helper()
	lines, err := parseIgnored(language, src)
	if err != nil {
		t.Fatalf("Ignored: %v", err)
	}
	var got []int
	for l := range lines {
		got = append(got, l)
	}
	slices.Sort(got)
	return fmt.Sprint(got)
}

func TestIgnoredMarksTheLineAMarkerAppliesTo(t *testing.T) {
	src := `// ddslop:ignore wire format, mirrors the Ledger's on purpose
/// Docs and attributes may sit between the marker and the declaration.
#[derive(
    Debug,
)]
pub struct LedgerRefusal {
    code: String,
}

fn fmt(&self, f: &mut Formatter) -> Result {
    f.debug_struct("Client")
        .field("request_timeout", &self.t) // ddslop:ignore Debug field name
        // ddslop:ignore the Ledger's code, not ours
        .field("ledger_unavailable", &self.u)
        .finish()
}

// ddslop:ignore a blank line ends the comment block, so this applies to nothing

pub const UNRELATED: &str = "x";
const MARKER: &str = "// ddslop:ignore"; // a marker inside a string is no comment
`
	// The struct's line, the trailing marker's own line and the line under
	// the second marker.
	if got := ignoredLines(t, rustLanguage, src); got != "[6 12 14]" {
		t.Errorf("ignored lines = %s, want [6 12 14]", got)
	}
}

func TestIgnoredWorksForGo(t *testing.T) {
	src := `package store

// ddslop:ignore the generated client's shape, kept on purpose
// Load reads a row.
func Load() {}

const (
	CodeUnavailable = "ledger_unavailable" // ddslop:ignore the Ledger's code
	Other           = "x"
)
`
	if got := ignoredLines(t, goLanguage, src); got != "[5 8]" {
		t.Errorf("ignored lines = %s, want [5 8]", got)
	}
}

func TestIgnoredNeedsAReason(t *testing.T) {
	for _, src := range []string{
		"fn f() {} // ddslop:ignore\n",
		"//   ddslop:ignore   \nfn f() {}\n",
		"/* ddslop:ignore */\nfn f() {}\n",
	} {
		_, err := parseIgnored(rustLanguage, src)
		if err == nil || !strings.Contains(err.Error(), "line 1: ddslop:ignore needs a reason") {
			t.Errorf("%q: error = %v, want one asking for a reason on line 1", src, err)
		}
	}
}

func TestIgnoredSkipsFilesWithoutMarkers(t *testing.T) {
	lines, err := parseIgnored(rustLanguage, "fn f() {} // ordinary comment\n")
	if err != nil || len(lines) != 0 {
		t.Errorf("Ignored = %v, %v; want no lines", lines, err)
	}
}
