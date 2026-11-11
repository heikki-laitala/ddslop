// Package report turns clone groups into refactoring candidates and writes
// them as text or JSON.
package report

import (
	"cmp"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/pattern"
	"github.com/heikki-laitala/ddslop/internal/similarity"
	"github.com/heikki-laitala/ddslop/internal/simplify"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Report is the full result of one run.
type Report struct {
	Candidates []Candidate `json:"candidates"`
	Summary    Summary     `json:"summary"`
}

// Summary says how much was analysed.
type Summary struct {
	Files          int `json:"files"`
	GeneratedFiles int `json:"generated_files"`
	Units          int `json:"units"`
	TestUnits      int `json:"test_units"`
	ParsedFiles    int `json:"parsed_files"`
	UnchangedFiles int `json:"unchanged_files"`
	Candidates     int `json:"candidates"`
	// Diff is the Git ref of diff mode: only candidates touching lines
	// changed since it are reported.
	Diff string `json:"diff,omitempty"`
	// Baselined counts candidates suppressed because the baseline file
	// already knows them.
	Baselined int `json:"baselined,omitempty"`
	// Ignored counts the functions, structs, enums and constants a
	// ddslop:ignore comment leaves out of the analysis.
	Ignored int `json:"ignored,omitempty"`
	// Version is the ddslop version: detection rules change between
	// versions, so health is comparable only between runs of one version.
	Version string `json:"version,omitempty"`
	Health  Health `json:"health"`
}

// Health measures what ddslop finds across the whole analysed code, before
// diff mode or a baseline narrow the candidates down.
type Health struct {
	// AnalysedLines are the lines of the functions, structs and constants
	// compared.
	AnalysedLines int `json:"analysed_lines"`
	// DuplicatedLines are the lines of every copy but one per duplication
	// candidate, each line once, without lines only a near-miss member has.
	DuplicatedLines    int     `json:"duplicated_lines"`
	DuplicationPercent float64 `json:"duplication_percent"`
	UnusedFunctions    int     `json:"unused_functions"`
	Wrappers           int     `json:"wrappers"`
	SingleImplTraits   int     `json:"single_impl_traits"`
	ParallelFlows      int     `json:"parallel_flows"`
	// FailOver is the --fail-over limit, when one was given, and OverLimit
	// whether the duplication percentage is above it.
	FailOver  *float64 `json:"fail_over,omitempty"`
	OverLimit bool     `json:"over_limit,omitempty"`
}

// Limit records a duplication percentage limit and whether it is exceeded.
func (h *Health) Limit(limit float64) {
	h.FailOver = &limit
	h.OverLimit = h.DuplicationPercent > limit
}

// Candidate is one refactoring candidate: a group of copies, what differs
// between them, and what to do about it.
type Candidate struct {
	ID          string       `json:"id"`
	Fingerprint string       `json:"fingerprint"`
	Language    string       `json:"language"`
	Kind        string       `json:"kind"`
	Similarity  float64      `json:"similarity"`
	Occurrences []Occurrence `json:"occurrences"`
	Differences []Difference `json:"differences"`
	Suggestion  Suggestion   `json:"suggestion"`
	Score       Score        `json:"score"`
	// Steps are the aligned steps of a parallel flow.
	Steps []Step `json:"steps,omitempty"`
	// Target is the existing code to use instead of the occurrences, when
	// there is one: the constant whose value is written inline, or the
	// function a wrapper calls.
	Target *Occurrence `json:"target,omitempty"`
	// Values are what the occurrences share, when that is not code: the
	// parameters of a parameter group, the strings of a string kind.
	Values []string `json:"values,omitempty"`
}

// Step is one aligned step of a parallel flow: the function playing that
// role in each feature, with its similarity to the anchor's.
type Step struct {
	Occurrences []Occurrence `json:"occurrences"`
}

// Occurrence is one copy.
type Occurrence struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	// Similarity is a near-miss member's similarity to the first
	// occurrence, the family's anchor.
	Similarity float64 `json:"similarity,omitempty"`
	// UnmatchedLines are line ranges of near-miss code without a
	// counterpart: for a member, code the anchor lacks; for the anchor,
	// code missing from at least one member.
	UnmatchedLines [][2]int `json:"unmatched_lines,omitempty"`
	// Changed is set in diff mode when the occurrence overlaps changed
	// lines: the copy the change introduced or touched.
	Changed bool `json:"changed,omitempty"`
}

