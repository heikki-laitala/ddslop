package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type patternCandidate struct {
	Kind        string `json:"kind"`
	Occurrences []struct {
		Name      string `json:"name"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	} `json:"occurrences"`
	Suggestion struct {
		Action  string `json:"action"`
		Pattern string `json:"pattern"`
		GoF     string `json:"gof"`
	} `json:"suggestion"`
}

func patternCandidates(t *testing.T, stdout, kind string) []patternCandidate {
	t.Helper()
	var r struct {
		Candidates []patternCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	var out []patternCandidate
	for _, c := range r.Candidates {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func TestCheckProposesAClosureHelperForRepeatedSetupAndTeardown(t *testing.T) {
	var src strings.Builder
	for i := range 6 {
		fmt.Fprintf(&src, `pub async fn write%d(db: &Db, id: u64) -> Result<()> {
    let mut tx = db.transaction().await?;
    step%d(&mut tx, id).await?;
    tx.commit().await?;
    Ok(())
}

`, i, i)
	}
	root := repo(t, map[string]string{"src/writes.rs": src.String()})

	_, stdout, stderr := run("check", "--format", "json", root)

	got := patternCandidates(t, stdout, "setup-teardown")
	if len(got) != 1 {
		t.Fatalf("got %d setup-teardown candidates, want 1; stderr: %s\n%s", len(got), stderr, stdout)
	}
	c := got[0]
	s := c.Suggestion
	if s.Action != "extract-scope" || s.Pattern != "closure-helper" || s.GoF != "Template Method" || len(c.Occurrences) != 6 {
		t.Errorf("candidate = %+v", c)
	}
	if o := c.Occurrences[0]; o.Name != "write0" || o.StartLine != 2 || o.EndLine != 4 {
		t.Errorf("first occurrence = %+v, want write0 lines 2-4 (begin to commit)", o)
	}
}
