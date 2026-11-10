package rust

import "testing"

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
	}
	for _, c := range cases {
		if got := Unpublished([]byte(c.manifest), []byte(c.workspace)); got != c.want {
			t.Errorf("%s: Unpublished = %v, want %v", c.name, got, c.want)
		}
	}
}
