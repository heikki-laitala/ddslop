package cli

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/config"
	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/golang"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/report"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

const mapUsage = `usage: ddslop map [flags] [path]

Describes the conventions of the code under path (default "."), for an
agent to read before writing code: the layers its modules form, the calls
breaking them, the helpers other modules share, the features that copy
one pipeline (parallel-flow candidates in check: extract the pipeline
rather than add another copy), and the idioms: the form the code uses
for each concern, such as checking and wrapping errors. Each language
gets a map of its own. A module is a Go package, or a Rust module directly under
its crate. A Rust workspace is described as crates, each with how many
modules and layers it holds; --crate describes one crate's modules, as a
single crate is by default, and --lang describes one language. Calls are resolved by name, as in flows; only
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
	Unplaced     []string      `json:"unplaced"`
	UnplacedMore int           `json:"unplaced_more"`
	Helpers      []helperJSON  `json:"helpers"`
	Features     []featureJSON `json:"features"`
	Idioms       []idiomJSON   `json:"idioms"`
	// Cycles are the groups of modules calling each other in a loop.
	Cycles []cycleJSON `json:"cycles"`
}

// cycleJSON is a group of modules calling each other in a loop: the pairs
// that call each other directly, those with the fewest calls one way
// first, or, when no pair does, a shortest loop through the group.
type cycleJSON struct {
	Modules     []string   `json:"modules"`
	ModulesMore int        `json:"modules_more"`
	Links       []linkJSON `json:"links"`
	LinksMore   int        `json:"links_more"`
	Loop        []hopJSON  `json:"loop,omitempty"`
}

// linkJSON is two modules of a cycle calling each other: the calls each
// way and a call site each way ("file:line caller -> callee").
//
// ddslop:ignore wire format of codemap.Link, call sites as text rather than unit indexes
type linkJSON struct {
	A       string `json:"a"`
	B       string `json:"b"`
	CallsAB int    `json:"calls_ab"`
	CallsBA int    `json:"calls_ba"`
	AB      string `json:"ab"`
	BA      string `json:"ba"`
}

// hopJSON is one step of a cycle's loop and its call site.
type hopJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
	Call string `json:"call"`
}

// idiomJSON is how the code does one concern: the convention (the form
// most occurrences use, with its share), or none and the language's
// idiomatic form, and every form with its count and first occurrence.
type idiomJSON struct {
	Concern     string          `json:"concern"`
	Convention  string          `json:"convention,omitempty"`
	Idiomatic   string          `json:"idiomatic,omitempty"`
	Share       float64         `json:"share"`
	Occurrences int             `json:"occurrences"`
	Forms       []formCountJSON `json:"forms"`
	// FormsMore counts the forms left out by --limit.
	FormsMore int `json:"forms_more"`
}

type formCountJSON struct {
	Form    string `json:"form"`
	Count   int    `json:"count"`
	Example string `json:"example"`
}

// featureJSON is a typical feature: so far only a parallel-flow family,
// flagged with its candidate kind and the pattern that extracts it.
type featureJSON struct {
	Concern string `json:"concern"`
	Flagged string `json:"flagged"`
	Pattern string `json:"pattern"`
	// Shape is the family's anchor and Members the others, as
	// "file:line name"; MembersMore and StepsMore count those left out by
	// --limit.
	Shape       string            `json:"shape"`
	Members     []string          `json:"members"`
	MembersMore int               `json:"members_more"`
	Steps       []featureStepJSON `json:"steps"`
	StepsMore   int               `json:"steps_more"`
}

// featureStepJSON is a step of a feature's shape and the shared helpers it
// calls.
type featureStepJSON struct {
	Function string   `json:"function"`
	Helpers  []string `json:"helpers"`
}

// crateJSON summarises a crate in a workspace map: how many modules it
// holds, how many layers they form, and how many call each other in loops
// (see --crate).
type crateJSON struct {
	Name    string `json:"name"`
	Modules int    `json:"modules"`
	Layers  int    `json:"layers"`
	// CycleModules counts the modules in its cycles (see --crate).
	CycleModules int `json:"cycle_modules"`
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
	limit := fs.Int("limit", 10, "list at most this many modules per layer, breaks, functions per break, helpers, features, steps and other members per feature, and forms per idiom")
	crate := fs.String("crate", "", "describe the modules of this Rust crate (default: a workspace's crates)")
	only := fs.String("lang", "", `describe only this language, "go" or "rust" (default: each language found)`)
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
	langs := []string{"go", "rust"}
	switch *only {
	case "":
	case "go", "rust":
		langs = []string{*only}
	default:
		return fail(stderr, fmt.Errorf(`--lang must be "go" or "rust", got %q`, *only))
	}
	if *crate != "" && *only == "go" {
		return fail(stderr, fmt.Errorf("--crate describes a Rust crate, not Go code"))
	}

	cfg, err := config.Load(root)
	if err != nil {
		return fail(stderr, err)
	}
	minNodes := defaultMinNodes
	if cfg.MinNodes != nil {
		minNodes = *cfg.MinNodes // as check reads it, so the map flags the families check reports
	}
	db, files, _, err := openFiles(root)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = db.Close() }()

	r := mapReport{Maps: []mapJSON{}}
	for _, lang := range langs {
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
		m, err := buildMap(root, lang, langFiles, mapOptions{limit: *limit, crate: *crate, minNodes: minNodes})
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
		err = writeMapText(stdout, r, langs)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

// mapOptions are map's settings: the --limit on each list, the --crate to
// describe, and the smallest function in a feature's steps.
type mapOptions struct {
	limit    int
	crate    string
	minNodes int
}

// buildMap describes one language's files: Go by package, a Rust
// workspace by crate, and one Rust crate (opts.crate, or the only one) by
// module.
func buildMap(root, lang string, files []index.File, opts mapOptions) (mapJSON, error) {
	limit, want := opts.limit, opts.crate
	g, traits := callGraph(root, files)
	crates := packages(root, files)
	m := mapJSON{Lang: lang, Scope: "packages", Calls: g.Calls, ResolvedCalls: g.Resolved,
		Layers: []layerJSON{}, Breaks: []breakJSON{}, Unplaced: []string{}, Helpers: []helperJSON{},
		Features: []featureJSON{}, Idioms: []idiomJSON{}, Cycles: []cycleJSON{}}
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
			bj.Functions = append(bj.Functions, located(g.Units[c]))
		}
		m.Breaks = append(m.Breaks, bj)
	}
	m.Cycles = cyclesJSON(g, codemap.Cycles(g, layerGroup), limit)
	helpers := codemap.Helpers(g, helperGroup, g.Entries(traits))
	for _, h := range helpers[:min(limit, len(helpers))] {
		u := g.Units[h.Unit]
		m.Helpers = append(m.Helpers, helperJSON{Name: shortName(u.Name), File: u.File, Line: u.StartLine,
			Signature: sourceLine(root, u.File, u.StartLine), Modules: h.Modules})
	}
	features := codemap.Features(g, families(g, traits, opts.minNodes), helperGroup, helpers)
	for _, f := range features[:min(limit, len(features))] {
		fj := featureJSON{Concern: "feature-pipelines", Flagged: "parallel-flow", Pattern: report.PipelinePattern,
			Shape: located(g.Units[f.Shape]), Members: []string{}, MembersMore: max(0, len(f.Members)-limit),
			StepsMore: max(0, len(f.Steps)-limit)}
		for _, u := range f.Members[:min(limit, len(f.Members))] {
			fj.Members = append(fj.Members, located(g.Units[u]))
		}
		for _, s := range f.Steps[:min(limit, len(f.Steps))] {
			sj := featureStepJSON{Function: located(g.Units[s.Unit]), Helpers: []string{}}
			for _, h := range s.Helpers {
				sj.Helpers = append(sj.Helpers, shortName(g.Units[h].Name))
			}
			fj.Steps = append(fj.Steps, sj)
		}
		m.Features = append(m.Features, fj)
	}
	idiomatic := golang.IdiomaticForms
	if lang == "rust" {
		idiomatic = rust.IdiomaticForms
	}
	for _, id := range codemap.Idioms(g, helperGroup, idiomatic) {
		ij := idiomJSON{Concern: id.Concern, Convention: id.Convention, Idiomatic: id.Idiomatic,
			Share: math.Round(float64(id.Forms[0].Count)/float64(id.Occurrences)*1000) / 1000, Occurrences: id.Occurrences,
			FormsMore: max(0, len(id.Forms)-limit)}
		for _, fc := range id.Forms[:min(limit, len(id.Forms))] {
			u := g.Units[fc.Unit]
			ij.Forms = append(ij.Forms, formCountJSON{Form: fc.Form, Count: fc.Count, Example: fmt.Sprintf("%s:%d %s", u.File, fc.Line, u.Name)})
		}
		m.Idioms = append(m.Idioms, ij)
	}
	return m, nil
}

// cyclesJSON describes cycles, each list capped at limit.
func cyclesJSON(g *flow.Graph, cycles []codemap.Cycle, limit int) []cycleJSON {
	site := func(s codemap.Site) string {
		if s.Caller < 0 {
			return ""
		}
		return located(g.Units[s.Caller]) + " -> " + g.Units[s.Callee].Name
	}
	out := []cycleJSON{}
	for _, c := range cycles[:min(limit, len(cycles))] {
		links := slices.Clone(c.Links)
		slices.SortStableFunc(links, func(x, y codemap.Link) int {
			return cmp.Compare(min(x.CallsAB, x.CallsBA), min(y.CallsAB, y.CallsBA))
		})
		cj := cycleJSON{Modules: c.Modules[:min(limit, len(c.Modules))], ModulesMore: max(0, len(c.Modules)-limit),
			Links: []linkJSON{}, LinksMore: max(0, len(links)-limit)}
		for _, l := range links[:min(limit, len(links))] {
			cj.Links = append(cj.Links, linkJSON{A: l.A, B: l.B, CallsAB: l.CallsAB, CallsBA: l.CallsBA, AB: site(l.AB), BA: site(l.BA)})
		}
		for _, h := range c.Loop {
			cj.Loop = append(cj.Loop, hopJSON{From: h.From, To: h.To, Call: site(h.Site)})
		}
		out = append(out, cj)
	}
	return out
}

// located names a function as "file:line name".
func located(u unit.Unit) string {
	return fmt.Sprintf("%s:%d %s", u.File, u.StartLine, u.Name)
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
	cycled := 0
	for _, c := range codemap.Cycles(g, in) {
		cycled += len(c.Modules)
	}
	return crateJSON{Name: name, Modules: len(slices.Compact(modules)), Layers: len(codemap.Layers(g, in).Layers), CycleModules: cycled}
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

// writeCyclesText prints the cycles section.
func writeCyclesText(b *strings.Builder, cycles []cycleJSON) {
	if len(cycles) == 0 {
		return
	}
	b.WriteString("\nCycles (modules calling each other in a loop):\n")
	for _, c := range cycles {
		more := ""
		if c.ModulesMore > 0 {
			more = fmt.Sprintf(" and %d more", c.ModulesMore)
		}
		fmt.Fprintf(b, "  %d module(s): %s%s\n", len(c.Modules)+c.ModulesMore, strings.Join(c.Modules, ", "), more)
		for _, l := range c.Links {
			fmt.Fprintf(b, "    %s <-> %s: %d call(s) / %d call(s)\n      %s\n      %s\n", l.A, l.B, l.CallsAB, l.CallsBA, l.AB, l.BA)
		}
		if c.LinksMore > 0 {
			fmt.Fprintf(b, "    and %d more pair(s)\n", c.LinksMore)
		}
		for _, h := range c.Loop {
			fmt.Fprintf(b, "    %s -> %s\n      %s\n", h.From, h.To, h.Call)
		}
	}
}

// writeMapText prints the maps of langs, the languages described.
func writeMapText(w io.Writer, r mapReport, langs []string) error {
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
				fmt.Fprintf(&b, "  %s  %d module(s), %d layer(s)", c.Name, c.Modules, c.Layers)
				if c.CycleModules > 0 {
					fmt.Fprintf(&b, ", %d in cycles", c.CycleModules)
				}
				b.WriteString("\n")
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
		writeCyclesText(&b, m.Cycles)
		writeFeaturesText(&b, m.Features)
		writeIdiomsText(&b, m.Idioms)
	}
	if len(r.Maps) == 0 {
		names := map[string]string{"go": "Go", "rust": "Rust"}
		var missing []string
		for _, l := range langs {
			missing = append(missing, names[l])
		}
		fmt.Fprintf(&b, "no %s code found\n", strings.Join(missing, " or "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeFeaturesText lists the typical features: copies of one pipeline,
// shown so the next feature extracts it rather than copies it again.
func writeFeaturesText(b *strings.Builder, features []featureJSON) {
	if len(features) == 0 {
		return
	}
	b.WriteString("\nTypical features copying one pipeline (parallel-flow in check): extract the pipeline, do not copy it again:\n")
	for _, f := range features {
		others := len(f.Members) + f.MembersMore
		fmt.Fprintf(b, "  %s and %d other(s), pattern %s\n", f.Shape, others, f.Pattern)
		for _, s := range f.Steps {
			fmt.Fprintf(b, "      step %s", s.Function)
			if len(s.Helpers) > 0 {
				fmt.Fprintf(b, ", calls %s", strings.Join(s.Helpers, ", "))
			}
			b.WriteString("\n")
		}
		if f.StepsMore > 0 {
			fmt.Fprintf(b, "      and %d more step(s)\n", f.StepsMore)
		}
		if len(f.Members) > 0 {
			fmt.Fprintf(b, "      others: %s", strings.Join(f.Members, "; "))
			if f.MembersMore > 0 {
				fmt.Fprintf(b, " and %d more", f.MembersMore)
			}
			b.WriteString("\n")
		}
	}
}

// writeIdiomsText lists how the code does each concern: follow the
// convention, or the idiomatic form where there is none.
func writeIdiomsText(b *strings.Builder, idioms []idiomJSON) {
	if len(idioms) == 0 {
		return
	}
	b.WriteString("\nIdioms (use the convention; where there is none, the idiomatic form):\n")
	for _, id := range idioms {
		if id.Convention != "" {
			fmt.Fprintf(b, "  %s: %s in %d of %d\n", id.Concern, id.Convention, id.Forms[0].Count, id.Occurrences)
		} else {
			fmt.Fprintf(b, "  %s: no convention in %d occurrence(s)", id.Concern, id.Occurrences)
			if id.Idiomatic != "" {
				fmt.Fprintf(b, "; idiomatic: %s", id.Idiomatic)
			}
			b.WriteString("\n")
		}
		for _, f := range id.Forms {
			fmt.Fprintf(b, "      %s %d, e.g. %s\n", f.Form, f.Count, f.Example)
		}
		if id.FormsMore > 0 {
			fmt.Fprintf(b, "      and %d more form(s)\n", id.FormsMore)
		}
	}
}
