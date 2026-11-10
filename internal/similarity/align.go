// Package similarity measures how alike two symbol sequences are.
package similarity

import (
	"math"
	"slices"
)

// Match pairs a symbol of sequence a with an equal symbol of sequence b.
type Match struct{ A, B int }

// Similar returns the similarity 2·LCS/(len(a)+len(b)) of a and b, if it
// reaches minSim, without building the alignment.
func Similar(a, b []uint32, minSim float64) (float64, bool) {
	total := len(a) + len(b)
	if total == 0 {
		return 1, true
	}
	if !lengthsAllow(len(a), len(b), minSim) {
		return 0, false
	}
	d, ok := distance(a, b, maxDistance(total, minSim), nil)
	if !ok {
		return 0, false
	}
	return float64(total-d) / float64(total), true
}

// Align finds a longest common subsequence of a and b with Myers' diff
// algorithm, provided their similarity 2·matches/(len(a)+len(b)) can reach
// minSim. The edit distance is bounded by that threshold, so dissimilar
// pairs stop early, and the frontiers needed to recover the matches are
// only recorded once the pair is known to pass. Matches are in increasing
// order in both sequences.
func Align(a, b []uint32, minSim float64) (matches []Match, sim float64, ok bool) {
	total := len(a) + len(b)
	if total == 0 {
		return nil, 1, true
	}
	d, ok := distance(a, b, maxDistance(total, minSim), nil)
	if !ok {
		return nil, 0, false
	}
	t := &trace{}
	_, _ = distance(a, b, d, t)
	matches = t.backtrack(len(a), len(b), d)
	return matches, 2 * float64(len(matches)) / float64(total), true
}

// maxDistance is the largest edit distance (insertions plus deletions) that
// keeps the similarity at or above minSim.
func maxDistance(total int, minSim float64) int {
	return min(int(math.Floor((1-minSim)*float64(total)+1e-9)), total)
}

// lengthsAllow reports whether sequences of lengths n and m can reach
// minSim at all: their similarity is at most 2·min(n, m)/(n + m).
func lengthsAllow(n, m int, minSim float64) bool {
	return 2*float64(min(n, m))/float64(n+m) >= minSim-1e-9
}

// trace records, for each step d, the frontier before that step on the
// diagonals -d..d, all in one buffer.
type trace struct {
	buf []int
}

// window returns the recorded frontier of step d; index k+d is diagonal k.
func (t *trace) window(d int) []int {
	start := d * d // steps 0..d-1 used 1, 3, 5, ... entries
	return t.buf[start : start+2*d+1]
}

// distance runs Myers' algorithm and returns the edit distance of a and b
// if it is at most maxD. With t set, it records the frontiers for
// backtracking.
func distance(a, b []uint32, maxD int, t *trace) (int, bool) {
	if maxD < 0 {
		return 0, false
	}
	n, m := len(a), len(b)
	// v[off+k] is the furthest x reached on diagonal k = x - y.
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	if t != nil {
		t.buf = make([]int, 0, (maxD+1)*(maxD+1))
	}
	for d := 0; d <= maxD; d++ {
		if t != nil {
			t.buf = append(t.buf, v[off-d:off+d+1]...)
		}
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1] // step down: insertion from b
			} else {
				x = v[off+k-1] + 1 // step right: deletion from a
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return d, true
			}
		}
	}
	return 0, false
}

// backtrack walks the recorded frontiers from (n, m) back to (0, 0) and
// collects the diagonal moves, which are the matches.
func (t *trace) backtrack(n, m, d int) []Match {
	var out []Match
	x, y := n, m
	for ; d > 0; d-- {
		v := t.window(d)
		k := x - y
		var prevK int
		if k == -d || (k != d && v[k-1+d] < v[k+1+d]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[prevK+d]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			out = append(out, Match{x, y})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x--
		y--
		out = append(out, Match{x, y})
	}
	slices.Reverse(out)
	return out
}
