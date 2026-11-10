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

	"github.com/heikki-laitala/ddslop/internal/baseline"
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
  baseline  record known duplication, so check --baseline reports only new
  flows     list feature entry points and the functions they reach
  index     build or refresh the index at <path>/.ddslop/index.db
  init      set ddslop up for coding agents (skill, instructions, .gitignore)
  stats     describe the index without updating it
  status    show what ddslop has installed in a project
  uninstall remove ddslop's skills, instructions and .gitignore entry
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

With --diff <ref>, the whole repository is still analysed, but only
candidates touching lines changed since ref are reported, and the changed
occurrences are marked: use it to check what a change introduced, for
example "--diff HEAD" for uncommitted work or "--diff origin/main" in a
pull request. Exits 0 when nothing is found, 1 when candidates are found,
2 on error.

Flags:
`

// command is one ddslop subcommand.
type command func(args []string, stdout, stderr io.Writer) int

// commands maps subcommand names to their implementations.
var commands = map[string]command{
	"check":     runCheck,
	"baseline":  runBaseline,
	"flows":     runFlows,
	"index":     runIndex,
	"init":      runInit,
	"stats":     runStats,
	"status":    runStatus,
	"uninstall": runUninstall,
	"version":   runVersion,
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

// versionString is the version stamped at build time, the module version
// for go install, or "dev".
func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func runVersion(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintf(stdout, "ddslop %s\n", versionString())
	return exitClean
}

const indexUsage = `usage: ddslop index [path]

Builds or refreshes the index of extracted functions at
<path>/.ddslop/index.db, so the next check is fast. Only new and changed
files are parsed.

Flags:
`

func runIndex(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("index", indexUsage, stderr)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}
	db, stats, err := syncIndex(root)
	if err != nil {
		return fail(stderr, err)
	}
	if err := db.Close(); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "indexed %d files: %d parsed, %d unchanged, %d deleted\n",
		stats.Parsed+stats.Unchanged, stats.Parsed, stats.Unchanged, stats.Deleted)
	return exitClean
}

// analysisFlags registers the flags that decide what counts as duplication,
// shared by check and baseline so both analyse the same way. The returned
// function reads them after parsing.
func analysisFlags(fs *flag.FlagSet) func() (options, error) {
	minNodes := fs.Int("min-nodes", 50, "ignore functions with fewer syntax nodes than this")
	minBlockNodes := fs.Int("min-block-nodes", 100, "ignore copied blocks inside functions with fewer syntax nodes than this (never below --min-nodes)")
	minNearNodes := fs.Int("min-near-nodes", 150, "ignore near-miss copies of functions with fewer syntax nodes than this (never below --min-nodes)")
	tests := fs.Bool("tests", false, "include test functions, test modules and test files")
	minSim := fs.Float64("min-similarity", 0.8, "report near-miss copies at least this similar, above 0 and at most 1")
	return func() (options, error) {
		if *minSim <= 0 || *minSim > 1 {
			return options{}, fmt.Errorf("--min-similarity %v is not above 0 and at most 1", *minSim)
		}
		return options{minNodes: *minNodes, minBlockNodes: *minBlockNodes, minNearNodes: *minNearNodes, minSim: *minSim, tests: *tests}, nil
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("check", checkUsage, stderr)
	analysis := analysisFlags(fs)
	format := fs.String("format", "", `output format, "text", "json" or "sarif" (default: text on a terminal, json otherwise)`)
	diff := fs.String("diff", "", "only report candidates touching lines changed since this Git ref (committed, staged, unstaged and untracked)")
	baselinePath := fs.String("baseline", "", "do not report duplication already recorded in this baseline file (see ddslop baseline)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	opts, err := analysis()
	if err != nil {
		return fail(stderr, err)
	}
	if *format == "" {
		*format = "json"
		if isTerminal(stdout) {
			*format = "text"
		}
	}
	if *format != "text" && *format != "json" && *format != "sarif" {
		return fail(stderr, fmt.Errorf("unknown format %q, want text, json or sarif", *format))
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}

	var known baseline.Baseline
	if *baselinePath != "" {
		if known, err = baseline.Load(*baselinePath); err != nil {
			return fail(stderr, err)
		}
	}
	var changes source.Changes
	if *diff != "" {
		if changes, err = source.ChangedLines(root, *diff); err != nil {
			return fail(stderr, err)
		}
	}
	r, err := check(root, opts)
	if err != nil {
		return fail(stderr, err)
	}
	if *baselinePath != "" {
		r = baseline.Filter(r, known)
	}
	if *diff != "" {
		r = report.KeepChanged(r, *diff, changes.Touches)
	}

	switch *format {
	case "text":
		err = report.WriteText(stdout, r)
	case "sarif":
		err = report.WriteSARIF(stdout, r, versionString())
	default:
		err = report.WriteJSON(stdout, r)
	}
	if err != nil {
		return fail(stderr, err)
	}
	if len(r.Candidates) > 0 {
		return exitFound
	}
	return exitClean
}

const baselineUsage = `usage: ddslop baseline [flags] [path]

