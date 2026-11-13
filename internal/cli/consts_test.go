package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type targetReport struct {
	Candidates []struct {
		Kind        string `json:"kind"`
		Occurrences []struct {
			File      string `json:"file"`
			Name      string `json:"name"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		} `json:"occurrences"`
		Target *struct {
			File      string `json:"file"`
			Name      string `json:"name"`
			StartLine int    `json:"start_line"`
		} `json:"target"`
		Suggestion struct {
			Action string `json:"action"`
		} `json:"suggestion"`
		Score struct {
			DuplicatedNodes int `json:"duplicated_nodes"`
		} `json:"score"`
	} `json:"candidates"`
}

func decodeTargets(t *testing.T, stdout string) targetReport {
	t.Helper()
	return decodeJSON[targetReport](t, stdout)
}

func TestCheckReportsConstantsDeclaredTwice(t *testing.T) {
	root := repo(t, map[string]string{
		"router/src/model_grant.rs": "pub const MAX_GRANTED_MODELS: usize = 64;\n",
		"ledger/src/model_grant.rs": "// Limits.\n\npub const MAX_GRANTED_MODELS: usize = 64;\n",
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
	var occs []string
	for _, o := range c.Occurrences {
		occs = append(occs, fmt.Sprintf("%s:%d-%d %s", filepath.ToSlash(o.File), o.StartLine, o.EndLine, o.Name))
	}
	want := "ledger/src/model_grant.rs:3-3 MAX_GRANTED_MODELS router/src/model_grant.rs:1-1 MAX_GRANTED_MODELS"
	if c.Kind != "duplicate-constant" || c.Suggestion.Action != "reuse-existing" || strings.Join(occs, " ") != want {
		t.Errorf("candidate = %s %s %s, want duplicate-constant reuse-existing %s", c.Kind, c.Suggestion.Action, strings.Join(occs, " "), want)
	}
}

func TestCheckReportsConstantValuesWrittenInline(t *testing.T) {
	root := repo(t, map[string]string{
		"src/gate.rs": "pub const LEDGER_UNAVAILABLE: &str = \"ledger_unavailable\";\n",
		"src/api.rs": `pub fn refuse(code: &str) -> bool {
    code == "ledger_unavailable"
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
	var occs []string
	for _, o := range c.Occurrences {
		occs = append(occs, fmt.Sprintf("%s:%d %s", filepath.ToSlash(o.File), o.StartLine, o.Name))
	}
	if c.Kind != "inlined-constant" || c.Suggestion.Action != "reuse-existing" || strings.Join(occs, " ") != "src/gate.rs:1 LEDGER_UNAVAILABLE src/api.rs:2 refuse" {
		t.Errorf("candidate = %s %s %v, want inlined-constant reuse-existing at the constant then its inline use", c.Kind, c.Suggestion.Action, occs)
	}
	if c.Target == nil || filepath.ToSlash(c.Target.File) != "src/gate.rs" || c.Target.Name != "LEDGER_UNAVAILABLE" || c.Target.StartLine != 1 {
		t.Errorf("target = %+v, want the constant src/gate.rs:1 LEDGER_UNAVAILABLE", c.Target)
	}
	if c.Score.DuplicatedNodes != 1 {
		t.Errorf("duplicated nodes = %d, want 1 per inline use", c.Score.DuplicatedNodes)
	}

	_, text, _ := run("check", "--format", "text", root)
	if !strings.Contains(text, "target: src/gate.rs:1-1 LEDGER_UNAVAILABLE") {
		t.Errorf("text output does not show the target:\n%s", text)
	}
}
