package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/report"
)

const explainUsage = `usage: ddslop explain [flags] ID [path]

Shows one candidate in full: its summary, its differences and each
occurrence's code, lines holding a difference that becomes a parameter
marked "*" and near-miss lines only some copies have marked "+". ID is a
candidate's id from check (c3) or its fingerprint, which stays the same
across runs. Give the same flags as the check that listed it (--diff,
--baseline, the size flags), since ids number that check's candidates.

Flags:
`

// maxShownOccurrences is how many occurrences explain shows the code of
// without --all.
const maxShownOccurrences = 3

type explanation struct {
	Candidate report.Candidate `json:"candidate"`
	Code      []occurrenceCode `json:"code"`
}

type occurrenceCode struct {
	File      string   `json:"file"`
	StartLine int      `json:"start_line"`
	Lines     []string `json:"lines"`
}

func runExplain(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("explain", explainUsage, stderr)
	analysis := analysisFlags(fs)
	format := fs.String("format", "", `output format, "text" or "json" (default: text on a terminal, json otherwise)`)
	diff := fs.String("diff", "", "number candidates as check --diff with this Git ref does")
	baselinePath := fs.String("baseline", "", "number candidates as check --baseline with this file does")
	all := fs.Bool("all", false, "show the code of every occurrence, not only the first three")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() == 0 || fs.NArg() > 2 {
		return fail(stderr, errors.New("expected a candidate id and at most one path"))
	}
	id, root := fs.Arg(0), "."
	if fs.NArg() == 2 {
		root = fs.Arg(1)
	}
	opts, err := analysis(root)
	if err != nil {
		return fail(stderr, err)
	}
	if *format, err = outputFormat(*format, stdout, "text", "json"); err != nil {
		return fail(stderr, err)
	}

	r, err := filteredCheck(root, opts, *diff, *baselinePath)
	if err != nil {
		return fail(stderr, err)
	}
	var c *report.Candidate
	for i := range r.Candidates {
		if r.Candidates[i].ID == id || r.Candidates[i].Fingerprint == id {
			c = &r.Candidates[i]
			break
		}
	}
	if c == nil {
		return fail(stderr, fmt.Errorf("no candidate %s; ids number the candidates of a check run with the same flags", id))
	}

	e := explanation{Candidate: *c, Code: []occurrenceCode{}}
	for i, o := range c.Occurrences {
		if i == maxShownOccurrences && !*all {
			break
		}
		lines, err := sourceLines(root, o.File, o.StartLine, o.EndLine)
		if err != nil {
			return fail(stderr, err)
		}
		e.Code = append(e.Code, occurrenceCode{File: o.File, StartLine: o.StartLine, Lines: lines})
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(e)
	} else {
		err = writeExplanation(stdout, e)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

// sourceLines returns lines start to end (1-based, inclusive) of a file
// under root.
func sourceLines(root, file string, start, end int) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // a file of the analysed repository
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")
	if start < 1 || end > len(lines) || start > end {
		return nil, fmt.Errorf("%s:%d-%d is outside the file; run check again", file, start, end)
	}
	return lines[start-1 : end], nil
}

func writeExplanation(w io.Writer, e explanation) error {
	c := e.Candidate
	var b strings.Builder
	b.WriteString(report.Headline(c) + "\n")
	if t := c.Target; t != nil {
		fmt.Fprintf(&b, "target: %s:%d-%d %s\n", t.File, t.StartLine, t.EndLine, t.Name)
	}
	if len(c.Values) > 0 {
		fmt.Fprintf(&b, "values: %s\n", strings.Join(c.Values, ", "))
	}
	for _, d := range c.Differences {
		if !d.Local {
			fmt.Fprintf(&b, "parameter (%s): %s\n", d.Kind, strings.Join(d.Values, " | "))
		}
	}
	for i, o := range c.Occurrences {
		b.WriteString("\n")
		if i >= len(e.Code) {
			fmt.Fprintf(&b, "%s:%d-%d %s (code not shown; --all shows it)\n", o.File, o.StartLine, o.EndLine, o.Name)
			continue
		}
		fmt.Fprintf(&b, "%s:%d-%d %s\n", o.File, o.StartLine, o.EndLine, o.Name)
		marks := map[int]byte{}
		for _, d := range c.Differences {
			if !d.Local && i < len(d.Lines) {
				for _, l := range d.Lines[i] {
					marks[l] = '*'
				}
			}
		}
		for _, r := range o.UnmatchedLines {
			for l := r[0]; l <= r[1]; l++ {
				marks[l] = '+'
			}
		}
		for j, text := range e.Code[i].Lines {
			n := o.StartLine + j
			mark := byte(' ')
			if m, ok := marks[n]; ok {
				mark = m
			}
			fmt.Fprintf(&b, "%c %4d | %s\n", mark, n, text)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
