package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCheckProposesAParameterObjectForParametersPassedTogether(t *testing.T) {
	var src strings.Builder
	for i := range 4 {
		fmt.Fprintf(&src, "pub fn step%d(build: i32, environment: &str, transaction: &Transaction<'_>) -> u32 {\n    transaction.run(build, environment, %d)\n}\n\n", i, i)
	}
	root := repo(t, map[string]string{"src/ingest.rs": src.String()})

	_, stdout, stderr := run("check", "--format", "json", root)

	var r struct {
		Candidates []struct {
			Kind        string   `json:"kind"`
			Values      []string `json:"values"`
			Occurrences []struct {
				Name string `json:"name"`
			} `json:"occurrences"`
			Suggestion struct {
				Action  string `json:"action"`
				Pattern string `json:"pattern"`
			} `json:"suggestion"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	var found bool
	for _, c := range r.Candidates {
		if c.Kind != "parameter-group" {
			continue
		}
		found = true
		got := fmt.Sprintf("%s %s %v %d", c.Suggestion.Action, c.Suggestion.Pattern, c.Values, len(c.Occurrences))
		if want := "introduce-parameter-object parameter-object [build: i32 environment: &str transaction: &Transaction<'_>] 4"; got != want {
			t.Errorf("candidate = %s, want %s", got, want)
		}
	}
	if !found {
		t.Fatalf("no parameter-group candidate; stderr: %s\n%s", stderr, stdout)
	}
}
