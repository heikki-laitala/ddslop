package cli

import (
	"testing"
)

func TestCheckProposesAnEnumForStringsMatchedInManyFunctions(t *testing.T) {
	c, root := numberedCopies(t, "string-kinds", "src/answers.rs", `pub fn interpret%d(status: &str) -> u32 {
    match status {
        "committed" => %d,
        "refused" => 2,
        "replayed" => 3,
        _ => 0,
    }
}

`, 3)
	// The first occurrence is the match.
	if got, want := c.summary(), `introduce-enum enum gof="", 3 occurrences from interpret0 2-7`; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
	}
	// The text names the pattern without an empty GoF.
	_, text, _ := run("check", "--format", "text", root)
	wantContains(t, text, "introduce-enum (enum)")
}
