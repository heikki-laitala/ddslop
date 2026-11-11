package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExplainShowsACandidatesCodeWithItsDifferencesMarked(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders, "c.rs": unrelated})

	code, text, stderr := run("explain", "--format", "text", "--min-nodes", "20", "c1", root)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	for _, want := range []string{
		"c1 renamed-clone: 2 copies of 9 lines",
		"extract-common with 1 parameter(s)",
		"a.rs:1-9 load_user",
		// line 2 holds the differing literal, a parameter: marked
		`*    2 |     let row = db.query("SELECT * FROM users WHERE id = $1", &[&user_id])?;`,
		// line 3 is the same in both copies: not marked
		"     3 |     let name: String = row.get(0);",
		"b.rs:1-9 load_order",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("explain output is missing %q:\n%s", want, text)
		}
	}

	// The fingerprint names the same candidate, independently of its ID.
	_, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)
	fp := decode(t, stdout).Candidates[0].Fingerprint
	code, out, _ := run("explain", "--format", "json", "--min-nodes", "20", fp, root)
	var e struct {
		Candidate struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"candidate"`
		Code []struct {
			File      string   `json:"file"`
			StartLine int      `json:"start_line"`
			Lines     []string `json:"lines"`
		} `json:"code"`
	}
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("explain JSON: %v\n%s", err, out)
	}
	if code != 0 || e.Candidate.ID != "c1" || len(e.Code) != 2 || len(e.Code[0].Lines) != 9 {
		t.Errorf("explain by fingerprint: exit %d, candidate %+v, %d code blocks", code, e.Candidate, len(e.Code))
	}
}

func TestExplainShowsTheFirstThreeOccurrencesUnlessAll(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": users, "c.rs": users, "d.rs": users})

	_, text, _ := run("explain", "--format", "text", "--min-nodes", "20", "c1", root)
	if strings.Count(text, "| pub fn load_user") != 3 || !strings.Contains(text, "d.rs:1-9 load_user (code not shown; --all shows it)") {
		t.Errorf("want code for three occurrences and the fourth listed:\n%s", text)
	}
	_, all, _ := run("explain", "--format", "text", "--all", "--min-nodes", "20", "c1", root)
	if strings.Count(all, "| pub fn load_user") != 4 {
		t.Errorf("--all should show every occurrence's code:\n%s", all)
	}
}

func TestExplainErrors(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})
	for name, args := range map[string][]string{
		"no id":        {"explain", root},
		"unknown id":   {"explain", "--min-nodes", "20", "c9", root},
		"unknown kind": {"explain", "--format", "sarif", "c1", root},
	} {
		if code, _, stderr := run(args...); code != 2 || stderr == "" {
			t.Errorf("%s: exit code %d, stderr %q; want 2 and a message", name, code, stderr)
		}
	}
}
