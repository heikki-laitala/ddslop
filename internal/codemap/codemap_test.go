package codemap

import "testing"

func TestModuleNamesGoPackagesAndRustTopModules(t *testing.T) {
	crates := map[string]string{"crates/ledger": "acme_ledger"}
	for file, want := range map[string]string{
		"internal/flow/flow.go":                 "internal/flow",
		"main.go":                               ".",
		"crates/ledger/src/lib.rs":              "acme_ledger",
		"crates/ledger/src/main.rs":             "acme_ledger",
		"crates/ledger/src/store.rs":            "acme_ledger::store",
		"crates/ledger/src/store/mod.rs":        "acme_ledger::store",
		"crates/ledger/src/store/postgres.rs":   "acme_ledger::store",
		"crates/ledger/src/bin/migrate.rs":      "acme_ledger::bin",
		"crates/token-credentials/src/lib.rs":   "token_credentials",
		"crates/token-credentials/src/codec.rs": "token_credentials::codec",
		"build.rs":                              ".",
		"src/lib.rs":                            ".",
		"src/store/postgres.rs":                 "store",
	} {
		if got := Module(file, crates); got != want {
			t.Errorf("Module(%q) = %q, want %q", file, got, want)
		}
	}
}
