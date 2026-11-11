package clone

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// BlockOccurrence is a run of statements in one block of a unit:
// statements [Start, End) of Unit.Blocks[Block].
type BlockOccurrence struct {
	Unit               unit.Unit
	Block, Start, End  int
	StartLine, EndLine int
}

// BlockClone is a run of statements that is repeated, with the same
// structure, inside larger functions.
type BlockClone struct {
	Occurrences []BlockOccurrence
	// Nodes is the size of one occurrence.
	Nodes int
	// Differences between the occurrences. A name is local only when it is
	// declared inside the run itself; anything else becomes a parameter of
	// an extracted helper.
	Differences []Difference
}

// BlockSize says which repeated runs are large enough to report.
type BlockSize struct {
	MinNodes int // least nodes in one occurrence
	// A smaller run is still reported when it has at least FrequentNodes
	// nodes and FrequentLines lines, and is copied often enough to
	// duplicate FrequentDuplicated nodes in all: a guard repeated in twenty
	// methods costs more than a large block copied once. Shorter runs are
	// idioms (open a transaction, look up the user) that a helper call
	// would not shorten.
	FrequentNodes, FrequentLines, FrequentDuplicated int
}

// keeps reports whether a run of nodes and lines with copies occurrences
// is large enough.
func (s BlockSize) keeps(nodes, lines, copies int) bool {
	return nodes >= s.MinNodes ||
		(s.FrequentNodes > 0 && nodes >= s.FrequentNodes && lines >= s.FrequentLines &&
			nodes*(copies-1) >= s.FrequentDuplicated)
}

// least is the smallest run that can be kept.
func (s BlockSize) least() int {
	if s.FrequentNodes > 0 {
		return min(s.MinNodes, s.FrequentNodes)
	}
	return s.MinNodes
}

// stmtRef locates a statement in the concatenated sequence.
type stmtRef struct{ unit, block, stmt int }

// BlockClones finds repeated runs of whole statements large enough by
// size. Every statement becomes one symbol identifying its structure, each
// block a run of symbols ended by a unique separator, and the maximal
// repeats of the concatenation (see similarity.Repeats) are the candidate
// runs. Repeats therefore consist of whole statements and stay inside one
// block. Overlapping occurrences are dropped, and so are runs that add
// nothing: occurrences in distinct functions that are reported together
// (a set in reported, such as a function clone group or near-miss
// family), or lying wholly inside a larger block clone.
func BlockClones(units []unit.Unit, size BlockSize, reported [][]unit.Unit) []BlockClone {
	sorted := slices.Clone(units)
	slices.SortFunc(sorted, compareLocation)

	ids := map[uint64]int{}
	var seq []int
	var refs []stmtRef
	separator := -1
	for ui, u := range sorted {
		for bi, block := range u.Blocks {
			for si, st := range block {
				h := similarity.HashSymbols(u.Seq[st.SeqStart:st.SeqEnd])
				id, ok := ids[h]
				if !ok {
					id = len(ids)
					ids[h] = id
				}
				seq = append(seq, id)
				refs = append(refs, stmtRef{ui, bi, si})
			}
			seq = append(seq, separator)
			refs = append(refs, stmtRef{-1, -1, -1})
			separator--
		}
	}

	var found []BlockClone
	for _, r := range similarity.Repeats(seq) {
		first := refs[r.Positions[0]]
		nodes := 0
		for _, st := range sorted[first.unit].Blocks[first.block][first.stmt : first.stmt+r.Length] {
			nodes += st.Nodes
		}
		if nodes < size.least() {
			continue
		}
		var occs []BlockOccurrence
		lastEnd := map[[2]int]int{} // unit, block -> end of the last kept occurrence
		for _, p := range r.Positions {
			ref := refs[p]
			k := [2]int{ref.unit, ref.block}
			if end, ok := lastEnd[k]; ok && ref.stmt < end {
				continue // overlaps the previous occurrence in this block
			}
			lastEnd[k] = ref.stmt + r.Length
			u := sorted[ref.unit]
			block := u.Blocks[ref.block]
			occs = append(occs, BlockOccurrence{
				Unit: u, Block: ref.block, Start: ref.stmt, End: ref.stmt + r.Length,
				StartLine: block[ref.stmt].StartLine, EndLine: block[ref.stmt+r.Length-1].EndLine,
			})
		}
		if len(occs) >= 2 && size.keeps(nodes, occs[0].EndLine-occs[0].StartLine+1, len(occs)) {
			found = append(found, BlockClone{Occurrences: occs, Nodes: nodes})
		}
	}
	slices.SortFunc(found, func(a, b BlockClone) int {
		if c := cmp.Compare(b.Nodes*(len(b.Occurrences)-1), a.Nodes*(len(a.Occurrences)-1)); c != 0 {
			return c
		}
		return compareLocation(a.Occurrences[0].Unit, b.Occurrences[0].Unit)
	})

	// A clone adds nothing when its occurrences are in different functions
	// already reported together, or all lie inside block clones kept
	// earlier (larger ones come first).
	setOf := map[unitKey]int{}
	for i, set := range reported {
		for _, u := range set {
			setOf[unitKey{u.File, u.StartLine}] = i
		}
	}
	cov := coverage{}
	var out []BlockClone
	for _, c := range found {
		if reportedTogether(c.Occurrences, setOf) || cov.covers(c.Occurrences) {
			continue
		}
		for _, o := range c.Occurrences {
			start, end := o.seqRange()
			cov.add(o.Unit, start, end)
		}
		c.Differences = runDifferences(c.Occurrences)
		out = append(out, c)
	}
	return out
}

