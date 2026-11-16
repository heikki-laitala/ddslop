package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// recorder keeps the failure a test would report instead of failing.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
}

func TestLinesFailsOnlyWhenTheLinesDiffer(t *testing.T) {
	r := &recorder{TB: t}
	Lines(r, "items", []string{"a", "b"}, []string{"a", "b"})
	if r.msg != "" {
		t.Fatalf("equal lines failed: %q", r.msg)
	}
	Lines(r, "items", []string{"a"}, []string{"a", "b"})
	if want := "items:\na\nwant:\na\nb"; r.msg != want {
		t.Fatalf("message %q, want %q", r.msg, want)
	}
}

func TestFormatKeepsTheOrderAndLeavesOutEmptyLines(t *testing.T) {
	got := Format([]int{1, 2, 3, 4}, func(n int) string {
		if n%2 == 0 {
			return ""
		}
		return fmt.Sprint(n * 10)
	})
	Lines(t, "lines", got, []string{"10", "30"})
}

func TestParseReturnsWhatTheAdapterParsed(t *testing.T) {
	var gotPath, gotSrc string
	units, _ := Parse(t, func(path string, src []byte) ([]unit.Unit, unit.Decls, error) {
		gotPath, gotSrc = path, string(src)
		return []unit.Unit{{Name: "f"}}, unit.Decls{}, nil
	}, "a/b.go", "package b")
	if gotPath != "a/b.go" || gotSrc != "package b" || len(units) != 1 || units[0].Name != "f" {
		t.Fatalf("parse saw %q %q, returned %v", gotPath, gotSrc, units)
	}
}

func TestProducerNamesTheCallAndResultOrADash(t *testing.T) {
	calls := []unit.Call{{Name: "open"}, {Name: "save", From: 1, Result: 2}}
	if got := Producer(calls, calls[1]); got != "open#2" {
		t.Errorf("produced call = %q, want open#2", got)
	}
	if got := Producer(calls, calls[0]); got != "-" {
		t.Errorf("unproduced call = %q, want -", got)
	}
	if got := Producer(calls, unit.Call{Result: 1}); got != "-#1" {
		t.Errorf("unproduced call with a result = %q, want -#1", got)
	}
}

func TestWriteFilesMakesTheDirectoriesTheFilesNeed(t *testing.T) {
	root := t.TempDir()
	WriteFiles(t, root, map[string]string{"a.txt": "a", "x/y/b.txt": "b"})
	for name, want := range map[string]string{"a.txt": "a", "x/y/b.txt": "b"} {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != want { //nolint:gosec // the test's own file
			t.Errorf("%s holds %q (%v), want %q", name, got, err, want)
		}
	}
}
