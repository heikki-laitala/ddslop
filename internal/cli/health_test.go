package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

type healthReport struct {
	Summary struct {
		Version string `json:"version"`
		Health  struct {
			AnalysedLines      int     `json:"analysed_lines"`
			DuplicatedLines    int     `json:"duplicated_lines"`
			DuplicationPercent float64 `json:"duplication_percent"`
			UnusedFunctions    int     `json:"unused_functions"`
			Wrappers           int     `json:"wrappers"`
			SingleImplTraits   int     `json:"single_impl_traits"`
			ParallelFlows      int     `json:"parallel_flows"`
		} `json:"health"`
	} `json:"summary"`
}

func decodeHealth(t *testing.T, stdout string) healthReport {
	t.Helper()
	var r healthReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	return r
}

func TestCheckSummarisesRepositoryHealth(t *testing.T) {
	wrapper := `pub fn visible(principal: &str, token_id: u64) -> bool {
    in_namespace(principal, token_id)
}

fn in_namespace(principal: &str, token_id: u64) -> bool {
    principal.len() as u64 > token_id
}
`
	// users and orders are 9-line renamed copies; the wrapper file has 7
	// lines of functions.
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders, "c.rs": wrapper})

	_, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", root)

	h := decodeHealth(t, stdout).Summary
	if h.Version == "" {
		t.Errorf("summary has no ddslop version; stderr: %s", stderr)
	}
	got := h.Health
	if got.AnalysedLines != 9+9+3+3 || got.DuplicatedLines != 9 || got.DuplicationPercent != 37.5 || got.Wrappers != 1 {
		t.Errorf("health = %+v, want 24 analysed lines, 9 duplicated (37.5%%) and 1 wrapper", got)
	}

	_, text, _ := run("check", "--format", "text", "--min-nodes", "20", root)
	if !strings.Contains(text, "Duplication 37.5% (9 of 24 lines); 0 unused functions, 1 wrappers, 0 single-implementation traits, 0 parallel flows") {
		t.Errorf("text output has no health line:\n%s", text)
	}
}

func TestCheckHealthDescribesTheWholeRepositoryInDiffMode(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": users, "b.rs": orders})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--diff", "HEAD", root)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0: nothing changed", code)
	}
	if got := decodeHealth(t, stdout).Summary.Health; got.DuplicatedLines != 9 {
		t.Errorf("health in diff mode = %+v, want the whole repository's 9 duplicated lines", got)
	}
}
