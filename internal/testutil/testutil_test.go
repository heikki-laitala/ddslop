package testutil

import (
	"fmt"
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
