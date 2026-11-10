// Package integrate installs ddslop into a project for coding agents: a
// skill per agent, a section in the agent's instructions file and a
// .gitignore entry for the index. The layout follows pruner's.
package integrate

import (
	"bytes"
	_ "embed" // skill and section templates
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
)

// Agent is a coding agent ddslop can be installed for.
type Agent string

// Supported agents.
const (
	Claude  Agent = "claude"
	Codex   Agent = "codex"
	Copilot Agent = "copilot"
)

// Agents lists every supported agent.
var Agents = []Agent{Claude, Codex, Copilot}

// target is where an agent's skill and instructions live, relative to the
// project root.
type target struct {
	skillDir, instructions string
}

var targets = map[Agent]target{
	Claude:  {".claude/skills/ddslop", "CLAUDE.md"},
	Codex:   {".codex/skills/ddslop", "AGENTS.md"},
	Copilot: {".copilot/skills/ddslop", ".github/copilot-instructions.md"},
}

const (
	indexDir      = ".ddslop"
	gitignoreLine = ".ddslop/"
	sectionTitle  = "## ddslop"
)

var (
	//go:embed templates/SKILL.md
	skillTemplate string
	//go:embed templates/section.md
	section string

	skill = template.Must(template.New("SKILL.md").Parse(skillTemplate))
)

// AgentStatus says what is installed for one agent.
type AgentStatus struct {
	Skill, Instructions bool
}

// Status says what ddslop has installed in a project.
type Status struct {
	Gitignore bool
	Index     bool
	Agents    map[Agent]AgentStatus
}

// Install sets ddslop up for the given agents in root and returns a line
// per change made. Running it again changes nothing. An instructions file
// shared through a symlink (CLAUDE.md -> AGENTS.md) gets one section.
func Install(root string, agents []Agent) ([]string, error) {
	var changes []string
	note := func(changed bool, err error, format string, args ...any) error {
		if err == nil && changed {
			changes = append(changes, fmt.Sprintf(format, args...))
		}
		return err
	}

	gitignore := filepath.Join(root, ".gitignore")
	changed, err := addLine(gitignore, gitignoreLine)
	if err := note(changed, err, "added %s to %s", gitignoreLine, gitignore); err != nil {
		return changes, err
	}
	written := map[string]bool{}
	for _, agent := range Agents {
		if !slices.Contains(agents, agent) {
			continue
		}
		t := targets[agent]
		var content bytes.Buffer
		if err := skill.Execute(&content, struct{ Claude bool }{agent == Claude}); err != nil {
			return changes, err
		}
		path := filepath.Join(root, t.skillDir, "SKILL.md")
		changed, err = writeFile(path, content.String())
		if err := note(changed, err, "installed %s skill -> %s", agent, path); err != nil {
			return changes, err
		}

		path = filepath.Join(root, t.instructions)
		resolved := resolve(path)
		if written[resolved] {
			continue
		}
		written[resolved] = true
		changed, err = upsertSection(path)
		if err := note(changed, err, "updated %s", path); err != nil {
			return changes, err
		}
	}
	return changes, nil
}

// Check reports what ddslop has installed in root.
func Check(root string) (Status, error) {
	s := Status{Agents: map[Agent]AgentStatus{}}
	var err error
	if s.Gitignore, err = hasLine(filepath.Join(root, ".gitignore"), gitignoreLine); err != nil {
		return s, err
	}
	s.Index = exists(filepath.Join(root, indexDir, "index.db"))
	for _, agent := range Agents {
		t := targets[agent]
		var a AgentStatus
		a.Skill = exists(filepath.Join(root, t.skillDir, "SKILL.md"))
		if a.Instructions, err = hasLine(filepath.Join(root, t.instructions), sectionTitle); err != nil {
			return s, err
		}
		s.Agents[agent] = a
	}
	return s, nil
}

