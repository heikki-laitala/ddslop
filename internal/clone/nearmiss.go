package clone

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// NearMiss is a pair of units that are similar but not the same shape:
// statements were added, removed or changed.
type NearMiss struct {
	Units      [2]unit.Unit
	Similarity float64
	// MatchedNodes is how many syntax nodes the two units share.
	MatchedNodes int
	// Differences are values that differ at positions the alignment matched.
	Differences []Difference
	// Unmatched holds, per unit, line ranges containing code that has no
	// counterpart in the other unit.
	Unmatched [2][]LineRange
}

// LineRange is an inclusive range of lines.
type LineRange struct{ Start, End int }

// NearMisses finds pairs of units with different shapes whose similarity is
// at least minSim, ignoring units smaller than minNodes. Each shape is
// represented by its first unit, so a group of exact copies yields one pair,
// not one per copy. Pairs are ordered by shared size, largest first, then by
// location.
func NearMisses(units []unit.Unit, minNodes int, minSim float64) []NearMiss {
	reps := representatives(units, minNodes)
	sigs := make([][]uint64, len(reps))
	for i, u := range reps {
		sigs[i] = u.Signature
		if len(sigs[i]) == 0 {
			sigs[i] = similarity.Signature(u.Seq)
		}
	}

	var out []NearMiss
	for _, p := range similarity.Candidates(sigs) {
		a, b := reps[p[0]], reps[p[1]]
		matches, sim, ok := similarity.Align(a.Seq, b.Seq, minSim)
		if !ok {
			continue
		}
		out = append(out, nearMiss(a, b, matches, sim))
	}
	slices.SortFunc(out, func(x, y NearMiss) int {
		if c := cmp.Compare(y.MatchedNodes, x.MatchedNodes); c != 0 {
			return c
		}
		if c := compareLocation(x.Units[0], y.Units[0]); c != 0 {
			return c
		}
		return compareLocation(x.Units[1], y.Units[1])
	})
	return out
}

// representatives returns the first unit, by location, of every shape with
// at least minNodes nodes, in location order.
func representatives(units []unit.Unit, minNodes int) []unit.Unit {
	sorted := slices.Clone(units)
	slices.SortFunc(sorted, compareLocation)
	seen := map[key]bool{}
	var reps []unit.Unit
	for _, u := range sorted {
		k := key{u.Shape, u.Nodes}
		if u.Nodes < minNodes || seen[k] {
			continue
		}
		seen[k] = true
		reps = append(reps, u)
	}
	return reps
}

func nearMiss(a, b unit.Unit, matches []similarity.Match, sim float64) NearMiss {
	nm := NearMiss{Units: [2]unit.Unit{a, b}, Similarity: sim}

	leafA, leafB := leafIndexes(a.Seq), leafIndexes(b.Seq)
	matchedA := make([]bool, len(a.Seq))
	matchedB := make([]bool, len(b.Seq))
	var rows [][]int
	for _, m := range matches {
		matchedA[m.A], matchedB[m.B] = true, true
		if a.Seq[m.A] != unit.Close {
			nm.MatchedNodes++
		}
		if leafA[m.A] >= 0 {
			rows = append(rows, []int{leafA[m.A], leafB[m.B]})
		}
	}
	nm.Differences = differences([]unit.Unit{a, b}, rows)
	nm.Unmatched = [2][]LineRange{unmatchedLines(a, matchedA), unmatchedLines(b, matchedB)}
	return nm
}

// leafIndexes maps each Seq position to its leaf index, or -1.
func leafIndexes(seq []uint32) []int {
	out := make([]int, len(seq))
	next := 0
	for i, sym := range seq {
		out[i] = -1
		if unit.IsLeaf(sym) {
			out[i] = next
			next++
		}
	}
	return out
}

// unmatchedLines returns the ranges of lines holding unmatched symbols.
// Close symbols are ignored: they carry a node's end line, which says
// nothing about where the differing code is.
func unmatchedLines(u unit.Unit, matched []bool) []LineRange {
	var lines []int
	for i, sym := range u.Seq {
		if !matched[i] && sym != unit.Close {
			lines = append(lines, u.SeqLines[i])
		}
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	var out []LineRange
	for _, l := range lines {
		if n := len(out); n > 0 && out[n-1].End+1 == l {
			out[n-1].End = l
			continue
		}
		out = append(out, LineRange{l, l})
	}
	return out
}
