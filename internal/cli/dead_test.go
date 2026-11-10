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
		"crates/util/src/lib.rs":      "pub fn compact_text_blocks(text: &str) -> String {\n    text.trim().to_owned()\n}\n\npub fn is_truthy(text: &str) -> bool {\n    text == \"true\"\n}\n",
		"crates/app/Cargo.toml":       "[package]\nname = \"app\"\npublish.workspace = true\n",
		"crates/app/src/main.rs":      "fn main() {\n    println!(\"{}\", util::is_truthy(\"true\"));\n}\n",
		"crates/published/Cargo.toml": "[package]\nname = \"published\"\n",
		"crates/published/src/lib.rs": "pub fn api_for_others() -> u32 {\n    1\n}\n",
	})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decodeTargets(t, stdout)
	var got []string
	for _, c := range r.Candidates {
		o := c.Occurrences[0]
		got = append(got, fmt.Sprintf("%s %s %s:%d-%d %s", c.Kind, c.Suggestion.Action, filepath.ToSlash(o.File), o.StartLine, o.EndLine, o.Name))
	}
	if want := "dead-code delete-dead crates/util/src/lib.rs:1-3 compact_text_blocks"; strings.Join(got, "\n") != want {
		t.Errorf("candidates:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}

	// One function, not copies, in the other formats too.
	_, text, _ := run("check", "--format", "text", root)
	if !strings.Contains(text, "c1 dead-code: 3 lines (") || strings.Contains(text, "copies") {
		t.Errorf("text output should describe one function of 3 lines:\n%s", text)
	}
	_, sarif, _ := run("check", "--format", "sarif", root)
	msg := decodeSARIF(t, sarif).Runs[0].Results[0].Message.Text
	if !strings.HasPrefix(msg, "Public function nothing uses") || strings.Contains(msg, "Duplicated") || strings.Contains(msg, "Other copies") {
		t.Errorf("SARIF message = %q, want the rule's description without copies", msg)
	}
}
