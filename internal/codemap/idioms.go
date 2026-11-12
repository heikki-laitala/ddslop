package codemap

import (
	"cmp"
	"slices"

	"github.com/heikki-laitala/ddslop/internal/flow"
)

// minIdiomOccurrences is the fewest occurrences of a concern for its
// majority form to be a convention: with fewer, three uses out of four
// would make one. The Go codebases measured had 7 to 27 error checks
// and 10 to 24 wraps, each all in one form.
const minIdiomOccurrences = 5

// Idiom is how the code does one concern: each form it uses, most used
// first, and the convention when most occurrences use one form. Without a
// convention, Idiomatic is the form the language adapter marks idiomatic.
type Idiom struct {
	Concern, Convention, Idiomatic string
	Occurrences                    int
	Forms                          []FormCount
}

// FormCount is how often a form occurs and its first occurrence: the unit
// and the line.
type FormCount struct {
	Form        string
	Count, Unit int
	Line        int
}

// Idioms describes each concern the units in the grouping use forms for,
// by concern name. idiomatic maps a concern to its idiomatic form.
func Idioms(g *flow.Graph, group Grouping, idiomatic map[string]string) []Idiom {
	byConcern := map[string]map[string]*FormCount{}
	for i, u := range g.Units {
		if u.Test || u.Ignored || len(u.Forms) == 0 || group(u) == "" {
			continue
		}
		for _, f := range u.Forms {
			if byConcern[f.Concern] == nil {
				byConcern[f.Concern] = map[string]*FormCount{}
			}
			fc := byConcern[f.Concern][f.Form]
			if fc == nil {
				fc = &FormCount{Form: f.Form, Unit: i, Line: f.Line}
				byConcern[f.Concern][f.Form] = fc
			}
			fc.Count++
			if first := g.Units[fc.Unit]; cmp.Or(cmp.Compare(u.File, first.File), cmp.Compare(f.Line, fc.Line)) < 0 {
				fc.Unit, fc.Line = i, f.Line
			}
		}
	}
	var out []Idiom
	for concern, forms := range byConcern {
		id := Idiom{Concern: concern}
		for _, fc := range forms {
			id.Forms = append(id.Forms, *fc)
			id.Occurrences += fc.Count
		}
		slices.SortFunc(id.Forms, func(a, b FormCount) int {
			return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Form, b.Form))
		})
		if top := id.Forms[0]; id.Occurrences >= minIdiomOccurrences && 2*top.Count > id.Occurrences {
			id.Convention = top.Form
		} else {
			id.Idiomatic = idiomatic[concern]
		}
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b Idiom) int { return cmp.Compare(a.Concern, b.Concern) })
	return out
}
