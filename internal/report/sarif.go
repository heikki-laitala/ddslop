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
	{ID: duplicateConst, Short: "Constant declared again with the same name and value", Full: "The same constant is declared in several places. Keep one declaration, in a module the users share, and import it."},
	{ID: inlinedConst, Short: "Constant's value written inline", Full: "A string literal repeats the value of a constant declared in the same crate. Use the constant (the target) instead of the literal."},
	{ID: deadCode, Short: "Public function nothing uses", Full: "The function is public in a crate that is not published, and its name appears nowhere else in the code: no call, import, macro or comment. Delete it, unless it is meant for code outside the repository."},
	{ID: passThrough, Short: "Wrapper that only forwards its parameters", Full: "The function's whole body calls another function (the target) with its own parameters, unchanged. Call the target directly and delete the wrapper, or rename the target if the wrapper's name is the better one."},
	{ID: singleImpl, Short: "Trait with a single implementation", Full: "The trait is implemented once, for a type the code declares, and no test double implements it. Make its methods the type's own and use the type directly, unless a second implementation is planned."},
	{ID: sharedPreamble, Short: "Shared preamble that can exit early", Full: "Several functions open with the same check, which can return early (sign-in, permission, input parsing). Move it into one guard: in request handlers, an extractor or middleware that hands the handler what it needs; elsewhere, a function returning the checked value or the early result."},
	{ID: setupTeardown, Short: "Same setup and teardown around different work", Full: "Several functions open the same kind of value at their start (a transaction, a lease) and close it at their end. Write one helper that opens the value, runs the work passed in as a closure and closes it, so no copy can forget the teardown; or an RAII guard that closes on drop."},
	{ID: stringKinds, Short: "Strings used as kinds", Full: "Several functions match on the same set of string literals: kinds, outcomes or states passed around as strings. Introduce an enum, parse the string into it once at the boundary (FromStr or TryFrom) and give it an as_str for the way back, then match on the enum."},
	{ID: paramGroup, Short: "Parameters passed together through many functions", Full: "Several functions take the same parameters (names and types). Group them in one struct and pass that instead; when one of them is a connection or context, consider a type holding it with the functions as methods."},
	{ID: conventionDrift, Short: "Code breaking a convention its peers follow", Full: "The code does a job, such as checking an error, in another form than most of the codebase (the convention, in the suggestion's pattern). Write it the way the target does, unless this place needs the other form; then say why in a ddslop:ignore comment."},
}

// ruleShort maps a candidate kind to its rule's short description.
var ruleShort = func() map[string]string {
	m := map[string]string{}
	for _, r := range rules {
		m[r.ID] = r.Short
	}
	return m
}()

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
		message := fmt.Sprintf("%s: %s; %s", ruleShort[c.Kind], size(c), c.Suggestion.Action)
		if c.Suggestion.Parameters > 0 {
			message += fmt.Sprintf(" with %d parameter(s)", c.Suggestion.Parameters)
		}
		if len(others) > 0 {
			message += ". Other occurrences: " + strings.Join(others, ", ")
		}
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