// Difference is one set of values that differ between the copies.
type Difference struct {
	Kind   string   `json:"kind"`
	Values []string `json:"values"`
	Lines  [][]int  `json:"lines"`
	Local  bool     `json:"local"`
}

// Suggestion is the proposed refactoring.
type Suggestion struct {
	Action     string `json:"action"`
	Parameters int    `json:"parameters"`
	// Pattern names the idiom the refactoring introduces, when there is
	// one, and GoF its Gang of Four counterpart.
	Pattern string `json:"pattern,omitempty"`
	GoF     string `json:"gof,omitempty"`
}

// Score says how much duplication the candidate represents.
type Score struct {
	Nodes           int `json:"nodes"`
	Lines           int `json:"lines"`
	Occurrences     int `json:"occurrences"`
	DuplicatedNodes int `json:"duplicated_nodes"`
}

// Kinds and actions.
const (
	exactClone     = "exact-clone"
	renamedClone   = "renamed-clone"
	nearMiss       = "near-miss"
	blockClone     = "block-clone"
	parallelFlow   = "parallel-flow"
	duplicateType  = "duplicate-type"
	duplicateConst = "duplicate-constant"
	inlinedConst   = "inlined-constant"
	deadCode       = "dead-code"
	deleteDead     = "delete-dead"
	passThrough    = "pass-through"
	inlineWrapper  = "inline-wrapper"
	singleImpl     = "single-impl-trait"
	sharedPreamble = "shared-preamble"
	setupTeardown  = "setup-teardown"
	extractScope   = "extract-scope"
	stringKinds    = "string-kinds"
	introduceEnum  = "introduce-enum"
	paramGroup     = "parameter-group"
	introduceParam = "introduce-parameter-object"
	extractPre     = "extract-preamble"
	removeTrait    = "remove-trait"
	extractFlow    = "extract-pipeline"
	reuseExisting  = "reuse-existing"
	extractCommon  = "extract-common"
	kindIdentifier = "identifier"
	kindType       = "type"
	kindLiteral    = "literal"
)

var leafKindNames = map[unit.LeafKind]string{
	unit.Identifier: kindIdentifier,
	unit.Type:       kindType,
	unit.Literal:    kindLiteral,
}

// Findings are everything one analysis found.
type Findings struct {
	Groups     []clone.Group
	NearMisses []clone.NearMiss
	Blocks     []clone.BlockClone
	Flows      []Flow
	Structs    []clone.StructGroup
	Enums      []clone.EnumGroup
	Consts     []clone.ConstGroup
	Inlined    []clone.InlinedConst
	Dead       []unit.Unit
	Wrappers   []simplify.Wrapper
	Traits     []simplify.SingleImplTrait
	Scopes     []pattern.ScopeGroup
	Kinds      []pattern.StringKind
	Params     []pattern.ParamGroup
}

// Flow is a family of features running the same pipeline through
// different functions: the entry points, anchor first, and the aligned
// steps.
type Flow struct {
	Entries      []unit.Unit
	Steps        []FlowStep
	MatchedNodes int
	Cover        float64
}

// FlowStep is one aligned step: the functions playing that role, the
// anchor's first, with each one's similarity to the anchor's.
type FlowStep struct {
	Units      []unit.Unit
	Similarity []float64
}