// Uninstall removes ddslop's skills, instruction sections and .gitignore
// entry from root, and with purge also the index, returning a line per
// change made.
func Uninstall(root string, purge bool) ([]string, error) {
	var changes []string
	written := map[string]bool{}
	for _, agent := range Agents {
		t := targets[agent]
		dir := filepath.Join(root, t.skillDir)
		if exists(dir) {
			if err := os.RemoveAll(dir); err != nil {
				return changes, err
			}
			changes = append(changes, "removed "+dir)
		}
		path := filepath.Join(root, t.instructions)
		resolved := resolve(path)
		if written[resolved] {
			continue
		}
		written[resolved] = true
		changed, err := removeSection(path)
		if err != nil {
			return changes, err
		}
		if changed {
			changes = append(changes, "removed the ddslop section from "+path)
		}
	}
	gitignore := filepath.Join(root, ".gitignore")
	changed, err := removeLine(gitignore, gitignoreLine)
	if err != nil {
		return changes, err
	}
	if changed {
		changes = append(changes, "removed "+gitignoreLine+" from "+gitignore)
	}
	if dir := filepath.Join(root, indexDir); purge && exists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return changes, err
		}
		changes = append(changes, "removed "+dir)
	}
	return changes, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// resolve returns the file a path finally refers to, following symlinks.
func resolve(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// readFile returns a file's content, or "" when it does not exist.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // paths are the project's own agent files
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// writeFile writes content unless the file already holds it, reporting
// whether it wrote. Writing through a symlink keeps the link.
func writeFile(path, content string) (bool, error) {
	old, err := readFile(path)
	if err != nil || old == content {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(content), 0o644) //nolint:gosec // project files are meant to be readable
}

func hasLine(path, line string) (bool, error) {
	content, err := readFile(path)
	if err != nil {
		return false, err
	}
	for l := range strings.Lines(content) {
		if strings.TrimSpace(l) == line {
			return true, nil
		}
	}
	return false, nil
}

// addLine appends line to a file unless it is already there.
func addLine(path, line string) (bool, error) {
	has, err := hasLine(path, line)
	if err != nil || has {
		return false, err
	}
	content, err := readFile(path)
	if err != nil {
		return false, err
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return writeFile(path, content+line+"\n")
}

func removeLine(path, line string) (bool, error) {
	content, err := readFile(path)
	if err != nil || content == "" {
		return false, err
	}
	var kept []string
	for l := range strings.Lines(content) {
		if strings.TrimSpace(l) != line {
			kept = append(kept, l)
		}
	}
	return writeFile(path, strings.Join(kept, ""))
}

// findSection returns the byte range of the ddslop section: from its
// heading to the next level-one or level-two heading, or the end.
func findSection(content string) (start, end int, ok bool) {
	offset := 0
	start = -1
	for l := range strings.Lines(content) {
		trimmed := strings.TrimRight(l, "\n")
		switch {
		case start < 0 && trimmed == sectionTitle:
			start = offset
		case start >= 0 && (strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ")):
			return start, offset, true
		}
		offset += len(l)
	}
	return start, len(content), start >= 0
}

// upsertSection replaces the ddslop section of a file, or appends it.
func upsertSection(path string) (bool, error) {
	content, err := readFile(path)
	if err != nil {
		return false, err
	}
	var updated string
	if start, end, ok := findSection(content); ok {
		after := content[end:]
		updated = content[:start] + section
		if after != "" {
			updated += "\n" + after
		}
	} else {
		updated = section
		if content != "" {
			updated = strings.TrimRight(content, "\n") + "\n\n" + section
		}
	}
	return writeFile(path, updated)
}

// removeSection deletes the ddslop section of a file, and the file itself
// when nothing else is left.
func removeSection(path string) (bool, error) {
	content, err := readFile(path)
	if err != nil {
		return false, err
	}
	start, end, ok := findSection(content)
	if !ok {
		return false, nil
	}
	before, after := content[:start], content[end:]
	if after == "" {
		before = strings.TrimRight(before, "\n")
		if before == "" {
			return true, os.Remove(path)
		}
		before += "\n"
	}
	return writeFile(path, before+after)
}
