package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckReportsUnusedPublicFunctionsOfUnpublishedCrates(t *testing.T) {
	root := repo(t, map[string]string{
		"Cargo.toml":                  "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.package]\npublish = false\n",
		"crates/util/Cargo.toml":      "[package]\nname = \"util\"\npublish.workspace = true\n",
		"crates/util/src/lib.rs":      "pub fn compact_text_blocks(text: &str) -> String {\n    text.trim().to_owned()\n}\n\npub fn is_truthy(text: &str) -> bool {\n    text == \"true\"\n}\n\npub const UNUSED_LIMIT: u32 = 3;\n\npub static UNUSED_NAME: &str = \"x\";\n\npub const USED_LIMIT: u32 = 4;\n\nconst PRIVATE_LIMIT: u32 = 5;\n\npub const UNUSED_TABLE: [u32; 2] = [\n    1,\n    2,\n];\n",
		"crates/app/Cargo.toml":       "[package]\nname = \"app\"\npublish.workspace = true\n",
		"crates/app/src/main.rs":      "fn main() {\n    println!(\"{} {}\", util::is_truthy(\"true\"), util::USED_LIMIT);\n}\n",
		"crates/published/Cargo.toml": "[package]\nname = \"published\"\n",
		"crates/published/src/lib.rs": "pub fn api_for_others() -> u32 {\n    1\n}\n",
	})

	stdout, _ := runCode(t, 1, "check", "--format", "json", root)
	r := decodeJSON[targetReport](t, stdout)
	var got []string
	for _, c := range r.Candidates {
		o := c.Occurrences[0]
		got = append(got, fmt.Sprintf("%s %s %s:%d-%d %s", c.Kind, c.Suggestion.Action, filepath.ToSlash(o.File), o.StartLine, o.EndLine, o.Name))
	}
	// A public constant or static nothing names is unused too, over all its
	// lines; a private one is the compiler's to report.
	if want := "dead-code delete-dead crates/util/src/lib.rs:1-3 compact_text_blocks\n" +
		"dead-code delete-dead crates/util/src/lib.rs:17-20 UNUSED_TABLE\n" +
		"dead-code delete-dead crates/util/src/lib.rs:11-11 UNUSED_NAME\n" +
		"dead-code delete-dead crates/util/src/lib.rs:9-9 UNUSED_LIMIT"; strings.Join(got, "\n") != want {
		t.Errorf("candidates:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}

	// One function, not copies, in the other formats too.
	_, text, _ := run("check", "--format", "text", root)
	if !strings.Contains(text, "c1 dead-code: 3 lines (") || strings.Contains(text, "copies") {
		t.Errorf("text output should describe one function of 3 lines:\n%s", text)
	}
	_, sarif, _ := run("check", "--format", "sarif", root)
	msg := decodeJSON[sarifLog](t, sarif).Runs[0].Results[0].Message.Text
	if !strings.HasPrefix(msg, "Public function or constant nothing uses") || strings.Contains(msg, "Duplicated") || strings.Contains(msg, "Other copies") {
		t.Errorf("SARIF message = %q, want the rule's description without copies", msg)
	}
}
