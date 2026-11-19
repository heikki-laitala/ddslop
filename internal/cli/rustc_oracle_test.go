package cli

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestTooPublicAgreesWithRustcOnReachability compares too-public with the
// Rust compiler on generated crates. Every module of such a crate is
// pub(crate), so an item outside the crate root is reachable from other
// crates only through pub use re-exports (globs, named, aliases, chains,
// in every path form), which rustc's unreachable_pub lint reports
// exactly. A reachable item too-public reports is a failure: narrowing it
// would drop it from the crate's API. An unreachable item it does not
// report is a miss, logged only: too-public errs toward reporting less.
//
// It runs cargo on each crate, so it is opt-in: DDSLOP_RUSTC_CASES sets
// how many crates (make oracle), DDSLOP_RUSTC_SEED the first seed.
func TestTooPublicAgreesWithRustcOnReachability(t *testing.T) {
	cases, _ := strconv.Atoi(os.Getenv("DDSLOP_RUSTC_CASES"))
	if cases <= 0 {
		t.Skip("set DDSLOP_RUSTC_CASES to compare too-public with rustc (make oracle)")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Fatal("cargo is needed to compare too-public with rustc")
	}
	first, _ := strconv.ParseUint(os.Getenv("DDSLOP_RUSTC_SEED"), 10, 64)
	target := t.TempDir() // shared, so cargo reuses its build state
	missed := 0
	for seed := first; seed < first+uint64(cases); seed++ { //nolint:gosec // a case count is positive
		mods := genCrate(rand.New(rand.NewPCG(seed, 0))) //nolint:gosec // reproducible test input, not security
		files := crateFiles(mods)
		root := repo(t, files)
		unreachable := rustcUnreachable(t, root, target, seed, files)
		reported := tooPublicNames(t, root)
		for _, m := range mods[1:] { // a root item is reachable by its own path: too-public narrows it on purpose
			for _, it := range m.items {
				switch {
				case !unreachable[it.name] && reported[it.name]:
					t.Errorf("seed %d: %s is reachable from outside the crate, but too-public reports it\n%s", seed, it.name, dump(files))
				case unreachable[it.name] && !reported[it.name]:
					missed++
				}
			}
		}
	}
	t.Logf("%d crates; %d unreachable items too-public leaves alone", cases, missed)
}

// rustcModule is a module of a generated crate: the root, an out-of-line
// file, a file a #[path] attribute names, or an inline module.
type rustcModule struct {
	name, kind string
	children   []*rustcModule
	items      []rustcItem
	uses       []string // use declarations, pub use re-exports among them
	segs       []string // its module path
}

type rustcItem struct {
	fn   bool
	name string
}

func (it rustcItem) source() string {
	if it.fn {
		return "pub fn " + it.name + "() {}"
	}
	return "pub const " + it.name + ": u32 = 1;"
}

// genCrate returns the modules of a random crate, the root first.
func genCrate(rng *rand.Rand) []*rustcModule {
	root := &rustcModule{kind: "root"}
	mods := []*rustcModule{root}
	var grow func(m *rustcModule, depth int)
	grow = func(m *rustcModule, depth int) {
		low, high := 1, 2
		if depth > 0 {
			low = 0
		}
		if depth >= 3 {
			high = 0
		}
		for range low + rng.IntN(high-low+1) {
			kind := []string{"file", "file", "inline", "path"}[rng.IntN(4)]
			if m.kind == "inline" && kind == "path" {
				kind = "inline" // a #[path] inside an inline module follows other rules
			}
			c := &rustcModule{name: "m" + strconv.Itoa(len(mods)), kind: kind, segs: append(slices.Clone(m.segs), "m"+strconv.Itoa(len(mods)))}
			m.children = append(m.children, c)
			mods = append(mods, c)
			grow(c, depth+1)
		}
	}
	grow(root, 0)
	items := 0
	for _, m := range mods {
		for range rng.IntN(3) {
			fn := rng.IntN(2) == 0
			name := "C" + strconv.Itoa(items)
			if fn {
				name = "f" + strconv.Itoa(items)
			}
			m.items = append(m.items, rustcItem{fn, name})
			items++
		}
	}
	named := map[*rustcModule]map[string]bool{}
	for a := range 1 + rng.IntN(5) {
		at, to := mods[rng.IntN(len(mods))], mods[rng.IntN(len(mods))]
		p := modulePathFrom(at, to, rng.IntN(4))
		if to == root || to == at || p == "" {
			continue
		}
		alias := "al" + strconv.Itoa(a)
		switch rng.IntN(6) {
		case 0, 1:
			at.uses = append(at.uses, "pub use "+p+"::*;")
		case 2:
			if len(to.items) == 0 {
				continue
			}
			name := to.items[rng.IntN(len(to.items))].name
			if named[at] == nil {
				named[at] = map[string]bool{}
			}
			if !named[at][name] { // a module may import a name once
				named[at][name] = true
				at.uses = append(at.uses, "pub use "+p+"::"+name+";")
			}
		case 3:
			at.uses = append(at.uses, "use "+p+" as "+alias+";", "pub use "+alias+"::*;")
		case 4:
			at.uses = append(at.uses, "use "+p+" as "+alias+";", "use "+alias+" as b"+alias+";", "pub use b"+alias+"::*;")
		default:
			at.uses = append(at.uses, "use "+p+"::{self as "+alias+"};", "pub use "+alias+"::*;")
		}
	}
	return mods
}

