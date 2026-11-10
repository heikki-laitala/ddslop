package clone

import "slices"

// edge is a verified near miss between nodes a and b, weighted by the
// number of nodes they share.
type edge struct{ a, b, weight int }

// family is an anchor and the members that are each a near miss of it.
type family struct {
	anchor  int
	members []int
}

type neighbour struct{ node, weight int }

// cluster groups nodes into families around anchors. It repeatedly takes
// the node with the most edges to unclustered nodes as an anchor (ties: the
// larger total weight, then the lower index) and makes those neighbours
// its members, so every member has a verified edge to its anchor. A node
// left over, whose neighbours were all clustered, forms a pair with its
// heaviest neighbour; the lower index anchors the pair.
func cluster(n int, edges []edge) []family {
	adj := make([][]neighbour, n)
	for _, e := range edges {
		adj[e.a] = append(adj[e.a], neighbour{e.b, e.weight})
		adj[e.b] = append(adj[e.b], neighbour{e.a, e.weight})
	}
	used := make([]bool, n)

	var out []family
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
		out = append(out, family{anchor: best, members: members})
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
		out = append(out, family{anchor: min(v, heaviest.node), members: []int{max(v, heaviest.node)}})
	}
	return out
}
