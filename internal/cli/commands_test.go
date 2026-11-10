package cli

import (
	"strings"
	"testing"
)

func TestVersionPrintsTheVersion(t *testing.T) {
	code, stdout, _ := run("version")
	if code != 0 || !strings.HasPrefix(stdout, "ddslop ") || strings.TrimSpace(stdout) == "ddslop" {
		t.Errorf("version: exit %d, output %q; want 0 and \"ddslop <version>\"", code, stdout)
	}
}

func TestHelpListsCommands(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		code, stdout, _ := run(arg)
		if code != 0 {
			t.Errorf("%s: exit code %d, want 0", arg, code)
		}
		for _, cmd := range []string{"check", "index", "version"} {
			if !strings.Contains(stdout, "\n  "+cmd+" ") {
				t.Errorf("%s: usage does not list %q:\n%s", arg, cmd, stdout)
			}
		}
	}
}

func TestCheckHelpExitsZero(t *testing.T) {
	code, _, stderr := run("check", "-h")
	if code != 0 || !strings.Contains(stderr, "min-nodes") {
		t.Errorf("check -h: exit %d, stderr %q; want 0 and the check flags", code, stderr)
	}
}

func TestIndexBuildsAndRefreshesTheIndex(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders, "gen.rs": "// @generated\n" + users})

	code, stdout, stderr := run("index", root)
	if code != 0 {
		t.Fatalf("index: exit %d, stderr %s", code, stderr)
	}
	if want := "indexed 3 files: 3 parsed, 0 unchanged, 0 deleted"; !strings.Contains(stdout, want) {
		t.Errorf("first index output %q, want it to contain %q", stdout, want)
	}

	_, stdout, _ = run("index", root)
	if want := "indexed 3 files: 0 parsed, 3 unchanged, 0 deleted"; !strings.Contains(stdout, want) {
		t.Errorf("second index output %q, want it to contain %q", stdout, want)
	}
}

func TestIndexErrors(t *testing.T) {
	if code, _, stderr := run("index", "/nonexistent/ddslop-test"); code != 2 || stderr == "" {
		t.Errorf("index on a missing path: exit %d, stderr %q; want 2 with an error", code, stderr)
	}
	if code, _, _ := run("index", "-h"); code != 0 {
		t.Errorf("index -h: exit %d, want 0", code)
	}
}
