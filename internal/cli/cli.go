// Package cli implements the ddslop command line.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/heikki-laitala/ddslop/internal/baseline"
	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/config"
	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/group"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/golang"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
	"github.com/heikki-laitala/ddslop/internal/lang/tsfile"
	"github.com/heikki-laitala/ddslop/internal/lang/typescript"
	"github.com/heikki-laitala/ddslop/internal/pattern"
	"github.com/heikki-laitala/ddslop/internal/report"
	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/simplify"
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
  map       describe the layers and shared helpers, before writing code
  similar   find existing functions like the one at FILE:LINE
  explain   show one candidate in full, with each occurrence's code
  index     build or refresh the index at <path>/.ddslop/index.db
  init      set ddslop up for coding agents (skill, instructions, .gitignore)
  stats     describe the index without updating it
  status    show what ddslop has installed in a project
  uninstall remove ddslop's skills, instructions and .gitignore entry
  upgrade   replace this ddslop with the latest release
  version   print the version
  help      show this help

Run "ddslop <command> -h" for a command's flags.
`

const checkUsage = `usage: ddslop check [flags] [path]

Finds duplicated Rust, Go and TypeScript code under path (default ".")
and proposes refactoring candidates: exact copies, renamed copies,
near-miss copies with added, removed or changed statements, copied
blocks of statements inside larger functions, features running the same
pipeline, structs with the same fields, constants declared twice or
written inline, unused public functions of unpublished crates, wrappers
that only forward their parameters, and traits with a single
implementation. Generated files are skipped, and so is test code unless
--tests is given. Extracted functions are kept in an index at
<path>/.ddslop/index.db so unchanged files are not parsed again. Files
are parsed on half the CPU cores; set DDSLOP_INDEX_THREADS to change
that.

With --diff <ref>, the whole repository is still analysed, but only
candidates touching lines changed since ref are reported, and the changed
occurrences are marked: use it to check what a change introduced, for
example "--diff HEAD" for uncommitted work or "--diff origin/main" in a
pull request. Exits 0 when nothing is found, 1 when candidates are found,
2 on error. With --baseline-ref <ref>, candidates the code at ref already
has are not reported, also where the change moved them to another file:
"--baseline-ref origin/main" in a pull request fails only on what it adds,
with no baseline file to keep. With --fail-over N, it exits 1 only when the repository's
duplication percentage is above N, still reporting every candidate: a
softer gate for an existing codebase.

