package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
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
	testutil.WriteFiles(t, root, map[string]string{"d.rs": syncPeople})

	_, full, _ := run("check", "--format", "json", "--min-nodes", "20", root)
	if got := len(decode(t, full).Candidates); got != 2 {
		t.Fatalf("without --diff: %d candidates, want the old pair and the new one", got)
	}

	stdout, _ := runCode(t, 1, "check", "--format", "json", "--min-nodes", "20", "--diff", "HEAD", root)
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
	testutil.WriteFiles(t, root, map[string]string{"c.rs": unrelated})

	// Exit code 0: no candidate, as the change adds no duplication.
	runCode(t, 0, "check", "--format", "json", "--min-nodes", "20", "--diff", "HEAD", root)
}

func TestCheckDiffTextMarksChangedOccurrences(t *testing.T) {
	stdout := checkCopyAdded(t, "c.rs", "d.rs", "text")
	if !strings.Contains(stdout, "d.rs:1-14 sync_people (changed)") || strings.Contains(stdout, "sync_users (changed)") {
		t.Errorf("text output should mark only d.rs as changed:\n%s", stdout)
	}
}

func TestCheckDiffErrors(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users})
	wantError(t, "", "check", "--diff", "no-such-ref", root)
	plain := repo(t, map[string]string{"a.rs": users})
	wantError(t, "Git", "check", "--diff", "HEAD", plain)
}

// A tracked file deleted but not yet committed is gone: Git still lists
// it, but check must not try to read it.
func TestCheckSkipsTrackedFilesDeletedFromTheWorkTree(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users, "b.rs": orders, "c.rs": unrelated})
	if err := os.Remove(filepath.Join(root, "b.rs")); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", root)

	if code != 0 || stderr != "" {
		t.Fatalf("exit code %d, stderr %q; want 0 (the copy is gone with b.rs)", code, stderr)
	}
	if r := decode(t, stdout); r.Summary.Files != 2 {
		t.Errorf("analysed %d files, want 2", r.Summary.Files)
	}
}
