package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
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

	runCode(t, 0, "check", "--format", "json", "--min-nodes", "20", "--baseline-ref", "HEAD~1", svc)

	people := strings.ReplaceAll(syncUsers, "sync_users", "sync_people")
	if err := os.WriteFile(filepath.Join(svc, "d.rs"), []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ := runCode(t, 1, "check", "--format", "json", "--min-nodes", "20", "--baseline-ref", "HEAD~1", svc)
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

	wantError(t, "no-such-ref", "check", "--baseline-ref", "no-such-ref", root)

	wantError(t, "--baseline-ref", "check", "--baseline-ref", "HEAD", "--baseline", "x.json", root)

	plain := repo(t, map[string]string{"a.rs": users})
	wantError(t, "Git", "check", "--baseline-ref", "HEAD", plain)
}

// A path the ref does not have yet, such as a component the change adds,
// has an empty baseline: everything in it is new, not an error.
func TestBaselineRefOfAPathTheRefLacksKnowsNothing(t *testing.T) {
	root := committedRepo(t, map[string]string{"old/a.rs": unrelated})
	svc := filepath.Join(root, "svc")
	if err := os.MkdirAll(svc, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"a.rs": users, "b.rs": orders} {
		if err := os.WriteFile(filepath.Join(svc, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _ := runCode(t, 1, "check", "--format", "json", "--min-nodes", "20", "--baseline-ref", "HEAD", svc)
	if r := decode(t, stdout); len(r.Candidates) != 1 || r.Summary.Baselined != 0 {
		t.Errorf("got %d candidates, %d baselined; want the new pair and nothing known", len(r.Candidates), r.Summary.Baselined)
	}
}

// Both trees are analysed with the change's configuration: duplication a
// change's narrower exclusions bring into view was there before it.
func TestBaselineRefAnalysesTheRefWithTheChangesConfiguration(t *testing.T) {
	root := committedRepo(t, map[string]string{
		".ddslop.yaml": "exclude:\n  - gen/**\n",
		"gen/a.rs":     users,
		"gen/b.rs":     orders,
	})
	testutil.WriteFiles(t, root, map[string]string{".ddslop.yaml": "min-nodes: 20\n"})

	stdout, _ := runCode(t, 0, "check", "--format", "json", "--baseline-ref", "HEAD", root)
	if r := decode(t, stdout); r.Summary.Baselined != 1 {
		t.Errorf("baselined = %d, want the pair", r.Summary.Baselined)
	}
}

// The ref's configuration file is replaced, never written through: a
// symlink there must not carry the current file to what it points at.
func TestBaselineRefDoesNotWriteThroughASymlinkedConfiguration(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := committedRepo(t, map[string]string{"a.rs": users})
	if err := os.Symlink(victim, filepath.Join(root, ".ddslop.yaml")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".ddslop.yaml")
	gitIn(t, root, "commit", "-q", "-m", "symlinked configuration")
	if err := os.Remove(filepath.Join(root, ".ddslop.yaml")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFiles(t, root, map[string]string{".ddslop.yaml": "min-nodes: 20\n"})

	if code, _, stderr := run("check", "--format", "json", "--baseline-ref", "HEAD", root); code == 2 {
		t.Fatalf("exit code 2: %s", stderr)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "keep\n" { //nolint:gosec // the test's own file
		t.Fatalf("the file the ref's symlink points at now holds %q (%v), want it untouched", got, err)
	}
}

// A path that was a file or a symlink at the ref is no directory there:
// nothing in it is known, and nothing outside it is analysed instead.
func TestBaselineRefOfAPathThatWasNoDirectoryKnowsNothing(t *testing.T) {
	root := committedRepo(t, map[string]string{"elsewhere/a.rs": users, "elsewhere/b.rs": orders})
	if err := os.Symlink("elsewhere", filepath.Join(root, "svc")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "svc")
	gitIn(t, root, "commit", "-q", "-m", "svc links elsewhere")
	gitIn(t, root, "rm", "-q", "svc")
	testutil.WriteFiles(t, root, map[string]string{"svc/a.rs": users, "svc/b.rs": orders})

	stdout, _ := runCode(t, 1, "check", "--format", "json", "--min-nodes", "20", "--baseline-ref", "HEAD", filepath.Join(root, "svc"))
	if r := decode(t, stdout); r.Summary.Baselined != 0 {
		t.Errorf("baselined = %d, want 0: the ref had no directory svc", r.Summary.Baselined)
	}
}

// A configuration path that was a directory at the ref is replaced too.
func TestBaselineRefReplacesAConfigurationDirectory(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users, ".ddslop.yaml/old.txt": "x\n"})
	gitIn(t, root, "rm", "-q", "-r", ".ddslop.yaml")
	testutil.WriteFiles(t, root, map[string]string{".ddslop.yaml": "min-nodes: 20\n"})

	if code, _, stderr := run("check", "--format", "json", "--baseline-ref", "HEAD", root); code == 2 {
		t.Fatalf("exit code 2: %s", stderr)
	}
}

// A baseline built from a ref knows the ref's functions, so a flow of
// handlers the ref already had is not new however a change regroups them.
func TestABaselineFromARefKnowsItsFunctions(t *testing.T) {
	root := committedRepo(t, map[string]string{"src/a.rs": users, "src/b.rs": orders})
	opts, err := analysisFlags(newFlags("check", "", nil))(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := baselineAt(root, opts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Functions[filepath.Join("src", "a.rs")+"#load_user"] || !b.Functions[filepath.Join("src", "b.rs")+"#load_order"] {
		t.Errorf("functions = %v, want the ref's two", b.Functions)
	}
}
