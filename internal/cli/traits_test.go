package cli

import (
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

	got := oneCandidate[targetReport](t, root).first()
	if want := "single-impl-trait remove-trait src/exporter.rs:5-7 DirectoryStore src/exporter.rs:9-13 DirectoryStore for DirectorySession"; got != want {
		t.Errorf("candidate = %s, want %s", got, want)
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
