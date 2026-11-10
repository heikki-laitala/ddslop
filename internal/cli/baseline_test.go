package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaselineSuppressesKnownAndReportsNewDuplication(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	code, stdout, stderr := run("baseline", "--min-nodes", "20", root)
	if code != 0 {
		t.Fatalf("baseline: exit %d, stderr %s", code, stderr)
	}
	path := filepath.Join(root, ".ddslop-baseline.json")
	if !strings.Contains(stdout, "1 known candidate") || !strings.Contains(stdout, path) {
		t.Errorf("baseline output %q, want the count and the file", stdout)
	}

	code, stdout, _ = run("check", "--format", "json", "--min-nodes", "20", "--baseline", path, root)
	r := decode(t, stdout)
	if code != 0 || len(r.Candidates) != 0 || r.Summary.Baselined != 1 {
		t.Errorf("check with the baseline: exit %d, %d candidates, %d baselined; want 0, 0, 1", code, len(r.Candidates), r.Summary.Baselined)
	}

	_, text, _ := run("check", "--format", "text", "--min-nodes", "20", "--baseline", path, root)
	if !strings.Contains(text, "1 known candidate(s) in the baseline") {
		t.Errorf("text summary does not mention the baselined candidate:\n%s", text)
	}

	// A third copy is new duplication even though the pair is known.
	people := strings.ReplaceAll(users, "load_user", "load_person")
	if err := os.WriteFile(filepath.Join(root, "c.rs"), []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = run("check", "--format", "json", "--min-nodes", "20", "--baseline", path, root)
	if r := decode(t, stdout); code != 1 || len(r.Candidates) != 1 || len(r.Candidates[0].Occurrences) != 3 {
		t.Errorf("after adding a copy: exit %d, %d candidates; want 1 candidate with all 3 copies", code, len(r.Candidates))
	}
}

func TestBaselineOutputFlagAndErrors(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})
	out := filepath.Join(t.TempDir(), "known.json")

	if code, _, stderr := run("baseline", "--min-nodes", "20", "--output", out, root); code != 0 {
		t.Fatalf("baseline --output: exit %d, stderr %s", code, stderr)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("--output file not written: %v", err)
	}
	if code, _, stderr := run("check", "--baseline", filepath.Join(root, "missing.json"), root); code != 2 || stderr == "" {
		t.Errorf("missing baseline: exit %d, stderr %q; want 2 with an error", code, stderr)
	}
}
