package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/integrate"
	"github.com/heikki-laitala/ddslop/internal/report"
)

const initUsage = `usage: ddslop init [--claude] [--codex] [--copilot] [path]

Sets ddslop up in the project at path (default "."): a ddslop skill for
each chosen coding agent, a "## ddslop" section in its instructions file
(CLAUDE.md, AGENTS.md or .github/copilot-instructions.md), a .ddslop/
entry in .gitignore, and the index. Without agent flags, installs for
Claude Code. Running it again only updates what changed.

Flags:
`

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("init", initUsage, stderr)
	claude := fs.Bool("claude", false, "install for Claude Code (.claude/skills, CLAUDE.md)")
	codex := fs.Bool("codex", false, "install for Codex (.codex/skills, AGENTS.md)")
	copilot := fs.Bool("copilot", false, "install for Copilot (.copilot/skills, .github/copilot-instructions.md)")
	root, code, ok := parseRoot(fs, args, stderr)
	if !ok {
		return code
	}
	var agents []integrate.Agent
	for agent, chosen := range map[integrate.Agent]bool{integrate.Claude: *claude, integrate.Codex: *codex, integrate.Copilot: *copilot} {
		if chosen {
			agents = append(agents, agent)
		}
	}
	if len(agents) == 0 {
		agents = []integrate.Agent{integrate.Claude}
	}

	changes, err := integrate.Install(root, agents)
	for _, c := range changes {
		fmt.Fprintln(stdout, c)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return runIndex([]string{root}, stdout, stderr)
}

const statusUsage = `usage: ddslop status [path]

Shows what ddslop has installed in the project at path (default "."):
the .gitignore entry, the index and each agent's skill and instructions.

Flags:
`

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("status", statusUsage, stderr)
	root, code, ok := parseRoot(fs, args, stderr)
	if !ok {
		return code
	}
	s, err := integrate.Check(root)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, ".gitignore: .ddslop/ %s\n", presence(s.Gitignore))
	fmt.Fprintf(stdout, "index: %s\n", presence(s.Index))
	for _, agent := range integrate.Agents {
		a := s.Agents[agent]
		var parts []string
		if a.Skill {
			parts = append(parts, "skill")
		}
		if a.Instructions {
			parts = append(parts, "instructions")
		}
		if len(parts) == 0 {
			parts = []string{"not installed"}
		}
		fmt.Fprintf(stdout, "%s: %s\n", agent, strings.Join(parts, ", "))
	}
	return exitClean
}

func presence(ok bool) string {
	if ok {
		return "present"
	}
	return "missing"
}

const uninstallUsage = `usage: ddslop uninstall [--purge] [path]

Removes ddslop's skills, instruction sections and .gitignore entry from
the project at path (default "."). Other content of those files is kept.

Flags:
`

func runUninstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("uninstall", uninstallUsage, stderr)
	purge := fs.Bool("purge", false, "also remove the index in .ddslop/")
	root, code, ok := parseRoot(fs, args, stderr)
	if !ok {
		return code
	}
	changes, err := integrate.Uninstall(root, *purge)
	for _, c := range changes {
		fmt.Fprintln(stdout, c)
	}
	if err != nil {
		return fail(stderr, err)
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "nothing to remove")
	}
	return exitClean
}

const statsUsage = `usage: ddslop stats [path]

Describes the index at <path>/.ddslop/index.db without updating it: how
many files and functions it holds.

Flags:
`

func runStats(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("stats", statsUsage, stderr)
	root, code, ok := parseRoot(fs, args, stderr)
	if !ok {
		return code
	}
	path := filepath.Join(root, indexPath)
	info, err := os.Stat(path)
	if err != nil {
		return fail(stderr, fmt.Errorf("no index at %s; run ddslop index first", path))
	}
	db, err := index.Open(path)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = db.Close() }()
	files, err := loadFiles(db)
	if err != nil {
		return fail(stderr, err)
	}
	var generated, functions, tests int
	for _, f := range files {
		if f.Generated {
			generated++
		}
		for _, u := range f.Units {
			functions++
			if u.Test {
				tests++
			}
		}
	}
	fmt.Fprintf(stdout, "index: %s (%.1f MB)\n", path, float64(info.Size())/(1<<20))
	fmt.Fprintf(stdout, "files: %d (%d generated)\n", len(files), generated)
	fmt.Fprintf(stdout, "functions: %d (%d test)\n", functions, tests)
	history, err := db.HealthHistory(trendEntries)
	if err != nil {
		return fail(stderr, err)
	}
	if err := writeTrend(stdout, history); err != nil {
		return fail(stderr, err)
	}
	return exitClean
}

// trendEntries is how many of the latest health entries stats shows.
const trendEntries = 10

// writeTrend prints the health history, oldest first, and the change in
// duplication over the latest entries made by one ddslop version: detection
// rules change between versions, so earlier numbers are not comparable.
func writeTrend(w io.Writer, history []index.HealthEntry) error {
	if len(history) == 0 {
		_, err := fmt.Fprintln(w, "health: no history yet; ddslop check records it")
		return err
	}
	var b strings.Builder
	b.WriteString("health history:\n")
	healths := make([]report.Health, len(history))
	for i, e := range history {
		if err := json.Unmarshal(e.Data, &healths[i]); err != nil {
			return fmt.Errorf("health history: %w", err)
		}
		h := healths[i]
		revision := e.Revision
		if revision == "" {
			revision = "-"
		}
		fmt.Fprintf(&b, "  %s  %-14s %.1f%% duplicated (%d of %d lines); %d unused functions, %d wrappers  %s\n",
			e.At.Local().Format("2006-01-02 15:04"), revision, h.DuplicationPercent, h.DuplicatedLines, h.AnalysedLines,
			h.UnusedFunctions, h.Wrappers, e.Version)
	}
	last := len(history) - 1
	first := last
	for first > 0 && history[first-1].Version == history[last].Version {
		first--
	}
	if first < last {
		from, to := healths[first].DuplicationPercent, healths[last].DuplicationPercent
		fmt.Fprintf(&b, "change: %.1f%% -> %.1f%% (%+.1f points) since %s\n", from, to, to-from, history[first].At.Local().Format("2006-01-02 15:04"))
	}
	if first > 0 {
		fmt.Fprintf(&b, "note: entries before %s were made by another ddslop version; their numbers are not comparable\n",
			history[first].At.Local().Format("2006-01-02 15:04"))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
