package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/index"
)

const mapUsage = `usage: ddslop map [flags] [path]

Describes the conventions of the code under path (default "."), for an
agent to read before writing code: the layers its modules form, the calls
breaking them, and the helpers other modules share. Each language gets a
map of its own. A module is a Go package, or a Rust module directly under
its crate. Calls are resolved by name, as in flows; only resolved calls
count, and test code is left out.

Flags:
`

// mapReport is the JSON output of map.
type mapReport struct {
	Maps []mapJSON `json:"maps"`
}

type mapJSON struct {
	Lang          string      `json:"lang"`
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
		var langFiles []index.File
		for _, f := range files {
			if languageOf(f.Path) == lang {
				langFiles = append(langFiles, f)
			}
		}
		if len(langFiles) > 0 {
			r.Maps = append(r.Maps, buildMap(root, lang, langFiles, *limit))
		}
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

// buildMap describes one language's files.
func buildMap(root, lang string, files []index.File, limit int) mapJSON {
	g, traits := callGraph(root, files)
	crates := graphInputs(root, files).Crates
	m := mapJSON{Lang: lang, Calls: g.Calls, ResolvedCalls: g.Resolved,
		Layers: []layerJSON{}, Breaks: []breakJSON{}, Unplaced: []string{}, Helpers: []helperJSON{}}

	layers := codemap.Layers(g, codemap.ByModule(crates))
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
	helpers := codemap.Helpers(g, codemap.ByModule(crates), g.Entries(traits))
	for _, h := range helpers[:min(limit, len(helpers))] {
		u := g.Units[h.Unit]
		m.Helpers = append(m.Helpers, helperJSON{Name: shortName(u.Name), File: u.File, Line: u.StartLine,
			Signature: sourceLine(root, u.File, u.StartLine), Modules: h.Modules})
	}
	return m
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
		fmt.Fprintf(&b, "%s: %d of %d calls resolved by name\n\nLayers, top first:\n", m.Lang, m.ResolvedCalls, m.Calls)
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
