package clone

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Similar is a unit found like a target, with its similarity to it.
type Similar struct {
	Unit       unit.Unit
	Similarity float64
}

// SimilarTo returns the units at least minSim similar to target, most
// similar first, the target itself left out. Similarity is the share of
// the two structure sequences that line up (see similarity.Similar), so a
// renamed copy is 1.
func SimilarTo(target unit.Unit, units []unit.Unit, minSim float64) []Similar {
	var out []Similar
	for _, u := range units {
		if u.File == target.File && u.StartLine == target.StartLine {
			continue
		}
		if sim, ok := similarity.Similar(target.Seq, u.Seq, minSim); ok {
			out = append(out, Similar{Unit: u, Similarity: sim})
		}
	}
	slices.SortStableFunc(out, func(a, b Similar) int {
		if c := cmp.Compare(b.Similarity, a.Similarity); c != 0 {
			return c
		}
		return compareLocation(a.Unit, b.Unit)
	})
	return out
}
