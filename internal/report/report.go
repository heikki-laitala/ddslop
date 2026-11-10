// Package report turns clone groups into refactoring candidates and writes
// them as text or JSON.
package report

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/clone"
	"github.com/heikki-laitala/ddslop/internal/similarity"
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

// Build turns clone groups, near misses and block clones into a report.
// Candidates are ranked by duplicated nodes, largest first; on ties, they
// keep this order: groups, near misses, block clones. The summary's
// candidate count is filled in.
func Build(language string, groups []clone.Group, nearMisses []clone.NearMiss, blocks []clone.BlockClone, summary Summary) Report {
	candidates := make([]Candidate, 0, len(groups)+len(nearMisses)+len(blocks))
	for _, g := range groups {
		candidates = append(candidates, groupCandidate(language, g))
	}
	for _, nm := range nearMisses {
		candidates = append(candidates, nearMissCandidate(language, nm))
	}
	for _, b := range blocks {
		candidates = append(candidates, blockCandidate(language, b))
	}
	candidates = slices.DeleteFunc(candidates, mostlyParameters)
	slices.SortStableFunc(candidates, func(a, b Candidate) int {
		return cmp.Compare(b.Score.DuplicatedNodes, a.Score.DuplicatedNodes)
	})
	for i := range candidates {
		candidates[i].ID = fmt.Sprintf("c%d", i+1)
	}
	summary.Candidates = len(candidates)
	return Report{Candidates: candidates, Summary: summary}
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
		for i, o := range c.Occurrences {
			if touches(o.File, o.StartLine, o.EndLine) {
				c.Occurrences[i].Changed = true
				changed = true
			}
		}
		return changed
	})
	r.Summary.Diff = base
	return r
}

// minNodesPerParameter is the least code a candidate needs per parameter.
// Below it, extracting the shared code would turn most of its body into
// arguments: the copies share a shape, not logic. Measured on a 735k-line
// Rust codebase: every audited real target had 24 or more nodes per
// parameter, while Debug and Display impls, match tables mapping variants
// to different strings, and same-shaped one-line delegations had 12.7-19.8.
const minNodesPerParameter = 20

// mostlyParameters reports whether a candidate has fewer than
// minNodesPerParameter nodes per parameter.
func mostlyParameters(c Candidate) bool {
	return c.Score.Nodes < minNodesPerParameter*c.Suggestion.Parameters
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
		Score: Score{
			Nodes:           b.Nodes,
			Lines:           first.EndLine - first.StartLine + 1,
			Occurrences:     len(b.Occurrences),
			DuplicatedNodes: b.Nodes * (len(b.Occurrences) - 1),
		},
	}
	for i, o := range b.Occurrences {
		c.Occurrences[i] = Occurrence{File: o.Unit.File, Name: o.Unit.Name, StartLine: o.StartLine, EndLine: o.EndLine}
	}
	c.Differences, c.Suggestion = differences(b.Differences)
	// A run of statements can only be shared by extracting it.
	c.Suggestion.Action = extractCommon
	return c
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

// WriteText writes the report for a human reading a terminal.
func WriteText(w io.Writer, r Report) error {
	var b strings.Builder
	for _, c := range r.Candidates {
		fmt.Fprintf(&b, "%s %s", c.ID, c.Kind)
		if c.Kind == nearMiss {
			fmt.Fprintf(&b, " (%.0f%% similar)", 100*c.Similarity)
		}
		fmt.Fprintf(&b, ": %d copies of %d lines (%d nodes), %s",
			c.Score.Occurrences, c.Score.Lines, c.Score.Nodes, c.Suggestion.Action)
		if c.Suggestion.Parameters > 0 {
			fmt.Fprintf(&b, " with %d parameter(s)", c.Suggestion.Parameters)
		}
		b.WriteString("\n")
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
		for _, d := range c.Differences {
			label := d.Kind
			if d.Local {
				label += ", local rename"
			}
			fmt.Fprintf(&b, "    %s (%s)\n", strings.Join(d.Values, " | "), label)
		}
	}
	fmt.Fprintf(&b, "%d candidate(s) in %d file(s), %d function(s) analysed; skipped %d generated file(s) and %d test function(s)\n",
		r.Summary.Candidates, r.Summary.Files, r.Summary.Units, r.Summary.GeneratedFiles, r.Summary.TestUnits)
	_, err := io.WriteString(w, b.String())
	return err
}
