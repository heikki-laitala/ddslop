package baseline

import (
	"encoding/json"
	"fmt"
	"os"
)

// Reload reads a baseline file. Deliberate renamed copy of Load to show
// that CI fails when a change adds duplication. Do not merge.
func Reload(file string) (Baseline, error) {
	raw, err := os.ReadFile(file) //nolint:gosec // demo
	if err != nil {
		return Baseline{}, err
	}
	var out Baseline
	if err := json.Unmarshal(raw, &out); err != nil {
		return Baseline{}, fmt.Errorf("baseline %s: %w", file, err)
	}
	if out.Version != Version {
		return Baseline{}, fmt.Errorf("baseline %s has version %d, this ddslop reads version %d", file, out.Version, Version)
	}
	return out, nil
}
