package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const directoryStore = `pub struct DirectorySession {
    rows: Vec<String>,
}

trait DirectoryStore {
    fn put(&mut self, row: String);
}

impl DirectoryStore for DirectorySession {
    fn put(&mut self, row: String) {
        self.rows.push(row);
    }
}
`

func TestCheckReportsTraitsWithASingleImplementation(t *testing.T) {
	root := repo(t, map[string]string{"src/exporter.rs": directoryStore})

	code, stdout, stderr := run("check", "--format", "json", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decodeTargets(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1: %s", len(r.Candidates), stdout)
	}
	c := r.Candidates[0]
	var occs []string
	for _, o := range c.Occurrences {
		occs = append(occs, fmt.Sprintf("%s:%d-%d %s", filepath.ToSlash(o.File), o.StartLine, o.EndLine, o.Name))
	}
	want := "src/exporter.rs:5-7 DirectoryStore src/exporter.rs:9-13 DirectoryStore for DirectorySession"
	if c.Kind != "single-impl-trait" || c.Suggestion.Action != "remove-trait" || strings.Join(occs, " ") != want {
		t.Errorf("candidate = %s %s %s, want single-impl-trait remove-trait %s", c.Kind, c.Suggestion.Action, strings.Join(occs, " "), want)
	}
}

func TestCheckKeepsTraitsATestDoubleImplements(t *testing.T) {
	root := repo(t, map[string]string{
		"src/exporter.rs": directoryStore + "\n#[cfg(test)]\nmod fake;\n",
		"src/exporter/fake.rs": `struct Fake;

impl super::DirectoryStore for Fake {
    fn put(&mut self, _row: String) {}
}
`,
	})

	if code, stdout, _ := run("check", "--format", "json", root); code != 0 {
		t.Errorf("exit code = %d, want 0: the test double is a second implementation\n%s", code, stdout)
	}
}