Records the duplication found under path (default ".") in a baseline file,
<path>/.ddslop-baseline.json by default. "ddslop check --baseline FILE"
then reports only duplication that is not in it: commit the file to adopt
ddslop on an existing codebase and gate CI on new duplication only. Use the
same analysis flags as check. A known candidate whose copies shrink during
cleanup stays known; a new copy of it is reported.

Flags:
`

func runBaseline(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("baseline", baselineUsage, stderr)
	analysis := analysisFlags(fs)
	output := fs.String("output", "", "file to write (default <path>/.ddslop-baseline.json)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	opts, err := analysis()
	if err != nil {
		return fail(stderr, err)
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}
	path := *output
	if path == "" {
		path = filepath.Join(root, baselineFile)
	}
	r, err := check(root, opts)
	if err != nil {
		return fail(stderr, err)
	}
	if err := baseline.Write(path, baseline.FromReport(r)); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "wrote %d known candidate(s) to %s\n", len(r.Candidates), path)
	return exitClean
}

// baselineFile is the default baseline, relative to the analysed root.
const baselineFile = ".ddslop-baseline.json"

// options are the analysis settings of one check.
type options struct {
	minNodes, minBlockNodes, minNearNodes int
	minSim                                float64
	tests                                 bool
}

// syncIndex opens the index under root and brings it up to date with the
// Rust files there. The caller closes the database.
func syncIndex(root string) (*index.DB, index.Stats, error) {
	paths, err := source.Files(root, ".rs")
	if err != nil {
		return nil, index.Stats{}, err
	}
	version, err := extractorVersion()
	if err != nil {
		return nil, index.Stats{}, err
	}
	db, err := index.Open(filepath.Join(root, indexPath))
	if err != nil {
		return nil, index.Stats{}, err
	}
	stats, err := db.Sync(index.Options{Root: root, Paths: paths, Version: version, Parse: parseRust, Threads: threadCount()})
	if err != nil {
		_ = db.Close()
		return nil, index.Stats{}, err
	}
	return db, stats, nil
}

// threadCount is how many goroutines parse files and verify near misses.
func threadCount() int {
	return index.ResolveThreads(os.Getenv("DDSLOP_INDEX_THREADS"), runtime.NumCPU())
}

func check(root string, opts options) (report.Report, error) {
	db, stats, err := syncIndex(root)
	if err != nil {
		return report.Report{}, err
	}
	defer func() { _ = db.Close() }()
	files, err := db.Files()
	if err != nil {
		return report.Report{}, err
	}
	threads := threadCount()

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
	nearMisses := clone.NearMisses(units, max(opts.minNearNodes, opts.minNodes), opts.minSim, threads)
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
		// Generated code is not analysed, but the traits it declares (such
		// as OpenAPI server traits) mark the entry points of features.
		return index.Parsed{Generated: true, Traits: rust.Traits(src)}, nil
	}
	units, err := rust.Units(path, src)
	for i := range units {
		units[i].Signature = similarity.Signature(units[i].Seq)
	}
	return index.Parsed{Units: units, Traits: rust.Traits(src)}, err
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
