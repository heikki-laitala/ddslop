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

// summary describes the candidate's suggestion and occurrences, the
// first one in full.
func (c patternCandidate) summary() string {
	o := c.Occurrences[0]
	return fmt.Sprintf("%s %s gof=%q, %d occurrences from %s %d-%d", c.Suggestion.Action, c.Suggestion.Pattern, c.Suggestion.GoF, len(c.Occurrences), o.Name, o.StartLine, o.EndLine)
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
	// The first occurrence runs from begin to commit.
	if got, want := c.summary(), `extract-scope closure-helper gof="Template Method", 6 occurrences from write0 2-4`; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
	}
}
