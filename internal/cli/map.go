package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
)

const mapUsage = `usage: ddslop map [flags] [path]

Describes the conventions of the code under path (default "."), for an
agent to read before writing code: the layers its modules form, the calls
breaking them, and the helpers other modules share. Each language gets a
map of its own. A module is a Go package, or a Rust module directly under
its crate. A Rust workspace is described as crates, each with how many
modules and layers it holds; --crate describes one crate's modules, as a
single crate is by default. Calls are resolved by name, as in flows; only
calls that name their target count, and test code is left out.

Flags:
`

// mapReport is the JSON output of map.
type mapReport struct {
	Maps []mapJSON `json:"maps"`
}

type mapJSON struct {
	Lang string `json:"lang"`
	// Scope says what the layers hold: "packages" (Go), "crates" (a Rust
	// workspace) or "crate" (the modules of Crate).
	Scope         string      `json:"scope"`
	Crate         string      `json:"crate,omitempty"`
	Crates        []crateJSON `json:"crates,omitempty"`
	Calls         int         `json:"calls"`
	ResolvedCalls int         `json:"resolved_calls"`
	Layers        []layerJSON `json:"layers"`
	Breaks        []breakJSON `json:"breaks"`
	// Unplaced are modules no counted call goes to or comes from, and
	// UnplacedMore counts those left out by --limit.
	Unplaced     []string     `json:"unplaced"`
	UnplacedMore int          `json:"unplaced_more"`
	Helpers      []helperJSON `json:"helpers"`
}

// crateJSON summarises a crate in a workspace map: how many modules it
// holds and how many layers they form (see --crate).
type crateJSON struct {
	Name    string `json:"name"`
	Modules int    `json:"modules"`
	Layers  int    `json:"layers"`
}

type layerJSON struct {
	Depth   int      `json:"depth"`
	Modules []string `json:"modules"`
	// More counts the modules of the layer left out by --limit.
	More int `json:"more"`
}

type breakJSON struct {
	Kind       string `json:"kind"`
	From       string `json:"from"`
	To         string `json:"to"`
	Via        string `json:"via,omitempty"`
	Calls      int    `json:"calls"`
	UsualCalls int    `json:"usual_calls"`
	// Functions are the calling functions, as "file:line name".
	Functions []string `json:"functions"`
}

