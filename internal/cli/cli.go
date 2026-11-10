// Package cli implements the ddslop command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/report"
	"github.com/heikki-laitala/ddslop/internal/source"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Exit codes.
const (
	exitClean = 0
	exitFound = 1
	exitError = 2
)

const usage = `usage: ddslop [check] [flags] [path]

Finds duplicated Rust functions under path (default ".") and proposes
refactoring candidates. Generated files are skipped. Exits 0 when nothing is found, 1 when candidates
are found, 2 on error.

Flags:
`

// Run executes ddslop with the given arguments and returns the exit code:
// 0 when nothing was found, 1 when candidates were found, 2 on error.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "check" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("ddslop check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	minNodes := fs.Int("min-nodes", 50, "ignore functions with fewer syntax nodes than this")
	format := fs.String("format", "", `output format, "text" or "json" (default: text on a terminal, json otherwise)`)
	if err := fs.Parse(args); err != nil {
		return exitError
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

	root := "."
	switch fs.NArg() {
	case 0:
	case 1:
		root = fs.Arg(0)
	default:
		return fail(stderr, errors.New("expected at most one path"))
	}

	r, err := check(root, *minNodes)
	if err != nil {
		return fail(stderr, err)
	}

	write := report.WriteJSON
	if *format == "text" {
		write = report.WriteText
	}
	if err := write(stdout, r); err != nil {
		return fail(stderr, err)
	}
	if len(r.Candidates) > 0 {
		return exitFound
	}
	return exitClean
}

func check(root string, minNodes int) (report.Report, error) {
	files, err := source.Files(root, ".rs")
	if err != nil {
		return report.Report{}, err
	}
	var units []unit.Unit
	var summary report.Summary
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // reading files under the analysed root is the point
		if err != nil {
			return report.Report{}, err
		}
		if rust.IsGenerated(src) {
			summary.GeneratedFiles++
			continue
		}
		summary.Files++
		fileUnits, err := rust.Units(f, src)
		if err != nil {
			return report.Report{}, fmt.Errorf("%s: %w", f, err)
		}
		units = append(units, fileUnits...)
	}
	summary.Units = len(units)
	return report.Build("rust", clone.Groups(units, minNodes), summary), nil
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "ddslop: %v\n", err)
	return exitError
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
