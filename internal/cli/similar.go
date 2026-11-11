package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

const similarUsage = `usage: ddslop similar [flags] FILE:LINE [path]

Finds existing functions like the one at FILE:LINE, most similar first, in
the repository at path (default "."). Use it before keeping a new helper:
write the draft, ask what already exists, and reuse it instead. Similarity
compares structure with names and values blanked out, so a renamed copy
scores 1. Test code is left out unless --tests is given.

Flags:
`

type similarOutput struct {
	Target  functionLocation `json:"target"`
	Similar []similarMatch   `json:"similar"`
}

type functionLocation struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type similarMatch struct {
	functionLocation
	Similarity float64 `json:"similarity"`
}

func runSimilar(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("similar", similarUsage, stderr)
	format := fs.String("format", "", `output format, "text" or "json" (default: text on a terminal, json otherwise)`)
	minSim := fs.Float64("min-similarity", 0.7, "report functions at least this similar, above 0 and at most 1")
	limit := fs.Int("limit", 10, "report at most this many functions")
	tests := fs.Bool("tests", false, "include test functions, test modules and test files")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if *minSim <= 0 || *minSim > 1 {
		return fail(stderr, fmt.Errorf("--min-similarity %v is not above 0 and at most 1", *minSim))
	}
	resolved, err := outputFormat(*format, stdout, "text", "json")
	if err != nil {
		return fail(stderr, err)
	}
	*format = resolved
	if fs.NArg() == 0 || fs.NArg() > 2 {
		return fail(stderr, errors.New("expected FILE:LINE and at most one path"))
	}
	root := "."
	if fs.NArg() == 2 {
		root = fs.Arg(1)
	}
	file, line, err := location(fs.Arg(0), root)
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
	var units []unit.Unit
	var target *unit.Unit
	for _, f := range files {
		for _, u := range f.Units {
			if u.File == file && u.StartLine <= line && line <= u.EndLine &&
				(target == nil || u.StartLine >= target.StartLine) { // the innermost
				target = &u
			}
			if !u.Test || *tests {
				units = append(units, u)
			}
		}
	}
	if target == nil {
		return fail(stderr, fmt.Errorf("no function at %s:%d", file, line))
	}

	r := similarOutput{Target: locate(*target), Similar: []similarMatch{}}
	for i, s := range clone.SimilarTo(*target, units, *minSim) {
		if i == *limit {
			break
		}
		r.Similar = append(r.Similar, similarMatch{locate(s.Unit), math.Round(s.Similarity*1000) / 1000})
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(r)
	} else {
		err = writeSimilarText(stdout, r)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

// location parses FILE:LINE, returning the file relative to root.
func location(arg, root string) (string, int, error) {
	i := strings.LastIndex(arg, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("location %q is not FILE:LINE", arg)
	}
	line, err := strconv.Atoi(arg[i+1:])
	if err != nil || line < 1 {
		return "", 0, fmt.Errorf("location %q has no line number", arg)
	}
	absFile, err := filepath.Abs(arg[:i])
	if err != nil {
		return "", 0, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", 0, err
	}
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", 0, fmt.Errorf("%s is not inside %s", arg[:i], root)
	}
	return rel, line, nil
}

func locate(u unit.Unit) functionLocation {
	return functionLocation{File: u.File, Name: u.Name, StartLine: u.StartLine, EndLine: u.EndLine}
}

func writeSimilarText(w io.Writer, r similarOutput) error {
	var b strings.Builder
	t := r.Target
	fmt.Fprintf(&b, "%s:%d-%d %s\n", t.File, t.StartLine, t.EndLine, t.Name)
	for _, s := range r.Similar {
		fmt.Fprintf(&b, "  %.2f  %s:%d-%d %s\n", s.Similarity, s.File, s.StartLine, s.EndLine, s.Name)
	}
	if len(r.Similar) == 0 {
		b.WriteString("  nothing similar\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
