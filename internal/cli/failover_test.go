package cli

import (
	"strings"
	"testing"
)

func TestFailOverGatesOnTheDuplicationPercentage(t *testing.T) {
	// users and orders are 9-line copies of each other: 9 of 18 lines, 50%.
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	if code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", "--fail-over", "60", root); code != 0 {
		t.Errorf("--fail-over 60 at 50%%: exit code %d, want 0; stderr %q", code, stderr)
	} else if len(decode(t, stdout).Candidates) != 1 {
		t.Errorf("--fail-over must still report the candidates:\n%s", stdout)
	}
	if code, _, _ := run("check", "--format", "json", "--min-nodes", "20", "--fail-over", "40", root); code != 1 {
		t.Errorf("--fail-over 40 at 50%%: exit code %d, want 1", code)
	}
	_, text, _ := run("check", "--format", "text", "--min-nodes", "20", "--fail-over", "40", root)
	if !strings.Contains(text, "Duplication 50.0% is over the 40.0% limit") {
		t.Errorf("text output does not say why it failed:\n%s", text)
	}
	if code, _, stderr := run("check", "--fail-over", "-1", root); code != 2 || !strings.Contains(stderr, "--fail-over") {
		t.Errorf("negative --fail-over: exit code %d, stderr %q; want 2", code, stderr)
	}
}
