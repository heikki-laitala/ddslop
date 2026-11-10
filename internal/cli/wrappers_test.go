package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckReportsPassThroughWrappers(t *testing.T) {
	root := repo(t, map[string]string{
		"src/namespace.rs": `pub fn visible(principal: &str, token_id: u64) -> bool {
    in_namespace(principal, token_id)
}

fn in_namespace(principal: &str, token_id: u64) -> bool {
    principal.len() as u64 > token_id
}
`,
	})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decodeTargets(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1: %s", len(r.Candidates), stdout)
	}
	c := r.Candidates[0]
	o := c.Occurrences[0]
	got := fmt.Sprintf("%s %s %s:%d-%d %s", c.Kind, c.Suggestion.Action, filepath.ToSlash(o.File), o.StartLine, o.EndLine, o.Name)
	if want := "pass-through inline-wrapper src/namespace.rs:1-3 visible"; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
	}
	if c.Target == nil || c.Target.Name != "in_namespace" || c.Target.StartLine != 5 {
		t.Errorf("target = %+v, want in_namespace at line 5", c.Target)
	}

	_, text, _ := run("check", "--format", "text", root)
	if !strings.Contains(text, "target: src/namespace.rs:5-7 in_namespace") {
		t.Errorf("text output does not show the target:\n%s", text)
	}
}
