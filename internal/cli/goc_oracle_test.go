package cli

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestGoSuggestionsKeepTheModuleBuilding applies too-public's and
// dead-code's suggestions to generated Go modules and fails when the
// module then no longer type-checks, its tests included, for linux,
// windows or darwin. Each module's internal packages export functions,
// constants and iota groups, used at random from their own package, a
// file with build constraints, the package's own tests, its external tests
// (package p_test), another package, package main or only a comment.
// Unexporting renames the name throughout its package's directory, and
// deleting removes its declaration.
//
// It runs go vet on each module, so it is opt-in: DDSLOP_GO_CASES sets how
// many modules (make oracle-go), DDSLOP_GO_SEED the first seed.
func TestGoSuggestionsKeepTheModuleBuilding(t *testing.T) {
	cache := t.TempDir() // shared, so go reuses its build state
	suggestionOracle{
		env: "DDSLOP_GO", target: "make oracle-go", gen: genGoModule,
		build:  func(root string) (string, bool) { return goVet(root, cache) },
		narrow: unexport, remove: deleteDeclaration,
	}.run(t)
}

// suggestionOracle applies too-public's and dead-code's suggestions to
// generated code and fails when build no longer accepts it. env names
// the environment variables of the case count and first seed (env_CASES,
// env_SEED), and target the make target that sets them.
type suggestionOracle struct {
	env, target    string
	gen            func(rng *rand.Rand) map[string]string
	build          func(root string) (string, bool)
	narrow, remove func(t *testing.T, root, name string)
}

func (o suggestionOracle) run(t *testing.T) {
	t.Helper()
	cases, _ := strconv.Atoi(os.Getenv(o.env + "_CASES"))
	if cases <= 0 {
		t.Skipf("set %s_CASES to apply ddslop's suggestions to generated code and build it (%s)", o.env, o.target)
	}
	first, _ := strconv.ParseUint(os.Getenv(o.env+"_SEED"), 10, 64)
	applied := 0
	for seed := first; seed < first+uint64(cases); seed++ { //nolint:gosec // a case count is positive
		files := o.gen(rand.New(rand.NewPCG(seed, 0))) //nolint:gosec // reproducible test input, not security
		root := repo(t, files)
		if out, ok := o.build(root); !ok {
			t.Fatalf("seed %d: the generated code does not build:\n%s\n%s", seed, out, dump(files))
		}
		names := candidateNames(t, root)
		var edits []string
		for name := range names["too-public"] {
			o.narrow(t, root, name)
			edits = append(edits, "narrowed "+name)
		}
		for name := range names["dead-code"] {
			o.remove(t, root, name)
			edits = append(edits, "deleted "+name)
		}
		applied += len(edits)
		if out, ok := o.build(root); !ok {
			slices.Sort(edits)
			t.Errorf("seed %d: after %s, the code does not build:\n%s\n%s", seed, strings.Join(edits, ", "), out, dump(files))
		}
	}
	t.Logf("%d cases; %d suggestions applied", cases, applied)
}

type goItem struct {
	name string
	fn   bool
}

