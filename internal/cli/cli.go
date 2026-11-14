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
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/heikki-laitala/ddslop/internal/baseline"
	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/codemap"
	"github.com/heikki-laitala/ddslop/internal/config"
	"github.com/heikki-laitala/ddslop/internal/flow"
	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/lang/golang"
	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/lang/syntax"
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

Finds duplicated Rust and Go code under path (default ".") and proposes
refactoring candidates: exact copies, renamed copies, near-miss copies
with added, removed or changed statements, copied blocks of statements
inside larger functions, features running the same pipeline, structs with
the same fields, constants declared twice or written inline, unused public
functions of unpublished crates, wrappers that only forward their
parameters, and traits with a single implementation. Generated files are
skipped, and so is test code unless --tests is given. Extracted functions are kept in an index at
<path>/.ddslop/index.db so unchanged files are not parsed again. Files are
parsed on half the CPU cores; set DDSLOP_INDEX_THREADS to change that.

With --diff <ref>, the whole repository is still analysed, but only
candidates touching lines changed since ref are reported, and the changed
occurrences are marked: use it to check what a change introduced, for
example "--diff HEAD" for uncommitted work or "--diff origin/main" in a
pull request. Exits 0 when nothing is found, 1 when candidates are found,
2 on error. With --fail-over N, it exits 1 only when the repository's
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

	r, err := filteredCheck(root, opts, *diff, *baselinePath)
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

