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
	"runtime/debug"

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

const usage = `usage: ddslop <command> [flags] [path]

ddslop finds duplicated code and proposes refactoring candidates for
coding agents. Without a command, it runs check.

Commands:
  check     report duplicated code (default)
  version   print the version
  help      show this help

Run "ddslop <command> -h" for a command's flags.
`

const checkUsage = `usage: ddslop check [flags] [path]

Finds duplicated Rust functions under path (default ".") and proposes
refactoring candidates: exact copies, renamed copies, near-miss copies
with added, removed or changed statements, and copied blocks of statements
inside larger functions. Generated files are skipped, and so is test code
unless --tests is given. Extracted functions are kept in an index at
<path>/.ddslop/index.db so unchanged files are not parsed again. Files are
parsed on half the CPU cores; set DDSLOP_INDEX_THREADS to change that.
Exits 0 when nothing is found, 1 when candidates are found, 2 on error.

Flags:
`

// command is one ddslop subcommand.
type command func(args []string, stdout, stderr io.Writer) int

// commands maps subcommand names to their implementations.
var commands = map[string]command{
	"check":   runCheck,
	"version": runVersion,
}

// Run executes ddslop with the given arguments and returns the exit code.
// The first argument selects a command; without one, check runs, so
// "ddslop ." and "ddslop --format json" keep working.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(stdout, usage)
			return exitClean
		}
		if cmd, ok := commands[args[0]]; ok {
			return cmd(args[1:], stdout, stderr)
		}
	}
	return runCheck(args, stdout, stderr)
}

// newFlags returns a flag set whose -h prints the given usage.
func newFlags(name, help string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("ddslop "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, help)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses args and reports the exit code to return when parsing
// ends the command: 0 for -h, 2 for bad flags.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitClean, false
	case err != nil:
		return exitError, false
	}
	return 0, true
}

// rootArg returns the single optional path argument, "." by default.
func rootArg(fs *flag.FlagSet) (string, error) {
	switch fs.NArg() {
	case 0:
		return ".", nil
	case 1:
		return fs.Arg(0), nil
	}
	return "", errors.New("expected at most one path")
}

// version is set at build time with -ldflags "-X ...cli.version=...".
var version = ""

func runVersion(_ []string, stdout, _ io.Writer) int {
	v := version
	if v == "" {
		v = "dev"
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
	}
	fmt.Fprintf(stdout, "ddslop %s\n", v)
	return exitClean
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("check", checkUsage, stderr)
	minNodes := fs.Int("min-nodes", 50, "ignore functions with fewer syntax nodes than this")
	minBlockNodes := fs.Int("min-block-nodes", 100, "ignore copied blocks inside functions with fewer syntax nodes than this (never below --min-nodes)")
	tests := fs.Bool("tests", false, "include test functions, test modules and test files")
	minSim := fs.Float64("min-similarity", 0.8, "report near-miss copies at least this similar, above 0 and at most 1")
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
	if *minSim <= 0 || *minSim > 1 {
		return fail(stderr, fmt.Errorf("--min-similarity %v is not above 0 and at most 1", *minSim))
	}
	if *format != "text" && *format != "json" {
		return fail(stderr, fmt.Errorf("unknown format %q, want text or json", *format))
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}

	r, err := check(root, options{minNodes: *minNodes, minBlockNodes: *minBlockNodes, minSim: *minSim, tests: *tests})
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

// options are the analysis settings of one check.
type options struct {
	minNodes, minBlockNodes int
	minSim                  float64
	tests                   bool
}

func check(root string, opts options) (report.Report, error) {
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
			if u.Test && !opts.tests {
				summary.TestUnits++
				continue
			}
			units = append(units, u)
		}
	}
	summary.Units = len(units)
	groups := clone.Groups(units, opts.minNodes)
	nearMisses := clone.NearMisses(units, opts.minNodes, opts.minSim, threads)
	// Functions reported together. A near-miss member represents its whole
	// shape, so a family also covers the member's exact and renamed copies.
	var reported [][]unit.Unit
	copiesOf := map[uint64][]unit.Unit{}
	for _, g := range groups {
		reported = append(reported, g.Units)
		copiesOf[g.Units[0].Shape] = g.Units
	}
	for _, nm := range nearMisses {
		var set []unit.Unit
		for _, u := range nm.Units {
			if copies, ok := copiesOf[u.Shape]; ok {
				set = append(set, copies...)
			} else {
				set = append(set, u)
			}
		}
		reported = append(reported, set)
	}
	blocks := clone.BlockClones(units, max(opts.minBlockNodes, opts.minNodes), reported)
	return report.Build("rust", groups, nearMisses, blocks, summary), nil
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
