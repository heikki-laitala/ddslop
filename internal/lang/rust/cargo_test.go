package rust

import (
	"slices"
	"testing"
)

func TestCrateName(t *testing.T) {
	cases := map[string]string{
		"package name, dashes": "[package]\nname = \"acme-token-credentials\"\nversion = \"0.1.0\"\n",
		"lib name wins":        "[package]\nname = \"acme-ledger\"\n\n[lib]\nname = \"ledger_core\"\npath = \"src/lib.rs\"\n",
		"dependency names":     "[package]\nname = \"app\"\n\n[dependencies]\nname = { version = \"1\" }\n",
		"no package":           "[workspace]\nmembers = [\"crates/*\"]\n",
	}
	want := map[string]string{
		"package name, dashes": "acme_token_credentials",
		"lib name wins":        "ledger_core",
		"dependency names":     "app",
		"no package":           "",
	}
	for name, toml := range cases {
		if got := CrateName([]byte(toml)); got != want[name] {
			t.Errorf("%s: CrateName = %q, want %q", name, got, want[name])
		}
	}
}

func TestUnpublished(t *testing.T) {
	workspacePrivate := "[workspace]\nmembers = []\n\n[workspace.package]\nedition = \"2024\"\npublish = false\n"
	workspacePublic := "[workspace]\nmembers = []\n"
	cases := []struct {
		name, manifest, workspace string
		want                      bool
	}{
		{"publish false", "[package]\nname = \"a\"\npublish = false\n", workspacePublic, true},
		{"publish empty list", "[package]\nname = \"a\"\npublish = []\n", workspacePublic, true},
		{"publish to a registry", "[package]\nname = \"a\"\npublish = [\"internal\"]\n", workspacePublic, false},
		{"no publish key", "[package]\nname = \"a\"\n", workspacePrivate, false},
		{"inherits private", "[package]\nname = \"a\"\npublish.workspace = true\n", workspacePrivate, true},
		{"inherits private, inline table", "[package]\nname = \"a\"\npublish = { workspace = true }\n", workspacePrivate, true},
		{"inherits public", "[package]\nname = \"a\"\npublish.workspace = true\n", workspacePublic, false},
		{"publish in another section", "[package]\nname = \"a\"\n\n[package.metadata.x]\npublish = false\n", workspacePublic, false},
		{"a header comment", "[package] # the crate\nname = \"a\"\npublish = false\n", workspacePublic, true},
	}
	for _, c := range cases {
		if got := Unpublished([]byte(c.manifest), []byte(c.workspace)); got != c.want {
			t.Errorf("%s: Unpublished = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLibPath(t *testing.T) {
	cases := map[string]string{
		"custom path":            "[package]\nname = \"app\"\n\n[lib]\nname = \"core\"\npath = \"lib/core.rs\"\n",
		"single quotes, spaces":  "[package]\nname = \"app\"\n[lib]\n  path='lib/core.rs'  \n",
		"default layout":         "[package]\nname = \"app\"\n",
		"a bin's path is no lib": "[package]\nname = \"app\"\n\n[[bin]]\nname = \"tool\"\npath = \"tools/main.rs\"\n",
		"another section's path": "[package]\nname = \"app\"\n\n[dependencies]\ncore = { path = \"../core\" }\n",
		"an inline comment":      "[package]\nname = \"app\"\n[lib]\npath = \"lib/core.rs\" # custom root\n",
		"a # in the string":      "[package]\nname = \"app\"\n[lib]\npath = \"lib/#core.rs\"\n",
		"a header comment":       "[package]\nname = \"app\"\n[lib] # custom library\npath = \"lib/core.rs\"\n",
	}
	want := map[string]string{
		"custom path":            "lib/core.rs",
		"single quotes, spaces":  "lib/core.rs",
		"default layout":         "",
		"a bin's path is no lib": "",
		"another section's path": "",
		"an inline comment":      "lib/core.rs",
		"a # in the string":      "lib/#core.rs",
		"a header comment":       "lib/core.rs",
	}
	for name, toml := range cases {
		if got := LibPath([]byte(toml)); got != want[name] {
			t.Errorf("%s: LibPath = %q, want %q", name, got, want[name])
		}
	}
}

func TestTargetPaths(t *testing.T) {
	toml := "[package]\nname = \"app\"\n\n[lib]\npath = \"core.rs\"\n\n[[bin]]\nname = \"tool\"\npath = \"tool.rs\"\n\n[[bin]]\nname = \"run\"\npath = \"tools/run/main.rs\" # the runner\n\n[[example]]\nname = \"demo\"\npath = \"demo.rs\"\n\n[[test]]\nname = \"it\"\n\n[dependencies]\nx = { path = \"../x\" }\n"
	got := TargetPaths([]byte(toml))
	want := []string{"tool.rs", "tools/run/main.rs", "demo.rs"}
	if !slices.Equal(got, want) {
		t.Errorf("TargetPaths = %q, want %q", got, want)
	}
}

func TestDeclaredModulesListAFilesOutOfLineModules(t *testing.T) {
	src := []byte("mod cli;\npub mod store;\nmod inline { }\n#[cfg(test)]\nmod tests;\n")
	if got, want := DeclaredModules(src), []string{"cli", "store", "tests"}; !slices.Equal(got, want) {
		t.Errorf("DeclaredModules = %q, want %q", got, want)
	}
}
