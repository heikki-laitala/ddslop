package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCheckProposesAnExtractorForASharedPreamble(t *testing.T) {
	var src strings.Builder
	for i := range 10 {
		fmt.Fprintf(&src, `pub async fn page%d(cx: &Cx) -> Result<Response> {
    trace::tag_event(%d);
    let user = match access(cx).await {
        Access::Anonymous => return sign_in_offer(cx),
        Access::SignedIn(user) => user,
    };
    let org = visited(cx, &user.sub).await?.ok_or_else(not_found)?;
    render(%s)
}

`, i, i, strings.Repeat("1, ", i))
	}
	root := repo(t, map[string]string{"src/pages.rs": src.String()})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	var r struct {
		Candidates []struct {
			Kind        string `json:"kind"`
			Occurrences []struct {
				StartLine int `json:"start_line"`
			} `json:"occurrences"`
			Suggestion struct {
				Action  string `json:"action"`
				Pattern string `json:"pattern"`
				GoF     string `json:"gof"`
			} `json:"suggestion"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want the preamble alone:\n%s", len(r.Candidates), stdout)
	}
	c := r.Candidates[0]
	got := fmt.Sprintf("%s %s %s/%s %d", c.Kind, c.Suggestion.Action, c.Suggestion.Pattern, c.Suggestion.GoF, len(c.Occurrences))
	if want := "shared-preamble extract-preamble extractor/Chain of Responsibility 10"; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
	}
	// The trace call has the same shape in every handler, so it is part of
	// the preamble, its event a parameter.
	if c.Occurrences[0].StartLine != 2 {
		t.Errorf("preamble starts at line %d, want 2", c.Occurrences[0].StartLine)
	}

	_, text, _ := run("check", "--format", "text", root)
	if !strings.Contains(text, "extract-preamble (extractor; GoF: Chain of Responsibility)") {
		t.Errorf("text output does not name the pattern:\n%s", text)
	}
}
