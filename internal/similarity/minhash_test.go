package similarity

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// randomSeq returns a reproducible sequence of n symbols.
func randomSeq(seed uint64, n int) []uint32 {
	rng := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // seeded on purpose: reproducible test data
	out := make([]uint32, n)
	for i := range out {
		out[i] = rng.Uint32N(50)
	}
	return out
}

// edit changes every step-th symbol of a copy of s.
func edit(s []uint32, step int) []uint32 {
	out := slices.Clone(s)
	for i := step / 2; i < len(out); i += step {
		out[i] = 1000 + uint32(i) //nolint:gosec // small test indices
	}
	return out
}

func TestSignatureIsDeterministicAndSized(t *testing.T) {
	s := randomSeq(1, 300)
	a, b := Signature(s), Signature(slices.Clone(s))
	if len(a) != SignatureSize {
		t.Fatalf("signature has %d values, want %d", len(a), SignatureSize)
	}
	if !slices.Equal(a, b) {
		t.Error("equal sequences have different signatures")
	}
}

func TestShortSequencesStillGetSignatures(t *testing.T) {
	short := []uint32{7, 8, 9}
	if sig := Signature(short); len(sig) != SignatureSize {
		t.Fatalf("short sequence signature has %d values", len(sig))
	}
	if got := Candidates([][]uint64{Signature(short), Signature(slices.Clone(short))}); len(got) != 1 {
		t.Errorf("equal short sequences are not candidates: %v", got)
	}
}

func TestCandidatesPairSimilarAndSkipUnrelated(t *testing.T) {
	// Changing 4 or 5 of 400 symbols keeps a shingle Jaccard similarity of
	// about 0.82-0.85, which 16 bands of 8 rows catch about 98% of the time.
	base := randomSeq(2, 400)
	sigs := [][]uint64{
		Signature(base),              // 0
		Signature(randomSeq(3, 400)), // 1: unrelated
		Signature(edit(base, 100)),   // 2: 4 symbols changed
		Signature(randomSeq(4, 400)), // 3: unrelated
		Signature(edit(base, 80)),    // 4: 5 symbols changed
	}

	got := Candidates(sigs)

	for _, want := range [][2]int{{0, 2}, {0, 4}} {
		if !slices.Contains(got, want) {
			t.Errorf("candidates %v miss the similar pair %v", got, want)
		}
	}
	for _, pair := range got {
		if pair[0] == 1 || pair[0] == 3 || pair[1] == 1 || pair[1] == 3 {
			t.Errorf("candidates %v include an unrelated sequence", got)
		}
	}
}

func TestCandidatesSortedWithoutDuplicates(t *testing.T) {
	s := randomSeq(5, 200)
	sig := Signature(s)
	// Identical signatures share all 16 bands but must be paired once.
	got := Candidates([][]uint64{sig, sig, sig})
	if fmt.Sprint(got) != "[[0 1] [0 2] [1 2]]" {
		t.Errorf("candidates = %v, want each pair once, sorted", got)
	}
}
