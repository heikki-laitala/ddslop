package source

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(prefix)
		b.WriteString(strings.Repeat("x", i))
		b.WriteString("\n")
	}
	return b.String()
}

// ddslop:ignore integrate's tests write files the same way; test helpers cannot be shared across packages without a package of their own
func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// committedRepo creates a repository with a.rs and c.rs (10 lines each)
// and sub/d.rs committed.
func committedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "test@example.com")
	mustGit(t, root, "config", "user.name", "test")
	writeFile(t, root, "a.rs", lines(10, "a"))
	writeFile(t, root, "c.rs", lines(10, "c"))
	writeFile(t, root, "sub/d.rs", lines(5, "d"))
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "base")
	return root
}

func TestChangedLines(t *testing.T) {
	root := committedRepo(t)
	// a.rs: line 3 modified, two lines inserted after line 7.
	a := strings.Split(lines(10, "a"), "\n")
	a[2] = "changed"
	a = append(a[:7], append([]string{"new1", "new2"}, a[7:]...)...)
	writeFile(t, root, "a.rs", strings.Join(a, "\n"))
	// c.rs: line 5 deleted, staged.
	c := strings.Split(lines(10, "c"), "\n")
	c = append(c[:4], c[5:]...)
	writeFile(t, root, "c.rs", strings.Join(c, "\n"))
	mustGit(t, root, "add", "c.rs")
	// b.rs: new and untracked.
	writeFile(t, root, "b.rs", "fn b() {}\n")

	changes, err := ChangedLines(root, "HEAD")
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}

	cases := []struct {
		file       string
		start, end int
		want       bool
	}{
		{"a.rs", 3, 3, true},
		{"a.rs", 1, 2, false},
		{"a.rs", 8, 9, true}, // the inserted lines
		{"a.rs", 10, 12, false},
		{"c.rs", 4, 4, true}, // next to the deletion
		{"c.rs", 5, 5, true},
		{"c.rs", 7, 9, false},
		{"b.rs", 1, 100, true}, // untracked: the whole file
		{"sub/d.rs", 1, 5, false},
	}
	for _, c := range cases {
		if got := changes.Touches(c.file, c.start, c.end); got != c.want {
			t.Errorf("Touches(%s, %d, %d) = %v, want %v (changes: %v)", c.file, c.start, c.end, got, c.want, changes)
		}
	}
}

func TestChangedLinesAreRelativeToRoot(t *testing.T) {
	root := committedRepo(t)
	writeFile(t, root, "sub/d.rs", lines(5, "e"))
	writeFile(t, root, "a.rs", lines(10, "z"))

	changes, err := ChangedLines(filepath.Join(root, "sub"), "HEAD")
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}
	if !changes.Touches("d.rs", 1, 1) {
		t.Errorf("d.rs relative to sub/ not reported as changed: %v", changes)
	}
	if changes.Touches("a.rs", 1, 10) || changes.Touches("../a.rs", 1, 10) {
		t.Errorf("a.rs outside the root reported: %v", changes)
	}
}

func TestChangedLinesErrors(t *testing.T) {
	root := committedRepo(t)
	if _, err := ChangedLines(root, "no-such-ref"); err == nil {
		t.Error("unknown ref returned no error")
	}
	if _, err := ChangedLines(t.TempDir(), "HEAD"); err == nil {
		t.Error("a directory outside Git returned no error")
	}
}

func TestRevisionNamesTheCommitAndUncommittedChanges(t *testing.T) {
	dir := t.TempDir()
	if got := Revision(dir); got != "" {
		t.Errorf("Revision outside Git = %q, want empty", got)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // a test running git on its own temporary repository
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.rs"), []byte("fn a() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.rs")
	run("commit", "-qm", "a")
	clean := Revision(dir)
	if len(clean) < 7 || strings.HasSuffix(clean, "-dirty") {
		t.Errorf("Revision of a clean checkout = %q, want a short commit hash", clean)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.rs"), []byte("fn b() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Revision(dir); got != clean+"-dirty" {
		t.Errorf("Revision with an uncommitted edit = %q, want %q", got, clean+"-dirty")
	}
}
