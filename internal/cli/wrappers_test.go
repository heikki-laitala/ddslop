package cli

import (
	"testing"
)

func TestCheckReportsPassThroughWrappers(t *testing.T) {
	root := repo(t, map[string]string{
		"src/namespace.rs": `pub fn visible(principal: &str, token_id: u64) -> bool {
    in_namespace(principal, token_id)
}

fn in_namespace(principal: &str, token_id: u64) -> bool {
    principal.len() as u64 > token_id
}
`,
	})

	if got, want := oneCandidate[targetReport](t, root).first(), "pass-through inline-wrapper src/namespace.rs:1-3 visible target src/namespace.rs:5-7 in_namespace"; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
	}
	_, text, _ := run("check", "--format", "text", root)
	wantContains(t, text, "target: src/namespace.rs:5-7 in_namespace")
}
