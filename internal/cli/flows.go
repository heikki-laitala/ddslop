package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
)

const flowsUsage = `usage: ddslop flows [flags] [path]

Lists the features of the code under path (default "."): every entry point
and the functions it reaches. Entry points are methods implementing a trait
declared in generated code, such as OpenAPI server traits. Calls are
resolved by name, without type information: ambiguous calls (a method name
defined by several types) and calls inside macros are not followed, and
test code is left out. The summary says how many calls were resolved.

Flags:
`

// flowsReport is the JSON output of flows.
type flowsReport struct {
	Flows   []flowJSON   `json:"flows"`
	Summary flowsSummary `json:"summary"`
}

type flowJSON struct {
	Entry stepJSON   `json:"entry"`
	Steps []stepJSON `json:"steps"`
}

type stepJSON struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Depth     int    `json:"depth"`
	// Caller is the name of the function that called this one; empty for
	// the entry point.
	Caller string `json:"caller"`
}

type flowsSummary struct {
	Entries       int `json:"entries"`
	Functions     int `json:"functions"`
	Calls         int `json:"calls"`
	ResolvedCalls int `json:"resolved_calls"`
}

func runFlows(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("flows", flowsUsage, stderr)
	depth := fs.Int("depth", 6, "follow calls at most this many levels from an entry point")
	format := fs.String("format", "", `output format, "text" or "json" (default: text on a terminal, json otherwise)`)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if *format == "" {
		*format = "json"
		if isTerminal(stdout) {
			*format = "text"
		}
	}
	if *format != "text" && *format != "json" {
		return fail(stderr, fmt.Errorf("unknown format %q, want text or json", *format))
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}

	db, _, err := syncIndex(root)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = db.Close() }()
	files, err := loadFiles(db)
	if err != nil {
		return fail(stderr, err)
	}
	g, traits := callGraph(root, files)
	r := flowsReport{Flows: []flowJSON{}, Summary: flowsSummary{Functions: len(g.Units), Calls: g.Calls, ResolvedCalls: g.Resolved}}
	for _, entry := range g.Entries(traits) {
		var f flowJSON
		for _, s := range treeOrder(g.Slice(entry, *depth)) {
			u := g.Units[s.Unit]
			step := stepJSON{File: u.File, Name: u.Name, StartLine: u.StartLine, EndLine: u.EndLine, Depth: s.Depth}
			if s.Caller >= 0 {
				step.Caller = g.Units[s.Caller].Name
			}
			f.Steps = append(f.Steps, step)
		}
		f.Entry = f.Steps[0]
		r.Flows = append(r.Flows, f)
	}
	r.Summary.Entries = len(r.Flows)

	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	} else {
		err = writeFlowsText(stdout, r)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

func writeFlowsText(w io.Writer, r flowsReport) error {
	var b strings.Builder
	for _, f := range r.Flows {
		for _, s := range f.Steps {
			fmt.Fprintf(&b, "%s%s (%s:%d-%d)\n", strings.Repeat("  ", s.Depth), s.Name, s.File, s.StartLine, s.EndLine)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d entry point(s); %d of %d calls resolved by name\n",
		r.Summary.Entries, r.Summary.ResolvedCalls, r.Summary.Calls)
	_, err := io.WriteString(w, b.String())
	return err
}

// manifest returns the Cargo.toml of a crate directory under root, or nil
// when there is none.
func manifest(root, dir string) []byte {
	toml, err := os.ReadFile(filepath.Join(root, dir, "Cargo.toml")) //nolint:gosec // a manifest under the analysed root
	if err != nil {
		return nil
	}
	return toml
}

// treeOrder orders a breadth-first slice depth first, each function right
// after the function that called it, so indentation by depth reads as a
// call tree. Callees keep their call order.
func treeOrder(steps []flow.Step) []flow.Step {
	children := map[int][]flow.Step{}
	for _, s := range steps[1:] {
		children[s.Caller] = append(children[s.Caller], s)
	}
	out := make([]flow.Step, 0, len(steps))
	var visit func(s flow.Step)
	visit = func(s flow.Step) {
		out = append(out, s)
		for _, c := range children[s.Unit] {
			visit(c)
		}
	}
	visit(steps[0])
	return out
}

// graphInputs collects what call resolution needs: each file's imports and
// each crate's name from its Cargo.toml under root.
func graphInputs(root string, files []index.File) flow.Inputs {
	in := flow.Inputs{Imports: map[string]map[string]string{}, Crates: map[string]string{}}
	for _, f := range files {
		if len(f.Imports) > 0 {
			in.Imports[f.Path] = f.Imports
		}
		dir := flow.CrateOf(f.Path)
		if _, done := in.Crates[dir]; done {
			continue
		}
		if name := rust.CrateName(manifest(root, dir)); name != "" {
			in.Crates[dir] = name
		} else {
			in.Crates[dir] = strings.ReplaceAll(filepath.Base(dir), "-", "_")
		}
	}
	return in
}
