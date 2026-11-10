// Package similarity measures how alike two symbol sequences are.
package similarity

import (
	"math"
	"slices"
)

// Match pairs a symbol of sequence a with an equal symbol of sequence b.
type Match struct{ A, B int }

// Align finds a longest common subsequence of a and b with Myers' diff
// algorithm, provided their similarity 2·matches/(len(a)+len(b)) can reach
// minSim. The edit distance is bounded by that threshold, so dissimilar
// pairs stop early. Matches are in increasing order in both sequences.
func Align(a, b []uint32, minSim float64) (matches []Match, sim float64, ok bool) {
	n, m := len(a), len(b)
	total := n + m
	if total == 0 {
		return nil, 1, true
	}
	maxD := min(int(math.Floor((1-minSim)*float64(total)+1e-9)), total)
	if maxD < 0 {
		return nil, 0, false
	}

	// v[off+k] is the furthest x reached on diagonal k = x - y.
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	var trace [][]int // v before each step d
	for d := 0; d <= maxD; d++ {
		trace = append(trace, slices.Clone(v))
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
				matches = backtrack(trace, off, n, m, d)
				return matches, 2 * float64(len(matches)) / float64(total), true
			}
		}
	}
	return nil, 0, false
}

// backtrack walks the saved frontiers from (n, m) back to (0, 0) and
// collects the diagonal moves, which are the matches.
func backtrack(trace [][]int, off, n, m, d int) []Match {
	var out []Match
	x, y := n, m
	for ; d > 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[off+prevK]
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
