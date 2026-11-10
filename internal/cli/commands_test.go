package cli

import (
	"os"
	"path/filepath"
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
		for _, cmd := range []string{"check", "index", "init", "stats", "status", "uninstall", "version"} {
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

func TestInitInstallsForClaudeByDefaultAndIndexes(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users})

	code, stdout, stderr := run("init", root)

	if code != 0 {
		t.Fatalf("init: exit %d, stderr %s", code, stderr)
	}
	for _, want := range []string{"claude skill", "CLAUDE.md", ".gitignore", "indexed 1 files"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("init output is missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".codex")); err == nil {
		t.Error("init without flags installed Codex too")
	}
}

func TestInitSelectedAgentsStatusAndUninstall(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users})

	if code, _, stderr := run("init", "--codex", "--copilot", root); code != 0 {
		t.Fatalf("init: exit %d, stderr %s", code, stderr)
	}

	_, stdout, _ := run("status", root)
	for _, want := range []string{
		".gitignore: .ddslop/ present",
		"index: present",
		"claude: not installed",
		"codex: skill, instructions",
		"copilot: skill, instructions",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output is missing %q:\n%s", want, stdout)
		}
	}

	if code, _, stderr := run("uninstall", root); code != 0 {
		t.Fatalf("uninstall: exit %d, stderr %s", code, stderr)
	}
	_, stdout, _ = run("status", root)
	if !strings.Contains(stdout, "codex: not installed") || !strings.Contains(stdout, "index: present") {
		t.Errorf("status after uninstall:\n%s\nwant codex removed and the index kept", stdout)
	}

	if code, _, _ := run("uninstall", "--purge", root); code != 0 {
		t.Fatalf("uninstall --purge: exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".ddslop")); err == nil {
		t.Error("uninstall --purge left .ddslop/")
	}
}

func TestStatsDescribesTheIndexWithoutSyncing(t *testing.T) {
	root := repo(t, map[string]string{
		"a.rs":     users,
		"gen.rs":   "// @generated\n" + users,
		"tests.rs": orders,
	})

	if code, _, stderr := run("stats", root); code != 2 || !strings.Contains(stderr, "ddslop index") {
		t.Errorf("stats before indexing: exit %d, stderr %q; want 2 suggesting ddslop index", code, stderr)
	}

	run("index", root)
	if err := os.WriteFile(filepath.Join(root, "new.rs"), []byte(orders), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := run("stats", root)

	if code != 0 {
		t.Fatalf("stats: exit %d", code)
	}
	for _, want := range []string{"files: 3 (1 generated)", "functions: 2 (1 test)", ".ddslop/index.db"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stats output is missing %q (new.rs must not be indexed by stats):\n%s", want, stdout)
		}
	}
}
