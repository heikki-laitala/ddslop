package clone

import (
	"cmp"
	"hash/fnv"
	"slices"
	"sync"

	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// NearMiss is a family of units that are similar but not the same shape:
// statements were added, removed or changed. Every member is a near miss
// of the anchor.
type NearMiss struct {
	// Units are the anchor first, then the members, each a near miss of it.
	Units []unit.Unit
	// Similarity holds, per unit, its similarity to the anchor.
	Similarity []float64
	// MatchedNodes is how many syntax nodes the members share with the
	// anchor, summed over members.
	MatchedNodes int
	// Agreement is the lowest share, over members, of aligned leaves whose
	// values agree with the anchor's: equal, or a name each declares
	// itself. Copies agree on most values; unrelated code with a similar
	// structure does not.
	Agreement float64
	// Differences are values that differ at anchor positions aligned in
	// every member, one value per unit.
	Differences []Difference
	// Unmatched holds, per unit, line ranges of code without a counterpart:
	// for a member, code the anchor lacks; for the anchor, code missing from
	// at least one member.
	Unmatched [][]LineRange
}

// minAgreement is the least value agreement a near miss needs. Measured on
// a 735k-line Rust codebase: pairs from 0.7 up were copies (often across
// services), pairs below 0.4 were unrelated code sharing common Rust
// structure (match tables, Default and Display impls), and pairs in between
// were families differing in dozens of messages.
const minAgreement = 0.7

// LineRange is an inclusive range of lines.
type LineRange struct{ Start, End int }

// pair is a verified near miss between representatives a < b.
type pair struct {
	a, b    int
	matches []similarity.Match
	sim     float64
}

// NearMisses finds families of units with different shapes that are near
// misses of each other: similarity at least minSim and values agreeing at
// least minAgreement, ignoring units smaller than minNodes. Each shape is
// represented by its first unit, so a group of exact copies takes part
// once. Candidate pairs are verified on threads goroutines; the result
// does not depend on the thread count. Families are ordered by shared
// size, largest first, then by location.
func NearMisses(units []unit.Unit, minNodes int, minSim float64, threads int) []NearMiss {
	reps := representatives(units, minNodes)
	sigs := make([][]uint64, len(reps))
	for i, u := range reps {
		sigs[i] = u.Signature
		if len(sigs[i]) == 0 {
			sigs[i] = similarity.Signature(u.Seq)
		}
	}

	candidates := similarity.Candidates(sigs)
	verified := make([]*pair, len(candidates))
	weights := make([]int, len(candidates))
	parallel(len(candidates), threads, func(i int) {
		c := candidates[i]
		a, b := reps[c[0]], reps[c[1]]
		matches, sim, ok := alignUnits(a, b, minSim)
		if !ok {
			return
		}
		nm := nearMiss(a, []unit.Unit{b}, [][]similarity.Match{matches}, []float64{sim})
		if nm.Agreement < minAgreement {
			return
		}
		verified[i] = &pair{c[0], c[1], matches, sim}
		weights[i] = nm.MatchedNodes
	})

	pairs := map[[2]int]pair{}
	var edges []Edge
	for i, p := range verified {
		if p != nil {
			pairs[candidates[i]] = *p
			edges = append(edges, Edge{p.a, p.b, weights[i]})
		}
	}

	var out []NearMiss
	for _, f := range Cluster(len(reps), edges) {
		members := make([]unit.Unit, len(f.Members))
		alignments := make([][]similarity.Match, len(f.Members))
		sims := make([]float64, len(f.Members))
		for i, m := range f.Members {
			members[i] = reps[m]
			p := pairs[[2]int{min(f.Anchor, m), max(f.Anchor, m)}]
			alignments[i], sims[i] = p.matches, p.sim
			if p.a != f.Anchor {
				alignments[i] = flip(p.matches)
			}
		}
		out = append(out, nearMiss(reps[f.Anchor], members, alignments, sims))
	}
	slices.SortFunc(out, func(x, y NearMiss) int {
		if c := cmp.Compare(y.MatchedNodes, x.MatchedNodes); c != 0 {
			return c
		}
		return compareLocation(x.Units[0], y.Units[0])
	})
	return out
}

// parallel calls fn(0) .. fn(n-1) on up to threads goroutines.
func parallel(n, threads int, fn func(i int)) {
	next := make(chan int)
	var wg sync.WaitGroup
	for range max(threads, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
}

// representatives returns the first unit, by location, of every shape with
// at least minNodes nodes, in location order.
func representatives(units []unit.Unit, minNodes int) []unit.Unit {
	sorted := slices.Clone(units)
	slices.SortFunc(sorted, compareLocation)
	seen := map[key]bool{}
	var reps []unit.Unit
	for _, u := range sorted {
		k := key{u.Shape, u.Nodes}
		if u.Nodes < minNodes || seen[k] {
			continue
		}
		seen[k] = true
		reps = append(reps, u)
	}
	return reps
}

// valueSlack is how much lower the similarity of two units' names and
// values may be than their structural similarity before value anchoring is
// given up. Near misses agree on at least minAgreement of their values.
const valueSlack = 0.2

// alignUnits aligns two units. The structure sequences decide whether the
// units are similar, and that similarity is returned. When code repeats
// the same statement structure, many alignments are equally long and a
// structural one may pair the wrong statements, so the matches are built
// differently: equal names and values (in order) anchor the alignment, and
// the stretches between anchors are aligned on structure, which pairs
// renamed and changed values (see alignGap). If names and values differ too
// much to anchor, the structural alignment is used.
func alignUnits(a, b unit.Unit, minSim float64) ([]similarity.Match, float64, bool) {
	sim, ok := similarity.Similar(a.Seq, b.Seq, minSim)
	if !ok {
		return nil, 0, false
	}
	posA, valA := leafValues(a)
	posB, valB := leafValues(b)
	anchors, _, ok := similarity.Align(valA, valB, max(minSim-valueSlack, 0))
	if !ok {
		structural, _, _ := similarity.Align(a.Seq, b.Seq, minSim)
		return structural, sim, true
	}

	var out []similarity.Match
	prevA, prevB := -1, -1
	prevLineA, prevLineB := a.StartLine, b.StartLine
	fill := func(endA, endB, nextLineA, nextLineB int) {
		out = append(out, alignGap(a, b, prevA+1, endA, prevB+1, endB, prevLineA, prevLineB, nextLineA, nextLineB)...)
	}
	for _, anchor := range anchors {
		m := similarity.Match{A: posA[anchor.A], B: posB[anchor.B]}
		fill(m.A, m.B, a.SeqLines[m.A], b.SeqLines[m.B])
		out = append(out, m)
		prevA, prevB = m.A, m.B
		prevLineA, prevLineB = a.SeqLines[m.A], b.SeqLines[m.B]
	}
	fill(len(a.Seq), len(b.Seq), a.EndLine, b.EndLine)
	return out, sim, true
}

// alignGap aligns the stretch [startA, endA) of a with [startB, endB) of b,
// which lies between two anchors. It holds the tail of the statement on the
// previous anchor's line, the differing code, and the head of the statement
// on the next anchor's line. So the common prefix on the previous anchor's
// lines and the common suffix on the next anchor's lines are matched first,
// and only the middle is aligned on structure.
func alignGap(a, b unit.Unit, startA, endA, startB, endB, prevLineA, prevLineB, nextLineA, nextLineB int) []similarity.Match {
	var out []similarity.Match
	for startA < endA && startB < endB && a.Seq[startA] == b.Seq[startB] &&
		a.SeqLines[startA] == prevLineA && b.SeqLines[startB] == prevLineB {
		out = append(out, similarity.Match{A: startA, B: startB})
		startA++
		startB++
	}
	var suffix []similarity.Match
	for endA > startA && endB > startB && a.Seq[endA-1] == b.Seq[endB-1] &&
		a.SeqLines[endA-1] == nextLineA && b.SeqLines[endB-1] == nextLineB {
		endA--
		endB--
		suffix = append(suffix, similarity.Match{A: endA, B: endB})
	}
	middle, _, _ := similarity.Align(a.Seq[startA:endA], b.Seq[startB:endB], 0)
	for _, m := range middle {
		out = append(out, similarity.Match{A: startA + m.A, B: startB + m.B})
	}
	slices.Reverse(suffix)
	return append(out, suffix...)
}

// leafValues returns the Seq positions of a unit's leaves and a symbol per
// leaf for its kind and text.
func leafValues(u unit.Unit) (positions []int, values []uint32) {
	leaf := 0
	for i, sym := range u.Seq {
		if !unit.IsLeaf(sym) {
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte{byte(sym)})
		_, _ = h.Write([]byte(u.Leaves[leaf].Text))
		positions = append(positions, i)
		values = append(values, h.Sum32())
		leaf++
	}
	return positions, values
}

// flip swaps the sides of an alignment.
func flip(matches []similarity.Match) []similarity.Match {
	out := make([]similarity.Match, len(matches))
	for i, m := range matches {
		out[i] = similarity.Match{A: m.B, B: m.A}
	}
	return out
}

// nearMiss builds a family from an anchor, its members and each member's
// alignment with the anchor (A indexes the anchor, B the member).
func nearMiss(anchor unit.Unit, members []unit.Unit, alignments [][]similarity.Match, sims []float64) NearMiss {
	nm := NearMiss{
		Units:      append([]unit.Unit{anchor}, members...),
		Similarity: append([]float64{1}, sims...),
		Agreement:  1,
		Unmatched:  make([][]LineRange, len(members)+1),
	}

	anchorLeaves := leafIndexes(anchor.Seq)
	declAnchor := declaredNames(anchor)
	// matchedBy[i] counts the members aligning anchor symbol i.
	matchedBy := make([]int, len(anchor.Seq))
	// leafIn[m][l] is member m's leaf aligned with anchor leaf l, or -1.
	leafIn := make([][]int, len(members))
	for m, member := range members {
		memberLeaves := leafIndexes(member.Seq)
		matched := make([]bool, len(member.Seq))
		leafIn[m] = slices.Repeat([]int{-1}, len(anchor.Leaves))
		declMember := declaredNames(member)
		agree, leaves := 0, 0
		for _, match := range alignments[m] {
			matched[match.B] = true
			matchedBy[match.A]++
			if anchor.Seq[match.A] != unit.Close {
				nm.MatchedNodes++
			}
			if l := anchorLeaves[match.A]; l >= 0 {
				leafIn[m][l] = memberLeaves[match.B]
				la, lb := anchor.Leaves[l], member.Leaves[memberLeaves[match.B]]
				leaves++
				if la.Text == lb.Text || (la.Kind == unit.Identifier && declAnchor[la.Text] && declMember[lb.Text]) {
					agree++
				}
			}
		}
		if leaves > 0 {
			nm.Agreement = min(nm.Agreement, float64(agree)/float64(leaves))
		}
		nm.Unmatched[m+1] = unmatchedLines(member, matched)
	}

	inAll := make([]bool, len(anchor.Seq))
	for i, n := range matchedBy {
		inAll[i] = n == len(members)
	}
	nm.Unmatched[0] = unmatchedLines(anchor, inAll)

	var rows [][]int
	for l := range anchor.Leaves {
		row := []int{l}
		for m := range members {
			row = append(row, leafIn[m][l])
		}
		if !slices.Contains(row, -1) {
			rows = append(rows, row)
		}
	}
	nm.Differences = differences(nm.Units, rows)
	return nm
}

// leafIndexes maps each Seq position to its leaf index, or -1.
func leafIndexes(seq []uint32) []int {
	out := make([]int, len(seq))
	next := 0
	for i, sym := range seq {
		out[i] = -1
		if unit.IsLeaf(sym) {
			out[i] = next
			next++
		}
	}
	return out
}

// unmatchedLines returns the ranges of lines holding unmatched symbols.
// Close symbols are ignored: they carry a node's end line, which says
// nothing about where the differing code is.
func unmatchedLines(u unit.Unit, matched []bool) []LineRange {
	var lines []int
	for i, sym := range u.Seq {
		if !matched[i] && sym != unit.Close {
			lines = append(lines, u.SeqLines[i])
		}
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	var out []LineRange
	for _, l := range lines {
		if n := len(out); n > 0 && out[n-1].End+1 == l {
			out[n-1].End = l
			continue
		}
		out = append(out, LineRange{l, l})
	}
	return out
}
