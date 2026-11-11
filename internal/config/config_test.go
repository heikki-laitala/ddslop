package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, File), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadWithoutAFileGivesNoSettings(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || c.MinNodes != nil || c.MinSimilarity != nil || c.Tests != nil || len(c.Exclude) != 0 {
		t.Errorf("Load = %+v, %v; want no settings and no error", c, err)
	}
}

func TestLoadReadsEverySetting(t *testing.T) {
	root := write(t, `min-nodes: 60
min-block-nodes: 120
min-near-nodes: 200
min-similarity: 0.75
tests: true
exclude:
  - "**/*_gen.go"
  - "crates/fake-*/**"
`)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if *c.MinNodes != 60 || *c.MinBlockNodes != 120 || *c.MinNearNodes != 200 || *c.MinSimilarity != 0.75 || !*c.Tests {
		t.Errorf("thresholds = %d %d %d %v %v", *c.MinNodes, *c.MinBlockNodes, *c.MinNearNodes, *c.MinSimilarity, *c.Tests)
	}
	if strings.Join(c.Exclude, " ") != "**/*_gen.go crates/fake-*/**" {
		t.Errorf("exclude = %v", c.Exclude)
	}
}

func TestLoadRejectsUnknownKeysAndBadYAML(t *testing.T) {
	for content, want := range map[string]string{
		"min-node: 60\n":      "field min-node not found",
		"min-nodes: [1, 2\n":  "did not find expected",
		"min-nodes: sixty\n":  "cannot unmarshal",
		"exclude: \"*.go\"\n": "cannot unmarshal",
	} {
		_, err := Load(write(t, content))
		if err == nil || !strings.Contains(err.Error(), File) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want one naming %s and saying %q", content, err, File, want)
		}
	}
}

func TestExcludedMatchesGlobsAgainstSlashPaths(t *testing.T) {
	c := Config{Exclude: []string{"**/*_gen.go", "crates/fake-*/**", "src/legacy.rs"}}
	for path, want := range map[string]bool{
		"api_gen.go":                     true,
		"internal/oapi/server_gen.go":    true,
		"crates/fake-bedrock/src/lib.rs": true,
		"crates/fake-openai/Cargo.rs":    true,
		"crates/real/src/lib.rs":         false,
		"src/legacy.rs":                  true,
		"src/legacy.rs.bak":              false,
		"other/src/legacy.rs":            false,
		"internal/oapi/server.go":        false,
	} {
		if got := c.Excluded(path); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestValidateRejectsBadGlobs(t *testing.T) {
	_, err := Load(write(t, "exclude:\n  - \"src/[a\"\n"))
	if err == nil || !strings.Contains(err.Error(), `exclude "src/[a"`) {
		t.Errorf("error = %v, want one naming the bad pattern", err)
	}
}
