// Package testutil holds assertions the packages' tests share.
package testutil

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Lines fails t when got and want differ, showing both one item per line
// under what.
func Lines(t testing.TB, what string, got, want []string) {
	t.Helper()
	if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
		t.Errorf("%s:\n%s\nwant:\n%s", what, g, w)
	}
}

// Format returns line(item) for each item, in order, leaving out the
// items line returns "" for.
func Format[T any](items []T, line func(T) string) []string {
	var out []string
	for _, item := range items {
		if s := line(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Parse runs a language adapter's parse on src as the file at path,
// failing t on an error.
func Parse(t testing.TB, parse func(string, []byte) ([]unit.Unit, unit.Decls, error), path, src string) ([]unit.Unit, unit.Decls) {
	t.Helper()
	units, decls, err := parse(path, []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return units, decls
}

// Producer names the call among calls producing c's operand, as
// "name#result", or "-" when no call does (with "#result" should a result
// be recorded all the same).
func Producer(calls []unit.Call, c unit.Call) string {
	name := "-"
	if c.From > 0 {
		name = calls[c.From-1].Name
	} else if c.Result == 0 {
		return name
	}
	return fmt.Sprintf("%s#%d", name, c.Result)
}