// Build turns findings into a report. Candidates are ranked by duplicated
// nodes, largest first; on ties, they keep this order: groups, near misses,
// block clones, flows, structs, constants, dead code, wrappers, traits,
// scopes, string kinds, parameter groups. The summary's candidate count is filled in.
// Each candidate's language is languageOf its first file.
func Build(languageOf func(file string) string, f Findings, summary Summary) Report {
	candidates := make([]Candidate, 0, len(f.Groups)+len(f.NearMisses)+len(f.Blocks)+len(f.Flows))
	for _, g := range f.Groups {
		candidates = append(candidates, groupCandidate(languageOf(g.Units[0].File), g))
	}
	for _, nm := range f.NearMisses {
		candidates = append(candidates, nearMissCandidate(languageOf(nm.Units[0].File), nm))
	}
	for _, b := range f.Blocks {
		candidates = append(candidates, blockCandidate(languageOf(b.Occurrences[0].Unit.File), b))
	}
	for _, fl := range f.Flows {
		candidates = append(candidates, flowCandidate(languageOf(fl.Entries[0].File), fl))
	}
	for _, s := range f.Structs {
		candidates = append(candidates, structCandidate(languageOf(s.Structs[0].File), s))
	}
	for _, e := range f.Enums {
		candidates = append(candidates, enumCandidate(languageOf(e.Enums[0].File), e))
	}
	for _, g := range f.Consts {
		candidates = append(candidates, constCandidate(languageOf(g.Consts[0].File), g))
	}
	for _, in := range f.Inlined {
		candidates = append(candidates, inlinedCandidate(languageOf(in.Const.File), in))
	}
	for _, u := range f.Dead {
		candidates = append(candidates, single(languageOf(u.File), deadCode, deleteDead, u))
	}
	for _, w := range f.Wrappers {
		c := single(languageOf(w.Unit.File), passThrough, inlineWrapper, w.Unit)
		t := occurrence(w.Target)
		c.Target = &t
		candidates = append(candidates, c)
	}
	for _, t := range f.Traits {
		candidates = append(candidates, traitCandidate(languageOf(t.Trait.File), t))
	}
	for _, g := range f.Scopes {
		candidates = append(candidates, scopeCandidate(languageOf(g.Units[0].File), g))
	}
	for _, k := range f.Kinds {
		candidates = append(candidates, kindCandidate(languageOf(k.Matches[0].Unit.File), k))
	}
	for _, g := range f.Params {
		candidates = append(candidates, paramCandidate(languageOf(g.Units[0].File), g))
	}
	candidates = slices.DeleteFunc(candidates, mostlyParameters)
	slices.SortStableFunc(candidates, func(a, b Candidate) int {
		return cmp.Compare(b.Score.DuplicatedNodes, a.Score.DuplicatedNodes)
	})
	for i := range candidates {
		candidates[i].ID = fmt.Sprintf("c%d", i+1)
	}
	summary.Candidates = len(candidates)
	summary.Health = health(candidates, summary.Health.AnalysedLines)
	return Report{Candidates: candidates, Summary: summary}
}

// duplicationKinds are the candidate kinds whose occurrences are copies of
// one another. Parallel flows run different functions, and an inlined
// constant's occurrences are single literals.
var duplicationKinds = map[string]bool{
	exactClone: true, renamedClone: true, nearMiss: true, blockClone: true,
	sharedPreamble: true, duplicateType: true, duplicateConst: true,
}

// health measures the candidates against the analysed lines. The first
// occurrence of each duplication candidate counts as the original.
func health(candidates []Candidate, analysed int) Health {
	h := Health{AnalysedLines: analysed}
	duplicated := map[string]map[int]bool{} // file -> line
	for _, c := range candidates {
		switch c.Kind {
		case deadCode:
			h.UnusedFunctions++
		case passThrough:
			h.Wrappers++
		case singleImpl:
			h.SingleImplTraits++
		case parallelFlow:
			h.ParallelFlows++
		}
		if !duplicationKinds[c.Kind] {
			continue
		}
		for _, o := range c.Occurrences[1:] {
			if duplicated[o.File] == nil {
				duplicated[o.File] = map[int]bool{}
			}
			for line := o.StartLine; line <= o.EndLine; line++ {
				if !inRanges(line, o.UnmatchedLines) {
					duplicated[o.File][line] = true
				}
			}
		}
	}
	for _, lines := range duplicated {
		h.DuplicatedLines += len(lines)
	}
	if analysed > 0 {
		h.DuplicationPercent = math.Round(1000*float64(h.DuplicatedLines)/float64(analysed)) / 10
	}
	return h
}

