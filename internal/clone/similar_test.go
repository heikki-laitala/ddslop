package clone

import (
	"fmt"
	"strings"
	"testing"
)

func TestSimilarToRanksExistingFunctionsLikeTheTarget(t *testing.T) {
	units := rustUnits(t, "a.rs", blockA)
	units = append(units, rustUnits(t, "b.rs", blockB)...)
	units = append(units, rustUnits(t, "c.rs", strings.ReplaceAll(blockA, "sync_users", "sync_people"))...)
	units = append(units, rustUnits(t, "d.rs", "fn tiny() -> u32 {\n    1\n}\n")...)
	target := units[0] // sync_users in a.rs

	var got []string
	for _, s := range SimilarTo(target, units, 0.5) {
		got = append(got, fmt.Sprintf("%s %s %.2f", s.Unit.File, s.Unit.Name, s.Similarity))
	}
	// The renamed copy first (identical structure), then the near miss; the
	// target itself and the unrelated function are left out.
	if len(got) != 2 || !strings.HasPrefix(got[0], "c.rs sync_people 1.00") || !strings.HasPrefix(got[1], "b.rs sync_orders") {
		t.Errorf("similar = %v, want c.rs sync_people (1.00) then b.rs sync_orders", got)
	}
}
