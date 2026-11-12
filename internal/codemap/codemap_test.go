package codemap

import "testing"

func TestModuleNamesGoPackagesAndRustTopModules(t *testing.T) {
	crates := map[string]string{"crates/ledger": "acme_ledger"}
	for file, want := range map[string]string{ //nolint:gosec // file paths, not credentials
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
		"crates/ledger/build.rs":                "acme_ledger",
		"src/lib.rs":                            ".",
		"src/store/postgres.rs":                 "store",
	} {
		if got := Module(file, crates); got != want {
			t.Errorf("Module(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestTheRootCrateStaysUnnamedWhateverItsManifestSays(t *testing.T) {
	// A root build.rs makes the crates map name ".", but the root crate's
	// modules must not be renamed by it.
	crates := map[string]string{".": "acme"}
	for file, want := range map[string]string{"src/lib.rs": ".", "src/store.rs": "store", "build.rs": "."} {
		if got := Module(file, crates); got != want {
			t.Errorf("Module(%q) = %q, want %q", file, got, want)
		}
	}
}
