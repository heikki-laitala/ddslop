package tsfile

import "testing"

func TestReadManifestReadsNameAndPrivacy(t *testing.T) {
	for manifest, want := range map[string]Manifest{
		`{"name": "@acme/db", "version": "1.0.0"}`: {Name: "@acme/db"},
		`{"name": "web", "private": true}`:         {Name: "web", Private: true},
	} {
		if got, ok := ReadManifest([]byte(manifest)); !ok || got != want {
			t.Errorf("ReadManifest(%s) = %+v, %v; want %+v", manifest, got, ok, want)
		}
	}
	if _, ok := ReadManifest([]byte("not json")); ok {
		t.Error("ReadManifest read a manifest from text that is no JSON")
	}
}

func TestPackageDirIsTheNearestPackageAbove(t *testing.T) {
	packages := map[string]bool{".": true, "packages/db": true, "packages/db/examples/demo": true}
	for file, want := range map[string]string{
		"packages/db/src/query.ts":                "packages/db",
		"packages/db/examples/demo/src/x.ts":      "packages/db/examples/demo",
		"apps/web/src/page.tsx":                   ".",
		"packages/db/examples/other/src/other.ts": "packages/db",
	} {
		if got := PackageDir(file, packages); got != want {
			t.Errorf("PackageDir(%s) = %s, want %s", file, got, want)
		}
	}
}

func TestAliasMayBeMatchesTheEndOfAPath(t *testing.T) {
	for _, c := range []struct {
		module, file string
		want         bool
	}{
		{"@/db/schema", "apps/web/src/db/schema.ts", true},
		{"~/server/db", "src/server/db/index.ts", true},
		{"#db/schema", "src/db/schema.ts", true}, // a package.json "imports" entry
		{"@/db/schema", "apps/web/src/db/schemas.ts", false},
		{"@acme/db", "packages/acme/db.ts", false}, // a scoped package, no alias
		{"./schema", "src/schema.ts", false},
	} {
		if got := AliasMayBe(c.module, c.file); got != c.want {
			t.Errorf("AliasMayBe(%s, %s) = %v, want %v", c.module, c.file, got, c.want)
		}
	}
}