func inRanges(line int, ranges [][2]int) bool {
	for _, r := range ranges {
		if r[0] <= line && line <= r[1] {
			return true
		}
	}
	return false
}

// AnalysedLines counts the lines of the units, structs and constants
// compared, each line once.
func AnalysedLines(units []unit.Unit, structs []unit.Struct, consts []unit.Const) int {
	lines := map[string]map[int]bool{}
	add := func(file string, start, end int) {
		if lines[file] == nil {
			lines[file] = map[int]bool{}
		}
		for l := start; l <= end; l++ {
			lines[file][l] = true
		}
	}
	for _, u := range units {
		add(u.File, u.StartLine, u.EndLine)
	}
	for _, s := range structs {
		add(s.File, s.StartLine, s.EndLine)
	}
	for _, c := range consts {
		add(c.File, c.Line, c.Line)
	}
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	return n
}

// Keep returns the report with only the candidates keep accepts,
// renumbered in order. keep may update the candidate it is given.
func Keep(r Report, keep func(c *Candidate) bool) Report {
	kept := []Candidate{}
	for _, c := range r.Candidates {
		if keep(&c) {
			c.ID = fmt.Sprintf("c%d", len(kept)+1)
			kept = append(kept, c)
		}
	}
	r.Candidates = kept
	r.Summary.Candidates = len(kept)
	return r
}

// KeepChanged marks the occurrences that overlap a change and keeps only
// the candidates with at least one, renumbering them in order.
func KeepChanged(r Report, base string, touches func(file string, start, end int) bool) Report {
	r = Keep(r, func(c *Candidate) bool {
		changed := false
		mark := func(occs []Occurrence) {
			for i, o := range occs {
				if touches(o.File, o.StartLine, o.EndLine) {
					occs[i].Changed = true
					changed = true
				}
			}
		}
		mark(c.Occurrences)
		for _, s := range c.Steps {
			mark(s.Occurrences)
		}
		return changed
	})
	r.Summary.Diff = base
	return r
}

// mostlyParameters reports whether a candidate's shared code would be
// mostly parameters (see clone.MostlyParameters).
func mostlyParameters(c Candidate) bool {
	return clone.MostlyParameters(c.Score.Nodes, c.Suggestion.Parameters)
}

func groupCandidate(language string, g clone.Group) Candidate {
	first := g.Units[0]
	c := Candidate{
		Fingerprint: fmt.Sprintf("%s:%016x", language, first.Shape),
		Language:    language,
		Kind:        exactClone,
		Similarity:  1,
		Occurrences: make([]Occurrence, len(g.Units)),
		Score: Score{
			Nodes:           first.Nodes,
			Lines:           first.EndLine - first.StartLine + 1,
			Occurrences:     len(g.Units),
			DuplicatedNodes: g.DuplicatedNodes(),
		},
	}
	for i, u := range g.Units {
		c.Occurrences[i] = occurrence(u)
	}
	c.Differences, c.Suggestion = differences(clone.Differences(g))
	if len(c.Differences) > 0 {
		c.Kind = renamedClone
	}
	return c
}

func nearMissCandidate(language string, nm clone.NearMiss) Candidate {
	anchor := nm.Units[0]
	c := Candidate{
		Fingerprint: fmt.Sprintf("%s:%016x~%d", language, anchor.Shape, len(nm.Units)),
		Language:    language,
		Kind:        nearMiss,
		Similarity:  1,
		Occurrences: make([]Occurrence, len(nm.Units)),
		Score: Score{
			Nodes:           anchor.Nodes,
			Lines:           anchor.EndLine - anchor.StartLine + 1,
			Occurrences:     len(nm.Units),
			DuplicatedNodes: nm.MatchedNodes,
		},
	}
	for i, u := range nm.Units {
		o := occurrence(u)
		o.Similarity = math.Round(nm.Similarity[i]*1000) / 1000
		c.Similarity = min(c.Similarity, o.Similarity)
		for _, r := range nm.Unmatched[i] {
			o.UnmatchedLines = append(o.UnmatchedLines, [2]int{r.Start, r.End})
		}
		c.Occurrences[i] = o
	}
	c.Differences, c.Suggestion = differences(nm.Differences)
	// Code present in only some copies has to become optional in a shared
	// function, so a near miss is never a plain reuse.
	c.Suggestion.Action = extractCommon
	return c
}

