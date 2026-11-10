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
