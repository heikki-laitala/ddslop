package cli

import (
	"fmt"
	"strings"
	"testing"
)

// driftRepo checks errors with errors.Is in 20 functions and by message
// in one, in the same file.
func driftRepo(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("package store\n\nimport (\n\t\"errors\"\n\t\"strings\"\n)\n\nvar ErrMissing = errors.New(\"missing\")\n\n")
	for i := range 20 {
		fmt.Fprintf(&b, "func Is%d(err error) bool { return errors.Is(err, ErrMissing) }\n", i)
	}
	b.WriteString("func ByMessage(err error) bool { return strings.Contains(err.Error(), \"missing\") }\n")
	return repo(t, map[string]string{"store/s.go": b.String()})
}

func TestCheckReportsAnErrorCheckDriftingFromTheConvention(t *testing.T) {
	root := driftRepo(t)

	code, stdout, stderr := run("check", "--format", "json", root)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %s", code, stderr)
	}
	var r struct {
		Candidates []struct {
			Kind        string `json:"kind"`
			Concern     string `json:"concern"`
			Occurrences []struct {
				File      string `json:"file"`
				Name      string `json:"name"`
				StartLine int    `json:"start_line"`
			} `json:"occurrences"`
			Target *struct {
				Name      string `json:"name"`
				StartLine int    `json:"start_line"`
			} `json:"target"`
			Values     []string `json:"values"`
			Suggestion struct {
				Action  string `json:"action"`
				Pattern string `json:"pattern"`
			} `json:"suggestion"`
		} `json:"candidates"`
	}
	decodeInto(t, stdout, &r)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want one drift", r.Candidates)
	}
	c := r.Candidates[0]
	if c.Kind != "convention-drift" || c.Concern != "error-checks" || c.Suggestion.Action != "follow-convention" || c.Suggestion.Pattern != "errors.Is/As" {
		t.Errorf("candidate = %+v, want a convention-drift on error-checks, following errors.Is/As", c)
	}
	if len(c.Occurrences) != 1 || c.Occurrences[0].Name != "ByMessage" || c.Occurrences[0].StartLine != 30 {
		t.Errorf("occurrences = %+v, want ByMessage's check at line 30", c.Occurrences)
	}
	if c.Target == nil || c.Target.Name != "Is19" || c.Target.StartLine != 29 {
		t.Errorf("target = %+v, want the nearest errors.Is check, Is19 at line 29", c.Target)
	}
	if strings.Join(c.Values, " ") != "message" {
		t.Errorf("values = %v, want the drifting form", c.Values)
	}
}
