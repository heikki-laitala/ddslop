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
	"github.com/heikki-laitala/ddslop/internal/lang/golang"
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
	resolved, err := outputFormat(*format, stdout, "text", "json")
	if err != nil {
		return fail(stderr, err)
	}
	*format = resolved
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}

	db, files, _, err := openFiles(root)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = db.Close() }()
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

// moduleAbove returns the import path of the directory root, from the
// go.mod of a module root lies inside ("example.com/app/internal" for
// root internal under example.com/app), or "".
func moduleAbove(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		gomod, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // a go.mod above the analysed root
		if path := golang.ModulePath(gomod); err == nil && path != "" {
			rel, err := filepath.Rel(dir, abs)
			if err != nil {
				return ""
			}
			return path + "/" + filepath.ToSlash(rel)
		}
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
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

// graphInputs collects what call resolution needs: each file's imports,
// each crate's name from its Cargo.toml under root, the types of Go
// struct fields, the module path of each go.mod above a Go file, and the
// Rust files, whose paths name their crates' modules.
func graphInputs(root string, files []index.File) flow.Inputs {
	in := flow.Inputs{Imports: map[string]map[string]string{}, Crates: map[string]string{},
		Fields: map[string]map[string]flow.FieldType{}, Modules: map[string]string{}}
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".go") {
			for dir := filepath.Dir(f.Path); !seen[dir]; dir = filepath.Dir(dir) {
				seen[dir] = true
				gomod, err := os.ReadFile(filepath.Join(root, dir, "go.mod")) //nolint:gosec // a go.mod under the analysed root
				if path := golang.ModulePath(gomod); err == nil && path != "" {
					in.Modules[filepath.ToSlash(dir)] = path
				}
				if dir == "." || dir == string(filepath.Separator) {
					break
				}
			}
		}
	}
	if _, ok := in.Modules["."]; len(seen) > 0 && !ok {
		if path := moduleAbove(root); path != "" {
			in.Modules["."] = path
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".rs") {
			in.RustFiles = append(in.RustFiles, f.Path)
		}
		if len(f.Imports) > 0 {
			in.Imports[f.Path] = f.Imports
		}
		for _, im := range f.Decls.Impls {
			if !im.Test {
				in.Impls = append(in.Impls, im)
			}
		}
		dir := flow.CrateOf(f.Path)
		for _, ft := range f.Decls.FieldTypes {
			if ft.Test {
				continue // the graph is of the non-test code
			}
			if in.Fields[dir] == nil {
				in.Fields[dir] = map[string]flow.FieldType{}
			}
			// Structs of one name in two Rust modules of a crate that type
			// a field differently leave it unknown.
			key := ft.Struct + "." + ft.Field
			// A relative Rust type (Db, db::Db) means each module's own:
			// declared in two places, it is unknown unless spelled from
			// crate::. A Go package is one scope, its files often build
			// variants of one struct.
			known, seen := in.Fields[dir][key]
			elsewhere := strings.HasSuffix(ft.File, ".rs") && (known.File != ft.File || known.Module != ft.Module)
			relative := func(t string) bool { return t != "" && !strings.HasPrefix(t, "crate::") }
			if seen && (known.Type != ft.Type || known.Unwrapped != ft.Unwrapped || elsewhere && (relative(ft.Type) || relative(ft.Unwrapped))) {
				in.Fields[dir][key] = flow.FieldType{}
			} else if !seen {
				in.Fields[dir][key] = flow.FieldType{Type: ft.Type, Unwrapped: ft.Unwrapped, File: ft.File, Module: ft.Module}
			}
		}
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
