package pattern

import (
	"cmp"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// ParamGroup is a set of parameters that several functions all take: data
// that travels together and could be one value.
type ParamGroup struct {
	// Params are sorted by name.
	Params []unit.Param
	Units  []unit.Unit
}

// Parameter groups: at least minGroupParams parameters, the same names and
// types, taken by at least minGroupFunctions functions. Functions with more
// than maxGroupedParams parameters are left out, since their subsets would
// dominate the search.
const (
	minGroupParams    = 3
	minGroupFunctions = 4
	maxGroupedParams  = 10
)

// ParameterGroups finds the largest sets of parameters that functions take
// together. For every set of functions sharing minGroupParams parameters,
// the group is everything those functions share, so a group is never
// reported again inside a larger one taken by the same functions. Trait
// methods are left out, since the trait dictates their parameters, and so
// is test code. Groups are ordered by size, the parameters a struct would
// save: parameters times extra functions.
func ParameterGroups(units []unit.Unit) []ParamGroup {
	var eligible []unit.Unit
	var keys [][]string
	bySubset := map[string][]int{}
	for _, u := range units {
		if u.Test || u.Trait != "" || len(u.Params) < minGroupParams || len(u.Params) > maxGroupedParams {
			continue
		}
		k := paramKeys(u)
		i := len(eligible)
		eligible = append(eligible, u)
		keys = append(keys, k)
		for a := 0; a < len(k); a++ {
			for b := a + 1; b < len(k); b++ {
				for c := b + 1; c < len(k); c++ {
					s := k[a] + "\x00" + k[b] + "\x00" + k[c]
					bySubset[s] = append(bySubset[s], i)
				}
			}
		}
	}
	groups := map[string][]int{} // shared parameters -> functions taking them all
	for _, members := range bySubset {
		if len(members) < minGroupFunctions {
			continue
		}
		shared := keys[members[0]]
		for _, m := range members[1:] {
			shared = slices.DeleteFunc(slices.Clone(shared), func(p string) bool { return !slices.Contains(keys[m], p) })
		}
		groups[strings.Join(shared, "\x00")] = members
	}
	var out []ParamGroup
	for g, members := range groups {
		if !maximal(g, members, groups) {
			continue
		}
		var pg ParamGroup
		for _, k := range strings.Split(g, "\x00") {
			name, typ, _ := strings.Cut(k, ":")
			pg.Params = append(pg.Params, unit.Param{Name: name, Type: typ})
		}
		for _, m := range members {
			pg.Units = append(pg.Units, eligible[m])
		}
		slices.SortFunc(pg.Units, func(a, b unit.Unit) int {
			if c := cmp.Compare(a.File, b.File); c != 0 {
				return c
			}
			if c := cmp.Compare(a.StartLine, b.StartLine); c != 0 {
				return c
			}
			return cmp.Compare(a.Name, b.Name)
		})
		out = append(out, pg)
	}
	slices.SortFunc(out, func(a, b ParamGroup) int {
		if c := cmp.Compare(b.Size(), a.Size()); c != 0 {
			return c
		}
		if c := cmp.Compare(len(b.Params), len(a.Params)); c != 0 {
			return c
		}
		return cmp.Compare(a.Units[0].Name, b.Units[0].Name)
	})
	// One group per family: groups sharing familyParams parameters are one
	// concept (an ingest context split by which three of its four fields a
	// function needs), represented by the largest.
	var kept []ParamGroup
	for _, g := range out {
		if !slices.ContainsFunc(kept, func(k ParamGroup) bool { return shared(g, k) >= familyParams }) {
			kept = append(kept, g)
		}
	}
	return kept
}

// familyParams is how many parameters two groups share to be one concept.
// Measured on a 735k-line Rust codebase: groups sharing two parameters
// were one context taken in parts (build, environment, now, transaction
// as four overlapping groups); groups sharing one, such as a database
// handle, were different concepts.
const familyParams = 2

// shared counts the parameters two groups have in common.
func shared(a, b ParamGroup) int {
	n := 0
	for _, p := range a.Params {
		if slices.Contains(b.Params, p) {
			n++
		}
	}
	return n
}

// Size is the parameters a parameter object would save: the group's
// parameters in every function but one.
func (g ParamGroup) Size() int { return len(g.Params) * (len(g.Units) - 1) }

// paramKeys returns a unit's parameters as sorted "name:type" keys.
func paramKeys(u unit.Unit) []string {
	k := make([]string, len(u.Params))
	for i, p := range u.Params {
		k[i] = p.Name + ":" + p.Type
	}
	slices.Sort(k)
	return k
}

// maximal reports whether no larger group is taken by the same functions.
func maximal(g string, members []int, groups map[string][]int) bool {
	params := strings.Split(g, "\x00")
	for other, otherMembers := range groups {
		if len(other) <= len(g) || !slices.Equal(sortedCopy(otherMembers), sortedCopy(members)) {
			continue
		}
		if !slices.ContainsFunc(params, func(p string) bool { return !slices.Contains(strings.Split(other, "\x00"), p) }) {
			return false
		}
	}
	return true
}

func sortedCopy(xs []int) []int {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s
}
