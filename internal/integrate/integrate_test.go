package integrate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/testutil"
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
	testutil.WriteFiles(t, root, map[string]string{name: content})
}

func install(t *testing.T, root string, agents ...Agent) []string {
	t.Helper()
	changes, err := Install(root, agents)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return changes
}

func TestInstalledGuidanceReadsTheMapBeforeAFeature(t *testing.T) {
	root := t.TempDir()
	install(t, root, Claude)

	skill := read(t, root, ".claude/skills/ddslop/SKILL.md")
	description, _, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---")
	if !ok {
		t.Fatalf("skill has no front matter:\n%s", skill[:min(300, len(skill))])
	}
	for _, want := range []string{"## Before you write", "ddslop map --format json .", "hints, not errors",
		"ddslop map --format json --crate <name> .", "- `features`: ", "extract the pipeline",
		"- `idioms`: ", "- `convention-drift` (`follow-convention`"} {
		if !strings.Contains(skill, want) {
			t.Errorf("skill lacks %q", want)
		}
	}
	if !strings.Contains(description, "before starting a feature") {
		t.Errorf("skill description does not say to use it before starting a feature:\n%s", description)
	}
	if section := read(t, root, "CLAUDE.md"); !strings.Contains(section, "Before starting a feature, run `ddslop map") {
		t.Errorf("CLAUDE.md section does not say to run map before a feature:\n%s", section)
	}
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

// A section an earlier ddslop wrote is brought up to date in place.
func TestInstallIsIdempotentAndUpdatesTheSectionInPlace(t *testing.T) {
	root := t.TempDir()
	former, err := os.ReadFile("templates/former/1.md")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "AGENTS.md", "# Rules\n\n"+string(former)+"\n## Testing\n\nRun tests.\n")

	install(t, root, Codex)
	second := install(t, root, Codex)

	agents := read(t, root, "AGENTS.md")
	if strings.Count(agents, sectionTitle) != 1 || !strings.Contains(agents, section) {
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

// With one instructions file a symlink to the other, either way round
// (the other way is common too), the section goes into the real file
// once, the link stays a link, and uninstall restores the real file.
func TestInstallWritesTheSectionIntoTheRealFileBehindALink(t *testing.T) {
	for _, tc := range []struct{ real, link string }{
		{real: "AGENTS.md", link: "CLAUDE.md"},
		{real: "CLAUDE.md", link: "AGENTS.md"},
	} {
		t.Run(tc.link+" links to "+tc.real, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tc.real, "# Rules\n")
			if err := os.Symlink(tc.real, filepath.Join(root, tc.link)); err != nil {
				t.Fatal(err)
			}
			isLink := func(when string) {
				t.Helper()
				if info, err := os.Lstat(filepath.Join(root, tc.link)); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("%s is no longer a symlink %s", tc.link, when)
				}
			}

			install(t, root, Claude, Codex)
			if got := strings.Count(read(t, root, tc.real), sectionTitle); got != 1 {
				t.Errorf("%s has %d ddslop sections, want 1", tc.real, got)
			}
			isLink("after install")

			if _, err := Uninstall(root, false); err != nil {
				t.Fatal(err)
			}
			if got := read(t, root, tc.real); got != "# Rules\n" {
				t.Errorf("%s after uninstall = %q, want it as before", tc.real, got)
			}
			isLink("after uninstall")
		})
	}
}

// The change report names the file actually edited, so nobody is told
// AGENTS.md changed when the section went into CLAUDE.md.
func TestInstallReportsTheRealFileBehindASymlink(t *testing.T) {
	root := t.TempDir()
	write(t, root, "CLAUDE.md", "# Rules\n")
	if err := os.Symlink("CLAUDE.md", filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}

	changes := strings.Join(install(t, root, Codex), "\n")

	want := "updated " + filepath.Join(root, "CLAUDE.md") + " (through the AGENTS.md symlink)"
	if !strings.Contains(changes, want) {
		t.Errorf("changes:\n%s\nwant a line %q", changes, want)
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
	if exists(filepath.Join(root, indexDir)) {
		t.Error("purge left .ddslop/")
	}
}

// A section edited by hand is the project's own rules: init keeps it while
// it updates the skill, uninstall keeps it while it removes the skill, and
// each says so, every time it runs.
func TestASectionEditedByHandIsKept(t *testing.T) {
	own := "# Rules\n\n## ddslop\n\nRun our gate, not --diff HEAD.\n\n## Testing\n\nRun tests.\n"
	for _, tc := range []struct {
		name  string
		run   func(t *testing.T, root string) []string
		skill bool // whether the skill is installed afterwards
	}{
		{"init", func(t *testing.T, root string) []string { return install(t, root, Codex) }, true},
		{"uninstall", func(t *testing.T, root string) []string {
			install(t, root, Codex)
			changes, err := Uninstall(root, false)
			if err != nil {
				t.Fatal(err)
			}
			return changes
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "AGENTS.md", own)
			for range 2 {
				changes := tc.run(t, root)
				if got := read(t, root, "AGENTS.md"); got != own {
					t.Errorf("AGENTS.md = %q, want it as edited", got)
				}
				if got := exists(filepath.Join(root, ".codex/skills/ddslop/SKILL.md")); got != tc.skill {
					t.Errorf("skill installed = %v, want %v", got, tc.skill)
				}
				if !slices.ContainsFunc(changes, func(c string) bool { return strings.Contains(c, "kept the ddslop section") }) {
					t.Errorf("changes = %v, want a line saying the edited section was kept", changes)
				}
			}
		})
	}
}