type helperJSON struct {
	Name      string   `json:"name"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	Signature string   `json:"signature"`
	Modules   []string `json:"modules"`
}

func runMap(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("map", mapUsage, stderr)
	format := fs.String("format", "", `output format, "text" or "json" (default: text on a terminal, json otherwise)`)
	limit := fs.Int("limit", 10, "list at most this many modules per layer, breaks, functions per break and helpers")
	crate := fs.String("crate", "", "describe the modules of this Rust crate (default: a workspace's crates)")
	root, code, ok := parseRoot(fs, args, stderr)
	if !ok {
		return code
	}
	resolved, err := outputFormat(*format, stdout, "text", "json")
	if err != nil {
		return fail(stderr, err)
	}
	if *limit < 1 {
		return fail(stderr, fmt.Errorf("--limit must be at least 1, got %d", *limit))
	}

	db, files, _, err := openFiles(root)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = db.Close() }()

	r := mapReport{Maps: []mapJSON{}}
	for _, lang := range []string{"go", "rust"} {
		if *crate != "" && lang != "rust" {
			continue
		}
		var langFiles []index.File
		for _, f := range files {
			if languageOf(f.Path) == lang {
				langFiles = append(langFiles, f)
			}
		}
		if len(langFiles) == 0 {
			continue
		}
		m, err := buildMap(root, lang, langFiles, *limit, *crate)
		if err != nil {
			return fail(stderr, err)
		}
		r.Maps = append(r.Maps, m)
	}
	if *crate != "" && len(r.Maps) == 0 {
		return fail(stderr, fmt.Errorf("no Rust code under %s for --crate %s", root, *crate))
	}

	if resolved == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	} else {
		err = writeMapText(stdout, r)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

// buildMap describes one language's files: Go by package, a Rust
// workspace by crate, and one Rust crate (want, or the only one) by module.
func buildMap(root, lang string, files []index.File, limit int, want string) (mapJSON, error) {
	g, traits := callGraph(root, files)
	crates := packages(root, files)
	m := mapJSON{Lang: lang, Scope: "packages", Calls: g.Calls, ResolvedCalls: g.Resolved,
		Layers: []layerJSON{}, Breaks: []breakJSON{}, Unplaced: []string{}, Helpers: []helperJSON{}}
	layerGroup, helperGroup := codemap.ByModule(crates), codemap.ByModule(crates)
	if lang == "rust" {
		names := crateNames(files, crates)
		switch {
		case want != "" && !slices.Contains(names, want):
			return m, fmt.Errorf("no crate %q; the crates are %s", want, strings.Join(names, ", "))
		case want != "":
			m.Scope, m.Crate = "crate", want
		case len(names) == 1:
			m.Scope, m.Crate = "crate", names[0]
		default:
			m.Scope = "crates"
			layerGroup = codemap.ByCrate(crates)
			for _, name := range names {
				m.Crates = append(m.Crates, crateSummary(g, crates, name))
			}
		}
		if m.Scope == "crate" {
			layerGroup, helperGroup = codemap.InCrate(m.Crate, crates), codemap.InCrate(m.Crate, crates)
			// The crate's own call sites, as its layers come from them alone.
			m.Calls, m.ResolvedCalls = 0, 0
			for i, u := range g.Units {
				if layerGroup(u) != "" {
					m.Calls += len(u.Calls)
					m.ResolvedCalls += g.ResolvedOf[i]
				}
			}
		}
	}

	layers := codemap.Layers(g, layerGroup)
	for d, modules := range layers.Layers {
		l := layerJSON{Depth: d, Modules: modules}
		if len(modules) > limit {
			l.Modules, l.More = modules[:limit], len(modules)-limit
		}
		m.Layers = append(m.Layers, l)
	}
	m.Unplaced = append(m.Unplaced, layers.Unplaced[:min(limit, len(layers.Unplaced))]...)
	m.UnplacedMore = len(layers.Unplaced) - len(m.Unplaced)
	for _, b := range layers.Breaks[:min(limit, len(layers.Breaks))] {
		bj := breakJSON{Kind: string(b.Kind), From: b.From, To: b.To, Via: b.Via, Calls: b.Calls, UsualCalls: b.UsualCalls}
		for _, c := range b.Callers[:min(limit, len(b.Callers))] {
			u := g.Units[c]
			bj.Functions = append(bj.Functions, fmt.Sprintf("%s:%d %s", u.File, u.StartLine, u.Name))
		}
		m.Breaks = append(m.Breaks, bj)
	}
	helpers := codemap.Helpers(g, helperGroup, g.Entries(traits))
	for _, h := range helpers[:min(limit, len(helpers))] {
		u := g.Units[h.Unit]
		m.Helpers = append(m.Helpers, helperJSON{Name: shortName(u.Name), File: u.File, Line: u.StartLine,
			Signature: sourceLine(root, u.File, u.StartLine), Modules: h.Modules})
	}
	return m, nil
}

// packages maps the directories under root holding a package's Cargo.toml
// to the crate's name, for every directory above a Rust file.
func packages(root string, files []index.File) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	for _, f := range files {
		if languageOf(f.Path) != "rust" {
			continue
		}
		for dir := filepath.Dir(f.Path); !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			if name := rust.CrateName(manifest(root, dir)); name != "" {
				out[filepath.ToSlash(dir)] = name
			}
			if dir == "." || dir == string(filepath.Separator) {
				break
			}
		}
	}
	return out
}

// crateNames returns the crates of the Rust files outside test paths,
// sorted: a crate of only types or constants is a crate too, though no
// function of it is in the call graph.
func crateNames(files []index.File, crates map[string]string) []string {
	var names []string
	for _, f := range files {
		if c := codemap.Crate(f.Path, crates); c != "" && !rust.IsTestPath(f.Path) {
			names = append(names, c)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// crateSummary counts a crate's modules and the layers they form.
func crateSummary(g *flow.Graph, crates map[string]string, name string) crateJSON {
	in := codemap.InCrate(name, crates)
	var modules []string
	for _, u := range g.Units {
		if m := in(u); m != "" {
			modules = append(modules, m)
		}
	}
	slices.Sort(modules)
	return crateJSON{Name: name, Modules: len(slices.Compact(modules)), Layers: len(codemap.Layers(g, in).Layers)}
}

// shortName drops a method's owner: helpers are listed by the name code
// calls them by.
func shortName(name string) string {
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// sourceLine returns a line of a file under root, trimmed, or "" when it
// cannot be read.
func sourceLine(root, file string, line int) string {
	f, err := os.Open(filepath.Join(root, file)) //nolint:gosec // a source file under the analysed root
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		if n == line {
			return strings.TrimSpace(s.Text())
		}
	}
	return ""
}

func writeMapText(w io.Writer, r mapReport) error {
	var b strings.Builder
	for i, m := range r.Maps {
		if i > 0 {
			b.WriteString("\n")
		}
		scope := m.Scope
		if m.Scope == "crate" {
			scope = "crate " + m.Crate
		}
		fmt.Fprintf(&b, "%s, %s: %d of %d calls resolved by name\n", m.Lang, scope, m.ResolvedCalls, m.Calls)
		if len(m.Crates) > 0 {
			b.WriteString("\nCrates (--crate NAME for one crate's modules):\n")
			for _, c := range m.Crates {
				fmt.Fprintf(&b, "  %s  %d module(s), %d layer(s)\n", c.Name, c.Modules, c.Layers)
			}
		}
		b.WriteString("\nLayers, top first:\n")
		for _, l := range m.Layers {
			more := ""
			if l.More > 0 {
				more = fmt.Sprintf(" and %d more", l.More)
			}
			fmt.Fprintf(&b, "  %d  %s%s\n", l.Depth, strings.Join(l.Modules, ", "), more)
		}
		if n := len(m.Unplaced) + m.UnplacedMore; n > 0 {
			fmt.Fprintf(&b, "\nUnplaced (no counted calls to or from other modules): %d\n  %s", n, strings.Join(m.Unplaced, ", "))
			if m.UnplacedMore > 0 {
				fmt.Fprintf(&b, " and %d more", m.UnplacedMore)
			}
			b.WriteString("\n")
		}
		if len(m.Breaks) > 0 {
			b.WriteString("\nCalls breaking the layers:\n")
			for _, br := range m.Breaks {
				fmt.Fprintf(&b, "  %s  %s -> %s", br.Kind, br.From, br.To)
				if br.Via != "" {
					fmt.Fprintf(&b, " past %s", br.Via)
				}
				fmt.Fprintf(&b, ": %d call(s) against %d\n", br.Calls, br.UsualCalls)
				for _, f := range br.Functions {
					fmt.Fprintf(&b, "      %s\n", f)
				}
			}
		}
		if len(m.Helpers) > 0 {
			b.WriteString("\nShared helpers:\n")
			for _, h := range m.Helpers {
				fmt.Fprintf(&b, "  %s (%s:%d), used by %s\n      %s\n", h.Name, h.File, h.Line, strings.Join(h.Modules, ", "), h.Signature)
			}
		}
	}
	if len(r.Maps) == 0 {
		b.WriteString("no Go or Rust code found\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
