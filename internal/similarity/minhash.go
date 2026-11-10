package similarity

import (
	"cmp"
	"slices"
)

// LSH parameters: SignatureSize = bands × rows. With 16 bands of 8 rows,
// pairs become candidates from a Jaccard similarity of roughly 0.7.
const (
	SignatureSize = bands * rows
	bands         = 16
	rows          = 8
	shingleSize   = 4
)

// seeds give each of the SignatureSize hash functions its own salt.
var seeds = func() [SignatureSize]uint64 {
	var out [SignatureSize]uint64
	x := uint64(0x9e3779b97f4a7c15)
	for i := range out {
		x += 0x9e3779b97f4a7c15
		out[i] = mix(x)
	}
	return out
}()

// mix is the SplitMix64 finalizer, a fast and well-distributed 64-bit hash.
func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// hashSymbols is FNV-1a over a run of symbols.
func hashSymbols(syms []uint32) uint64 {
	h := uint64(14695981039346656037)
	for _, s := range syms {
		h ^= uint64(s)
		h *= 1099511628211
	}
	return h
}

// Signature returns the MinHash signature of a symbol sequence: for each of
// SignatureSize salted hash functions, the minimum over all shingles (runs
// of shingleSize symbols). A sequence shorter than a shingle is one shingle.
func Signature(seq []uint32) []uint64 {
	sig := make([]uint64, SignatureSize)
	for i := range sig {
		sig[i] = ^uint64(0)
	}
	add := func(h uint64) {
		for i, seed := range seeds {
			if v := mix(h ^ seed); v < sig[i] {
				sig[i] = v
			}
		}
	}
	if len(seq) < shingleSize {
		add(hashSymbols(seq))
		return sig
	}
	for i := 0; i+shingleSize <= len(seq); i++ {
		add(hashSymbols(seq[i : i+shingleSize]))
	}
	return sig
}

// Candidates returns the pairs (i, j), i < j, of signatures that share at
// least one LSH band, sorted. Each band hashes rows consecutive signature
// values; equal band hashes put two signatures in the same bucket.
func Candidates(sigs [][]uint64) [][2]int {
	seen := map[[2]int]bool{}
	for b := range bands {
		buckets := map[uint64][]int{}
		for i, sig := range sigs {
			h := uint64(b)
			for _, v := range sig[b*rows : (b+1)*rows] {
				h = mix(h ^ v)
			}
			buckets[h] = append(buckets[h], i)
		}
		for _, members := range buckets {
			for x := range members {
				for y := x + 1; y < len(members); y++ {
					seen[[2]int{members[x], members[y]}] = true
				}
			}
		}
	}
	pairs := make([][2]int, 0, len(seen))
	for p := range seen {
		pairs = append(pairs, p)
	}
	slices.SortFunc(pairs, func(a, b [2]int) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})
	return pairs
}
