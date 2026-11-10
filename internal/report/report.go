// Package report turns clone groups into refactoring candidates and writes
// them as text or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/clone"
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
	Candidates     int `json:"candidates"`
}

// Candidate is one refactoring candidate: a group of copies, what differs
// between them, and what to do about it.
type Candidate struct {
	ID          string       `json:"id"`
	Fingerprint string       `json:"fingerprint"`
	Language    string       `json:"language"`
	Kind        string       `json:"kind"`
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

// Build turns clone groups, already in ranking order, into a report. The
// summary's candidate count is filled in from the groups.
func Build(language string, groups []clone.Group, summary Summary) Report {
	summary.Candidates = len(groups)
	r := Report{
		Candidates: make([]Candidate, 0, len(groups)),
		Summary:    summary,
	}
	for i, g := range groups {
		r.Candidates = append(r.Candidates, candidate(i+1, language, g))
	}
	return r
}

func candidate(n int, language string, g clone.Group) Candidate {
	first := g.Units[0]
	c := Candidate{
		ID:          fmt.Sprintf("c%d", n),
		Fingerprint: fmt.Sprintf("%s:%016x", language, first.Shape),
		Language:    language,
		Kind:        exactClone,
		Occurrences: make([]Occurrence, len(g.Units)),
		Differences: []Difference{},
		Suggestion:  Suggestion{Action: reuseExisting},
		Score: Score{
			Nodes:           first.Nodes,
			Lines:           first.EndLine - first.StartLine + 1,
			Occurrences:     len(g.Units),
			DuplicatedNodes: g.DuplicatedNodes(),
		},
	}
	for i, u := range g.Units {
		c.Occurrences[i] = Occurrence{File: u.File, Name: u.Name, StartLine: u.StartLine, EndLine: u.EndLine}
	}
	for _, d := range clone.Differences(g) {
		c.Differences = append(c.Differences, Difference{
			Kind:   leafKindNames[d.Kind],
			Values: d.Values,
			Lines:  d.Lines,
			Local:  d.Local,
		})
		if !d.Local {
			c.Suggestion.Parameters++
		}
	}
	if len(c.Differences) > 0 {
		c.Kind = renamedClone
	}
	if c.Suggestion.Parameters > 0 {
		c.Suggestion.Action = extractCommon
	}
	return c
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
		fmt.Fprintf(&b, "%s %s: %d copies of %d lines (%d nodes), %s",
			c.ID, c.Kind, c.Score.Occurrences, c.Score.Lines, c.Score.Nodes, c.Suggestion.Action)
		if c.Suggestion.Parameters > 0 {
			fmt.Fprintf(&b, " with %d parameter(s)", c.Suggestion.Parameters)
		}
		b.WriteString("\n")
		for _, o := range c.Occurrences {
			fmt.Fprintf(&b, "  %s:%d-%d %s\n", o.File, o.StartLine, o.EndLine, o.Name)
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
