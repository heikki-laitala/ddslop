package source

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := git(dir, args...); err != nil {
		t.Fatal(err)
	}
}

func TestFilesInGitRepoRespectsGitignore(t *testing.T) {
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	write(t, root, ".gitignore", "a.rs", "sub/b.rs", "target/gen.rs", "ignored/c.rs", "main.go")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("target/\nignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "a.rs") // tracked; sub/b.rs stays untracked

	files, err := Files(root, ".rs")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got := fmt.Sprint(files); got != "[a.rs sub/b.rs]" {
		t.Errorf("files = %s, want [a.rs sub/b.rs]", got)
	}
}

func TestFilesOutsideGitSkipsHiddenAndTargetDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "z.rs", "a/b.rs", "target/gen.rs", ".hidden/h.rs", "notes.txt")

	files, err := Files(root, ".rs")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got := fmt.Sprint(files); got != "[a/b.rs z.rs]" {
		t.Errorf("files = %s, want [a/b.rs z.rs]", got)
	}
}

func TestFilesMissingRootIsAnError(t *testing.T) {
	if _, err := Files(filepath.Join(t.TempDir(), "missing"), ".rs"); err == nil {
		t.Error("Files on a missing directory returned no error")
	}
}