// filteredCheck analyses root and narrows the candidates as check reports
// them: to those not in the baseline file, if one is given, and to those
// touching lines changed since diffRef, if one is given. Candidate IDs
// refer to this list, so commands that take an ID filter the same way.
func filteredCheck(root string, opts options, diffRef, baselinePath string) (report.Report, error) {
	var known baseline.Baseline
	var err error
	if baselinePath != "" {
		if known, err = baseline.Load(baselinePath); err != nil {
			return report.Report{}, err
		}
	}
	var changes source.Changes
	if diffRef != "" {
		if changes, err = source.ChangedLines(root, diffRef); err != nil {
			return report.Report{}, err
		}
	}
	r, err := check(root, opts)
	if err != nil {
		return report.Report{}, err
	}
	if baselinePath != "" {
		r = baseline.Filter(r, known)
	}
	if diffRef != "" {
		r = report.KeepChanged(r, diffRef, changes.Touches)
	}
	return r, nil
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
// Rust and Go files there that .ddslop.yaml does not exclude. The caller
// closes the database.
func syncIndex(root string) (*index.DB, index.Stats, error) {
	paths, err := source.Files(root, ".rs", ".go")
	if err != nil {
		return nil, index.Stats{}, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, index.Stats{}, err
	}
	paths = slices.DeleteFunc(paths, func(p string) bool { return cfg.Excluded(filepath.ToSlash(p)) })
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
	files, err := loadFiles(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, stats, err
	}
	return db, files, stats, nil
}

// threadCount is how many goroutines parse files and verify near misses.
func threadCount() int {
	return index.ResolveThreads(os.Getenv("DDSLOP_INDEX_THREADS"), runtime.NumCPU())
}

func check(root string, opts options) (report.Report, error) {
	db, files, stats, err := openFiles(root)
	if err != nil {
		return report.Report{}, err
	}
	defer func() { _ = db.Close() }()
	threads := threadCount()

	summary := report.Summary{ParsedFiles: stats.Parsed, UnchangedFiles: stats.Unchanged}
	var units []unit.Unit
	var structs []unit.Struct
	var enums []unit.Enum
	var consts []unit.Const
	var decls unit.Decls    // every file's, test code included, for traits
	var helpers []unit.Unit // test code's helpers, fakes and fixtures, without --tests
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
		decls.Traits = append(decls.Traits, f.Decls.Traits...)
		decls.Impls = append(decls.Impls, f.Decls.Impls...)
		decls.TypeNames = append(decls.TypeNames, f.Decls.TypeNames...)
		decls.FuncTypes = append(decls.FuncTypes, f.Decls.FuncTypes...)
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
	internal := internalCrates(root, files)
	flows := parallelFlows(g, traits, opts.minNodes)
	r := report.Build(languageOf, report.Findings{
		Groups: append(groups, helperGroups...), NearMisses: nearMisses, Blocks: blocks, Flows: flows,
		Structs:  clone.Structs(structs, minStructFields),
		Enums:    clone.Enums(enums),
		Consts:   clone.Constants(consts),
		Inlined:  clone.InlinedConstants(consts, units, flow.CrateOf),
		Dead:     simplify.Dead(units, wordCounts(files), internal),
		Wrappers: simplify.PassThrough(g),
		Traits:   simplify.SingleImpl(decls, internal),
		Scopes:   pattern.Scopes(units),
		Kinds:    pattern.StringKinds(units),
		Params:   pattern.ParameterGroups(units, decls.FuncTypes),
		Drifts:   drifts(units, packages(root, files)),
		Cycles:   dependencyCycles(cycleGraph(g, root, files, units, opts.tests), files, packages(root, files), opts.tests),
	}, summary)
	return r, recordHealth(db, root, r.Summary.Health)
}

// drifts finds the convention drifts in the units check analyses (no
// generated code, test code only with --tests), each language on its own
// (see codemap.Drifts): Go's and Rust's forms are never compared.
func drifts(units []unit.Unit, crates *codemap.Packages) []report.Drift {
	g := &flow.Graph{Units: units}
	var out []report.Drift
	for _, lang := range []string{"go", "rust"} {
		inLang := codemap.Grouping(func(u unit.Unit) string {
			if languageOf(u.File) != lang {
				return ""
			}
			return lang
		})
		for _, d := range codemap.Drifts(g, inLang, codemap.ByModule(crates)) {
			out = append(out, report.Drift{Concern: d.Concern, Form: d.Form, Convention: d.Convention,
				Unit: g.Units[d.Unit], Lines: d.Lines, Target: g.Units[d.Target], TargetLine: d.TargetLine})
		}
	}
	return out
}

// dependencyCycles finds the modules of each Rust crate depending on each
// other in a loop (see codemap.Cycles), in the code check reports on: no
// generated files, and test code only with tests. A module depends on
// another it calls or names in a path: a constant, a type or a use is a
// dependency as much as a call. A pair depending on each other is one
// cycle, closed by its two sites; a group with no such pair is one, closed
// by each step of a shortest loop. A cycle any of whose sites a
// ddslop:ignore marks is deliberate, and left out. Go packages and Rust
// crates cannot form one.
func dependencyCycles(g *flow.Graph, files []index.File, crates *codemap.Packages, tests bool) []report.Cycle {
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
		for _, c := range codemap.Cycles(g, group, dependencies(files, crates, name, generated, libraries, tests)) {
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
	}
	return out
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
	rootValues, selfAliases := map[string]bool{}, map[string]bool{}
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
		// a name the root imports (pub use values::foo) beside its mod foo
		// cannot be a module too: it is a value
		for name := range f.Imports {
			rootValues[strings.TrimPrefix(name, "r#")] = true
		}
		for _, r := range f.References {
			if r.Alias != "" && (tests || !r.Test) {
				selfAliases[r.Alias] = true
			}
		}
	}
	facts := codemap.CrateFacts{Modules: modules, RootValues: rootValues, SelfAliases: selfAliases}
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
// files included: a name used only there is still used.
func wordCounts(files []index.File) map[string]int {
	words := map[string]int{}
	for _, f := range files {
		for w, n := range f.Words {
			words[w] += n
		}
	}
	return words
}

// internalCrates reports, for a file, whether its crate or package serves
// only the analysed code (see rust.Unpublished and golang.Internal). The
// Rust workspace manifest is the one at root.
func internalCrates(root string, files []index.File) func(file string) bool {
	workspace := manifest(root, ".")
	libraries := packages(root, files).Libraries
	internal := map[string]bool{}
	for _, f := range files {
		dir := libraries.CrateOf(f.Path)
		if _, done := internal[dir]; done {
			continue
		}
		if languageOf(f.Path) == "go" {
			internal[dir] = golang.Internal(f.Path, f.Decls.Package)
		} else {
			internal[dir] = rust.Unpublished(manifest(root, dir), workspace)
		}
	}
	return func(file string) bool { return internal[libraries.CrateOf(file)] }
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
func loadFiles(db *index.DB) ([]index.File, error) {
	files, err := db.Files()
	if err != nil {
		return nil, err
	}
	testFiles := map[string]bool{}
	var testDirs []string
	for _, f := range files {
		for _, m := range f.TestModules {
			dir := filepath.Join(moduleDir(f.Path), m)
			testFiles[dir+".rs"] = true
			testDirs = append(testDirs, dir+string(filepath.Separator))
		}
	}
	for i, f := range files {
		test := testFiles[f.Path] || slices.ContainsFunc(testDirs, func(d string) bool { return strings.HasPrefix(f.Path, d) })
		if !test {
			continue
		}
		files[i].Imports = nil
		markTest(&files[i])
	}
	return files, nil
}

// markTest marks everything a file declares as test code.
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
	golangFile := languageOf(path) == "go"
	if syntax.IsGenerated(src) {
		entries := rust.Traits(src)
		if golangFile {
			entries = golang.InterfaceMethods(src)
		}
		return index.Parsed{Generated: true, Traits: entries, Words: source.Words(src)}, nil
	}
	var parsed index.Parsed
	var err error
	if golangFile {
		parsed.Units, parsed.Decls, err = golang.Parse(path, src)
	} else {
		parsed, err = parseRust(path, src)
	}
	for i := range parsed.Units {
		parsed.Units[i].Signature = similarity.Signature(parsed.Units[i].Seq)
	}
	parsed.Words = source.Words(src)
	return parsed, err
}

// languageOf names the language of a source file by its extension.
func languageOf(path string) string {
	if strings.HasSuffix(path, ".go") {
		return "go"
	}
	return "rust"
}

// parseRust adds what only Rust files have: out-of-line test modules, the
// traits a file declares, its imports and the paths naming other modules.
func parseRust(path string, src []byte) (index.Parsed, error) {
	units, decls, err := rust.Parse(path, src)
	parsed := index.Parsed{Units: units, Decls: decls, TestModules: rust.TestModules(src), References: rust.References(path, src)}
	for _, t := range decls.Traits {
		parsed.Traits = append(parsed.Traits, t.Name)
	}
	if !rust.IsTestPath(path) {
		// Test files' imports are not dependencies of the code.
		parsed.Imports = rust.Imports(src)
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
