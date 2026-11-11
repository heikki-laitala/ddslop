package cli

import (
	"fmt"
	"strings"
	"testing"
)

func TestCheckReportsEnumsWithTheSameVariantsAsDuplicateTypes(t *testing.T) {
	root := repo(t, map[string]string{
		"src/sandbox.rs": "pub enum SandboxState {\n    Starting,\n    Running,\n    Exited,\n}\n",
		"src/runs.rs":    "pub enum RunStatus {\n    Exited,\n    Running,\n    Starting,\n}\n",
	})

	_, stdout, stderr := run("check", "--format", "json", root)

	got := patternCandidates(t, stdout, "duplicate-type")
	if len(got) != 1 {
		t.Fatalf("got %d duplicate types, want the two enums; stderr: %s\n%s", len(got), stderr, stdout)
	}
	var occs []string
	for _, o := range got[0].Occurrences {
		occs = append(occs, fmt.Sprintf("%s:%d-%d", o.Name, o.StartLine, o.EndLine))
	}
	if strings.Join(occs, " ") != "RunStatus:1-5 SandboxState:1-5" || got[0].Suggestion.Action != "reuse-existing" {
		t.Errorf("candidate = %v %+v", occs, got[0].Suggestion)
	}
}
