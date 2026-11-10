package similarity

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func randomInts(rng *rand.Rand, maxLen, alphabet int) []int {
	out := make([]int, rng.IntN(maxLen+1))
	for i := range out {
		out[i] = rng.IntN(alphabet)
	}
	return out
}

func TestSuffixArrayAndLCPMatchNaive(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8)) //nolint:gosec // seeded on purpose: reproducible test data
	for range 3000 {
		seq := randomInts(rng, 30, 3)

		want := make([]int, len(seq))
		for i := range want {
			want[i] = i
		}
		slices.SortFunc(want, func(a, b int) int { return slices.Compare(seq[a:], seq[b:]) })

		sa := SuffixArray(seq)
		if !slices.Equal(sa, want) {
			t.Fatalf("SuffixArray(%v) = %v, want %v", seq, sa, want)
		}
		lcp := LCP(seq, sa)
		for i := 1; i < len(sa); i++ {
			n := 0
			for sa[i-1]+n < len(seq) && sa[i]+n < len(seq) && seq[sa[i-1]+n] == seq[sa[i]+n] {
				n++
			}
			if lcp[i] != n {
				t.Fatalf("LCP(%v)[%d] = %d, want %d", seq, i, lcp[i], n)
			}
		}
	}
}

// naiveRepeats enumerates every run occurring twice or more and keeps the
// ones no single symbol extends on the right or on the left in all
// occurrences.
func naiveRepeats(seq []int) []string {
	occurrences := map[string][]int{}
	for i := range seq {
		for j := i + 1; j <= len(seq); j++ {
			key := fmt.Sprint(seq[i:j])
			occurrences[key] = append(occurrences[key], i)
		}
	}
	var out []string
	for key, pos := range occurrences {
		if len(pos) < 2 {
			continue
		}
		length := 0
		for j := pos[0]; j <= len(seq); j++ {
			if fmt.Sprint(seq[pos[0]:j]) == key {
				length = j - pos[0]
				break
			}
		}
		extendable := func(at func(p int) (int, bool)) bool {
			first, ok := at(pos[0])
			if !ok {
				return false
			}
			for _, p := range pos[1:] {
				if s, ok := at(p); !ok || s != first {
					return false
				}
			}
			return true
		}
		right := extendable(func(p int) (int, bool) {
			if p+length < len(seq) {
				return seq[p+length], true
			}
			return 0, false
		})
		left := extendable(func(p int) (int, bool) {
			if p > 0 {
				return seq[p-1], true
			}
			return 0, false
		})
		if !right && !left {
			out = append(out, fmt.Sprintf("%d@%v", length, pos))
		}
	}
	slices.Sort(out)
	return out
}

func TestRepeatsMatchNaive(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10)) //nolint:gosec // seeded on purpose: reproducible test data
	for range 2000 {
		seq := randomInts(rng, 14, 3)
		var got []string
		for _, r := range Repeats(seq) {
			got = append(got, fmt.Sprintf("%d@%v", r.Length, r.Positions))
		}
		slices.Sort(got)
		if want := naiveRepeats(seq); !slices.Equal(got, want) {
			t.Fatalf("Repeats(%v) = %v, want %v", seq, got, want)
		}
	}
}

func TestRepeatsNeverCrossUniqueSeparators(t *testing.T) {
	// Blocks [1 2 3] and [1 2 3] separated by unique negative symbols.
	seq := []int{1, 2, 3, -1, 1, 2, 3, -2}
	got := Repeats(seq)
	if len(got) != 1 || got[0].Length != 3 || fmt.Sprint(got[0].Positions) != "[0 4]" {
		t.Errorf("Repeats = %+v, want one repeat of length 3 at [0 4]", got)
	}
}
