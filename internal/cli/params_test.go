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

// A function type declared in one file dictates the parameters of the
// functions in another that are its values, such as a command table's
// commands.
func TestCheckLeavesOutTheValuesOfAFunctionTypeFromParameterGroups(t *testing.T) {
	var src strings.Builder
	src.WriteString("package cli\n\nimport \"io\"\n\n")
	for i := range 4 {
		fmt.Fprintf(&src, "func run%d(args []string, stdout, stderr io.Writer) int {\n\treturn write(stdout, stderr, args, %d)\n}\n\n", i, i)
	}
	files := map[string]string{"cli/commands.go": src.String()}
	root := repo(t, files)
	if _, stdout, _ := run("check", "--format", "json", root); !strings.Contains(stdout, `"parameter-group"`) {
		t.Fatalf("without a function type, the commands are no parameter group:\n%s", stdout)
	}
	files["cli/cli.go"] = "package cli\n\nimport \"io\"\n\ntype command func(args []string, stdout, stderr io.Writer) int\n"
	root = repo(t, files)
	if _, stdout, _ := run("check", "--format", "json", root); strings.Contains(stdout, `"parameter-group"`) {
		t.Errorf("the values of a function type are a parameter group:\n%s", stdout)
	}
}
