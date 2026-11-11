package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatsShowsTheHealthTrendOfCheckRuns(t *testing.T) {
	root := committedRepo(t, map[string]string{"src/users.rs": users, "src/orders.rs": orders})

	run("check", "--format", "json", "--min-nodes", "20", root)
	run("check", "--format", "json", "--min-nodes", "20", root) // nothing changed: no new entry

	// A cleanup: the copy goes.
	if err := os.WriteFile(filepath.Join(root, "src", "orders.rs"), []byte(unrelated), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "commit", "-qam", "remove the copy")
	run("check", "--format", "json", "--min-nodes", "20", root)

	code, out, stderr := run("stats", root)
	if code != 0 {
		t.Fatalf("stats exit code %d; stderr: %s", code, stderr)
	}
	if got := strings.Count(out, "% duplicated"); got != 2 {
		t.Errorf("history has %d entries, want 2 (the repeated run adds none):\n%s", got, out)
	}
	for _, want := range []string{"50.0% duplicated (9 of 18 lines)", "0.0% duplicated (0 of 12 lines)", "change: 50.0% -> 0.0% (-50.0 points)"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats output is missing %q:\n%s", want, out)
		}
	}
}