// reportedTogether reports whether the occurrences are in distinct units
// that all belong to one reported set.
func reportedTogether(occs []BlockOccurrence, setOf map[unitKey]int) bool {
	seen := map[unitKey]bool{}
	set := -1
	for _, o := range occs {
		k := unitKey{o.Unit.File, o.Unit.StartLine}
		s, ok := setOf[k]
		if !ok || seen[k] || (set >= 0 && s != set) {
			return false
		}
		seen[k] = true
		set = s
	}
	return true
}

// seqRange returns the occurrence's symbols as a range of its unit's Seq.
func (o BlockOccurrence) seqRange() (start, end int) {
	block := o.Unit.Blocks[o.Block]
	return block[o.Start].SeqStart, block[o.End-1].SeqEnd
}

// coverage records reported ranges of units' sequences, per unit.
type coverage map[unitKey][][2]int

// unitKey identifies a unit by where it starts.
type unitKey struct {
	file string
	line int
}

func (c coverage) add(u unit.Unit, start, end int) {
	k := unitKey{u.File, u.StartLine}
	c[k] = append(c[k], [2]int{start, end})
}

// covers reports whether every occurrence lies inside a recorded range.
func (c coverage) covers(occs []BlockOccurrence) bool {
	for _, o := range occs {
		start, end := o.seqRange()
		inside := false
		for _, r := range c[unitKey{o.Unit.File, o.Unit.StartLine}] {
			if r[0] <= start && end <= r[1] {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	return true
}

// runDifferences compares the leaves of the runs. Each run is treated as a
// unit of its own, so only names declared inside the run count as local.
func runDifferences(occs []BlockOccurrence) []Difference {
	runs := make([]unit.Unit, len(occs))
	for i, o := range occs {
		block := o.Unit.Blocks[o.Block]
		runs[i] = unit.Unit{Leaves: o.Unit.Leaves[block[o.Start].LeafStart:block[o.End-1].LeafEnd]}
	}
	rows := make([][]int, len(runs[0].Leaves))
	for l := range rows {
		rows[l] = make([]int, len(runs))
		for i := range runs {
			rows[l][i] = l
		}
	}
	return differences(runs, rows)
}
