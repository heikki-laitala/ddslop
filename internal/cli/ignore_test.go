package cli

import (
	"strings"
	"testing"
)

func TestCheckLeavesOutCodeMarkedDdslopIgnore(t *testing.T) {
	root := repo(t, map[string]string{
		// Copied functions: one copy marked, so nothing is left to pair.
		"src/users.rs":  users,
		"src/orders.rs": "// ddslop:ignore mirrors the users loader on purpose\n" + orders,
		// The same constant twice: one declaration marked.
		"router/src/limits.rs": "pub const MAX_GRANTED_MODELS: usize = 64; // ddslop:ignore the Ledger's limit, kept apart\n",
		"ledger/src/limits.rs": "pub const MAX_GRANTED_MODELS: usize = 64;\n",
		// A constant's value written inline twice: one use marked, so the
		// candidate keeps only the other.
		"api/src/gate.rs": "pub const LEDGER_UNAVAILABLE: &str = \"ledger_unavailable\";\n",
		"api/src/reads.rs": `pub fn refuse(code: &str) -> bool {
    code == "ledger_unavailable"
}

pub fn parse(code: &str) -> u16 {
    match code {
        // ddslop:ignore the Ledger's code, not the router's
        "ledger_unavailable" => 503,
        _ => 500,
    }
}
`,
		// Go: one of two copied functions marked.
		"go.mod":     "module example.com/app\n",
		"store/a.go": goCopy("Load"),
		"store/b.go": strings.Replace(goCopy("Fetch"), "func Fetch", "// ddslop:ignore kept apart for the v1 API\nfunc Fetch", 1),
	})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	var r struct {
		Candidates []struct {
			Kind        string `json:"kind"`
			Occurrences []struct {
				File      string `json:"file"`
				StartLine int    `json:"start_line"`
			} `json:"occurrences"`
		} `json:"candidates"`
		Summary struct {
			Ignored int `json:"ignored"`
		} `json:"summary"`
	}
	decodeInto(t, stdout, &r)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want only the unmarked inline use: %s", len(r.Candidates), stdout)
	}
	c := r.Candidates[0]
	if c.Kind != "inlined-constant" || len(c.Occurrences) != 2 || c.Occurrences[1].StartLine != 2 {
		t.Errorf("candidate = %+v, want the constant and the use on line 2 only", c)
	}
	// The marked functions (one Rust, one Go) and the marked constant; a
	// marked literal is not a declaration.
	if r.Summary.Ignored != 3 {
		t.Errorf("summary ignored = %d, want 3", r.Summary.Ignored)
	}
}

func TestCheckFailsOnDdslopIgnoreWithoutReason(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs": "// ddslop:ignore\n" + users,
	})

	code, _, stderr := run("check", "--format", "json", root)

	if code != 2 || !strings.Contains(stderr, "src/users.rs: line 1: ddslop:ignore needs a reason") {
		t.Errorf("exit code = %d, stderr = %q; want 2 naming the file and line", code, stderr)
	}
}

// goCopy is a Go function large enough to be compared, named name.
func goCopy(name string) string {
	return `package store

func ` + name + `(db *DB, id int) (*Row, error) {
	row, err := db.Query("SELECT * FROM rows WHERE id = $1", id)
	if err != nil {
		return nil, err
	}
	if row.Name == "" {
		return nil, ErrNotFound
	}
	for _, v := range row.Values {
		if v < 0 {
			return nil, ErrInvalid
		}
	}
	return row, nil
}
`
}

func TestCheckTextSaysHowManyDeclarationsAreIgnored(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs":  users,
		"src/orders.rs": "// ddslop:ignore mirrors the users loader on purpose\n" + orders,
	})

	_, stdout, _ := run("check", "--format", "text", root)

	if !strings.Contains(stdout, "; 1 declaration(s) marked ddslop:ignore") {
		t.Errorf("text output does not count the ignored declaration:\n%s", stdout)
	}
}

func TestCheckDoesNotReportAWrapperMarkedDdslopIgnore(t *testing.T) {
	root := repo(t, map[string]string{
		"src/namespace.rs": `// ddslop:ignore the public name the API documents
pub fn visible(principal: &str, token_id: u64) -> bool {
    in_namespace(principal, token_id)
}

fn in_namespace(principal: &str, token_id: u64) -> bool {
    principal.len() as u64 > token_id
}
`,
	})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (no candidates); stdout: %s stderr: %s", code, stdout, stderr)
	}
}
