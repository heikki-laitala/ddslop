package similarity

import (
	"math/rand/v2"
	"testing"
)

func seq(s string) []uint32 {
	out := make([]uint32, len(s))
	for i, c := range s {
		out[i] = uint32(c)
	}
	return out
}

// checkMatches verifies that matches pair equal symbols and are strictly
// increasing in both sequences.
func checkMatches(t *testing.T, a, b []uint32, matches []Match) {
	t.Helper()
	for i, m := range matches {
		if m.A < 0 || m.A >= len(a) || m.B < 0 || m.B >= len(b) || a[m.A] != b[m.B] {
			t.Fatalf("match %d = %+v pairs unequal or out-of-range symbols", i, m)
		}
		if i > 0 && (m.A <= matches[i-1].A || m.B <= matches[i-1].B) {
			t.Fatalf("matches not strictly increasing at %d: %+v after %+v", i, m, matches[i-1])
		}
	}
}

func TestAlignIdentical(t *testing.T) {
	a := seq("abcdef")
	matches, sim, ok := Align(a, a, 0.9)
	if !ok || sim != 1 || len(matches) != 6 {
		t.Errorf("Align(identical) = %d matches, sim %v, ok %v; want 6, 1, true", len(matches), sim, ok)
	}
	checkMatches(t, a, a, matches)
}

func TestAlignInsertion(t *testing.T) {
	a, b := seq("abcdefgh"), seq("abcXYdefgh")
	matches, sim, ok := Align(a, b, 0.8)
	if !ok || len(matches) != 8 {
		t.Fatalf("Align = %d matches, ok %v; want 8, true", len(matches), ok)
	}
	if want := 16.0 / 18.0; sim != want {
		t.Errorf("sim = %v, want %v", sim, want)
	}
	checkMatches(t, a, b, matches)
	if matches[3] != (Match{3, 5}) {
		t.Errorf("match after the insertion = %+v, want {3 5}", matches[3])
	}
}

func TestAlignBelowThresholdIsRejected(t *testing.T) {
	if _, _, ok := Align(seq("abcdefgh"), seq("stuvwxyz"), 0.5); ok {
		t.Error("unrelated sequences were accepted")
	}
	// 9 of 10 match each way: sim 0.9 passes 0.8 but not 0.95.
	if _, _, ok := Align(seq("abcdeXfghi"), seq("abcdeYfghi"), 0.8); !ok {
		t.Error("similarity exactly at the threshold was rejected")
	}
	if _, _, ok := Align(seq("abcdeXfghi"), seq("abcdeYfghi"), 0.95); ok {
		t.Error("similarity 0.9 passed a 0.95 threshold")
	}
}

func TestAlignEmpty(t *testing.T) {
	if _, sim, ok := Align(nil, nil, 0.9); !ok || sim != 1 {
		t.Errorf("two empty sequences: sim %v, ok %v; want 1, true", sim, ok)
	}
	if _, _, ok := Align(seq("abc"), nil, 0.1); ok {
		t.Error("empty against non-empty was accepted")
	}
}

// lcsLength is the textbook dynamic-programming LCS, used as an oracle.
func lcsLength(a, b []uint32) int {
	prev := make([]int, len(b)+1)
	for i := range a {
		cur := make([]int, len(b)+1)
		for j := range b {
			if a[i] == b[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(prev[j+1], cur[j])
			}
		}
		prev = cur
	}
	return prev[len(b)]
}

func TestAlignMatchesDynamicProgrammingOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // seeded on purpose: reproducible test data
	for range 2000 {
		a := make([]uint32, rng.IntN(25))
		b := make([]uint32, rng.IntN(25))
		for i := range a {
			a[i] = rng.Uint32N(4)
		}
		for i := range b {
			b[i] = rng.Uint32N(4)
		}
		matches, _, ok := Align(a, b, 0) // no threshold: always aligns fully
		if !ok {
			t.Fatalf("Align(%v, %v, 0) not ok", a, b)
		}
		checkMatches(t, a, b, matches)
		if want := lcsLength(a, b); len(matches) != want {
			t.Fatalf("Align(%v, %v) found %d matches, LCS is %d", a, b, len(matches), want)
		}
	}
}
