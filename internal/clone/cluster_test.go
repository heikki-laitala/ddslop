package clone

import (
	"fmt"
	"testing"
)

func TestClusterStarWithLeftover(t *testing.T) {
	// 0 is near 1, 2 and 4; 3 is near only 1. 0 anchors {1, 2, 4}; 3 is
	// left over and pairs with its only neighbour 1.
	edges := []edge{{0, 1, 10}, {0, 2, 10}, {0, 4, 10}, {1, 3, 10}}

	got := fmt.Sprint(cluster(5, edges))

	if want := "[{0 [1 2 4]} {1 [3]}]"; got != want {
		t.Errorf("cluster = %s, want %s", got, want)
	}
}

func TestClusterChainAnchorsOnTheMiddle(t *testing.T) {
	got := fmt.Sprint(cluster(3, []edge{{0, 1, 10}, {1, 2, 10}}))
	if want := "[{1 [0 2]}]"; got != want {
		t.Errorf("cluster = %s, want %s", got, want)
	}
}

func TestClusterSeparatePairs(t *testing.T) {
	got := fmt.Sprint(cluster(4, []edge{{2, 3, 10}, {0, 1, 10}}))
	if want := "[{0 [1]} {2 [3]}]"; got != want {
		t.Errorf("cluster = %s, want %s", got, want)
	}
}

func TestClusterTiesPreferWeightThenLowerIndex(t *testing.T) {
	// Triangle: all degrees 2. Node 1 has the most shared nodes.
	got := fmt.Sprint(cluster(3, []edge{{0, 1, 10}, {1, 2, 20}, {0, 2, 5}}))
	if want := "[{1 [0 2]}]"; got != want {
		t.Errorf("weighted triangle: cluster = %s, want %s", got, want)
	}
	// Equal weights: the lowest index wins.
	got = fmt.Sprint(cluster(3, []edge{{0, 1, 10}, {1, 2, 10}, {0, 2, 10}}))
	if want := "[{0 [1 2]}]"; got != want {
		t.Errorf("even triangle: cluster = %s, want %s", got, want)
	}
}

func TestClusterLeftoverPicksHeaviestEdge(t *testing.T) {
	// 0 anchors {1, 2}; 3 is near 1 (weight 5) and 2 (weight 9): pairs with 2.
	edges := []edge{{0, 1, 10}, {0, 2, 10}, {0, 4, 10}, {1, 3, 5}, {2, 3, 9}}
	got := fmt.Sprint(cluster(5, edges))
	if want := "[{0 [1 2 4]} {2 [3]}]"; got != want {
		t.Errorf("cluster = %s, want %s", got, want)
	}
}
