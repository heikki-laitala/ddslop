// Package group collects items that share a key, the first step of every
// detector that finds copies among declarations.
package group

import (
	"cmp"
	"slices"
)

// Located is anything with a place in the source.
type Located interface {
	At() (file string, line int)
}

// By groups items by key, leaving out items whose key reports false, and
// sorts each group's members by location (see Compare).
func By[T Located, K comparable](items []T, key func(T) (K, bool)) map[K][]T {
	out := map[K][]T{}
	for _, it := range items {
		if k, ok := key(it); ok {
			out[k] = append(out[k], it)
		}
	}
	for _, members := range out {
		slices.SortFunc(members, Compare[T])
	}
	return out
}

// Compare orders by file, then line.
func Compare[T Located](a, b T) int {
	fa, la := a.At()
	fb, lb := b.At()
	return cmp.Or(cmp.Compare(fa, fb), cmp.Compare(la, lb))
}

// Collect groups items by key (see By) and builds a result from each
// group, keeping the results build accepts. The results are in no
// particular order; callers sort them.
func Collect[T Located, K comparable, G any](items []T, key func(T) (K, bool), build func(K, []T) (G, bool)) []G {
	var out []G
	for k, members := range By(items, key) {
		if g, ok := build(k, members); ok {
			out = append(out, g)
		}
	}
	return out
}
