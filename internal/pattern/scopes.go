// Package pattern finds repeated code that is a missing idiom rather than
// a missing helper, and names the idiom: deterministic rules over the
// units, no model involved.
package pattern

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/group"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// ScopeGroup is a set of functions that open the same kind of value at
// their start and close it the same way at their end.
type ScopeGroup struct {
	Opens, Closes string
	Units         []unit.Unit
}

// minScopeFunctions is the fewest functions repeating a scope for it to be
// worth a helper. Measured on a 188k-line Rust codebase: 54 functions
// began a transaction and committed it with no shared helper; on a
// 735k-line one, no opening and closing pair repeated in five functions.
const minScopeFunctions = 5

// Scopes groups non-test units by the scope they open and close (see
// unit.Scope), keeping groups of at least minScopeFunctions functions, in
// order of size, largest first. The functions repeat the setup and the
// teardown around different work: one helper taking the work as a closure
// can own both, so no copy forgets the teardown.
func Scopes(units []unit.Unit) []ScopeGroup {
	groups := group.Collect(units, func(u unit.Unit) ([2]string, bool) {
		if u.Scope == nil || u.Test {
			return [2]string{}, false
		}
		return [2]string{u.Scope.Opens, u.Scope.Closes}, true
	}, func(k [2]string, members []unit.Unit) (ScopeGroup, bool) {
		return ScopeGroup{Opens: k[0], Closes: k[1], Units: members}, len(members) >= minScopeFunctions
	})
	slices.SortFunc(groups, func(a, b ScopeGroup) int {
		return cmp.Or(cmp.Compare(len(b.Units), len(a.Units)), cmp.Compare(a.Opens, b.Opens), cmp.Compare(a.Closes, b.Closes))
	})
	return groups
}
