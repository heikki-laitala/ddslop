package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// rules describes each candidate kind as a SARIF rule.
var rules = []sarifRule{
	{ID: exactClone, Short: "Exact copy of a function", Full: "The functions are identical apart from formatting and comments. Keep one copy and call it from the other places."},
	{ID: renamedClone, Short: "Renamed copy of a function", Full: "The functions have the same structure; names, types or values differ. Extract one function taking the differences as parameters."},
	{ID: nearMiss, Short: "Near-miss copy of a function", Full: "The functions are similar, with statements added, removed or changed. Extract the shared code; make the parts only some copies have optional."},
	{ID: blockClone, Short: "Copied block of statements", Full: "A run of statements is repeated inside larger functions. Extract it into a helper; names declared outside the run become parameters."},
	{ID: parallelFlow, Short: "Features running the same pipeline", Full: "Several features call different functions that play the same roles, step by step. Extract one pipeline (a generic handler or service) taking the varying steps as parameters."},
	{ID: duplicateType, Short: "Struct with the same fields as another", Full: "The structs declare the same field names. Keep one type and use it everywhere, converting at the boundary if one is a wire format; differing field types become type parameters or conversions."},
}

type sarifRule struct {
	ID    string
	Short string
	Full  string
}

// WriteSARIF writes the report as SARIF 2.1.0, as read by GitHub code
// scanning. Each candidate is one result: its primary location is the first
// changed occurrence in diff mode and the first occurrence otherwise, and
// the other copies are related locations.
func WriteSARIF(w io.Writer, r Report, toolVersion string) error {
	ruleList := make([]map[string]any, len(rules))
	for i, rule := range rules {
		ruleList[i] = map[string]any{
			"id":                   rule.ID,
			"shortDescription":     map[string]string{"text": rule.Short},
			"fullDescription":      map[string]string{"text": rule.Full},
			"defaultConfiguration": map[string]string{"level": "warning"},
		}
	}
	results := make([]map[string]any, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		primary := 0
		for i, o := range c.Occurrences {
			if o.Changed {
				primary = i
				break
			}
		}
		var related []map[string]any
		var others []string
		for i, o := range c.Occurrences {
			if i == primary {
				continue
			}
			loc := sarifLocation(o)
			loc["id"] = len(related)
			loc["message"] = map[string]string{"text": o.Name}
			related = append(related, loc)
			others = append(others, fmt.Sprintf("%s:%d-%d %s", o.File, o.StartLine, o.EndLine, o.Name))
		}
		message := fmt.Sprintf("Duplicated code (%s): %d copies of %d lines; %s", c.Kind, c.Score.Occurrences, c.Score.Lines, c.Suggestion.Action)
		if c.Suggestion.Parameters > 0 {
			message += fmt.Sprintf(" with %d parameter(s)", c.Suggestion.Parameters)
		}
		message += ". Other copies: " + strings.Join(others, ", ")
		results = append(results, map[string]any{
			"ruleId":              c.Kind,
			"level":               "warning",
			"message":             map[string]string{"text": message},
			"locations":           []map[string]any{sarifLocation(c.Occurrences[primary])},
			"relatedLocations":    related,
			"partialFingerprints": map[string]string{"ddslop/v1": c.Fingerprint},
		})
	}
	log := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []map[string]any{{
			"tool": map[string]any{"driver": map[string]any{
				"name":           "ddslop",
				"version":        toolVersion,
				"informationUri": "https://github.com/heikki-laitala/ddslop",
				"rules":          ruleList,
			}},
			"results": results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

func sarifLocation(o Occurrence) map[string]any {
	return map[string]any{
		"physicalLocation": map[string]any{
			"artifactLocation": map[string]string{"uri": filepath.ToSlash(o.File)},
			"region":           map[string]int{"startLine": o.StartLine, "endLine": o.EndLine},
		},
	}
}
