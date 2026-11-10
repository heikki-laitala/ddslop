package index

import "strconv"

// ResolveThreads returns how many files to parse in parallel, given the
// DDSLOP_INDEX_THREADS value and the number of cores. A valid override (a
// positive integer) wins; otherwise half the cores, at least one, so a run
// started from an editor or agent hook leaves CPU for them (as in pruner).
func ResolveThreads(env string, cores int) int {
	if n, err := strconv.Atoi(env); err == nil && n >= 1 {
		return n
	}
	return max(cores/2, 1)
}
