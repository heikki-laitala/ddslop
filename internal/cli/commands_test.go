package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionPrintsTheVersion(t *testing.T) {
	// The command, and the flag spellings other tools accept.
	for _, arg := range []string{"version", "--version", "-version"} {
		code, stdout, stderr := run(arg)
		if code != 0 || !strings.HasPrefix(stdout, "ddslop ") || strings.TrimSpace(stdout) == "ddslop" {
			t.Errorf("%s: exit %d, output %q, stderr %q; want 0 and \"ddslop <version>\"", arg, code, stdout, stderr)
		}
	}
}

func TestHelpListsCommands(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		stdout, _ := runCode(t, 0, arg)
		for _, cmd := range []string{"check", "baseline", "flows", "map", "index", "init", "stats", "status", "uninstall", "version"} {
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

	stdout, _ := runCode(t, 0, "index", root)
	if want := "indexed 3 files: 3 parsed, 0 unchanged, 0 deleted"; !strings.Contains(stdout, want) {
		t.Errorf("first index output %q, want it to contain %q", stdout, want)
	}

	_, stdout, _ = run("index", root)
	if want := "indexed 3 files: 0 parsed, 3 unchanged, 0 deleted"; !strings.Contains(stdout, want) {
		t.Errorf("second index output %q, want it to contain %q", stdout, want)
	}
}

func TestIndexErrors(t *testing.T) {
	wantError(t, "", "index", "/nonexistent/ddslop-test")
	if code, _, _ := run("index", "-h"); code != 0 {
		t.Errorf("index -h: exit %d, want 0", code)
	}
}

func TestInitInstallsForClaudeByDefaultAndIndexes(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users})

	stdout, _ := runCode(t, 0, "init", root)
	wantContains(t, stdout, "claude skill", "CLAUDE.md", ".gitignore", "indexed 1 files")
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
	wantContains(t, stdout,
		".gitignore: .ddslop/ present",
		"index: present",
		"claude: not installed",
		"codex: skill, instructions",
		"copilot: skill, instructions",
	)

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

	wantError(t, "ddslop index", "stats", root)

	run("index", root)
	if err := os.WriteFile(filepath.Join(root, "new.rs"), []byte(orders), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ := runCode(t, 0, "stats", root)
	wantContains(t, stdout, "files: 3 (1 generated)", "functions: 2 (1 test)", indexPath)
}
