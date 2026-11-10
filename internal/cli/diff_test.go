package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed test arguments
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// committedRepo commits files as the base of a diff.
func committedRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := repo(t, files)
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "config", "user.email", "test@example.com")
	gitIn(t, root, "config", "user.name", "test")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "base")
	return root
}

func TestCheckDiffReportsOnlyNewDuplication(t *testing.T) {
	// The base already has a renamed pair (users, orders) and sync_users.
	root := committedRepo(t, map[string]string{"a.rs": users, "b.rs": orders, "c.rs": syncUsers})
	// The change adds a renamed copy of sync_users.
	people := strings.ReplaceAll(syncUsers, "sync_users", "sync_people")
	if err := os.WriteFile(filepath.Join(root, "d.rs"), []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}

	_, full, _ := run("check", "--format", "json", "--min-nodes", "20", root)
	if got := len(decode(t, full).Candidates); got != 2 {
		t.Fatalf("without --diff: %d candidates, want the old pair and the new one", got)
	}

	code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", "--diff", "HEAD", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 || r.Summary.Candidates != 1 {
		t.Fatalf("got %d candidates (summary %d), want only the new duplication", len(r.Candidates), r.Summary.Candidates)
	}
	c := r.Candidates[0]
	if c.ID != "c1" {
		t.Errorf("id = %s, want c1 after filtering", c.ID)
	}
	changed := map[string]bool{}
	for _, o := range c.Occurrences {
		changed[o.File] = o.Changed
	}
	if len(changed) != 2 || changed["c.rs"] || !changed["d.rs"] {
		t.Errorf("changed flags = %v, want c.rs unchanged and d.rs changed", changed)
	}
}

func TestCheckDiffWithoutNewDuplicationExitsZero(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users, "b.rs": orders})
	if err := os.WriteFile(filepath.Join(root, "c.rs"), []byte(unrelated), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--diff", "HEAD", root)

	if code != 0 || len(decode(t, stdout).Candidates) != 0 {
		t.Errorf("exit code = %d, want 0: the change adds no duplication", code)
	}
}

func TestCheckDiffTextMarksChangedOccurrences(t *testing.T) {
	root := committedRepo(t, map[string]string{"c.rs": syncUsers})
	people := strings.ReplaceAll(syncUsers, "sync_users", "sync_people")
	if err := os.WriteFile(filepath.Join(root, "d.rs"), []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stdout, _ := run("check", "--format", "text", "--min-nodes", "20", "--diff", "HEAD", root)

	if !strings.Contains(stdout, "d.rs:1-14 sync_people (changed)") || strings.Contains(stdout, "sync_users (changed)") {
		t.Errorf("text output should mark only d.rs as changed:\n%s", stdout)
	}
}

func TestCheckDiffErrors(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users})
	if code, _, stderr := run("check", "--diff", "no-such-ref", root); code != 2 || stderr == "" {
		t.Errorf("unknown ref: exit %d, stderr %q; want 2 with an error", code, stderr)
	}
	plain := repo(t, map[string]string{"a.rs": users})
	if code, _, stderr := run("check", "--diff", "HEAD", plain); code != 2 || !strings.Contains(stderr, "Git") {
		t.Errorf("outside Git: exit %d, stderr %q; want 2 explaining Git is needed", code, stderr)
	}
}
