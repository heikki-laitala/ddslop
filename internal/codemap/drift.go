package codemap

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// minDriftShare is the least share of a concern's occurrences the
// convention's form must hold before another form is drift. Below it the
// forms are a mix the code chose, not a slip: measured, lens-artifact-hub
// wraps errors with map_err in 69% and anyhow's context, across its main
// crate, in the rest; the conventions that held (error checks by variant,
// errors.Is/As, %w) were at 99% and above.
const minDriftShare = 0.9

// Drift is an occurrence of another form where a convention exists: the
// unit and line, and the convention's occurrence to follow (Target and
// TargetLine), the closest one.
type Drift struct {
	Concern, Form, Convention string
	Unit, Line                int
	Target, TargetLine        int
}

// occurrence is one use of a form.
type occurrence struct {
	form       string
	unit, line int
}

// concernOccurrences collects the forms the non-test, non-ignored units in
// the grouping use, by concern, in unit order.
func concernOccurrences(g *flow.Graph, group Grouping) map[string][]occurrence {
	out := map[string][]occurrence{}
	for i, u := range g.Units {
		if u.Test || u.Ignored || len(u.Forms) == 0 || group(u) == "" {
			continue
		}
		for _, f := range u.Forms {
			out[f.Concern] = append(out[f.Concern], occurrence{form: f.Form, unit: i, line: f.Line})
		}
	}
	return out
}

// Drifts finds, for each concern of the units in group, the occurrences
// of other forms when the convention holds at least minDriftShare of at
// least minIdiomOccurrences. Each drift's target is the convention's
// occurrence in the same file nearest to it, else the first in the same
// module (by module), else the first anywhere. Drifts come in unit order.
func Drifts(g *flow.Graph, group, module Grouping) []Drift {
	var out []Drift
	for concern, occs := range concernOccurrences(g, group) {
		counts := map[string]int{}
		for _, o := range occs {
			counts[o.form]++
		}
		convention := ""
		for form, n := range counts {
			if convention == "" || n > counts[convention] || (n == counts[convention] && form < convention) {
				convention = form
			}
		}
		if len(occs) < minIdiomOccurrences || float64(counts[convention]) < minDriftShare*float64(len(occs)) {
			continue
		}
		var conventional []occurrence
		for _, o := range occs {
			if o.form == convention {
				conventional = append(conventional, o)
			}
		}
		slices.SortFunc(conventional, func(a, b occurrence) int {
			return cmp.Or(cmp.Compare(g.Units[a.unit].File, g.Units[b.unit].File), cmp.Compare(a.line, b.line))
		})
		for _, o := range occs {
			if o.form == convention {
				continue
			}
			t := closest(g, module, o, conventional)
			out = append(out, Drift{Concern: concern, Form: o.form, Convention: convention, Unit: o.unit, Line: o.line,
				Target: t.unit, TargetLine: t.line})
		}
	}
	slices.SortFunc(out, func(a, b Drift) int { return cmp.Or(cmp.Compare(a.Unit, b.Unit), cmp.Compare(a.Line, b.Line)) })
	return out
}

// closest returns the conventional occurrence (sorted by file and line)
// to follow from o: in o's file the nearest, else the first in o's
// module, else the first.
func closest(g *flow.Graph, module Grouping, o occurrence, conventional []occurrence) occurrence {
	u := g.Units[o.unit]
	best, found := occurrence{}, false
	for _, c := range conventional {
		if g.Units[c.unit].File == u.File && (!found || abs(c.line-o.line) < abs(best.line-o.line)) {
			best, found = c, true
		}
	}
	if found {
		return best
	}
	for _, c := range conventional {
		if module(g.Units[c.unit]) == module(u) {
			return c
		}
	}
	return conventional[0]
}

func abs(x int) int { return max(x, -x) }
