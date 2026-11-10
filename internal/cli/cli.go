// Package cli implements the ddslop command line.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/report"
	"github.com/heikki-laitala/ddslop/internal/similarity"
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
refactoring candidates: exact copies, renamed copies, and near-miss copies
with added, removed or changed statements. Generated files are skipped, and so is test code
unless --tests is given. Extracted functions are kept in an index at
<path>/.ddslop/index.db so unchanged files are not parsed again. Files are
parsed on half the CPU cores; set DDSLOP_INDEX_THREADS to change that. Exits 0 when nothing is found, 1 when candidates
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
	tests := fs.Bool("tests", false, "include test functions, test modules and test files")
	minSim := fs.Float64("min-similarity", 0.8, "report near-miss copies at least this similar, above 0 and at most 1")
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
	if *minSim <= 0 || *minSim > 1 {
		return fail(stderr, fmt.Errorf("--min-similarity %v is not above 0 and at most 1", *minSim))
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

	r, err := check(root, *minNodes, *minSim, *tests)
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

func check(root string, minNodes int, minSim float64, tests bool) (report.Report, error) {
	paths, err := source.Files(root, ".rs")
	if err != nil {
		return report.Report{}, err
	}
	version, err := extractorVersion()
	if err != nil {
		return report.Report{}, err
	}
	db, err := index.Open(filepath.Join(root, indexPath))
	if err != nil {
		return report.Report{}, err
	}
	defer func() { _ = db.Close() }()

	threads := index.ResolveThreads(os.Getenv("DDSLOP_INDEX_THREADS"), runtime.NumCPU())
	stats, err := db.Sync(index.Options{Root: root, Paths: paths, Version: version, Parse: parseRust, Threads: threads})
	if err != nil {
		return report.Report{}, err
	}
	files, err := db.Files()
	if err != nil {
		return report.Report{}, err
	}

	summary := report.Summary{ParsedFiles: stats.Parsed, UnchangedFiles: stats.Unchanged}
	var units []unit.Unit
	for _, f := range files {
		if f.Generated {
			summary.GeneratedFiles++
			continue
		}
		summary.Files++
		for _, u := range f.Units {
			if u.Test && !tests {
				summary.TestUnits++
				continue
			}
			units = append(units, u)
		}
	}
	summary.Units = len(units)
	groups := clone.Groups(units, minNodes)
	nearMisses := clone.NearMisses(units, minNodes, minSim, threads)
	return report.Build("rust", groups, nearMisses, summary), nil
}

// indexPath is where the index lives, relative to the analysed root.
const indexPath = ".ddslop/index.db"

func parseRust(path string, src []byte) (index.Parsed, error) {
	if rust.IsGenerated(src) {
		return index.Parsed{Generated: true}, nil
	}
	units, err := rust.Units(path, src)
	for i := range units {
		units[i].Signature = similarity.Signature(units[i].Seq)
	}
	return index.Parsed{Units: units}, err
}

// extractorVersion identifies the running binary by its content, so any
// rebuild that changes how units are extracted invalidates the index.
func extractorVersion() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(exe) //nolint:gosec // the running executable's own path
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