func blockCandidate(language string, b clone.BlockClone) Candidate {
	first := b.Occurrences[0]
	start, end := first.Unit.Blocks[first.Block][first.Start].SeqStart, first.Unit.Blocks[first.Block][first.End-1].SeqEnd
	c := Candidate{
		Fingerprint: fmt.Sprintf("%s:block:%016x", language, similarity.HashSymbols(first.Unit.Seq[start:end])),
		Language:    language,
		Kind:        blockClone,
		Similarity:  1,
		Occurrences: make([]Occurrence, len(b.Occurrences)),
		Score:       copiesScore(b.Nodes, first.EndLine-first.StartLine+1, len(b.Occurrences)),
	}
	for i, o := range b.Occurrences {
		c.Occurrences[i] = Occurrence{File: o.Unit.File, Name: o.Unit.Name, StartLine: o.StartLine, EndLine: o.EndLine}
	}
	c.Differences, c.Suggestion = differences(b.Differences)
	// A run of statements can only be shared by extracting it.
	c.Suggestion.Action = extractCommon
	if b.Preamble {
		c.Kind = sharedPreamble
		c.Suggestion.Action = extractPre
		c.Suggestion.Pattern, c.Suggestion.GoF = "extractor", "Chain of Responsibility"
	}
	return c
}

func flowCandidate(language string, f Flow) Candidate {
	anchor := f.Entries[0]
	h := fnv.New64a()
	c := Candidate{
		Language:    language,
		Kind:        parallelFlow,
		Similarity:  math.Round(f.Cover*1000) / 1000,
		Differences: []Difference{},
		Suggestion:  Suggestion{Action: extractFlow, Pattern: "generic-pipeline", GoF: "Template Method"},
		Score: Score{
			Lines:           anchor.EndLine - anchor.StartLine + 1,
			Occurrences:     len(f.Entries),
			DuplicatedNodes: f.MatchedNodes,
		},
	}
	for _, e := range f.Entries {
		c.Occurrences = append(c.Occurrences, occurrence(e))
		_, _ = h.Write([]byte(e.File + "#" + e.Name + "\x00"))
	}
	c.Fingerprint = fmt.Sprintf("%s:flow:%016x", language, h.Sum64())
	for _, s := range f.Steps {
		var st Step
		for i, u := range s.Units {
			o := occurrence(u)
			o.Similarity = math.Round(s.Similarity[i]*1000) / 1000
			st.Occurrences = append(st.Occurrences, o)
		}
		c.Score.Nodes += s.Units[0].Nodes
		c.Steps = append(c.Steps, st)
	}
	return c
}

func structCandidate(language string, g clone.StructGroup) Candidate {
	first := g.Structs[0]
	var names strings.Builder
	for _, f := range first.Fields {
		names.WriteString(f.Name + "\x00")
	}
	c := Candidate{
		Fingerprint: fmt.Sprintf("%s:type:%016x", language, hashString(names.String())),
		Language:    language,
		Kind:        duplicateType,
		Similarity:  1,
		Score: Score{
			Nodes:           first.Nodes,
			Lines:           first.EndLine - first.StartLine + 1,
			Occurrences:     len(g.Structs),
			DuplicatedNodes: g.DuplicatedNodes(),
		},
	}
	for _, s := range g.Structs {
		c.Occurrences = append(c.Occurrences, Occurrence{File: s.File, Name: s.Name, StartLine: s.StartLine, EndLine: s.EndLine})
	}
	c.Differences, c.Suggestion = differences(g.Differences)
	return c
}

