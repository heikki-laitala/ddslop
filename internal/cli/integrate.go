package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/index"
	"github.com/heikki-laitala/ddslop/internal/integrate"
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
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
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
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
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
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
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
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root, err := rootArg(fs)
	if err != nil {
		return fail(stderr, err)
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
	files, err := db.Files()
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
	return exitClean
}
