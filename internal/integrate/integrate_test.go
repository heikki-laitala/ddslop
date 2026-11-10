package integrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, root, name string) string {
	t.Helper()
	content, err := readFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return content
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func install(t *testing.T, root string, agents ...Agent) []string {
	t.Helper()
	changes, err := Install(root, agents)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return changes
}

func TestInstallClaude(t *testing.T) {
	root := t.TempDir()
	write(t, root, "CLAUDE.md", "# Project\n\nExisting rules.\n")
	write(t, root, ".gitignore", "target/")

	changes := install(t, root, Claude)

	skill := read(t, root, ".claude/skills/ddslop/SKILL.md")
	if !strings.HasPrefix(skill, "---\nname: ddslop\n") || !strings.Contains(skill, "allowed-tools: Bash(ddslop *)") {
		t.Errorf("Claude skill front matter is wrong:\n%s", skill[:min(300, len(skill))])
	}
	if strings.Contains(skill, "{{") {
		t.Error("skill still contains template directives")
	}
	claudeMD := read(t, root, "CLAUDE.md")
	if !strings.HasPrefix(claudeMD, "# Project\n\nExisting rules.\n") || !strings.Contains(claudeMD, "\n## ddslop\n") {
		t.Errorf("CLAUDE.md lost its content or lacks the section:\n%s", claudeMD)
	}
	if got := read(t, root, ".gitignore"); got != "target/\n.ddslop/\n" {
		t.Errorf(".gitignore = %q, want the entry appended on its own line", got)
	}
	if len(changes) != 3 {
		t.Errorf("changes = %v, want 3 lines (skill, CLAUDE.md, .gitignore)", changes)
	}
}

func TestInstallCodexAndCopilot(t *testing.T) {
	root := t.TempDir()

	install(t, root, Codex, Copilot)

	for _, skillPath := range []string{".codex/skills/ddslop/SKILL.md", ".copilot/skills/ddslop/SKILL.md"} {
		skill := read(t, root, skillPath)
		if !strings.HasPrefix(skill, "---\nname: ddslop\n") || strings.Contains(skill, "allowed-tools") {
			t.Errorf("%s: want plain front matter without Claude's allowed-tools", skillPath)
		}
	}
	for _, instructions := range []string{"AGENTS.md", ".github/copilot-instructions.md"} {
		if !strings.HasPrefix(read(t, root, instructions), "## ddslop\n") {
			t.Errorf("%s does not start with the ddslop section", instructions)
		}
	}
	if exists(filepath.Join(root, "CLAUDE.md")) || exists(filepath.Join(root, ".claude")) {
		t.Error("Claude files were written without asking for Claude")
	}
}

func TestInstallIsIdempotentAndUpdatesTheSectionInPlace(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# Rules\n\n## ddslop\n\nOld text.\n\n## Testing\n\nRun tests.\n")

	install(t, root, Codex)
	second := install(t, root, Codex)

	agents := read(t, root, "AGENTS.md")
	if strings.Count(agents, "## ddslop") != 1 || strings.Contains(agents, "Old text.") {
		t.Errorf("section not replaced in place:\n%s", agents)
	}
	if !strings.HasPrefix(agents, "# Rules\n\n## ddslop\n") || !strings.HasSuffix(agents, "## Testing\n\nRun tests.\n") {
		t.Errorf("content around the section changed:\n%s", agents)
	}
	if got := read(t, root, ".gitignore"); got != ".ddslop/\n" {
		t.Errorf(".gitignore = %q after two installs, want one entry", got)
	}
	if len(second) != 0 {
		t.Errorf("second install reported changes %v, want none", second)
	}
}

func TestInstallWritesTheSectionOnceWhenClaudeMDLinksToAgentsMD(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# Rules\n")
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	install(t, root, Claude, Codex)

	if got := strings.Count(read(t, root, "AGENTS.md"), "## ddslop"); got != 1 {
		t.Errorf("AGENTS.md has %d ddslop sections, want 1", got)
	}
	info, err := os.Lstat(filepath.Join(root, "CLAUDE.md"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("CLAUDE.md is no longer a symlink")
	}
}

func TestCheckReportsInstalledParts(t *testing.T) {
	root := t.TempDir()
	install(t, root, Claude)
	write(t, root, ".ddslop/index.db", "")

	s, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Gitignore || !s.Index {
		t.Errorf("status = %+v, want gitignore and index present", s)
	}
	if got := s.Agents[Claude]; !got.Skill || !got.Instructions {
		t.Errorf("Claude status = %+v, want skill and instructions", got)
	}
	if got := s.Agents[Codex]; got.Skill || got.Instructions {
		t.Errorf("Codex status = %+v, want nothing installed", got)
	}
}

func TestUninstallRemovesEverythingButOtherContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "CLAUDE.md", "# Project\n\nExisting rules.\n")
	write(t, root, ".gitignore", "target/\n")
	install(t, root, Claude, Codex, Copilot)
	write(t, root, ".ddslop/index.db", "")

	if _, err := Uninstall(root, false); err != nil {
		t.Fatal(err)
	}

	if got := read(t, root, "CLAUDE.md"); got != "# Project\n\nExisting rules.\n" {
		t.Errorf("CLAUDE.md after uninstall = %q", got)
	}
	if got := read(t, root, ".gitignore"); got != "target/\n" {
		t.Errorf(".gitignore after uninstall = %q", got)
	}
	for _, gone := range []string{".claude/skills/ddslop", ".codex/skills/ddslop", ".copilot/skills/ddslop"} {
		if exists(filepath.Join(root, gone)) {
			t.Errorf("%s still exists", gone)
		}
	}
	if !exists(filepath.Join(root, ".ddslop/index.db")) {
		t.Error("index removed without --purge")
	}

	if _, err := Uninstall(root, true); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, ".ddslop")) {
		t.Error("purge left .ddslop/")
	}
}
