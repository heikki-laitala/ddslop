package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
)

func TestBaselineSuppressesKnownAndReportsNewDuplication(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	stdout, _ := runCode(t, 0, "baseline", "--min-nodes", "20", root)
	path := filepath.Join(root, baselineFile)
	if !strings.Contains(stdout, "1 known candidate") || !strings.Contains(stdout, path) {
		t.Errorf("baseline output %q, want the count and the file", stdout)
	}

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--baseline", path, root)
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
	testutil.WriteFiles(t, root, map[string]string{"c.rs": people})
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
	wantError(t, "", "check", "--baseline", filepath.Join(root, "missing.json"), root)
}
