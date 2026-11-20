package cli

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestTypeScriptSuggestionsKeepThePackageCompiling applies too-public's
// and dead-code's suggestions to generated private TypeScript packages and
// fails when tsc (strict, noUnusedLocals) then reports an error. Each
// package's files export functions and constants, used at random from
// their own file, an import, a namespace import, an export * barrel, an
// aliased re-export, a test file or only a comment. Narrowing removes the
// export keyword, and deleting removes the declaration.
//
// It runs tsc on each package, so it is opt-in: DDSLOP_TS_CASES sets how
// many packages (make oracle-ts), DDSLOP_TS_SEED the first seed, and
// DDSLOP_TSC a tsc command (default: TypeScript 5.9 through npx).
func TestTypeScriptSuggestionsKeepThePackageCompiling(t *testing.T) {
	tsc := strings.Fields(os.Getenv("DDSLOP_TSC"))
	if len(tsc) == 0 {
		tsc = []string{"npx", "--yes", "-p", "typescript@5.9", "tsc"}
	}
	suggestionOracle{
		env: "DDSLOP_TS", target: "make oracle-ts", gen: genTSPackage,
		build: func(root string) (string, bool) { return runTSC(tsc, root) },
		narrow: func(t *testing.T, root, name string) {
			editTSDeclaration(t, root, name, func(decl string) string { return strings.TrimPrefix(decl, "export ") })
		},
		remove: func(t *testing.T, root, name string) {
			editTSDeclaration(t, root, name, func(string) string { return "" })
		},
	}.run(t)
}

// genTSPackage returns the files of a random private TypeScript package.
func genTSPackage(rng *rand.Rand) map[string]string {
	files := map[string]string{
		"package.json":  `{"name": "web", "private": true}` + "\n",
		"tsconfig.json": `{"compilerOptions": {"strict": true, "noEmit": true, "noUnusedLocals": true, "module": "esnext", "moduleResolution": "bundler", "target": "es2022", "skipLibCheck": true}, "include": ["src"]}` + "\n",
	}
	n, users := 0, 0
	user := func(lines ...string) { // a file of its own for each use
		files[fmt.Sprintf("src/u%d.ts", users)] = strings.Join(lines, "\n") + "\n"
		users++
	}
	for m := range 1 + rng.IntN(3) {
		mod := "m" + strconv.Itoa(m)
		var decls, own, tested, testUses []string
		for range 1 + rng.IntN(4) {
			name, ref := fmt.Sprintf("C%d", n), fmt.Sprintf("C%d", n)
			if rng.IntN(2) == 0 {
				name = fmt.Sprintf("f%d", n)
				ref = name + "()"
				decls = append(decls, "export function "+name+"() { return "+strconv.Itoa(n)+"; }")
			} else {
				decls = append(decls, "export const "+name+" = "+strconv.Itoa(n)+";")
			}
			n++
			for range rng.IntN(3) {
				switch rng.IntN(7) {
				case 0:
					own = append(own, "console.log("+ref+");")
				case 1:
					user(`import { `+name+` } from "./`+mod+`";`, "console.log("+ref+");")
				case 2:
					user(`import * as ns from "./`+mod+`";`, "console.log(ns."+ref+");")
				case 3: // a barrel and a module importing through it
					files["src/index.ts"] += `export * from "./` + mod + `";` + "\n"
					user(`import { `+name+` } from "./index";`, "console.log("+ref+");")
				case 4:
					alias := "alias" + strconv.Itoa(users)
					user(`export { ` + name + ` as ` + alias + ` } from "./` + mod + `";`)
					user(`import { `+alias+` } from "./u`+strconv.Itoa(users-1)+`";`, "console.log("+strings.Replace(ref, name, alias, 1)+");")
				case 5: // the module's test file imports each name once
					if !slices.Contains(tested, name) {
						tested = append(tested, name)
					}
					testUses = append(testUses, "console.log("+ref+");")
				default:
					own = append(own, "// "+name+" is only mentioned here.")
				}
			}
		}
		files["src/"+mod+".ts"] = strings.Join(append(decls, own...), "\n") + "\n"
		if len(tested) > 0 {
			files["src/"+mod+".test.ts"] = `import { ` + strings.Join(tested, ", ") + ` } from "./` + mod + `";` + "\n" + strings.Join(testUses, "\n") + "\n"
		}
	}
	return files
}

// runTSC type-checks the package at root.
func runTSC(tsc []string, root string) (string, bool) {
	cmd := exec.Command(tsc[0], append(tsc[1:], "-p", root)...) //nolint:gosec // the test's own tsc command
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// editTSDeclaration replaces the one-line declaration of name with what
// edit makes of it ("" deletes it).
func editTSDeclaration(t *testing.T, root, name string, edit func(decl string) string) {
	t.Helper()
	decl := regexp.MustCompile(`(?m)^(export )?(function ` + name + `\(\).*|const ` + name + ` = .*)\n`)
	files, _ := filepath.Glob(filepath.Join(root, "src", "*.ts"))
	for _, f := range files {
		src, _ := os.ReadFile(f) //nolint:gosec // a generated package's file
		if decl.Match(src) {
			out := decl.ReplaceAllStringFunc(string(src), func(d string) string {
				if e := edit(strings.TrimSuffix(d, "\n")); e != "" {
					return e + "\n"
				}
				return ""
			})
			_ = os.WriteFile(f, []byte(out), 0o600) //nolint:gosec // a generated package's file
			return
		}
	}
	t.Fatalf("no declaration of %s", name)
}