// enumCandidate reports enums with the same variants as one duplicate
// type; differing enum names are local.
func enumCandidate(language string, g clone.EnumGroup) Candidate {
	first := g.Enums[0]
	var key strings.Builder
	for _, v := range first.Variants {
		key.WriteString(v.Name + v.Payload + "\x00")
	}
	c := Candidate{
		Fingerprint: fmt.Sprintf("%s:enum:%016x", language, hashString(key.String())),
		Language:    language,
		Kind:        duplicateType,
		Similarity:  1,
		Differences: []Difference{},
		Suggestion:  Suggestion{Action: reuseExisting},
		Score: Score{
			Nodes:           first.Nodes,
			Lines:           first.EndLine - first.StartLine + 1,
			Occurrences:     len(g.Enums),
			DuplicatedNodes: g.DuplicatedNodes(),
		},
	}
	names := Difference{Kind: kindIdentifier, Local: true}
	for _, e := range g.Enums {
		c.Occurrences = append(c.Occurrences, Occurrence{File: e.File, Name: e.Name, StartLine: e.StartLine, EndLine: e.EndLine})
		names.Values = append(names.Values, e.Name)
		names.Lines = append(names.Lines, []int{e.StartLine})
	}
	if slices.ContainsFunc(names.Values, func(n string) bool { return n != names.Values[0] }) {
		c.Differences = append(c.Differences, names)
	}
	return c
}

func constCandidate(language string, g clone.ConstGroup) Candidate {
	first := g.Consts[0]
	var occs []Occurrence
	for _, k := range g.Consts {
		occs = append(occs, constOccurrence(k))
	}
	return newCandidate(language, duplicateConst, "const", first.Name+"="+first.Value, Suggestion{Action: reuseExisting},
		copiesScore(first.Nodes, 1, len(g.Consts)), occs)
}
func inlinedCandidate(language string, in clone.InlinedConst) Candidate {
	target := constOccurrence(in.Const)
	occs := []Occurrence{target}
	for _, u := range in.Uses {
		occs = append(occs, Occurrence{File: u.File, Name: u.Name, StartLine: u.Line, EndLine: u.Line})
	}
	c := newCandidate(language, inlinedConst, "inline", in.Const.Name+"="+in.Const.Value, Suggestion{Action: reuseExisting},
		Score{Nodes: 1, Lines: 1, Occurrences: len(in.Uses) + 1, DuplicatedNodes: len(in.Uses)}, occs)
	c.Target = &target
	return c
}

// single builds a candidate for one function to remove or flatten. Its
// score counts the function's nodes as the code the change removes.
func single(language, kind, action string, u unit.Unit) Candidate {
	return newCandidate(language, kind, kind, u.File+"#"+u.Name, Suggestion{Action: action},
		Score{Nodes: u.Nodes, Lines: u.EndLine - u.StartLine + 1, Occurrences: 1, DuplicatedNodes: u.Nodes},
		[]Occurrence{occurrence(u)})
}
func traitCandidate(language string, s simplify.SingleImplTrait) Candidate {
	t, im := s.Trait, s.Impl
	return newCandidate(language, singleImpl, singleImpl, t.File+"#"+t.Name, Suggestion{Action: removeTrait},
		Score{Nodes: t.Nodes, Lines: t.EndLine - t.StartLine + 1, Occurrences: 1, DuplicatedNodes: t.Nodes},
		[]Occurrence{
			{File: t.File, Name: t.Name, StartLine: t.StartLine, EndLine: t.EndLine},
			{File: im.File, Name: im.Trait + " for " + im.Type, StartLine: im.StartLine, EndLine: im.EndLine},
		})
}
func scopeCandidate(language string, g pattern.ScopeGroup) Candidate {
	first := g.Units[0].Scope
	var occs []Occurrence
	for _, u := range g.Units {
		occs = append(occs, Occurrence{File: u.File, Name: u.Name, StartLine: u.Scope.StartLine, EndLine: u.Scope.EndLine})
	}
	return newCandidate(language, setupTeardown, setupTeardown, g.Opens+".."+g.Closes,
		Suggestion{Action: extractScope, Pattern: "closure-helper", GoF: "Template Method"},
		copiesScore(first.Nodes, first.EndLine-first.StartLine+1, len(g.Units)),
		occs)
}

