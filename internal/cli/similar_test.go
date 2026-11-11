package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type similarReport struct {
	Target struct {
		File      string `json:"file"`
		Name      string `json:"name"`
		StartLine int    `json:"start_line"`
	} `json:"target"`
	Similar []struct {
		File       string  `json:"file"`
		Name       string  `json:"name"`
		StartLine  int     `json:"start_line"`
		EndLine    int     `json:"end_line"`
		Similarity float64 `json:"similarity"`
	} `json:"similar"`
}

func TestSimilarFindsExistingFunctionsLikeTheOneAtALine(t *testing.T) {
	root := repo(t, map[string]string{"src/users.rs": users, "src/orders.rs": orders, "src/math.rs": unrelated})

	// A line inside load_user, not only its first line.
	code, stdout, stderr := run("similar", "--format", "json", filepath.Join(root, "src", "users.rs")+":3", root)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var r similarReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if r.Target.Name != "load_user" || filepath.ToSlash(r.Target.File) != "src/users.rs" {
		t.Errorf("target = %+v, want src/users.rs load_user", r.Target)
	}
	if len(r.Similar) != 1 || r.Similar[0].Name != "load_order" || r.Similar[0].Similarity != 1 || r.Similar[0].EndLine != 9 {
		t.Errorf("similar = %+v, want load_order with similarity 1", r.Similar)
	}

	_, text, _ := run("similar", "--format", "text", filepath.Join(root, "src", "users.rs")+":3", root)
	if !strings.Contains(text, "1.00  src/orders.rs:1-9 load_order") {
		t.Errorf("text output:\n%s", text)
	}
}

func TestSimilarErrors(t *testing.T) {
	root := repo(t, map[string]string{"src/users.rs": users})
	cases := map[string][]string{
		"no location":             {"similar", root},
		"no line":                 {"similar", filepath.Join(root, "src", "users.rs"), root},
		"line outside a function": {"similar", filepath.Join(root, "src", "users.rs") + ":40", root},
		"file outside the root":   {"similar", "/elsewhere/x.rs:1", root},
	}
	for name, args := range cases {
		if code, _, stderr := run(args...); code != 2 || stderr == "" {
			t.Errorf("%s: exit code %d, stderr %q; want 2 and a message", name, code, stderr)
		}
	}
}
