package tsfile

import (
	"fmt"
	"os"
	"testing"
)

// A tsconfig.json is JSON with comments and trailing commas. Its paths
// are read from the directory of the file declaring them, or its baseUrl;
// a relative extends gives what the file does not say itself.
func TestReadConfigReadsPathsFromItsDirectoryOrBaseURL(t *testing.T) {
	files := map[string]string{
		"tsconfig.base.json": `{
  // shared settings
  "compilerOptions": {
    "baseUrl": "./src", /* resolved here, not in the app */
    "paths": {"#shared/*": ["shared/*",],},
  },
}`,
		"apps/admin/tsconfig.json": `{"extends": "../../tsconfig.base"}`,
		"lib/tsconfig.json":        `{"extends": "@tsconfig/node20/tsconfig.json", "compilerOptions": {"paths": {"~/*": ["./src/*"]}}}`,
	}
	read := func(file string) ([]byte, error) {
		if data, ok := files[file]; ok {
			return []byte(data), nil
		}
		return nil, os.ErrNotExist
	}
	for _, c := range []struct {
		dir, data, want string
	}{
		{"apps/web", `{
  "extends": "../../tsconfig.base.json",
  "compilerOptions": {
    "paths": {
      "@/*": ["./*", "./generated/*"], // "//" in a string is no comment
      "config": ["./config/index.ts"],
    },
  },
}`, "{src map[@/*:[src/* src/generated/*] config:[src/config/index.ts]]}"},
		{"apps/web", `{"extends": "./missing.json", "compilerOptions": {"paths": {"@/*": ["./src/*"]}}}`, "{ map[@/*:[apps/web/src/*]]}"},
		{"apps/admin/nested", `{"extends": "../tsconfig.json"}`, "{src map[#shared/*:[src/shared/*]]}"},
		{"lib", files["lib/tsconfig.json"], "{ map[~/*:[lib/src/*]]}"},
		{".", `{"compilerOptions": {"baseUrl": "."}}`, "{. map[]}"},
	} {
		got, ok := ReadConfig(c.dir, []byte(c.data), read)
		if s := fmt.Sprint(got); !ok || s != c.want {
			t.Errorf("ReadConfig(%s) = %s, %v; want %s", c.dir, s, ok, c.want)
		}
	}
	if _, ok := ReadConfig(".", []byte("not json"), read); ok {
		t.Error("ReadConfig read a tsconfig from text that is no JSON")
	}
}

// A module path through the nearest tsconfig.json's paths names the first
// of the pattern's targets that is a file of ours, as tsc's does; with no
// pattern matching, a path under its baseUrl does. The exact pattern wins
// over a wildcard, the longest prefix over a shorter one.
func TestModulesPathFollowsTheNearestTsconfig(t *testing.T) {
	m := Modules{
		Configs: map[string]Config{
			"apps/web": {Paths: map[string][]string{
				"@/*":          {"apps/web/src/*", "apps/web/generated/*"},
				"@/ui/*":       {"packages/ui/src/*"},
				"@config":      {"apps/web/src/config.ts"},
				"@/missing/*":  {"apps/web/nowhere/*"},
				"virtual:pwa*": {"apps/web/src/pwa*"},
			}},
			"apps/api": {BaseURL: "apps/api/src"},
		},
		Stems: map[string]bool{
			"apps/web/src/lib/db": true, "apps/web/generated/schema": true, "apps/web/src/hooks/index": true,
			"packages/ui/src/button": true, "apps/web/src/config": true, "apps/api/src/services/mail": true,
			"apps/web/src/pwa-register": true, "apps/legacy/src/lib/db": true,
		},
	}
	for _, c := range []struct {
		importer, module, want string
	}{
		{"apps/web/src/pages/home.tsx", "@/lib/db", "apps/web/src/lib/db"},
		{"apps/web/src/pages/home.tsx", "@/lib/db.ts", "apps/web/src/lib/db"},
		{"apps/web/src/pages/home.tsx", "@/schema", "apps/web/generated/schema"},
		{"apps/web/src/pages/home.tsx", "@/hooks", "apps/web/src/hooks"},
		{"apps/web/src/pages/home.tsx", "@/ui/button", "packages/ui/src/button"},
		{"apps/web/src/pages/home.tsx", "@config", "apps/web/src/config"},
		{"apps/web/src/pages/home.tsx", "virtual:pwa-register", "apps/web/src/pwa-register"},
		{"apps/web/src/pages/home.tsx", "./util", "apps/web/src/pages/util"},
		{"apps/api/src/routes/send.ts", "services/mail", "apps/api/src/services/mail"},
	} {
		if got, ok := m.Path(c.importer, c.module); !ok || got != c.want {
			t.Errorf("Path(%s, %s) = %s, %v; want %s", c.importer, c.module, got, ok, c.want)
		}
	}
	for _, c := range []struct{ importer, module string }{
		{"apps/web/src/pages/home.tsx", "@/missing/db"}, // a matched pattern with no target of ours
		{"apps/web/src/pages/home.tsx", "zod"},
		{"apps/api/src/routes/send.ts", "zod"},
		{"apps/legacy/src/app.ts", "@/lib/db"}, // no tsconfig above it
	} {
		if got, ok := m.Path(c.importer, c.module); ok {
			t.Errorf("Path(%s, %s) = %s, want none", c.importer, c.module, got)
		}
	}
	if !m.Files("apps/web/src/pages/home.tsx", "@/hooks")("apps/web/src/hooks/index.ts") {
		t.Error("Files(@/hooks) is not the directory's index file")
	}
}

// A tsconfig's paths come before the workspace packages, as tsc looks in
// node_modules only for a path they do not resolve.
func TestModulesFilesPreferATsconfigPathToAPackage(t *testing.T) {
	m := Modules{
		Packages: map[string]string{"packages/ui": "@acme/ui"},
		Configs:  map[string]Config{"apps/web": {Paths: map[string][]string{"@acme/ui": {"apps/web/src/ui-shim"}}}},
		Stems:    map[string]bool{"apps/web/src/ui-shim": true, "packages/ui/src/index": true},
	}
	names := m.Files("apps/web/src/app.ts", "@acme/ui")
	if !names("apps/web/src/ui-shim.ts") || names("packages/ui/src/index.ts") {
		t.Error("Files(@acme/ui) is not the file the tsconfig's paths map it to")
	}
	if !m.Files("apps/api/src/app.ts", "@acme/ui")("packages/ui/src/index.ts") {
		t.Error("Files(@acme/ui) outside the tsconfig is not the workspace package")
	}
}