// kindCandidate proposes an enum for strings matched in many functions.
// Its score counts one node per string arm in each extra match.
func kindCandidate(language string, k pattern.StringKind) Candidate {
	first := k.Matches[0]
	var occs []Occurrence
	for _, m := range k.Matches {
		occs = append(occs, Occurrence{File: m.Unit.File, Name: m.Unit.Name, StartLine: m.StartLine, EndLine: m.EndLine})
	}
	c := newCandidate(language, stringKinds, stringKinds, strings.Join(k.Values, "\x00"), Suggestion{Action: introduceEnum, Pattern: "enum"},
		copiesScore(len(k.Values), first.EndLine-first.StartLine+1, len(k.Matches)),
		occs)
	c.Values = k.Values
	return c
}

// paramCandidate proposes one struct for parameters passed together. Its
// score counts one node per parameter in every function but one.
func paramCandidate(language string, g pattern.ParamGroup) Candidate {
	var values []string
	for _, p := range g.Params {
		values = append(values, p.Name+": "+p.Type)
	}
	c := newCandidate(language, paramGroup, paramGroup, strings.Join(values, "\x00"),
		Suggestion{Action: introduceParam, Pattern: "parameter-object"},
		Score{Nodes: len(g.Params), Lines: 1, Occurrences: len(g.Units), DuplicatedNodes: g.Size()}, occurrences(g.Units))
	c.Values = values
	return c
}

// newCandidate builds a candidate without differences. Its fingerprint is
// language:tag:hash(key), where key identifies what the occurrences share.
func newCandidate(language, kind, tag, key string, s Suggestion, score Score, occs []Occurrence) Candidate {
	return Candidate{
		Fingerprint: fmt.Sprintf("%s:%s:%016x", language, tag, hashString(key)),
		Language:    language,
		Kind:        kind,
		Similarity:  1,
		Occurrences: occs,
		Differences: []Difference{},
		Suggestion:  s,
		Score:       score,
	}
}

// copiesScore scores copies of nodes and lines each: every copy but one is
// duplication.
func copiesScore(nodes, lines, copies int) Score {
	return Score{Nodes: nodes, Lines: lines, Occurrences: copies, DuplicatedNodes: nodes * (copies - 1)}
}

// occurrences lists units as occurrences.
func occurrences(units []unit.Unit) []Occurrence {
	out := make([]Occurrence, len(units))
	for i, u := range units {
		out[i] = occurrence(u)
	}
	return out
}

