package cli

import (
	"strings"
	"testing"
)

func TestFlagsMayFollowThePath(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	code, stdout, stderr := run("check", root, "--format", "json", "--min-nodes", "20")
	if code != 1 || len(decode(t, stdout).Candidates) != 1 {
		t.Errorf("check PATH --flags: exit code %d, stderr %q; want 1 and the renamed copy", code, stderr)
	}

	code, stdout, _ = run("flows", root, "--format", "json")
	if code != 0 || !strings.Contains(stdout, `"flows"`) {
		t.Errorf("flows PATH --format json: exit code %d, output %q", code, stdout)
	}
}

func TestDoubleDashEndsFlags(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	if code, _, stderr := run("check", "--format", "json", "--min-nodes", "20", "--", root); code != 1 {
		t.Errorf("check --flags -- PATH: exit code %d, stderr %q; want 1", code, stderr)
	}
	// After --, a flag-like argument is a path.
	wantError(t, "--format", "check", "--", "--format")
}

func TestMoreThanOnePathIsStillAnError(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users})
	wantError(t, "at most one path", "check", root, "--format", "json", root)
}
