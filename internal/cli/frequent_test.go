package cli

import (
	"fmt"
	"strings"
	"testing"
)

func TestCheckReportsSmallBlocksCopiedManyTimes(t *testing.T) {
	// Each function makes a call of a different arity after the shared
	// guard, so the functions themselves are not copies.
	var src strings.Builder
	for i := range 12 {
		fmt.Fprintf(&src, `pub fn op%d(conn: &mut Conn) -> Result<(), NotCopied> {
    if !conn.usable {
        return Err(NotCopied::Unavailable("unusable".to_owned()));
    }
    conn.usable = false;
    conn.step(%s)
}

`, i, strings.Repeat("1, ", i))
	}
	root := repo(t, map[string]string{"src/bindings.rs": src.String()})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 || r.Candidates[0].Kind != "block-clone" || len(r.Candidates[0].Occurrences) != 12 {
		t.Fatalf("want one block clone with 12 occurrences, got:\n%s", stdout)
	}
	if n := r.Candidates[0].Score.Nodes; n >= 100 {
		t.Errorf("the guard has %d nodes; the test needs it below --min-block-nodes", n)
	}
}
