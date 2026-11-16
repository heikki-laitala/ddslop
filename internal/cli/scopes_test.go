package cli

import (
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

// numberedCopies checks a crate whose file holds n copies of template,
// each with the copy's number in both its %d verbs, and stops the test
// unless check reports one kind candidate, which it returns with the
// crate's root.
func numberedCopies(t *testing.T, kind, file, template string, n int) (patternCandidate, string) {
	t.Helper()
	var src strings.Builder
	for i := range n {
		fmt.Fprintf(&src, template, i, i)
	}
	root := repo(t, map[string]string{file: src.String()})
	_, stdout, stderr := run("check", "--format", "json", root)
	got := patternCandidates(t, stdout, kind)
	if len(got) != 1 {
		t.Fatalf("got %d %s candidates, want 1; stderr: %s\n%s", len(got), kind, stderr, stdout)
	}
	return got[0], root
}

func patternCandidates(t *testing.T, stdout, kind string) []patternCandidate {
	t.Helper()
	var r struct {
		Candidates []patternCandidate `json:"candidates"`
	}
	decodeInto(t, stdout, &r)
	var out []patternCandidate
	for _, c := range r.Candidates {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func TestCheckProposesAClosureHelperForRepeatedSetupAndTeardown(t *testing.T) {
	c, _ := numberedCopies(t, "setup-teardown", "src/writes.rs", `pub async fn write%d(db: &Db, id: u64) -> Result<()> {
    let mut tx = db.transaction().await?;
    step%d(&mut tx, id).await?;
    tx.commit().await?;
    Ok(())
}

`, 6)
	s := c.Suggestion
	if s.Action != "extract-scope" || s.Pattern != "closure-helper" || s.GoF != "Template Method" || len(c.Occurrences) != 6 {
		t.Errorf("candidate = %+v", c)
	}
	if o := c.Occurrences[0]; o.Name != "write0" || o.StartLine != 2 || o.EndLine != 4 {
		t.Errorf("first occurrence = %+v, want write0 lines 2-4 (begin to commit)", o)
	}
}