Flags:
`

// command is one ddslop subcommand.
type command func(args []string, stdout, stderr io.Writer) int

// commands maps subcommand names to their implementations.
var commands = map[string]command{
	"check":     runCheck,
	"baseline":  runBaseline,
	"flows":     runFlows,
	"map":       runMap,
	"similar":   runSimilar,
	"explain":   runExplain,
	"upgrade":   runUpgrade,
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
		case "--version", "-version":
			return runVersion(nil, stdout, stderr)
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
// ends the command: 0 for -h, 2 for bad flags. Flags may come before or
// after the path ("check ~/x --format json"), and "--" ends them: the
// standard flag package stops at the first non-flag, so parsing resumes
// after each one. The non-flag arguments are left in fs.Args().
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	var positional []string
	for {
		err := fs.Parse(args)
		switch {
		case errors.Is(err, flag.ErrHelp):
			return exitClean, false
		case err != nil:
			return exitError, false
		}
		rest := fs.Args()
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			positional = append(positional, rest...) // everything after -- is an argument
			break
		}
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	_ = fs.Parse(append([]string{"--"}, positional...)) // only sets fs.Args()
	return 0, true
}

// outputFormat resolves a --format flag: empty means text on a terminal
// and JSON otherwise; anything else must be one of allowed.
func outputFormat(format string, stdout io.Writer, allowed ...string) (string, error) {
	if format == "" {
		format = "json"
		if isTerminal(stdout) {
			format = "text"
		}
	}
	if !slices.Contains(allowed, format) {
		return "", fmt.Errorf("unknown format %q, want %s", format, strings.Join(allowed, ", "))
	}
	return format, nil
}

// writeOutput writes a command's result v to stdout, as indented JSON when
// format is "json" and with text otherwise, and returns the command's
// exit code.
func writeOutput[T any](stdout, stderr io.Writer, format string, v T, text func(io.Writer, T) error) int {
	var err error
	if format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(v)
	} else {
		err = text(stdout, v)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

// parseRoot parses a command's flags and its optional path argument (see
// rootArg), reporting the exit code to return when the command ends here.
func parseRoot(fs *flag.FlagSet, args []string, stderr io.Writer) (string, int, bool) {
	if code, ok := parseFlags(fs, args); !ok {
		return "", code, false
	}
	root, err := rootArg(fs)
	if err != nil {
		return "", fail(stderr, err), false
	}
	return root, 0, true
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
	root, code, ok := parseRoot(fs, args, stderr)
	if !ok {
		return code
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
// analysisFlags defines the flags that tune the analysis. The returned
// function reads them for the repository at root: a setting in its
// .ddslop.yaml applies unless the flag was given.
func analysisFlags(fs *flag.FlagSet) func(root string) (options, error) {
	minNodes := fs.Int("min-nodes", defaultMinNodes, "ignore functions with fewer syntax nodes than this")
	minBlockNodes := fs.Int("min-block-nodes", 100, "ignore copied blocks inside functions with fewer syntax nodes than this (never below --min-nodes)")
	minNearNodes := fs.Int("min-near-nodes", 150, "ignore near-miss copies of functions with fewer syntax nodes than this (never below --min-nodes)")
	tests := fs.Bool("tests", false, "include test functions, test modules and test files")
	minSim := fs.Float64("min-similarity", 0.8, "report near-miss copies at least this similar, above 0 and at most 1")
	return func(root string) (options, error) {
		cfg, err := config.Load(root)
		if err != nil {
			return options{}, err
		}
		given := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
		from := "--min-similarity %v"
		if !given["min-similarity"] && cfg.MinSimilarity != nil {
			*minSim, from = *cfg.MinSimilarity, "min-similarity %v in "+config.File
		}
		if *minSim <= 0 || *minSim > 1 {
			return options{}, fmt.Errorf(from+" is not above 0 and at most 1", *minSim)
		}
		configured(given, "min-nodes", cfg.MinNodes, minNodes)
		configured(given, "min-block-nodes", cfg.MinBlockNodes, minBlockNodes)
		configured(given, "min-near-nodes", cfg.MinNearNodes, minNearNodes)
		configured(given, "tests", cfg.Tests, tests)
		return options{minNodes: *minNodes, minBlockNodes: *minBlockNodes, minNearNodes: *minNearNodes, minSim: *minSim, tests: *tests}, nil
	}
}

// configured sets a flag's value from the configuration file, when the
// file sets it and the flag was not given.
func configured[T any](given map[string]bool, name string, setting, flagValue *T) {
	if setting != nil && !given[name] {
		*flagValue = *setting
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("check", checkUsage, stderr)
	analysis := analysisFlags(fs)
	format := fs.String("format", "", `output format, "text", "json" or "sarif" (default: text on a terminal, json otherwise)`)
	diff := fs.String("diff", "", "only report candidates touching lines changed since this Git ref (committed, staged, unstaged and untracked)")
	baselinePath := fs.String("baseline", "", "do not report duplication already recorded in this baseline file (see ddslop baseline)")
	baselineRef := fs.String("baseline-ref", "", "do not report duplication the code at this Git ref already has, also where it moved to another file: a gate on what a change adds, with no baseline file")
	failOver := fs.Float64("fail-over", 0, "exit 1 only when the repository's duplication percentage is above this, instead of on any candidate (candidates are still reported)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	limited := false
	fs.Visit(func(f *flag.Flag) { limited = limited || f.Name == "fail-over" })
	if limited && (*failOver < 0 || *failOver > 100) {
		return fail(stderr, fmt.Errorf("--fail-over %v is not a percentage from 0 to 100", *failOver))
	}

	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}
	opts, err := analysis(root)
	if err != nil {
		return fail(stderr, err)
	}
	if *format, err = outputFormat(*format, stdout, "text", "json", "sarif"); err != nil {
		return fail(stderr, err)
	}

	r, err := filteredCheck(root, opts, filters{diff: *diff, baseline: *baselinePath, baselineRef: *baselineRef})
	if err != nil {
		return fail(stderr, err)
	}
	if limited {
		r.Summary.Health.Limit(*failOver)
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
	if limited {
		if r.Summary.Health.OverLimit {
			return exitFound
		}
		return exitClean
	}
	if len(r.Candidates) > 0 {
		return exitFound
	}
	return exitClean
}

// filters are how check narrows its candidates: to those touching lines
// changed since diff, and to those neither the baseline file nor the code
// at baselineRef already has.
type filters struct {
	diff, baseline, baselineRef string
}

// filteredCheck analyses root and narrows the candidates as check reports
// them (see filters). Candidate IDs refer to this list, so commands that
// take an ID filter the same way.
func filteredCheck(root string, opts options, f filters) (report.Report, error) {
	if f.baseline != "" && f.baselineRef != "" {
		return report.Report{}, fmt.Errorf("--baseline and --baseline-ref both name what is known: give one")
	}
	var known baseline.Baseline
	var err error
	if f.baseline != "" {
		if known, err = baseline.Load(f.baseline); err != nil {
			return report.Report{}, err
		}
	}
	if f.baselineRef != "" {
		if known, err = baselineAt(root, opts, f.baselineRef); err != nil {
			return report.Report{}, err
		}
	}
	var changes source.Changes
	if f.diff != "" {
		if changes, err = source.ChangedLines(root, f.diff); err != nil {
			return report.Report{}, err
		}
	}
	r, _, err := check(root, opts, known.Conventions)
	if err != nil {
		return report.Report{}, err
	}
	if f.baseline != "" || f.baselineRef != "" {
		current, err := functionsAt(root)
		if err != nil {
			return report.Report{}, err
		}
		r = baseline.FilterAgainst(r, known, current)
	}
	if f.diff != "" {
		r = report.KeepChanged(r, f.diff, changes.Touches)
	}
	return r, nil
}

// sameConfig gives the checkout at dir root's configuration file, or none
// when root has none, so both trees are analysed with the same exclusions.
func sameConfig(root, dir string) error {
	data, err := os.ReadFile(filepath.Join(root, config.File)) //nolint:gosec // the analysed repository's own file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Removed first, never written through: at ref it may be a symlink
	// (RemoveAll removes the link, not its target) or a directory.
	target := filepath.Join(dir, config.File)
	if rmErr := os.RemoveAll(target); rmErr != nil {
		return rmErr
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the temporary checkout
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// plainDirectory reports whether dir is a directory reached from tree
// through no symlink: each path component, from dir up to tree, is one.
func plainDirectory(tree, dir string) bool {
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			return false
		}
		if p == tree || filepath.Dir(p) == p {
			return true
		}
	}
}

// baselineAt is the baseline of the code at ref: root's counterpart in a
// temporary checkout of it, analysed with the same options and root's
// configuration file.
func baselineAt(root string, opts options, ref string) (baseline.Baseline, error) {
	tree, dir, cleanup, err := source.CheckoutAt(root, ref)
	if err != nil {
		return baseline.Baseline{}, err
	}
	defer cleanup()
	if !plainDirectory(tree, dir) {
		// A path the change adds, or one that was a file or a symlink at
		// ref: nothing in it is known, and nothing it points at is read.
		return baseline.Baseline{Version: baseline.Version, Candidates: []baseline.Entry{}}, nil
	}
	if err := sameConfig(root, dir); err != nil {
		return baseline.Baseline{}, err
	}
	r, conventions, err := check(dir, opts, nil)
	if err != nil {
		return baseline.Baseline{}, err
	}
	b := baseline.FromReport(r)
	b.Conventions = conventions
	if b.Functions, err = functionsAt(dir); err != nil {
		return baseline.Baseline{}, err
	}
	return b, nil
}

// functionsAt lists the functions of the code at dir, as "file#function",
// from the index check left there.
func functionsAt(dir string) (map[string]bool, error) {
	files, err := indexedFiles(dir)
	if err != nil {
		return nil, err
	}
	functions := map[string]bool{}
	for _, f := range files {
		for _, u := range f.Units {
			functions[u.File+"#"+u.Name] = true
		}
	}
	return functions, nil
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
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
	}
	opts, err := analysis(root)
	if err != nil {
		return fail(stderr, err)
	}
	path := *output
	if path == "" {
		path = filepath.Join(root, baselineFile)
	}
	r, _, err := check(root, opts, nil)
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

// includedFiles lists the files under root with one of the extensions
// that .ddslop.yaml does not exclude (see source.Files).
func includedFiles(root string, exts ...string) ([]string, error) {
	paths, err := source.Files(root, exts...)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(paths, func(p string) bool { return cfg.Excluded(filepath.ToSlash(p)) }), nil
}

// syncIndex opens the index under root and brings it up to date with the
// Rust, Go and TypeScript files there that .ddslop.yaml does not exclude,
// TypeScript declaration files aside. The caller closes the database.
func syncIndex(root string) (*index.DB, index.Stats, error) {
	paths, err := includedFiles(root, append([]string{".rs", ".go"}, tsfile.Extensions...)...)
	if err != nil {
		return nil, index.Stats{}, err
	}
	paths = slices.DeleteFunc(paths, tsfile.IsDeclarationFile)
	version, err := extractorVersion()
	if err != nil {
		return nil, index.Stats{}, err
	}
	db, err := index.Open(filepath.Join(root, indexPath))
	if err != nil {
		return nil, index.Stats{}, err
	}
	stats, err := db.Sync(index.Options{Root: root, Paths: paths, Version: version, Parse: parseFile, Threads: threadCount()})
	if err != nil {
		_ = db.Close()
		return nil, index.Stats{}, err
	}
	return db, stats, nil
}

// openFiles brings the index under root up to date and loads its files
// (see loadFiles). The caller closes the database.
func openFiles(root string) (*index.DB, []index.File, index.Stats, error) {
	db, stats, err := syncIndex(root)
	if err != nil {
		return nil, nil, stats, err
	}
	files, err := loadFiles(root, db)
	if err != nil {
		_ = db.Close()
		return nil, nil, stats, err
	}
	return db, files, stats, nil
}

// indexedFiles brings the index under root up to date and loads its
// files (see openFiles), for a command that needs nothing else from it.
func indexedFiles(root string) ([]index.File, error) {
	db, files, _, err := openFiles(root)
	if err != nil {
		return nil, err
	}
	return files, db.Close()
}

// threadCount is how many goroutines parse files and verify near misses.
func threadCount() int {
	return index.ResolveThreads(os.Getenv("DDSLOP_INDEX_THREADS"), runtime.NumCPU())
}

// check analyses the code at root, judging its convention drifts by judged
// where it names a concern (see drifts), and returns its report with the
// conventions measured on that code, by language.
func check(root string, opts options, judged map[string]map[string]codemap.Convention) (report.Report, map[string]map[string]codemap.Convention, error) {
	db, files, stats, err := openFiles(root)
	if err != nil {
		return report.Report{}, nil, err
	}
	defer func() { _ = db.Close() }()
	threads := threadCount()

	summary := report.Summary{ParsedFiles: stats.Parsed, UnchangedFiles: stats.Unchanged}
	var units []unit.Unit
	var structs []unit.Struct
	var enums []unit.Enum
	var consts []unit.Const
	var funcTypes []unit.FuncType
	traitDecls := map[string]unit.Decls{} // each language's, test code included, for traits
	var helpers []unit.Unit               // test code's helpers, fakes and fixtures, without --tests
	for _, f := range files {
		if f.Generated {
			summary.GeneratedFiles++
			continue
		}
		summary.Files++
		for _, u := range f.Units {
			switch {
			case u.Test && !opts.tests && isHelper(u):
				helpers = append(helpers, u)
			case u.Test && !opts.tests:
				summary.TestUnits++
			case u.Ignored:
				summary.Ignored++
			default:
				units = append(units, u)
			}
		}
		structs = appendAnalysed(structs, f.Decls.Structs, opts.tests, &summary, func(s unit.Struct) (bool, bool) { return s.Test, s.Ignored })
		consts = appendAnalysed(consts, f.Decls.Consts, opts.tests, &summary, func(c unit.Const) (bool, bool) { return c.Test, c.Ignored })
		enums = appendAnalysed(enums, f.Decls.Enums, opts.tests, &summary, func(e unit.Enum) (bool, bool) { return e.Test, e.Ignored })
		d := traitDecls[languageOf(f.Path)]
		d.Traits = append(d.Traits, f.Decls.Traits...)
		d.Impls = append(d.Impls, f.Decls.Impls...)
		d.TypeNames = append(d.TypeNames, f.Decls.TypeNames...)
		traitDecls[languageOf(f.Path)] = d
		funcTypes = append(funcTypes, f.Decls.FuncTypes...)
	}
	summary.Units = len(units) + len(helpers)
	summary.Version = versionString()
	// The helpers compared for copies are analysed code too.
	summary.Health.AnalysedLines = report.AnalysedLines(units, structs, consts) + report.AnalysedLines(helpers, nil, nil)
	groups := clone.Groups(units, opts.minNodes)
	// Without --tests, test code is checked for one thing: helpers copied
	// between test files, a bigger bar than production code's. Test cases
	// are repetitive by design and stay out. The helper groups join the
	// report only: production's near-miss and block analysis never sees
	// them.
	var helperGroups []clone.Group
	for _, g := range clone.Groups(helpers, max(minTestHelperNodes, opts.minNodes)) {
		if inFiles(g.Units) > 1 { // copies within one test file are its own business
			g.TestHelpers = true
			helperGroups = append(helperGroups, g)
		}
	}
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
	blocks := clone.BlockClones(units, clone.BlockSize{
		MinNodes:           max(opts.minBlockNodes, opts.minNodes),
		FrequentNodes:      frequentBlockNodes,
		FrequentLines:      frequentBlockLines,
		FrequentDuplicated: frequentBlockDuplicated,
	}, reported)
	g, traits := callGraph(root, files)
	crates := packages(root, files)
	internal := internalCrates(root, files, crates)
	flows := parallelFlows(g, traits, opts.minNodes)
	found, conventions := drifts(units, crates, judged)
	importedWhole := wholeImported(root, files)
	words, err := wordCounts(root, files)
	if err != nil {
		return report.Report{}, nil, err
	}
	r := report.Build(languageOf, report.Findings{
		Groups: append(groups, helperGroups...), NearMisses: nearMisses, Blocks: blocks, Flows: flows,
		Structs: byLanguage(structs, func(_ string, s []unit.Struct) []clone.StructGroup { return clone.Structs(s, minStructFields) }),
		Enums:   byLanguage(enums, func(_ string, e []unit.Enum) []clone.EnumGroup { return clone.Enums(e) }),
		Consts:  byLanguage(consts, func(_ string, c []unit.Const) []clone.ConstGroup { return clone.Constants(c) }),
		Inlined: byLanguage(consts, func(lang string, c []unit.Const) []clone.InlinedConst {
			return clone.InlinedConstants(c, inLanguage(lang, units), flow.CrateOf)
		}),
		Dead: simplify.Dead(units, words, func(file string) bool {
			return internal(file) && !importedWhole[file]
		}),
		TooPublic: tooPublic(units, consts, words, libraryWords(root, files, crates, internal)),
		Wrappers:  simplify.PassThrough(g),
		Traits:    singleImpls(traitDecls, files, internal),
		Scopes:    byLanguage(units, func(_ string, u []unit.Unit) []pattern.ScopeGroup { return pattern.Scopes(u) }),
		Kinds:     byLanguage(units, func(_ string, u []unit.Unit) []pattern.StringKind { return pattern.StringKinds(u) }),
		Params: byLanguage(units, func(_ string, u []unit.Unit) []pattern.ParamGroup {
			return pattern.ParameterGroups(u, funcTypes)
		}),
		Drifts: found,
		Cycles: dependencyCycles(cycleGraph(g, root, files, units, opts.tests), files, crates, tsModules(root, files), opts.tests),
	}, summary)
	return r, conventions, recordHealth(db, root, r.Summary.Health)
}

// languages are the languages check analyses, each on its own.
var languages = []string{"go", "rust", "typescript"}

// languageNames are the languages' names in text output.
var languageNames = map[string]string{"go": "Go", "rust": "Rust", "typescript": "TypeScript"}

// byLanguage runs detect on each language's items on its own, so no
// candidate puts together declarations or functions of two languages,
// which neither could share.
func byLanguage[T group.Located, G any](items []T, detect func(lang string, items []T) []G) []G {
	var out []G
	for _, lang := range languages {
		out = append(out, detect(lang, inLanguage(lang, items))...)
	}
	return out
}

// inLanguage keeps the items whose file is in lang.
func inLanguage[T group.Located](lang string, items []T) []T {
	return slices.DeleteFunc(slices.Clone(items), func(x T) bool {
		file, _ := x.At()
		return languageOf(file) != lang
	})
}

// drifts finds the convention drifts in the units check analyses (no
// generated code, test code only with --tests), each language on its own
// (see codemap.Drifts): Go's and Rust's forms are never compared. Test
// code has conventions of its own: it asserts an error's message and
// fails with a string where production code would not, so it is measured
// and judged apart from production code, which it neither sets
// conventions for nor is held to. A concern judged has the convention
// judged gives it, else the one measured here; the conventions measured
// here come back, by side (see side).
func drifts(units []unit.Unit, crates *codemap.Packages, judged map[string]map[string]codemap.Convention) ([]report.Drift, map[string]map[string]codemap.Convention) {
	g := &flow.Graph{Units: units}
	var out []report.Drift
	measured := map[string]map[string]codemap.Convention{}
	for _, lang := range languages {
		for _, test := range []bool{false, true} {
			key := side(lang, test)
			in := codemap.Grouping(func(u unit.Unit) string {
				if languageOf(u.File) != lang || u.Test != test {
					return ""
				}
				return key
			})
			measured[key] = codemap.Conventions(g, in)
			conventions := maps.Clone(measured[key])
			maps.Copy(conventions, judged[key])
			out = appendDrifts(out, g, codemap.Drifts(g, in, codemap.ByModule(crates), conventions))
		}
	}
	return out, measured
}

// side names the code a convention is measured on: a language's
// production code ("rust") or its test code ("rust tests").
func side(lang string, test bool) string {
	if test {
		return lang + " tests"
	}
	return lang
}

// appendDrifts appends codemap's drifts as the report's.
func appendDrifts(out []report.Drift, g *flow.Graph, found []codemap.Drift) []report.Drift {
	for _, d := range found {
		r := report.Drift{Concern: d.Concern, Form: d.Form, Convention: d.Convention, Share: d.Share,
			Unit: g.Units[d.Unit], Lines: d.Lines}
		if d.Target >= 0 {
			r.Target, r.TargetLine = g.Units[d.Target], d.TargetLine
		}
		out = append(out, r)
	}
	return out
}

// dependencyCycles finds the modules of each Rust crate, and the
// TypeScript directories, depending on each other in a loop (see
// codemap.Graph.Cycles), in the code check reports on: no generated files, and
// test code only with tests. A Rust module depends on another it calls or
// names in a path: a constant, a type or a use is a dependency as much as
// a call; a TypeScript directory on another it calls or imports from. A
// pair depending on each other is one cycle, closed by its two sites; a
// group with no such pair is one, closed by each step of a shortest loop.
// A cycle any of whose sites a ddslop:ignore marks is deliberate, and
// left out. Go packages and Rust crates cannot form one.
func dependencyCycles(g *flow.Graph, files []index.File, crates *codemap.Packages, modules tsfile.Modules, tests bool) []report.Cycle {
	generated := map[string]bool{}
	for _, f := range files {
		generated[f.Path] = f.Generated
	}
	libraries := libraryCrates(files, crates)
	var out []report.Cycle
	for _, name := range crateNames(files, crates) {
		in := codemap.InCrate(name, crates)
		group := codemap.Grouping(func(u unit.Unit) string {
			// a binary, a build script or an example is a crate of its own
			if generated[u.File] || !inLibrary(u.File, crates, libraries) {
				return ""
			}
			return in(u)
		})
		out = appendCycles(out, g, name, codemap.Graph{Calls: g, Group: group, Deps: dependencies(files, crates, name, generated, libraries, tests)}.Cycles())
	}
	imports := func(group func(file string) string) []codemap.Dependency {
		return tsDependencies(files, modules, tests, group)
	}
	loops := tsCycles(g, imports)
	out = appendCycles(out, g, "typescript", loops)
	return appendSharedDown(out, g, imports, loops)
}

// tsDirectory groups a TypeScript file by its directory, the module map
// describes, and leaves any other file out ("").
func tsDirectory(path string) string {
	if !tsfile.IsSource(path) {
		return ""
	}
	return codemap.Module(path, nil)
}

// appendSharedDown adds a cycle for each TypeScript directory that
// directories below it depend on, and that depends on them, with no loop
// of files joining them (see tsCycles): the directory holds both the code
// wiring them together and files they import back, which belong below
// them. Its steps are each directory below's two directions, a way back
// for each file of the upper directory it reaches; its sites each way
// back's first call, else import, not marked deliberate. Sibling
// directories depending on each other without a loop of files are no
// cycle. imports lists the TypeScript imports between groups (see
// tsDependencies).
func appendSharedDown(out []report.Cycle, g *flow.Graph, imports func(group func(file string) string) []codemap.Dependency, loops []codemap.Cycle) []report.Cycle {
	inLoop := map[[2]string]bool{}
	for _, c := range loops {
		for _, l := range c.Links {
			inLoop[[2]string{l.A, l.B}] = true
		}
	}
	directories := codemap.Graph{Calls: g, Group: func(u unit.Unit) string { return tsDirectory(u.File) }, Deps: imports(tsDirectory)}
	below := map[string][]codemap.Link{} // upper directory -> its links with the directories below it
	for _, c := range directories.Cycles() {
		for _, l := range c.Links {
			if inLoop[[2]string{l.A, l.B}] {
				continue
			}
			switch {
			case isBelow(l.B, l.A):
				below[l.A] = append(below[l.A], l)
			case isBelow(l.A, l.B):
				l.A, l.B, l.AB, l.BA = l.B, l.A, l.BA, l.AB
				below[l.A] = append(below[l.A], l)
			}
		}
	}
	for _, upper := range slices.Sorted(maps.Keys(below)) {
		c := report.Cycle{Key: "typescript\x00" + upper + "\x00" + report.MoveSharedDown, Action: report.MoveSharedDown}
		for _, l := range below[upper] {
			back := map[string]report.CycleSite{} // file of the upper directory reached -> its first site not marked deliberate
			for _, s := range directories.Sites(l.B, l.A) {
				if _, ok := back[siteTarget(g, s)]; !ok {
					if site := appendCycle(nil, g, "", nil, []codemap.Site{s}); len(site) > 0 {
						back[siteTarget(g, s)] = site[0].Sites[0]
					}
				}
			}
			if len(back) > 0 {
				c.Steps = append(c.Steps, l.A+" -> "+l.B)
			}
			for _, file := range slices.Sorted(maps.Keys(back)) {
				c.Steps = append(c.Steps, l.B+" -> "+file)
				c.Sites = append(c.Sites, back[file])
			}
		}
		if len(c.Sites) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// isBelow reports whether directory dir is below directory upper: in it,
// or in a directory below it. The root (".") is above every other.
func isBelow(dir, upper string) bool {
	return upper == "." && dir != "." || strings.HasPrefix(dir, upper+"/")
}

// siteTarget returns the file a site reaches: the called unit's, or the
// file a dependency names.
func siteTarget(g *flow.Graph, s codemap.Site) string {
	if s.Dependency != nil {
		return s.Dependency.Target
	}
	return g.Units[s.Callee].File
}

// tsCycles finds the TypeScript directories depending on each other in a
// loop that files close: a directory's dependency counts only between two
// files of one loop of files (see codemap.Graph.Loops), so an aggregator file
// importing a directory below it, beside a file that directory imports,
// is no cycle here (see appendSharedDown). Each loop of files is grouped
// by directory on its own, so edges of different loops close none
// either. imports lists the TypeScript imports between groups (see
// tsDependencies).
func tsCycles(g *flow.Graph, imports func(group func(file string) string) []codemap.Dependency) []codemap.Cycle {
	file := func(path string) string {
		if !tsfile.IsSource(path) {
			return ""
		}
		return path
	}
	loopOf := map[string]string{}
	for i, loop := range (codemap.Graph{Calls: g, Group: func(u unit.Unit) string { return file(u.File) }, Deps: imports(file)}).Loops() {
		for _, f := range loop {
			loopOf[f] = strconv.Itoa(i)
		}
	}
	directory := func(path string) string {
		if loop, ok := loopOf[path]; ok {
			return tsDirectory(path) + "\x00" + loop
		}
		return ""
	}
	cycles := codemap.Graph{Calls: g, Group: func(u unit.Unit) string { return directory(u.File) }, Deps: imports(directory)}.Cycles()
	return dropLoopIDs(cycles)
}

// dropLoopIDs names the directories of cycles tsCycles found as the
// directories themselves, without the loop of files each is grouped by,
// reporting each pair of directories once, and each loop of a set of
// directories with no such pair once: their names key a cycle's
// fingerprint, the same with or without the grouping.
func dropLoopIDs(cycles []codemap.Cycle) []codemap.Cycle {
	strip := func(name string) string { return strings.Split(name, "\x00")[0] }
	seen := map[string]bool{}
	firstTime := func(names ...string) bool {
		key := strings.Join(names, "\x00")
		if seen[key] {
			return false
		}
		seen[key] = true
		return true
	}
	var out []codemap.Cycle
	for _, c := range cycles {
		for i := range c.Modules {
			c.Modules[i] = strip(c.Modules[i])
		}
		for i := range c.Loop {
			c.Loop[i].From, c.Loop[i].To = strip(c.Loop[i].From), strip(c.Loop[i].To)
		}
		var links []codemap.Link
		for _, l := range c.Links {
			if l.A, l.B = strip(l.A), strip(l.B); firstTime(l.A, l.B) {
				links = append(links, l)
			}
		}
		switch {
		case len(c.Links) > 0 && len(links) > 0:
			c.Links = links
			out = append(out, c)
		case len(c.Links) == 0 && firstTime(c.Modules...):
			out = append(out, c)
		}
	}
	return out
}

// appendCycles adds the cycles among the modules of one crate, or of a
// language's code, named name: a cycle for each pair depending on each
// other, and one for each group with no such pair, closed by its loop.
func appendCycles(out []report.Cycle, g *flow.Graph, name string, cycles []codemap.Cycle) []report.Cycle {
	for _, c := range cycles {
		for _, l := range c.Links {
			out = appendCycle(out, g, name+"\x00"+l.A+"\x00"+l.B,
				[]string{l.A + " -> " + l.B, l.B + " -> " + l.A}, []codemap.Site{l.AB, l.BA})
		}
		if len(c.Loop) > 0 {
			var steps []string
			var sites []codemap.Site
			for _, h := range c.Loop {
				steps = append(steps, h.From+" -> "+h.To)
				sites = append(sites, h.Site)
			}
			out = appendCycle(out, g, name+"\x00"+strings.Join(c.Modules, "\x00"), steps, sites)
		}
	}
	return out
}

// tsDependencies lists the imports of a TypeScript file of another group
// than the importer's, as group names a file ("" to leave it out): one
// whose path names a file of ours (see tsfile.Modules.File), in files
// order and each file's source order. Test files' imports count only with
// tests; a re-export (export * from) or a dynamic import is none.
func tsDependencies(files []index.File, modules tsfile.Modules, tests bool, group func(file string) string) []codemap.Dependency {
	stems := map[string]string{}
	for _, f := range files {
		if tsfile.IsSource(f.Path) {
			stems[tsfile.Stem(f.Path)] = f.Path
		}
	}
	var deps []codemap.Dependency
	for _, f := range files {
		from := group(f.Path)
		if from == "" {
			continue
		}
		for _, im := range f.Decls.Imports {
			stem, ok := modules.File(f.Path, im.Module)
			if !ok || (im.Test && !tests) {
				continue
			}
			file, found := stems[stem]
			if to := group(file); found && to != "" && to != from {
				deps = append(deps, codemap.Dependency{From: from, To: to, File: f.Path, Item: "import", Target: file, Line: im.Line, Ignored: im.Ignored})
			}
		}
	}
	return deps
}

// dependencies lists what the modules of one crate's library depend on
// besides their calls: the modules their paths name (see
// codemap.ReferencedModule), in files order and each file's source order.
func dependencies(files []index.File, crates *codemap.Packages, name string, generated, libraries map[string]bool, tests bool) []codemap.Dependency {
	modules := map[string]bool{}
	for _, f := range files {
		if codemap.Crate(f.Path, crates) == name {
			modules[codemap.Module(f.Path, crates)] = true
		}
	}
	rootValues, selfAliases, rootImports, rootItems := map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]bool{}
	inline, moduleItems, moduleReexports := map[string]bool{}, map[string]map[string]bool{}, map[string]map[string]string{}
	moduleGlobs := map[string][]string{}
	var globs []string
	for _, f := range files {
		if codemap.Crate(f.Path, crates) != name {
			continue
		}
		m := codemap.Module(f.Path, crates)
		if moduleItems[m] == nil {
			moduleItems[m] = map[string]bool{}
		}
		if !codemap.OwnFile(f.Path, crates) {
			continue // a submodule's scope is not what a glob of the module supplies
		}
		for _, n := range f.TopExports {
			moduleItems[m][n] = true
		}
		declared := map[string]bool{}
		for _, d := range append(slices.Clone(f.Modules), f.InlineModules...) {
			declared[strings.TrimPrefix(d, "r#")] = true
		}
		for n, path := range f.TopReexports {
			path = rust.OwnPath(path, declared, f.TopImports)
			if strings.HasPrefix(n, "*") {
				if target, ok := codemap.ReexportPath(f.Path, crates, path); ok {
					moduleGlobs[m] = append(moduleGlobs[m], target)
				}
				continue
			}
			if target, ok := codemap.ReexportPath(f.Path, crates, path); ok {
				if moduleReexports[m] == nil {
					moduleReexports[m] = map[string]string{}
				}
				moduleReexports[m][strings.TrimPrefix(n, "r#")] = target
			}
		}
	}
	for _, f := range files {
		crate, rel := codemap.CratePath(f.Path, crates)
		root := rel == "lib.rs" || rel == "main.rs" && !libraries[crate]
		if crate != name || !root {
			continue
		}
		for _, u := range f.Units {
			if u.Module == "" && !strings.Contains(u.Name, "::") {
				rootValues[strings.TrimPrefix(u.Name, "r#")] = true
			}
		}
		for _, k := range f.Decls.Consts {
			rootValues[strings.TrimPrefix(k.Name, "r#")] = true
		}
		for _, n := range f.TopNames {
			rootItems[n] = true
		}
		// a name the root imports (pub use values::foo) beside its mod foo
		// cannot be a module too: it is a value
		for name := range f.Imports {
			if !strings.HasPrefix(name, "*") {
				rootValues[strings.TrimPrefix(name, "r#")] = true
			}
		}
		for name, path := range f.TopImports {
			if strings.HasPrefix(name, "*") {
				globs = append(globs, path)
				continue
			}
			rootImports[strings.TrimPrefix(name, "r#")] = path
		}
		for _, m := range f.InlineModules {
			inline[strings.TrimPrefix(m, "r#")] = true
		}
		for _, r := range f.References {
			if r.Alias != "" && (tests || !r.Test) {
				selfAliases[r.Alias] = true
			}
		}
	}
	slices.Sort(globs)
	facts := codemap.CrateFacts{Modules: modules, RootValues: rootValues, SelfAliases: selfAliases, RootImports: rootImports,
		RootItems: rootItems, RootGlobs: globs, InlineModules: inline, ModuleItems: moduleItems, ModuleReexports: moduleReexports,
		ModuleGlobs: moduleGlobs}
	var deps []codemap.Dependency
	for _, f := range files {
		if generated[f.Path] || codemap.Crate(f.Path, crates) != name || !inLibrary(f.Path, crates, libraries) {
			continue
		}
		from := codemap.Module(f.Path, crates)
		for _, r := range f.References {
			if r.Test && !tests {
				continue
			}
			if to := codemap.ReferencedModule(f.Path, crates, r, facts); to != "" && to != from {
				deps = append(deps, codemap.Dependency{From: from, To: to, File: f.Path, Item: r.Item, Line: r.Line, Ignored: r.Ignored})
			}
		}
	}
	return deps
}

// libraryCrates returns the crates whose package has a library target
// (src/lib.rs).
func libraryCrates(files []index.File, crates *codemap.Packages) map[string]bool {
	libraries := map[string]bool{}
	for _, f := range files {
		if crate, rel := codemap.CratePath(f.Path, crates); rel == "lib.rs" {
			libraries[crate] = true
		}
	}
	return libraries
}

// tooPublic finds the too public functions and constants (see
// simplify.TooPublic), judging constants as units.
func tooPublic(units []unit.Unit, consts []unit.Const, words map[string]int, library func(file string) *simplify.Library) []unit.Unit {
	asUnits := make([]unit.Unit, 0, len(consts))
	for _, c := range consts {
		asUnits = append(asUnits, unit.Unit{File: c.File, Name: c.Name, Module: c.Module, StartLine: c.Line, EndLine: c.Line, Nodes: c.Nodes,
			Public: c.Public, External: c.External, Test: c.Test})
	}
	return append(simplify.TooPublic(asUnits, words, library), simplify.TooPublic(units, words, library)...)
}

// libraryWords returns, for a Rust file in the library of an unpublished
// crate, the words of that library's files (see simplify.Library); for
// any other file, nil. A library's files are its root file's module tree:
// a binary's modules beside them are not.
func libraryWords(root string, files []index.File, crates *codemap.Packages, internal func(file string) bool) func(file string) *simplify.Library {
	byPath := map[string]index.File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	type module struct {
		path     string
		segments []string // its module path
		optional bool
	}
	owner := map[string]*simplify.Library{}
	for _, f := range files {
		if _, rel := codemap.CratePath(f.Path, crates); rel != "lib.rs" || languageOf(f.Path) != "rust" || !internal(f.Path) {
			continue
		}
		lib := &simplify.Library{Words: map[string]int{}, Optional: map[string]int{}, Declared: map[string]int{}, Exposed: map[string]bool{}}
		tree := map[string]index.File{} // by module path
		for queue := []module{{path: f.Path}}; len(queue) > 0; queue = queue[1:] {
			m := queue[0]
			file, ok := byPath[m.path]
			if !ok || owner[m.path] != nil && owner[m.path] != lib {
				continue
			}
			tree[strings.Join(m.segments, "::")] = file // under each name two #[path]s give it
			if owner[m.path] == lib {
				continue
			}
			owner[m.path] = lib
			optional := m.optional || file.Decls.Reach.AllOptional
			addLibraryWords(root, lib, file, crates.Libraries.PackageOf(file.Path), optional)
			dir := moduleDir(file.Path)
			if m.path == f.Path {
				dir = filepath.Dir(f.Path) // a crate root's modules sit beside it, whatever its name
			}
			for _, name := range file.Modules {
				for _, path := range moduleFiles(file.Path, dir, name) {
					for _, declared := range declaredNames(file, name) {
						segments := append(slices.Clone(m.segments), declared)
						queue = append(queue, module{path, segments, optional || slices.Contains(file.Decls.Reach.CfgModules, name)})
					}
				}
			}
		}
		addGlobbed(lib.Exposed, tree)
	}
	return func(file string) *simplify.Library { return owner[file] }
}

// declaredNames returns the names a file declares a module by, in
// order, given its entry in the file's Modules: those whose #[path]
// attribute names the entry, or the name itself.
func declaredNames(file index.File, entry string) []string {
	var names []string
	for name, path := range file.Decls.Reach.PathModules {
		if path == entry {
			names = append(names, name)
		}
	}
	if names == nil {
		return []string{entry}
	}
	slices.Sort(names)
	return names
}

// addGlobbed adds to exposed the modules a crate's plain pub globs
// re-export whole (see unit.Reach.Globs): one out of line by its file,
// and the modules inside it, or an inline one, as "file#module". A glob's
// path, which rust.Reach reads from the file's module, an imported name
// as its path, is resolved from the file's module (crate::, self::,
// super::, or a child's name); a path into an inline module exposes it
// whole, and one that leaves the crate's modules (another crate, an
// enum's variants) exposes nothing. tree is the crate's module files, by
// module path.
func addGlobbed(exposed map[string]bool, tree map[string]index.File) {
	for here, file := range tree {
		for _, glob := range file.Decls.Reach.Globs {
			resolved, ok := flow.ResolvePath(glob, here)
			if !ok {
				resolved, _ = flow.ResolvePath("self::"+glob, here) // shapes is self::shapes
			}
			exposeModule(exposed, tree, resolved)
		}
	}
}

// exposeModule adds to exposed the module at a module path, with the
// modules inside it, or nothing if the crate has no such module.
func exposeModule(exposed map[string]bool, tree map[string]index.File, module string) {
	at := ""
	for _, segment := range strings.Split(module, "::") {
		if segment == "" {
			break
		}
		child := strings.TrimPrefix(at+"::"+segment, "::")
		if _, ok := tree[child]; ok {
			at = child
			continue
		}
		if file := tree[at]; slices.Contains(file.InlineModules, segment) {
			exposed[file.Path+"#"+segment] = true
		}
		return
	}
	for path, file := range tree {
		if path == at || at == "" || strings.HasPrefix(path, at+"::") {
			exposed[file.Path] = true
		}
	}
}

// addLibraryWords adds a file's words to its library's: all of them
// optional when the file is compiled in some builds only. The doc tests
// of the files its doc attributes include count as outside the library;
// packageDir is the directory of the file's package ("" for none).
func addLibraryWords(root string, lib *simplify.Library, f index.File, packageDir string, optional bool) {
	for w, n := range f.Words {
		lib.Words[w] += n
	}
	for w, n := range f.Decls.Reach.Exported {
		lib.Words[w] -= n
	}
	for _, doc := range f.Decls.Reach.DocFiles {
		subtractDocTests(root, lib, filepath.Join(filepath.Dir(f.Path), doc))
	}
	if packageDir != "" {
		for _, doc := range f.Decls.Reach.PackageDocFiles {
			subtractDocTests(root, lib, filepath.Join(packageDir, doc))
		}
	}
	if !optional {
		addDeclared(lib.Declared, f) // an optional file's are among its optional words
	}
	optionalWords := f.Decls.Reach.Optional
	if optional {
		optionalWords = f.Words
	}
	for w, n := range optionalWords {
		lib.Optional[w] += n
	}
}

// addDeclared counts the names of the items a file declares outside test
// code, whose words are optional already: functions, methods, constants,
// statics, struct fields and enum variants.
func addDeclared(declared map[string]int, f index.File) {
	for _, u := range f.Units {
		if !u.Test {
			declared[simplify.CalledName(u.Name)]++
		}
	}
	for _, c := range f.Decls.Consts {
		if !c.Test {
			declared[simplify.CalledName(c.Name)]++
		}
	}
	for _, s := range f.Decls.Structs {
		if s.Test {
			continue
		}
		for _, field := range s.Fields {
			declared[simplify.CalledName(field.Name)]++
		}
	}
	for _, e := range f.Decls.Enums {
		if e.Test {
			continue
		}
		for _, v := range e.Variants {
			declared[simplify.CalledName(v.Name)]++
		}
	}
}

// subtractDocTests counts the words of the code blocks of a Markdown file
// the library includes as documentation, at path under root, as outside
// the library: rustdoc compiles them as doc tests.
func subtractDocTests(root string, lib *simplify.Library, path string) {
	if !filepath.IsLocal(path) {
		return // outside the analysed code
	}
	src, err := os.ReadFile(filepath.Join(root, path)) //nolint:gosec // a file under the analysed root
	if err != nil {
		return // a missing file fails the build, not this check
	}
	for w, n := range rust.DocTestWords(src) {
		lib.Words[w] -= n
	}
}

// moduleFiles returns the files that may hold a module a file declares
// out of line, as index.Parsed.Modules names it: the file a #[path]
// attribute names, relative to the declaring file's directory, or
// name.rs or name/mod.rs in dir, the declaring file's module directory.
func moduleFiles(parent, dir, name string) []string {
	if strings.HasSuffix(name, ".rs") {
		return []string{filepath.Join(filepath.Dir(parent), name)}
	}
	return []string{filepath.Join(dir, name+".rs"), filepath.Join(dir, name, "mod.rs")}
}

// inLibrary reports whether a Rust file belongs to its package's library
// target, whose crate:: paths name the modules Module names: a file under
// src/, but no binary's (src/bin, or src/main.rs beside a library). A
// build script, an example or a binary is a crate of its own.
func inLibrary(file string, crates *codemap.Packages, libraries map[string]bool) bool {
	crate, rel := codemap.CratePath(file, crates)
	return rel != "" && !strings.HasPrefix(rel, "bin/") && (rel != "main.rs" || !libraries[crate])
}

// cycleGraph is the call graph cycles are found in: the non-test code's,
// or, with --tests, that of the units check analyses, test code included.
func cycleGraph(g *flow.Graph, root string, files []index.File, units []unit.Unit, tests bool) *flow.Graph {
	if !tests {
		return g
	}
	return flow.Build(graphInputs(root, true, files), units)
}

// appendCycle adds a cycle closed by the calls and paths at sites, unless
// a ddslop:ignore marks one of them or the callers.
func appendCycle(out []report.Cycle, g *flow.Graph, key string, steps []string, sites []codemap.Site) []report.Cycle {
	c := report.Cycle{Key: key, Steps: steps}
	for _, s := range sites {
		if d := s.Dependency; d != nil {
			if d.Ignored {
				return out
			}
			c.Sites = append(c.Sites, report.CycleSite{Unit: unit.Unit{File: d.File, Name: d.Item}, Line: d.Line})
			continue
		}
		if s.Caller < 0 || g.Units[s.Caller].Ignored {
			return out
		}
		caller := g.Units[s.Caller]
		line := caller.StartLine
		if s.Call >= 0 {
			if caller.Calls[s.Call].Ignored {
				return out
			}
			line = caller.Calls[s.Call].Line
		}
		c.Sites = append(c.Sites, report.CycleSite{Unit: caller, Line: line})
	}
	return append(out, c)
}

// recordHealth adds a check's health to the index's history (see
// index.DB.RecordHealth), with the ddslop version and the Git revision.
func recordHealth(db *index.DB, root string, h report.Health) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return db.RecordHealth(index.HealthEntry{At: time.Now(), Version: versionString(), Revision: source.Revision(root), Data: data})
}

// wordCounts adds up the words of every indexed file, generated and test
// code included, and, where TypeScript is analysed, of the files that may
// import it or name it without being analysed (see tsfile.Importers and
// tsfile.Configs), files .ddslop.yaml excludes and unreadable ones aside:
// a .vue component or a serverless.yml naming an export uses it.
func wordCounts(root string, files []index.File) (map[string]int, error) {
	words := map[string]int{}
	for _, f := range files {
		for w, n := range f.Words {
			words[w] += n
		}
	}
	if !slices.ContainsFunc(files, func(f index.File) bool { return tsfile.IsSource(f.Path) }) {
		return words, nil
	}
	others, err := includedFiles(root, slices.Concat(tsfile.Importers, tsfile.Configs)...)
	if err != nil {
		return nil, err
	}
	for _, path := range others {
		src, err := os.ReadFile(filepath.Join(root, path)) //nolint:gosec // a file under the analysed root
		if err != nil {
			continue // an unreadable file (a dangling symlink) adds no words
		}
		for w, n := range source.Words(src) {
			words[w] += n
		}
	}
	return words, nil
}

// singleImpls finds the traits with a single implementation in each
// language on its own: a Rust trait and a TypeScript interface, or a type
// of one and a class of the other, never match by their names.
func singleImpls(traitDecls map[string]unit.Decls, files []index.File, internal func(file string) bool) []simplify.SingleImplTrait {
	var out []simplify.SingleImplTrait
	for _, lang := range languages {
		decls := traitDecls[lang]
		if lang == "typescript" {
			decls = withoutImplicitImplementations(decls, files)
		}
		out = append(out, simplify.SingleImpl(decls, internal)...)
	}
	return out
}

// withoutImplicitImplementations leaves out the TypeScript interfaces
// with an implementation no implements clause states: one a test file
// names, as a test's fake of it is one (const fake: IClock = {...}), and
// one a class matches by shape (see shapeImplemented).
func withoutImplicitImplementations(decls unit.Decls, files []index.File) unit.Decls {
	shaped := shapeImplemented(files)
	named := map[string]bool{}
	for _, f := range files {
		if tsfile.IsSource(f.Path) && typescript.IsTestPath(f.Path) {
			for w := range f.Words {
				named[w] = true
			}
		}
	}
	decls.Traits = slices.DeleteFunc(slices.Clone(decls.Traits), func(t unit.Trait) bool {
		return named[t.Name] || shaped[t.File+"#"+t.Name]
	})
	return decls
}

// shapeImplemented returns the TypeScript interfaces, as "file#name",
// that a class of the non-test code implements without saying so: it
// declares every method of the interface, and TypeScript checks
// interfaces by shape.
func shapeImplemented(files []index.File) map[string]bool {
	classes := map[string]map[string]bool{} // "file#Class" -> its methods
	stated := map[string]bool{}             // "file#Class#Interface"
	for _, f := range files {
		if !tsfile.IsSource(f.Path) {
			continue
		}
		for _, u := range f.Units {
			if class, method, ok := strings.Cut(u.Name, "."); ok && !u.Test {
				key := f.Path + "#" + class
				if classes[key] == nil {
					classes[key] = map[string]bool{}
				}
				classes[key][method] = true
			}
		}
		for _, im := range f.Decls.Impls {
			stated[im.File+"#"+im.Type+"#"+im.Trait] = true
		}
	}
	out := map[string]bool{}
	for _, f := range files {
		if !tsfile.IsSource(f.Path) {
			continue
		}
		for _, t := range f.Decls.Traits {
			for class, methods := range classes {
				if !stated[class+"#"+t.Name] && !slices.ContainsFunc(t.Methods, func(m string) bool { return !methods[m] }) {
					out[t.File+"#"+t.Name] = true
					break
				}
			}
		}
	}
	return out
}

// wholeImported reports the TypeScript files another file imports whole
// (import * as schema, import("./routes")), by a relative path, a
// workspace package's name (see tsfile.Modules.Files) or a path alias,
// the file it resolves to, else, for an alias no tsconfig resolves, any
// it may be (see tsfile.AliasMayBe): they may use any of its exports
// without naming it.
func wholeImported(root string, files []index.File) map[string]bool {
	modules := tsModules(root, files)
	out := map[string]bool{}
	for _, importer := range files {
		for _, module := range importer.Decls.WholeImports {
			names := modules.Files(importer.Path, module)
			_, resolved := modules.File(importer.Path, module)
			for _, f := range files {
				if tsfile.IsSource(f.Path) && (names(f.Path) || !resolved && tsfile.AliasMayBe(module, f.Path)) {
					out[f.Path] = true
				}
			}
		}
	}
	return out
}

// internalCrates reports, for a file, whether its crate or package serves
// only the analysed code (see rust.Unpublished and golang.Internal). The
// Rust workspace manifest is the one at root. A TypeScript package is
// internal when its package.json, the nearest above the file, is private,
// which keeps the package from being published (see tsfile.Manifest).
func internalCrates(root string, files []index.File, crates *codemap.Packages) func(file string) bool {
	workspace := manifest(root, ".")
	ts := tsManifests(root, files)
	libraries := crates.Libraries
	internal := map[string]bool{}
	for _, f := range files {
		dir := libraries.CrateOf(f.Path)
		if _, done := internal[dir]; done {
			continue
		}
		switch languageOf(f.Path) {
		case "go":
			internal[dir] = golang.Internal(f.Path, f.Decls.Package)
		case "rust":
			pkg := libraries.PackageOf(f.Path) // a placed target's crate is not its package
			if pkg == "" {
				pkg = dir
			}
			internal[dir] = rust.Unpublished(manifest(root, pkg), workspace)
		}
	}
	return func(file string) bool {
		if languageOf(file) == "typescript" {
			return ts[tsfile.PackageDir(file, ts)].Private
		}
		return internal[libraries.CrateOf(file)]
	}
}

// frequentBlockNodes, frequentBlockLines and frequentBlockDuplicated keep
// small blocks that are copied often. Measured on a 735k-line Rust
// codebase: blocks of 25 to 100 nodes whose copies duplicated 400 nodes or
// more in all (as much as a 100-node block in five places) were shared
// code worth a helper, such as a 46-node connection guard in 27 methods;
// below 400 they were mostly two or three copies of ordinary statements.
// On it and a 188k-line one, every such block of one or two lines was an
// idiom a helper call would not shorten (open a transaction, look up the
// signed-in user, 14 in all), while from three lines on they were guards
// and lookups worth extracting.
const (
	frequentBlockNodes      = 25
	frequentBlockLines      = 3
	frequentBlockDuplicated = 400
)

// minStructFields is the fewest fields a struct needs to be compared with
// others. Measured on a 735k-line Rust codebase: two-field structs
// sharing names were mostly coincidences (host and port, key and value);
// from three fields on they were copies of one type in several services,
// or a wire type mirroring a domain type.
const minStructFields = 3

// parallelFlowOptions compare feature slices. Measured on a 735k-line Rust
// codebase: pairs of features with 4 or more matched steps covering a fifth
// of their own code were copies of one pipeline (handler, authorise,
// fingerprint, read receipt, build event, interpret, refuse), even at 20-30%
// coverage, since each feature also has steps of its own; a single matched
// step is already a near miss.
// defaultMinNodes is the smallest function analysed unless --min-nodes or
// .ddslop.yaml says otherwise.
const defaultMinNodes = 50

var parallelFlowOptions = flow.ParallelOptions{
	Depth:          6,
	MinSteps:       4,
	MinCover:       0.2,
	MinStepSim:     0.7,
	SharedFraction: 0.25,
}

// isHelper reports whether a test unit is a helper compared for copies
// without --tests: no test case, not marked ignored, and not a Go file in
// a testdata directory, a fixture the Go tool never builds (Rust reserves
// no such directory).
func isHelper(u unit.Unit) bool {
	goFixture := strings.HasSuffix(u.File, ".go") && slices.Contains(strings.Split(filepath.ToSlash(u.File), "/"), "testdata")
	return !u.TestCase && !u.Ignored && !goFixture
}

// inFiles counts the files units are in.
func inFiles(units []unit.Unit) int {
	files := map[string]bool{}
	for _, u := range units {
		files[u.File] = true
	}
	return len(files)
}

// minTestHelperNodes is the smallest test helper (a function in test code
// that is no test case) reported as a copy without --tests. Measured on a
// 739k-line Rust workspace: 297 groups of helper copies at 50 nodes, most
// of the smallest one-line fakes and accessors, 156 at 80; ddslop had 5
// and 2.
const minTestHelperNodes = 80

// appendAnalysed appends the declarations to analyse: test code only when
// tests are asked for, and none a ddslop:ignore comment marks, which the
// summary counts.
func appendAnalysed[T any](to, decls []T, tests bool, summary *report.Summary, flags func(T) (test, ignored bool)) []T {
	for _, d := range decls {
		test, ignored := flags(d)
		switch {
		case test && !tests:
		case ignored:
			summary.Ignored++
		default:
			to = append(to, d)
		}
	}
	return to
}

// callGraph builds the call graph of the non-test code and returns it with
// the traits declared in generated files, whose implementations are the
// features' entry points.
func callGraph(root string, files []index.File) (*flow.Graph, map[string]bool) {
	traits := map[string]bool{}
	var units []unit.Unit
	for _, f := range files {
		if f.Generated {
			for _, t := range f.Traits {
				traits[t] = true
			}
		}
		for _, u := range f.Units {
			if !u.Test {
				units = append(units, u)
			}
		}
	}
	return flow.Build(graphInputs(root, false, files), units), traits
}

// families finds the parallel-flow families among the entry points,
// ignoring steps smaller than minNodes.
func families(g *flow.Graph, traits map[string]bool, minNodes int) []flow.ParallelFlow {
	opts := parallelFlowOptions
	opts.MinNodes = minNodes
	return g.Parallel(g.Entries(traits), opts)
}

// parallelFlows finds features running the same pipeline.
func parallelFlows(g *flow.Graph, traits map[string]bool, minNodes int) []report.Flow {
	var out []report.Flow
	for _, pf := range families(g, traits, minNodes) {
		f := report.Flow{MatchedNodes: pf.MatchedNodes, Cover: pf.Cover}
		for _, e := range pf.Entries {
			f.Entries = append(f.Entries, g.Units[e])
		}
		for _, s := range pf.Steps {
			var st report.FlowStep
			for i, u := range s.Units {
				if u >= 0 {
					st.Units = append(st.Units, g.Units[u])
					st.Similarity = append(st.Similarity, s.Similarity[i])
				}
			}
			f.Steps = append(f.Steps, st)
		}
		out = append(out, f)
	}
	return out
}

// loadFiles reads the indexed files and marks the files of out-of-line
// test modules ("#[cfg(test)] mod name;" in a parent file) as test code:
// their functions become test units and their imports are not
// dependencies. Submodules of a test module are test code too.
func loadFiles(root string, db *index.DB) ([]index.File, error) {
	files, err := db.Files()
	if err != nil {
		return nil, err
	}
	if err := parseLibrariesUnderTestPaths(root, files); err != nil {
		return nil, err
	}
	test := inModules(files, func(f index.File) []string { return f.TestModules })
	for i, f := range files {
		if !test(f.Path) {
			continue
		}
		files[i].Imports, files[i].TopImports, files[i].TopReexports, files[i].TopExports = nil, nil, nil, nil
		markTest(&files[i])
	}
	return files, nil
}

// inModules reports, for a file, whether it holds one of the modules
// files declare out of line, as modules lists them, or a submodule of one.
func inModules(files []index.File, modules func(f index.File) []string) func(path string) bool {
	paths := map[string]bool{}
	var dirs []string
	for _, f := range files {
		for _, m := range modules(f) {
			dir := filepath.Join(moduleDir(f.Path), m)
			paths[dir+".rs"] = true
			dirs = append(dirs, dir+string(filepath.Separator))
		}
	}
	return func(path string) bool {
		return paths[path] || slices.ContainsFunc(dirs, func(d string) bool { return strings.HasPrefix(path, d) })
	}
}

// parseLibrariesUnderTestPaths parses again, as the library code they
// are, the files of a library its manifest roots in a directory Cargo
// otherwise keeps for tests or benches (autotests = false, path =
// "tests/lib.rs"): indexed alone, a file there is read as test code.
func parseLibrariesUnderTestPaths(root string, files []index.File) error {
	libraries := packages(root, files).Libraries
	for i, f := range files {
		if !rust.IsTestPath(f.Path) || languageOf(f.Path) != "rust" {
			continue
		}
		if _, _, _, ok := libraries.Locate(f.Path); !ok {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, f.Path)) //nolint:gosec // a file under the analysed root
		if err != nil {
			return err
		}
		parsed, err := parseSource(syntax.Source{Path: f.Path, Src: src})
		if err != nil {
			return err
		}
		files[i].Parsed = parsed
	}
	return nil
}

// markTest marks everything a file declares as test code.
// ddslop:ignore each loop sets Test on another declaration type, a field Go generics cannot reach
func markTest(f *index.File) {
	for i := range f.Units {
		f.Units[i].Test = true
	}
	for i := range f.References {
		f.References[i].Test = true
	}
	d := &f.Decls
	for i := range d.Structs {
		d.Structs[i].Test = true
	}
	for i := range d.FieldTypes {
		d.FieldTypes[i].Test = true
	}
	for i := range d.Consts {
		d.Consts[i].Test = true
	}
	for i := range d.Enums {
		d.Enums[i].Test = true
	}
	for i := range d.Traits {
		d.Traits[i].Test = true
	}
	for i := range d.Impls {
		d.Impls[i].Test = true
	}
}

// moduleDir is the directory holding the files of a Rust file's
// submodules: next to lib.rs, main.rs and mod.rs, else in a directory
// named after the file.
func moduleDir(path string) string {
	switch filepath.Base(path) {
	case "lib.rs", "main.rs", "mod.rs":
		return filepath.Dir(path)
	}
	return strings.TrimSuffix(path, ".rs")
}

// indexPath is where the index lives, relative to the analysed root.
const indexPath = ".ddslop/index.db"

// parseFile parses a source file with the adapter for its language.
// Generated code is not analysed, but the traits or interfaces it declares
// (such as an OpenAPI server's) mark the entry points of features.
func parseFile(path string, src []byte) (index.Parsed, error) {
	return parseSource(syntax.Source{Path: path, Src: src, Test: rust.IsTestPath(path)})
}

// parseSource is parseFile with whether a Rust file is test code by its
// place given (see rust.ParseAs).
func parseSource(file syntax.Source) (index.Parsed, error) {
	path, src := file.Path, file.Src
	language := languageOf(path)
	if syntax.IsGenerated(src) {
		var entries []string
		switch language {
		case "go":
			entries = golang.InterfaceMethods(src)
		case "rust":
			entries = rust.Traits(src)
		}
		parsed := index.Parsed{Generated: true, Traits: entries, Words: source.Words(src)}
		if language == "rust" {
			// Its pub uses still re-export what they name.
			parsed.Decls.Reach = rust.Reach(file)
			// A generated module's names still resolve paths through it.
			parsed.TopNames, parsed.TopExports, parsed.TopReexports = rust.TopNames(src), rust.TopExports(src), rust.TopReexports(src)
			parsed.Modules, parsed.InlineModules = rust.DeclaredModules(src), rust.InlineModules(src)
			_, parsed.TopImports = rust.Imports(src)
		}
		return parsed, nil
	}
	var parsed index.Parsed
	var err error
	switch language {
	case "go":
		parsed.Units, parsed.Decls, err = golang.Parse(path, src)
		parsed.Imports = golang.Imports(src)
	case "typescript":
		parsed.Units, parsed.Decls, err = typescript.Parse(path, src)
	case "rust":
		parsed, err = parseRust(file)
	}
	for i := range parsed.Units {
		parsed.Units[i].Signature = similarity.Signature(parsed.Units[i].Seq)
	}
	parsed.Words = source.Words(src)
	return parsed, err
}

// languageOf names the language of a source file by its extension.
func languageOf(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case tsfile.IsSource(path):
		return "typescript"
	}
	return "rust"
}

// parseRust adds what only Rust files have: out-of-line test modules, the
// traits a file declares, its imports and the paths naming other modules.
func parseRust(file syntax.Source) (index.Parsed, error) {
	units, decls, err := rust.ParseAs(file)
	parsed := index.Parsed{Units: units, Decls: decls, TestModules: rust.TestModules(file.Src),
		Modules: rust.DeclaredModules(file.Src), InlineModules: rust.InlineModules(file.Src), References: rust.ReferencesAs(file),
		TopNames: rust.TopNames(file.Src), TopReexports: rust.TopReexports(file.Src), TopExports: rust.TopExports(file.Src)}
	for _, t := range decls.Traits {
		parsed.Traits = append(parsed.Traits, t.Name)
	}
	if !file.Test {
		// Test files' imports are not dependencies of the code.
		parsed.Imports, parsed.TopImports = rust.Imports(file.Src)
	}
	return parsed, err
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