func constOccurrence(k unit.Const) Occurrence {
	return Occurrence{File: k.File, Name: k.Name, StartLine: k.Line, EndLine: k.Line}
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func occurrence(u unit.Unit) Occurrence {
	return Occurrence{File: u.File, Name: u.Name, StartLine: u.StartLine, EndLine: u.EndLine}
}

// differences converts differences and derives the suggestion: every
// non-local difference is a parameter; without parameters, reuse a copy.
func differences(diffs []clone.Difference) ([]Difference, Suggestion) {
	out := []Difference{}
	s := Suggestion{Action: reuseExisting}
	for _, d := range diffs {
		out = append(out, Difference{Kind: leafKindNames[d.Kind], Values: d.Values, Lines: d.Lines, Local: d.Local})
		if !d.Local {
			s.Parameters++
		}
	}
	if s.Parameters > 0 {
		s.Action = extractCommon
	}
	return out, s
}

// WriteJSON writes the report as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Headline is a candidate's one-line summary: its ID and kind, its extent
// and the suggested action.
func Headline(c Candidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", c.ID, c.Kind)
	if c.Kind == nearMiss {
		fmt.Fprintf(&b, " (%.0f%% similar)", 100*c.Similarity)
	}
	fmt.Fprintf(&b, ": %s (%d nodes), %s", size(c), c.Score.Nodes, c.Suggestion.Action)
	switch {
	case c.Suggestion.GoF != "":
		fmt.Fprintf(&b, " (%s; GoF: %s)", c.Suggestion.Pattern, c.Suggestion.GoF)
	case c.Suggestion.Pattern != "":
		fmt.Fprintf(&b, " (%s)", c.Suggestion.Pattern)
	}
	if c.Suggestion.Parameters > 0 {
		fmt.Fprintf(&b, " with %d parameter(s)", c.Suggestion.Parameters)
	}
	return b.String()
}

// size describes a candidate's extent: its lines, and the number of copies
// when there are several.
func size(c Candidate) string {
	if c.Score.Occurrences == 1 {
		return fmt.Sprintf("%d lines", c.Score.Lines)
	}
	return fmt.Sprintf("%d copies of %d lines", c.Score.Occurrences, c.Score.Lines)
}

// WriteText writes the report for a human reading a terminal.
func WriteText(w io.Writer, r Report) error {
	var b strings.Builder
	for _, c := range r.Candidates {
		b.WriteString(Headline(c) + "\n")
		for _, o := range c.Occurrences {
			fmt.Fprintf(&b, "  %s:%d-%d %s", o.File, o.StartLine, o.EndLine, o.Name)
			if len(o.UnmatchedLines) > 0 {
				ranges := make([]string, len(o.UnmatchedLines))
				for i, r := range o.UnmatchedLines {
					ranges[i] = fmt.Sprint(r[0])
					if r[1] != r[0] {
						ranges[i] += fmt.Sprintf("-%d", r[1])
					}
				}
				fmt.Fprintf(&b, " (unmatched: %s)", strings.Join(ranges, ", "))
			}
			if o.Changed {
				b.WriteString(" (changed)")
			}
			b.WriteString("\n")
		}
		for _, s := range c.Steps {
			parts := make([]string, len(s.Occurrences))
			for i, o := range s.Occurrences {
				parts[i] = fmt.Sprintf("%s:%d-%d %s", o.File, o.StartLine, o.EndLine, o.Name)
				if o.Changed {
					parts[i] += " (changed)"
				}
			}
			fmt.Fprintf(&b, "    step: %s\n", strings.Join(parts, " | "))
		}
		if t := c.Target; t != nil {
			fmt.Fprintf(&b, "    target: %s:%d-%d %s\n", t.File, t.StartLine, t.EndLine, t.Name)
		}
		for _, d := range c.Differences {
			label := d.Kind
			if d.Local {
				label += ", local rename"
			}
			fmt.Fprintf(&b, "    %s (%s)\n", strings.Join(d.Values, " | "), label)
		}
	}
	fmt.Fprintf(&b, "%d candidate(s) in %d file(s), %d function(s) analysed; skipped %d generated file(s) and %d test function(s)",
		r.Summary.Candidates, r.Summary.Files, r.Summary.Units, r.Summary.GeneratedFiles, r.Summary.TestUnits)
	if r.Summary.Baselined > 0 {
		fmt.Fprintf(&b, "; %d known candidate(s) in the baseline", r.Summary.Baselined)
	}
	if r.Summary.Ignored > 0 {
		fmt.Fprintf(&b, "; %d declaration(s) marked ddslop:ignore", r.Summary.Ignored)
	}
	h := r.Summary.Health
	fmt.Fprintf(&b, "\nDuplication %.1f%% (%d of %d lines); %d unused functions, %d wrappers, %d single-implementation traits, %d parallel flows\n",
		h.DuplicationPercent, h.DuplicatedLines, h.AnalysedLines, h.UnusedFunctions, h.Wrappers, h.SingleImplTraits, h.ParallelFlows)
	if h.FailOver != nil {
		verdict := "within"
		if h.OverLimit {
			verdict = "over"
		}
		fmt.Fprintf(&b, "Duplication %.1f%% is %s the %.1f%% limit (--fail-over)\n", h.DuplicationPercent, verdict, *h.FailOver)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
