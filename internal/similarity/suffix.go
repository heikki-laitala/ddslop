package similarity

import (
	"cmp"
	"slices"
)

// SuffixArray returns the start positions of seq's suffixes in sorted order,
// built by prefix doubling: suffixes are sorted by their first k symbols,
// then 2k, until every rank is distinct.
func SuffixArray(seq []int) []int {
	n := len(seq)
	sa := make([]int, n)
	rank := make([]int, n)
	for i := range n {
		sa[i] = i
		rank[i] = seq[i]
	}
	next := make([]int, n)
	for k := 1; ; k *= 2 {
		second := func(i int) int {
			if i+k < n {
				return rank[i+k]
			}
			return minInt
		}
		compare := func(a, b int) int {
			if c := cmp.Compare(rank[a], rank[b]); c != 0 {
				return c
			}
			return cmp.Compare(second(a), second(b))
		}
		slices.SortFunc(sa, compare)
		distinct := 0
		for i := range n {
			if i > 0 && compare(sa[i-1], sa[i]) != 0 {
				distinct++
			}
			next[sa[i]] = distinct
		}
		copy(rank, next)
		if distinct == n-1 || k >= n {
			return sa
		}
	}
}

// minInt sorts before every symbol, so a suffix ending first sorts first.
const minInt = -int(^uint(0)>>1) - 1

// LCP returns, for each i > 0, the length of the longest common prefix of
// the suffixes at sa[i-1] and sa[i]; lcp[0] is 0. Kasai's algorithm.
func LCP(seq, sa []int) []int {
	n := len(seq)
	lcp := make([]int, n)
	rank := make([]int, n)
	for i, p := range sa {
		rank[p] = i
	}
	h := 0
	for p := range n {
		if rank[p] == 0 {
			h = 0
			continue
		}
		q := sa[rank[p]-1]
		for p+h < n && q+h < n && seq[p+h] == seq[q+h] {
			h++
		}
		lcp[rank[p]] = h
		if h > 0 {
			h--
		}
	}
	return lcp
}

// Repeat is a maximal repeated run: Length symbols starting at each of
// Positions (sorted).
type Repeat struct {
	Length    int
	Positions []int
}

// Repeats returns the maximal repeats of seq: runs occurring at least twice
// that cannot be extended to the left or right in all their occurrences.
// Each lcp-interval of the suffix array is a right-maximal repeat; it is
// reported when its occurrences are also left-maximal. Unique separator
// symbols between blocks keep repeats inside blocks.
func Repeats(seq []int) []Repeat {
	sa := SuffixArray(seq)
	lcp := LCP(seq, sa)

	type interval struct{ lcp, lb int }
	var out []Repeat
	report := func(length, lb, rb int) {
		if length == 0 {
			return
		}
		positions := slices.Clone(sa[lb : rb+1])
		slices.Sort(positions)
		if !leftMaximal(seq, positions) {
			return
		}
		out = append(out, Repeat{Length: length, Positions: positions})
	}
	stack := []interval{{0, 0}}
	for i := 1; i <= len(sa); i++ {
		cur := 0
		if i < len(sa) {
			cur = lcp[i]
		}
		lb := i - 1
		for cur < stack[len(stack)-1].lcp {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			report(top.lcp, top.lb, i-1)
			lb = top.lb
		}
		if cur > stack[len(stack)-1].lcp {
			stack = append(stack, interval{cur, lb})
		}
	}
	slices.SortFunc(out, func(a, b Repeat) int {
		if c := cmp.Compare(a.Positions[0], b.Positions[0]); c != 0 {
			return c
		}
		return cmp.Compare(a.Length, b.Length)
	})
	return out
}

// leftMaximal reports whether the occurrences cannot all be extended by the
// same symbol on the left.
func leftMaximal(seq, positions []int) bool {
	if positions[0] == 0 {
		return true
	}
	first := seq[positions[0]-1]
	for _, p := range positions[1:] {
		if p == 0 || seq[p-1] != first {
			return true
		}
	}
	return false
}
