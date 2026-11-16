package tsfile

import "testing"

func TestPackageNameReadsPackageJSON(t *testing.T) {
	for manifest, want := range map[string]string{
		`{"name": "@acme/db", "version": "1.0.0"}`: "@acme/db",
		`{"private": true}`:                        "",
		`not json`:                                 "",
	} {
		if got := PackageName([]byte(manifest)); got != want {
			t.Errorf("PackageName(%s) = %q, want %q", manifest, got, want)
		}
	}
}