// modulePathFrom returns a path from module from to module to, in one of
// four forms: from the crate root, through self::, bare, or relative
// (super:: up to the modules they share); "" when the form cannot name it.
func modulePathFrom(from, to *rustcModule, form int) string {
	if form == 0 {
		return strings.Join(append([]string{"crate"}, to.segs...), "::")
	}
	common := 0
	for common < min(len(from.segs), len(to.segs)) && from.segs[common] == to.segs[common] {
		common++
	}
	up, rest := len(from.segs)-common, to.segs[common:]
	switch {
	case up > 0:
		return strings.Join(append(slices.Repeat([]string{"super"}, up), rest...), "::")
	case len(rest) == 0:
		return ""
	case form == 1:
		return "self::" + strings.Join(rest, "::")
	}
	return strings.Join(rest, "::")
}

// crateFiles returns the files of a crate with these modules: a library
// that uses every item once, through the item's own path.
func crateFiles(mods []*rustcModule) map[string]string {
	files := map[string]string{"Cargo.toml": "[package]\nname = \"fz\"\nversion = \"0.1.0\"\nedition = \"2021\"\npublish = false\n"}
	lines := []string{"#![warn(unreachable_pub)]", "#![allow(dead_code, unused_imports, ambiguous_glob_reexports)]"}
	lines = append(lines, moduleBody(mods[0], 0, files, "src", true)...)
	lines = append(lines, "pub(crate) fn touch_all() {")
	for _, m := range mods {
		for _, it := range m.items {
			p := strings.Join(append(append([]string{"crate"}, m.segs...), it.name), "::")
			if it.fn {
				lines = append(lines, "    "+p+"();")
			} else {
				lines = append(lines, "    let _ = "+p+";")
			}
		}
	}
	files["src/lib.rs"] = strings.Join(append(lines, "}"), "\n") + "\n"
	return files
}

// moduleBody returns the lines of a module's contents, adding its
// out-of-line children to files. dir holds those children's files, and
// modRS says whether the file holding m is a root, mod.rs or #[path] file,
// whose own #[path] attributes are relative to its directory.
func moduleBody(m *rustcModule, indent int, files map[string]string, dir string, modRS bool) []string {
	pad := strings.Repeat("    ", indent)
	var out []string
	for _, u := range m.uses {
		out = append(out, pad+u)
	}
	for _, it := range m.items {
		out = append(out, pad+it.source())
	}
	for _, c := range m.children {
		switch c.kind {
		case "inline":
			out = append(out, pad+"pub(crate) mod "+c.name+" {")
			out = append(out, moduleBody(c, indent+1, files, path.Join(dir, c.name), modRS)...)
			out = append(out, pad+"}")
		case "file":
			out = append(out, pad+"pub(crate) mod "+c.name+";")
			files[path.Join(dir, c.name+".rs")] = strings.Join(moduleBody(c, 0, files, path.Join(dir, c.name), false), "\n") + "\n"
		default: // a #[path] file: its own modules sit beside it
			rel := "p_" + c.name + "/lib_" + c.name + ".rs"
			out = append(out, pad+"#[path = \""+rel+"\"]", pad+"pub(crate) mod "+c.name+";")
			base := dir
			if !modRS {
				base = path.Dir(dir)
			}
			file := path.Join(base, rel)
			files[file] = strings.Join(moduleBody(c, 0, files, path.Dir(file), true), "\n") + "\n"
		}
	}
	return out
}

var unreachableItem = regexp.MustCompile(`pub (?:fn|const) (\w+)`)

// rustcUnreachable returns the items rustc's unreachable_pub lint reports
// in the crate at root, failing the test if the crate does not compile.
func rustcUnreachable(t *testing.T, root, target string, seed uint64, files map[string]string) map[string]bool {
	t.Helper()
	cmd := exec.Command("cargo", "check", "--quiet", "--message-format=json")
	cmd.Dir, cmd.Env = root, append(os.Environ(), "CARGO_TARGET_DIR="+target)
	out, _ := cmd.Output()
	unreachable := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		var msg struct {
			Reason  string `json:"reason"`
			Message struct {
				Level    string `json:"level"`
				Rendered string `json:"rendered"`
				Code     *struct {
					Code string `json:"code"`
				} `json:"code"`
				Spans []struct {
					Primary bool `json:"is_primary"`
					Text    []struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"spans"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil || msg.Reason != "compiler-message" {
			continue
		}
		if msg.Message.Level == "error" {
			t.Fatalf("seed %d: the generated crate does not compile:\n%s\n%s", seed, msg.Message.Rendered, dump(files))
		}
		if msg.Message.Code == nil || msg.Message.Code.Code != "unreachable_pub" {
			continue
		}
		for _, s := range msg.Message.Spans {
			if s.Primary && len(s.Text) > 0 {
				if m := unreachableItem.FindStringSubmatch(s.Text[0].Text); m != nil {
					unreachable[m[1]] = true
				}
			}
		}
	}
	return unreachable
}

// tooPublicNames returns the names of the items check reports too public.
func tooPublicNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	_, stdout, _ := run("check", "--format", "json", root)
	names := map[string]bool{}
	for _, c := range decodeJSON[targetReport](t, stdout).Candidates {
		if c.Kind == "too-public" {
			names[c.Occurrences[0].Name] = true
		}
	}
	return names
}

// dump returns a crate's files, for a failure message.
func dump(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "== %s\n%s", p, files[p])
	}
	return b.String()
}
