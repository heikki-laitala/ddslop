package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const verifierLedger = `pub struct VerifierFile {
    pub key_id: String,
    pub digest: String,
    pub state: State,
}
`

const verifierInsights = `pub struct VerifierFile {
    pub digest: String,
    pub key_id: String,
    pub state: Option<State>,
}

#[cfg(test)]
mod tests {
    struct VerifierFile {
        key_id: String,
        digest: String,
        state: State,
    }
}
`

func TestCheckReportsStructsWithTheSameFields(t *testing.T) {
	root := repo(t, map[string]string{
		"ledger/src/principal.rs":   verifierLedger,
		"insights/src/principal.rs": verifierInsights,
	})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 (the test module's copy is test code): %+v", len(r.Candidates), r.Candidates)
	}
	c := r.Candidates[0]
	if c.Kind != "duplicate-type" || !strings.HasPrefix(c.Fingerprint, "rust:type:") {
		t.Errorf("kind = %s, fingerprint = %s; want duplicate-type, rust:type:<hash>", c.Kind, c.Fingerprint)
	}
	var occs []string
	for _, o := range c.Occurrences {
		occs = append(occs, fmt.Sprintf("%s:%s:%d-%d", filepath.ToSlash(o.File), o.Name, o.StartLine, o.EndLine))
	}
	if got, want := strings.Join(occs, " "), "insights/src/principal.rs:VerifierFile:1-5 ledger/src/principal.rs:VerifierFile:1-5"; got != want {
		t.Errorf("occurrences = %s, want %s", got, want)
	}
	if len(c.Differences) != 1 || c.Differences[0].Kind != "type" || strings.Join(c.Differences[0].Values, "|") != "Option<State>|State" {
		t.Errorf("differences = %+v, want the state field's types", c.Differences)
	}
	if c.Suggestion.Action != "extract-common" || c.Suggestion.Parameters != 1 {
		t.Errorf("suggestion = %+v, want extract-common with 1 parameter", c.Suggestion)
	}
	if c.Score.Occurrences != 2 || c.Score.Nodes == 0 || c.Score.DuplicatedNodes != c.Score.Nodes {
		t.Errorf("score = %+v", c.Score)
	}

	_, stdout, _ = run("check", "--format", "json", "--tests", root)
	if r := decode(t, stdout); len(r.Candidates) != 1 || len(r.Candidates[0].Occurrences) != 3 {
		t.Errorf("with --tests, want one candidate with 3 occurrences: %+v", r.Candidates)
	}
}

func TestCheckIgnoresStructsWithFewFields(t *testing.T) {
	small := "pub struct Target {\n    host: String,\n    port: u16,\n}\n"
	root := repo(t, map[string]string{"a.rs": small, "b.rs": small})

	if code, stdout, _ := run("check", "--format", "json", root); code != 0 {
		t.Errorf("exit code = %d, want 0 for two-field structs:\n%s", code, stdout)
	}
}
