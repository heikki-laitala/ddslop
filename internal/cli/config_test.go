package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckReadsThresholdsFromTheConfigFile(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs":  users,
		"src/orders.rs": orders,
		".ddslop.yaml":  "min-nodes: 1000\n",
	})

	if code, stdout, stderr := run("check", "--format", "json", root); code != 0 {
		t.Errorf("exit code = %d, want 0: the copies are below the configured min-nodes\n%s%s", code, stdout, stderr)
	}
	// A flag given on the command line overrides the file.
	if code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "50", root); code != 1 {
		t.Errorf("exit code = %d, want 1 with --min-nodes 50\n%s%s", code, stdout, stderr)
	}
}

func TestCheckLeavesOutExcludedFiles(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs":          users,
		"src/orders.rs":         orders,
		"crates/fake-db/lib.rs": strings.Replace(users, "load_user", "fake_user", 1),
	})
	if code, _, _ := run("check", "--format", "json", root); code != 1 {
		t.Fatalf("exit code = %d before excluding, want 1", code)
	}
	// Excluding files indexed by an earlier run drops them from the index.
	if err := os.WriteFile(filepath.Join(root, ".ddslop.yaml"), []byte("exclude:\n  - src/orders.rs\n  - \"crates/fake-*/**\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 0 {
		t.Errorf("exit code = %d, want 0: only users.rs is left\n%s%s", code, stdout, stderr)
	}
	if r := decode(t, stdout); r.Summary.Files != 1 {
		t.Errorf("files analysed = %d, want 1", r.Summary.Files)
	}
}

func TestCheckFailsOnABadConfigFile(t *testing.T) {
	for content, want := range map[string]string{
		"min-node: 60\n":        ".ddslop.yaml: yaml: unmarshal errors",
		"min-similarity: 1.5\n": "min-similarity 1.5 in .ddslop.yaml is not above 0 and at most 1",
	} {
		root := repo(t, map[string]string{"src/users.rs": users, ".ddslop.yaml": content})

		code, _, stderr := run("check", "--format", "json", root)

		if code != 2 || !strings.Contains(stderr, want) {
			t.Errorf("%q: exit code = %d, stderr = %q; want 2 and %q", content, code, stderr, want)
		}
	}
}
