package cli

import (
	"strings"
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
	if s := c.Suggestion; s.Action != "introduce-enum" || s.Pattern != "enum" || s.GoF != "" || len(c.Occurrences) != 3 {
		t.Errorf("candidate = %+v", c)
	}
	if o := c.Occurrences[0]; o.Name != "interpret0" || o.StartLine != 2 || o.EndLine != 7 {
		t.Errorf("first occurrence = %+v, want interpret0 lines 2-7 (the match)", o)
	}

	_, text, _ := run("check", "--format", "text", root)
	if !strings.Contains(text, "introduce-enum (enum)") {
		t.Errorf("text output should name the pattern without an empty GoF:\n%s", text)
	}
}
