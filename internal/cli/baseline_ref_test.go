package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A gate against a ref reports only what the change added: known
// duplication, moved to another file, stays known, and a new copy is
// reported. The analysed path is a subdirectory of the repository, and the
// ref's tree is analysed without leaving a checkout behind.
func TestBaselineRefReportsOnlyWhatTheChangeAdded(t *testing.T) {
	root := committedRepo(t, map[string]string{"svc/a.rs": users, "svc/b.rs": orders, "svc/c.rs": syncUsers})
	svc := filepath.Join(root, "svc")
	gitIn(t, root, "mv", "svc/a.rs", "svc/moved.rs")
	gitIn(t, root, "commit", "-q", "-m", "move")

	code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", "--baseline-ref", "HEAD~1", svc)
	if code != 0 {
		t.Fatalf("a move alone: exit code = %d, want 0; stdout: %s stderr: %s", code, stdout, stderr)
	}

	people := strings.ReplaceAll(syncUsers, "sync_users", "sync_people")
	if err := os.WriteFile(filepath.Join(svc, "d.rs"), []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = run("check", "--format", "json", "--min-nodes", "20", "--baseline-ref", "HEAD~1", svc)
	if code != 1 {
		t.Fatalf("a new copy: exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want only the new copy: %s", len(r.Candidates), stdout)
	}
	var files []string
	for _, o := range r.Candidates[0].Occurrences {
		files = append(files, filepath.ToSlash(o.File))
	}
	if got := strings.Join(files, " "); !strings.Contains(got, "d.rs") || strings.Contains(got, "moved.rs") {
		t.Errorf("occurrences = %s, want the new copy in d.rs and not the moved pair", got)
	}
	if r.Summary.Baselined != 1 {
		t.Errorf("baselined = %d, want 1 (the moved pair)", r.Summary.Baselined)
	}

	out, err := exec.Command("git", "-C", root, "worktree", "list").Output() //nolint:gosec // fixed test arguments
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(out)), "\n") + 1; n != 1 {
		t.Errorf("git worktree list shows %d worktrees after the check, want only the repository's own:\n%s", n, out)
	}
}

func TestBaselineRefErrors(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users})

	code, _, stderr := run("check", "--baseline-ref", "no-such-ref", root)
	if code != 2 || !strings.Contains(stderr, "no-such-ref") {
		t.Errorf("unknown ref: exit %d, stderr %q; want 2 naming the ref", code, stderr)
	}

	code, _, stderr = run("check", "--baseline-ref", "HEAD", "--baseline", "x.json", root)
	if code != 2 || !strings.Contains(stderr, "--baseline-ref") {
		t.Errorf("both baselines: exit %d, stderr %q; want 2 naming the conflict", code, stderr)
	}

	plain := repo(t, map[string]string{"a.rs": users})
	code, _, stderr = run("check", "--baseline-ref", "HEAD", plain)
	if code != 2 || !strings.Contains(stderr, "Git") {
		t.Errorf("outside Git: exit %d, stderr %q; want 2 saying a ref needs a Git repository", code, stderr)
	}
}
