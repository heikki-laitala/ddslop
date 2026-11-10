package clone

import (
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Difference is one set of values that differ between the units of a group
// at the same positions. Each non-local difference is a likely parameter of
// a shared function.
type Difference struct {
	Kind unit.LeafKind
	// Values holds one value per unit, in group order.
	Values []string
	// Lines holds, per unit, the lines where the value occurs.
	Lines [][]int
	// Local is true when every unit declares its own value itself, so the
	// difference is only a local naming choice and needs no parameter.
	Local bool
}

// Differences returns the differences between the units of a group, in
// order of first appearance. Units in a group have equal shapes, so their
// leaves line up one to one.
func Differences(g Group) []Difference {
	rows := make([][]int, len(g.Units[0].Leaves))
	for pos := range rows {
		rows[pos] = make([]int, len(g.Units))
		for i := range g.Units {
			rows[pos][i] = pos
		}
	}
	return differences(g.Units, rows)
}

// differences compares aligned leaves. Each row holds, per unit, the index
// of a leaf; the leaves of one row are at corresponding positions.
func differences(units []unit.Unit, rows [][]int) []Difference {
	declared := make([]map[string]bool, len(units))
	for i, u := range units {
		declared[i] = map[string]bool{}
		for _, l := range u.Leaves {
			if l.Declares {
				declared[i][l.Text] = true
			}
		}
	}

	var diffs []Difference
	index := map[string]int{} // values key -> position in diffs
	for _, row := range rows {
		values := make([]string, len(units))
		same := true
		for i, u := range units {
			values[i] = u.Leaves[row[i]].Text
			same = same && values[i] == values[0]
		}
		if same {
			continue
		}
		key := strings.Join(values, "\x00")
		di, ok := index[key]
		if !ok {
			kind := units[0].Leaves[row[0]].Kind
			di = len(diffs)
			index[key] = di
			diffs = append(diffs, Difference{
				Kind:   kind,
				Values: values,
				Lines:  make([][]int, len(units)),
				Local:  isLocal(kind, values, declared),
			})
		}
		for i, u := range units {
			diffs[di].Lines[i] = appendLine(diffs[di].Lines[i], u.Leaves[row[i]].Line)
		}
	}
	return diffs
}

// isLocal reports whether every unit declares its own value.
func isLocal(kind unit.LeafKind, values []string, declared []map[string]bool) bool {
	if kind != unit.Identifier {
		return false
	}
	for i, v := range values {
		if !declared[i][v] {
			return false
		}
	}
	return true
}

// appendLine appends a line unless it repeats the last one.
func appendLine(lines []int, line int) []int {
	if n := len(lines); n > 0 && lines[n-1] == line {
		return lines
	}
	return append(lines, line)
}
