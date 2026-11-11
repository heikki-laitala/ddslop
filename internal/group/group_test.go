package group

import (
	"fmt"
	"testing"
)

type item struct {
	file string
	line int
	key  string
}

func (i item) At() (string, int) { return i.file, i.line }

func TestByGroupsByKeyAndSortsMembersByLocation(t *testing.T) {
	items := []item{
		{"b.rs", 3, "x"}, {"a.rs", 9, "x"}, {"a.rs", 2, "x"},
		{"c.rs", 1, "y"},
		{"d.rs", 1, "skip"},
	}
	got := By(items, func(i item) (string, bool) { return i.key, i.key != "skip" })
	if fmt.Sprint(got["x"]) != "[{a.rs 2 x} {a.rs 9 x} {b.rs 3 x}]" || len(got["y"]) != 1 || got["skip"] != nil {
		t.Errorf("By = %v", got)
	}
}

func TestCompareOrdersByFileThenLine(t *testing.T) {
	a, b, c := item{"a.rs", 9, ""}, item{"b.rs", 1, ""}, item{"a.rs", 2, ""}
	if Compare(a, b) >= 0 || Compare(c, a) >= 0 || Compare(a, a) != 0 {
		t.Error("Compare does not order by file, then line")
	}
}

func TestCollectBuildsAResultFromEachAcceptedGroup(t *testing.T) {
	items := []item{{"a.rs", 1, "x"}, {"b.rs", 1, "x"}, {"c.rs", 1, "y"}}
	got := Collect(items, func(i item) (string, bool) { return i.key, true }, func(k string, members []item) (string, bool) {
		return fmt.Sprintf("%s:%d", k, len(members)), len(members) >= 2
	})
	if fmt.Sprint(got) != "[x:2]" {
		t.Errorf("Collect = %v, want [x:2]", got)
	}
}
