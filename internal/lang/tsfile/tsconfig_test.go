package tsfile

import (
	"os"
	"path"
	"strings"
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
}`, "{src @/*:src/*,src/generated/* config:src/config/index.ts}"},
		{"apps/web", `{"extends": "./missing.json", "compilerOptions": {"paths": {"@/*": ["./src/*"]}}}`, "{ @/*:apps/web/src/*}"},
		{"apps/admin/nested", `{"extends": "../tsconfig.json"}`, "{src #shared/*:src/shared/*}"},
		{"lib", files["lib/tsconfig.json"], "{ ~/*:lib/src/*}"},
		{".", `{"compilerOptions": {"baseUrl": "."}}`, "{. }"},
	} {
		got, ok := ReadConfig(c.dir, []byte(c.data), read)
		if s := describe(got); !ok || s != c.want {
			t.Errorf("ReadConfig(%s) = %s, %v; want %s", c.dir, s, ok, c.want)
		}
	}
	if _, ok := ReadConfig(".", []byte("not json"), read); ok {
		t.Error("ReadConfig read a tsconfig from text that is no JSON")
	}
}

// describe writes a config as its baseUrl and each pattern with its
// targets, read from where they resolve.
func describe(c Config) string {
	from := c.BaseURL
	if from == "" {
		from = c.PathsDir
	}
	var patterns []string
	for _, p := range c.Paths {
		var targets []string
		for _, t := range p.Targets {
			targets = append(targets, path.Join(from, t))
		}
		patterns = append(patterns, p.Pattern+":"+strings.Join(targets, ","))
	}
	return "{" + c.BaseURL + " " + strings.Join(patterns, " ") + "}"
}

// A module path through the nearest tsconfig.json's paths names the first
// of the pattern's targets that is a file of ours, as tsc's does; with no
// pattern matching, a path under its baseUrl does. The exact pattern wins
// over a wildcard, the longest prefix over a shorter one.
func TestModulesPathFollowsTheNearestTsconfig(t *testing.T) {
	m := Modules{
		Configs: map[string]Config{
			"apps/web": {Paths: []Pattern{
				{"@/*", []string{"apps/web/src/*", "apps/web/generated/*"}},
				{"@/ui/*", []string{"packages/ui/src/*"}},
				{"@config", []string{"apps/web/src/config.ts"}},
				{"@/missing/*", []string{"apps/web/nowhere/*"}},
				{"virtual:pwa*", []string{"apps/web/src/pwa*"}},
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
		Configs:  map[string]Config{"apps/web": {Paths: []Pattern{{"@acme/ui", []string{"apps/web/src/ui-shim"}}}}},
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

// A tsconfig is read as tsc reads it: inherited paths resolve from the
// child's own baseUrl when it sets one, of two wildcard patterns with
// prefixes of the same length the first declared wins, and a file may
// start with a UTF-8 byte order mark.
func TestReadConfigFollowsTsc(t *testing.T) {
	files := map[string]string{"shared/tsconfig.json": `{"compilerOptions": {"paths": {"@/*": ["lib/*"]}}}`}
	read := func(file string) ([]byte, error) {
		if data, ok := files[file]; ok {
			return []byte(data), nil
		}
		return nil, os.ErrNotExist
	}
	for _, c := range []struct {
		name, data, module, want string
	}{
		{"inherited paths", `{"extends": "../../shared/tsconfig.json", "compilerOptions": {"baseUrl": "./src"}}`, "@/db", "apps/web/src/lib/db"},
		{"declaration order", `{"compilerOptions": {"paths": {"@/*/db": ["first/*/db"], "@/*": ["second/*"]}}}`, "@/x/db", "apps/web/first/x/db"},
		{"byte order mark", "\ufeff" + `{"compilerOptions": {"paths": {"@/*": ["src/*"]}}}`, "@/db", "apps/web/src/db"},
	} {
		for range 20 {
			config, ok := ReadConfig("apps/web", []byte(c.data), read)
			m := Modules{Configs: map[string]Config{"apps/web": config}, Stems: map[string]bool{
				"apps/web/src/lib/db": true, "shared/lib/db": true, "apps/web/first/x/db": true, "apps/web/second/x/db": true, "apps/web/src/db": true,
			}}
			if got, found := m.Path("apps/web/src/app.ts", c.module); !ok || !found || got != c.want {
				t.Errorf("%s: Path(%s) = %s, %v (read %v); want %s", c.name, c.module, got, found, ok, c.want)
				break
			}
		}
	}
}
