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
		for _, cmd := range []string{"check", "version"} {
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
