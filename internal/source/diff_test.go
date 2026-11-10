package source

import (
	"os"
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
