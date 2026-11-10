package clone

import "slices"

// Edge connects nodes A and B with a weight, such as the number of nodes
// two near misses share.
type Edge struct{ A, B, Weight int }

// Family is an anchor and the members that each have an edge to it.
type Family struct {
	Anchor  int
	Members []int
}

type neighbour struct{ node, weight int }

// Cluster groups nodes into families around anchors. It repeatedly takes
// the node with the most edges to unclustered nodes as an anchor (ties: the
// larger total weight, then the lower index) and makes those neighbours
// its members, so every member has a verified edge to its anchor. A node
// left over, whose neighbours were all clustered, forms a pair with its
// heaviest neighbour; the lower index anchors the pair.
func Cluster(n int, edges []Edge) []Family {
	adj := make([][]neighbour, n)
	for _, e := range edges {
		adj[e.A] = append(adj[e.A], neighbour{e.B, e.Weight})
		adj[e.B] = append(adj[e.B], neighbour{e.A, e.Weight})
	}
	used := make([]bool, n)

	var out []Family
	for {
		best, bestDeg, bestWeight := -1, 0, 0
		for v := range n {
			if used[v] {
				continue
			}
			deg, weight := 0, 0
			for _, nb := range adj[v] {
				if !used[nb.node] {
					deg++
					weight += nb.weight
				}
			}
			if deg > bestDeg || (deg == bestDeg && deg > 0 && weight > bestWeight) {
				best, bestDeg, bestWeight = v, deg, weight
			}
		}
		if best < 0 {
			break
		}
		var members []int
		for _, nb := range adj[best] {
			if !used[nb.node] {
				members = append(members, nb.node)
			}
		}
		slices.Sort(members)
		used[best] = true
		for _, m := range members {
			used[m] = true
		}
		out = append(out, Family{Anchor: best, Members: members})
	}

	for v := range n {
		if used[v] || len(adj[v]) == 0 {
			continue
		}
		heaviest := adj[v][0]
		for _, nb := range adj[v][1:] {
			if nb.weight > heaviest.weight || (nb.weight == heaviest.weight && nb.node < heaviest.node) {
				heaviest = nb
			}
		}
		out = append(out, Family{Anchor: min(v, heaviest.node), Members: []int{max(v, heaviest.node)}})
	}
	return out
}
