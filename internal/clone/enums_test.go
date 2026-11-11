package clone

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func en(file, name string, variants ...string) unit.Enum {
	e := unit.Enum{File: file, Name: name, StartLine: 1, EndLine: len(variants) + 2, Nodes: 5 * len(variants)}
	for _, v := range variants {
		n, p, _ := strings.Cut(v, "(")
		if p != "" {
			p = "(" + p
		}
		e.Variants = append(e.Variants, unit.Variant{Name: n, Payload: p})
	}
	return e
}

func TestEnumsGroupsEnumsWithTheSameVariants(t *testing.T) {
	enums := []unit.Enum{
		en("sandbox.rs", "SandboxState", "Starting", "Running", "Exited"),
		en("runs.rs", "RunStatus", "Exited", "Starting", "Running"), // any order
		en("sweep_a.rs", "NotSwept", "HistoryUnavailable(String)", "Store(StoreFault)"),
		en("sweep_b.rs", "NotSwept", "HistoryUnavailable(String)", "Store(StoreFault)"),
		en("other.rs", "Different", "HistoryUnavailable(String)", "Store(Fault)"), // another payload
		en("volume.rs", "VolumeType", "Bind", "Volume"),                           // two bare variants: coincidence
		en("mount.rs", "MountType", "Bind", "Volume"),
		en("ports_a.rs", "PortAccess", "Public", "SignedIn"), // ...unless the name is the same too
		en("ports_b.rs", "PortAccess", "Public", "SignedIn"),
	}
	var got []string
	for _, g := range Enums(enums) {
		var members []string
		for _, e := range g.Enums {
			members = append(members, fmt.Sprintf("%s %s", e.File, e.Name))
		}
		got = append(got, strings.Join(members, " | "))
	}
	want := []string{
		"runs.rs RunStatus | sandbox.rs SandboxState",
		"ports_a.rs PortAccess | ports_b.rs PortAccess", // ties in size: by file
		"sweep_a.rs NotSwept | sweep_b.rs NotSwept",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("groups:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Groups of equal size whose first enums share a file are ordered by line,
// never by map iteration.
func TestEnumsOrderIsDeterministic(t *testing.T) {
	a1 := en("project.rs", "Bucket", "Hour", "Day", "Week")
	a1.StartLine, a1.EndLine = 47, 51
	b1 := en("project.rs", "Scope", "Token", "Account", "Org")
	b1.StartLine, b1.EndLine = 64, 68
	enums := []unit.Enum{a1, b1, en("reporting.rs", "Bucket", "Hour", "Day", "Week"), en("reporting.rs", "Scope", "Token", "Account", "Org")}
	for range 50 {
		got := Enums(enums)
		if len(got) != 2 || got[0].Enums[0].StartLine != 47 || got[1].Enums[0].StartLine != 64 {
			t.Fatalf("groups start at %d and %d, want 47 then 64", got[0].Enums[0].StartLine, got[1].Enums[0].StartLine)
		}
	}
}