// genGoModule returns the files of a random Go module.
func genGoModule(rng *rand.Rand) map[string]string {
	files := map[string]string{"go.mod": "module example.com/fz\n\ngo 1.25\n"}
	type use struct{ file, pkg, text string }
	var uses []use
	n := 0
	pkgs := 1 + rng.IntN(3)
	for p := range pkgs {
		pkg := "p" + strconv.Itoa(p)
		dir := "internal/" + pkg
		var decls []string
		var items []goItem
		for range 1 + rng.IntN(4) {
			it := goItem{fmt.Sprintf("C%d", n), false}
			if rng.IntN(2) == 0 {
				it = goItem{fmt.Sprintf("F%d", n), true}
				decls = append(decls, "func "+it.name+"() int { return "+strconv.Itoa(n)+" }")
			} else {
				decls = append(decls, "const "+it.name+" = "+strconv.Itoa(n))
			}
			items = append(items, it)
			n++
		}
		if rng.IntN(3) == 0 { // an iota group: deleting one would renumber the rest
			a, b := fmt.Sprintf("K%d", n), fmt.Sprintf("K%d", n+1)
			decls = append(decls, "const (\n\t"+a+" = iota\n\t"+b+"\n)")
			items = append(items, goItem{a, false}, goItem{b, false})
			n += 2
		}
		files[dir+"/"+pkg+".go"] = "package " + pkg + "\n\n" + strings.Join(decls, "\n\n") + "\n"
		for _, it := range items {
			ref := it.name
			if it.fn {
				ref += "()"
			}
			for range rng.IntN(3) {
				switch rng.IntN(9) {
				case 0:
					uses = append(uses, use{dir + "/use.go", pkg, "var _ = " + ref})
				case 1:
					uses = append(uses, use{dir + "/use_linux.go", pkg, "var _ = " + ref})
				case 2:
					uses = append(uses, use{dir + "/use_windows.go", pkg, "var _ = " + ref})
				case 3:
					uses = append(uses, use{dir + "/use_internal_test.go", pkg, "var _ = " + ref})
				case 4:
					uses = append(uses, use{dir + "/use_ext_test.go", pkg + "_test", "var _ = " + pkg + "." + ref})
				case 5:
					if p+1 < pkgs { // a later package imports an earlier one: no import cycle
						other := "p" + strconv.Itoa(p+1+rng.IntN(pkgs-p-1))
						uses = append(uses, use{"internal/" + other + "/use_" + pkg + ".go", other, "var _ = " + pkg + "." + ref})
					}
				case 6:
					uses = append(uses, use{"cmd/app/use_" + pkg + ".go", "main", "var _ = " + pkg + "." + ref})
				case 7:
					uses = append(uses, use{dir + "/use_tagged.go", pkg, "var _ = " + ref})
				default:
					uses = append(uses, use{dir + "/doc.go", pkg, "// " + it.name + " is only mentioned here."})
				}
			}
		}
	}
	files["cmd/app/main.go"] = "package main\n\nfunc main() {}\n"
	byFile := map[string][]use{}
	for _, u := range uses {
		byFile[u.file] = append(byFile[u.file], u)
	}
	for file, us := range byFile {
		imports := map[string]bool{}
		var lines []string
		for _, u := range us {
			if pkg, _, ok := strings.Cut(strings.TrimPrefix(u.text, "var _ = "), "."); ok && !strings.HasPrefix(u.text, "//") {
				imports[pkg] = true
			}
			lines = append(lines, u.text)
		}
		var head []string
		if path.Base(file) == "use_tagged.go" {
			head = append(head, "//go:build linux", "")
		}
		head = append(head, "package "+us[0].pkg, "")
		for _, pkg := range slices.Sorted(maps.Keys(imports)) {
			head = append(head, `import "example.com/fz/internal/`+pkg+`"`)
		}
		files[file] = strings.Join(append(head, lines...), "\n") + "\n"
	}
	return files
}

// goVet type-checks the module at root, its tests included, for each
// operating system ddslop's users build for.
func goVet(root, cache string) (string, bool) {
	for _, goos := range []string{"linux", "windows", "darwin"} {
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir, cmd.Env = root, append(os.Environ(), "GOOS="+goos, "GOCACHE="+cache, "GOFLAGS=-mod=mod")
		if out, err := cmd.CombinedOutput(); err != nil {
			return goos + ": " + string(out), false
		}
	}
	return "", true
}

// unexport renames name to its lower-case form in every Go file of the
// directory declaring it, as an agent unexporting it would.
func unexport(t *testing.T, root, name string) {
	t.Helper()
	word := regexp.MustCompile(`\b` + name + `\b`)
	lower := string(unicode.ToLower(rune(name[0]))) + name[1:]
	dir := filepath.Dir(declaringFile(t, root, name))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if p := filepath.Join(dir, e.Name()); strings.HasSuffix(p, ".go") {
			src, _ := os.ReadFile(p)                                        //nolint:gosec // a generated module's file
			_ = os.WriteFile(p, word.ReplaceAll(src, []byte(lower)), 0o600) //nolint:gosec // a generated module's file
		}
	}
}

// deleteDeclaration removes a one-line declaration of name.
func deleteDeclaration(t *testing.T, root, name string) {
	t.Helper()
	file := declaringFile(t, root, name)
	src, _ := os.ReadFile(file) //nolint:gosec // a generated module's file
	decl := regexp.MustCompile(`(?m)^(func ` + name + `\(\).*|const ` + name + ` = .*)\n`)
	_ = os.WriteFile(file, decl.ReplaceAll(src, nil), 0o600) //nolint:gosec // a generated module's file
}

// declaringFile returns the file of the generated module declaring name.
func declaringFile(t *testing.T, root, name string) string {
	t.Helper()
	decl := regexp.MustCompile(`(?m)^(func ` + name + `\(|const ` + name + ` |\t` + name + `\b)`)
	var found string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error { //nolint:gosec // the test's own temporary module
		if found == "" && !d.IsDir() && strings.HasSuffix(p, ".go") {
			if src, err := os.ReadFile(p); err == nil && decl.Match(src) { //nolint:gosec // a generated module's file
				found = p
			}
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no declaration of %s", name)
	}
	return found
}
