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

func (r targetReport) count() int { return len(r.Candidates) }

// first describes the report's first candidate as its kind, action and
// occurrences, each as file:start-end name.
func (r targetReport) first() string {
	c := r.Candidates[0]
	parts := []string{c.Kind, c.Suggestion.Action}
	for _, o := range c.Occurrences {
		parts = append(parts, fmt.Sprintf("%s:%d-%d %s", filepath.ToSlash(o.File), o.StartLine, o.EndLine, o.Name))
	}
	return strings.Join(parts, " ")
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

	got := oneCandidate[targetReport](t, root).first()
	if want := "duplicate-constant reuse-existing ledger/src/model_grant.rs:3-3 MAX_GRANTED_MODELS router/src/model_grant.rs:1-1 MAX_GRANTED_MODELS"; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
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

	r := oneCandidate[targetReport](t, root)
	c := r.Candidates[0]
	// The constant, then its inline use.
	if got, want := r.first(), "inlined-constant reuse-existing src/gate.rs:1-1 LEDGER_UNAVAILABLE src/api.rs:2-2 refuse"; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
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
