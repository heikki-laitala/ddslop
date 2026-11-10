package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sarifLog struct {
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name    string `json:"name"`
				Version string `json:"version"`
				Rules   []struct {
					ID string `json:"id"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations           []sarifLocation   `json:"locations"`
			RelatedLocations    []sarifLocation   `json:"relatedLocations"`
			PartialFingerprints map[string]string `json:"partialFingerprints"`
		} `json:"results"`
	} `json:"runs"`
}

type sarifLocation struct {
	PhysicalLocation struct {
		ArtifactLocation struct {
			URI string `json:"uri"`
		} `json:"artifactLocation"`
		Region struct {
			StartLine int `json:"startLine"`
			EndLine   int `json:"endLine"`
		} `json:"region"`
	} `json:"physicalLocation"`
}

func decodeSARIF(t *testing.T, out string) sarifLog {
	t.Helper()
	var l sarifLog
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	return l
}

func TestCheckSARIF(t *testing.T) {
	root := repo(t, map[string]string{"src/orders.rs": orders, "src/users.rs": users})

	code, stdout, _ := run("check", "--format", "sarif", "--min-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	l := decodeSARIF(t, stdout)
	if l.Version != "2.1.0" || len(l.Runs) != 1 || l.Runs[0].Tool.Driver.Name != "ddslop" || l.Runs[0].Tool.Driver.Version == "" {
		t.Fatalf("SARIF header = %+v", l)
	}
	var rules []string
	for _, r := range l.Runs[0].Tool.Driver.Rules {
		rules = append(rules, r.ID)
	}
	if strings.Join(rules, " ") != "exact-clone renamed-clone near-miss block-clone parallel-flow" {
		t.Errorf("rules = %v, want every candidate kind", rules)
	}
	if len(l.Runs[0].Results) != 1 {
		t.Fatalf("got %d results, want 1", len(l.Runs[0].Results))
	}
	res := l.Runs[0].Results[0]
	if res.RuleID != "renamed-clone" || res.Level != "warning" {
		t.Errorf("result rule %q level %q, want renamed-clone warning", res.RuleID, res.Level)
	}
	loc := res.Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "src/orders.rs" || loc.Region.StartLine != 1 || loc.Region.EndLine != 9 {
		t.Errorf("primary location = %+v, want src/orders.rs:1-9", loc)
	}
	if len(res.RelatedLocations) != 1 || res.RelatedLocations[0].PhysicalLocation.ArtifactLocation.URI != "src/users.rs" {
		t.Errorf("related locations = %+v, want src/users.rs", res.RelatedLocations)
	}
	for _, want := range []string{"src/users.rs:1-9", "extract-common"} {
		if !strings.Contains(res.Message.Text, want) {
			t.Errorf("message %q is missing %q", res.Message.Text, want)
		}
	}
	if !strings.HasPrefix(res.PartialFingerprints["ddslop/v1"], "rust:") {
		t.Errorf("partial fingerprints = %v, want the candidate fingerprint", res.PartialFingerprints)
	}
}

func TestCheckSARIFPutsTheChangedCopyFirst(t *testing.T) {
	root := committedRepo(t, map[string]string{"a.rs": syncUsers})
	people := strings.ReplaceAll(syncUsers, "sync_users", "sync_people")
	if err := os.WriteFile(filepath.Join(root, "z.rs"), []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stdout, _ := run("check", "--format", "sarif", "--min-nodes", "20", "--diff", "HEAD", root)

	res := decodeSARIF(t, stdout).Runs[0].Results
	if len(res) != 1 || res[0].Locations[0].PhysicalLocation.ArtifactLocation.URI != "z.rs" {
		t.Errorf("primary location should be the changed copy z.rs: %+v", res)
	}
}
