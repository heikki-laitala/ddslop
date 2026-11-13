package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cycleReport struct {
	Candidates []struct {
		Kind        string   `json:"kind"`
		Concern     string   `json:"concern"`
		Values      []string `json:"values"`
		Occurrences []struct {
			File      string `json:"file"`
			Name      string `json:"name"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		} `json:"occurrences"`
		Suggestion struct {
			Action string `json:"action"`
		} `json:"suggestion"`
	} `json:"candidates"`
}

// cycles keeps a report's dependency-cycle candidates.
func cycles(t *testing.T, stdout string) cycleReport {
	t.Helper()
	var r cycleReport
	decodeInto(t, stdout, &r)
	kept := r.Candidates[:0]
	for _, c := range r.Candidates {
		if c.Kind == "dependency-cycle" {
			kept = append(kept, c)
		}
	}
	r.Candidates = kept
	return r
}

func TestCheckReportsModulesCallingEachOther(t *testing.T) {
	code, stdout, stderr := run("check", "--format", "json", repo(t, cyclicCrate))
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %s", code, stderr)
	}
	r := cycles(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want one dependency cycle", r.Candidates)
	}
	c := r.Candidates[0]
	if c.Concern != "layers" || c.Suggestion.Action != "break-cycle" || strings.Join(c.Values, ", ") != "config -> store, store -> config" {
		t.Errorf("candidate = %+v, want config and store calling each other, to break", c)
	}
	var sites []string
	for _, o := range c.Occurrences {
		sites = append(sites, fmt.Sprintf("%s:%s:%d-%d", filepath.ToSlash(o.File), o.Name, o.StartLine, o.EndLine))
	}
	if got := strings.Join(sites, " "); got != "src/config.rs:load:1-1 src/store.rs:open:1-1" {
		t.Errorf("occurrences = %s, want the two call lines", got)
	}
}

func TestCheckIgnoresACycleMarkedAtACallSite(t *testing.T) {
	files := maps.Clone(cyclicCrate)
	files["src/config.rs"] = "// ddslop:ignore config reads the store's endpoint on purpose\n" + files["src/config.rs"]
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want the marked cycle left out", r.Candidates)
	}
}

// In diff mode a cycle is reported when the change touched one of its
// call sites: the change that closed the loop.
func TestDiffReportsTheChangeClosingACycle(t *testing.T) {
	files := maps.Clone(cyclicCrate)
	files["src/store.rs"] = "pub fn open() {}\npub fn endpoint() {}\n" // no call back yet
	root := committedRepo(t, files)
	if err := os.WriteFile(filepath.Join(root, "src/store.rs"), []byte(cyclicCrate["src/store.rs"]), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stdout, _ := run("check", "--format", "json", "--diff", "HEAD", root)
	if r := cycles(t, stdout); len(r.Candidates) != 1 {
		t.Errorf("candidates = %+v, want the cycle the change closed", r.Candidates)
	}
	gitIn(t, root, "commit", "-qam", "close")
	if err := os.WriteFile(filepath.Join(root, "src/api.rs"), []byte("pub fn serve() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stdout, _ = run("check", "--format", "json", "--diff", "HEAD", root)
	if r := cycles(t, stdout); len(r.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none: the change touched no call of the cycle", r.Candidates)
	}
}
