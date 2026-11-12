package codemap

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func TestModuleNamesGoPackagesAndRustTopModules(t *testing.T) {
	crates := map[string]string{"crates/ledger": "acme_ledger", "crates/token-credentials": "token_credentials", ".": "acme"} //nolint:gosec // crate names, not credentials
	for file, want := range map[string]string{                                                                                //nolint:gosec // file paths, not credentials
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

func TestCrateNamesTheRustCrateOfAFile(t *testing.T) {
	crates := map[string]string{"crates/ledger": "acme_ledger", "crates/token-credentials": "token_credentials", ".": "acme"} //nolint:gosec // crate names, not credentials
	for file, want := range map[string]string{                                                                                //nolint:gosec // file paths, not credentials
		"crates/ledger/src/store/postgres.rs": "acme_ledger",
		"crates/ledger/src/lib.rs":            "acme_ledger",
		"crates/ledger/build.rs":              "acme_ledger",
		"crates/token-credentials/src/lib.rs": "token_credentials",
		"src/store.rs":                        ".", // the root crate stays unnamed
		"build.rs":                            ".",
		"internal/flow/flow.go":               "", // Go has no crates
	} {
		if got := Crate(file, crates); got != want {
			t.Errorf("Crate(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestLayersOfCratesAndOfTheModulesInsideOne(t *testing.T) {
	g := &flow.Graph{}
	add := func(file string, callees ...int) int {
		g.Units = append(g.Units, unit.Unit{File: file, Name: fmt.Sprint("f", len(g.Units))})
		g.Callees = append(g.Callees, callees)
		return len(g.Units) - 1
	}
	save := add("crates/store/src/lib.rs")
	check := add("crates/service/src/validate.rs", save)
	create := add("crates/service/src/lib.rs", check, save)
	add("crates/api/src/lib.rs", create)

	byCrate := Layers(g, ByCrate(nil))
	if want := [][]string{{"api"}, {"service"}, {"store"}}; !reflect.DeepEqual(byCrate.Layers, want) {
		t.Errorf("crate layers = %v, want %v", byCrate.Layers, want)
	}
	inService := Layers(g, InCrate("service", nil))
	if want := [][]string{{"service"}, {"service::validate"}}; !reflect.DeepEqual(inService.Layers, want) {
		t.Errorf("service's module layers = %v, want %v: calls leaving the crate take no part", inService.Layers, want)
	}
}

func TestFilesOutsideSrcBelongToTheNearestCrate(t *testing.T) {
	// crates maps the directories holding a Cargo.toml.
	crates := map[string]string{"crates/ledger": "acme_ledger", ".": "acme"}
	for file, want := range map[string][2]string{
		"crates/ledger/examples/demo.rs": {"acme_ledger", "acme_ledger"},
		"crates/ledger/src/store.rs":     {"acme_ledger", "acme_ledger::store"},
		"examples/demo.rs":               {".", "."},
		"src/store.rs":                   {".", "store"},
	} {
		if c, m := Crate(file, crates), Module(file, crates); c != want[0] || m != want[1] {
			t.Errorf("%s: crate %q, module %q; want %q, %q", file, c, m, want[0], want[1])
		}
	}
}

func TestFilesOutsideEveryPackageBelongToNoCrate(t *testing.T) {
	crates := map[string]string{"crates/app": "app"}
	if c, m := Crate("scripts/release.rs", crates), Module("scripts/release.rs", crates); c != "" || m != "" {
		t.Errorf("scripts/release.rs: crate %q, module %q; want neither, it is in no package", c, m)
	}
	if c := Crate("scripts/release.rs", nil); c != "scripts" {
		t.Errorf("without manifests, crate %q, want the directory scripts", c)
	}
}
