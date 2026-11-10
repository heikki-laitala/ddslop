package clone

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/lang/rust"
	"github.com/heikki-laitala/ddslop/internal/unit"
)

func rustUnits(t *testing.T, path, src string) []unit.Unit {
	t.Helper()
	units, err := rust.Units(path, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return units
}

func occurrenceLines(c BlockClone) string {
	var out []string
	for _, o := range c.Occurrences {
		out = append(out, fmt.Sprintf("%s:%d-%d %s", o.Unit.File, o.StartLine, o.EndLine, o.Unit.Name))
	}
	return fmt.Sprint(out)
}

// The same four statements sit inside two otherwise different functions.
// In b.rs the local is renamed, the retry count differs, and the run reads
// a different outer variable.
const blockA = `fn sync_users(client: &Client, users: &[User]) -> Result<(), Error> {
    let started = Instant::now();
    for user in users {
        client.push(user)?;
    }
    let mut attempt = 0;
    let body = client.render(users)?;
    while attempt < 3 {
        attempt += 1;
        client.send(&body)?;
    }
    metrics::record("users", started.elapsed());
    Ok(())
}
`

const blockB = `fn sync_orders(client: &Client, orders: &[Order], extra: u32) -> Result<(), Error> {
    if orders.is_empty() {
        return Ok(());
    }
    let mut tries = 0;
    let body = client.render(orders)?;
    while tries < 5 {
        tries += 1;
        client.send(&body)?;
    }
    audit(extra);
    Ok(())
}
`

func TestBlockClonesFindRunInsideDifferentFunctions(t *testing.T) {
	units := append(rustUnits(t, "a.rs", blockA), rustUnits(t, "b.rs", blockB)...)

	got := BlockClones(units, BlockSize{MinNodes: 20}, nil)

	if len(got) != 1 {
		t.Fatalf("got %d block clones, want 1", len(got))
	}
	c := got[0]
	if want := "[a.rs:6-11 sync_users b.rs:5-10 sync_orders]"; occurrenceLines(c) != want {
		t.Errorf("occurrences = %s, want %s", occurrenceLines(c), want)
	}
	byValues := map[string]Difference{}
	for _, d := range c.Differences {
		byValues[fmt.Sprint(d.Values)] = d
	}
	if d, ok := byValues["[attempt tries]"]; !ok || !d.Local {
		t.Errorf("attempt | tries should be a local rename (declared inside the run): %+v", c.Differences)
	}
	if d, ok := byValues["[3 5]"]; !ok || d.Local {
		t.Errorf("3 | 5 should be a parameter: %+v", c.Differences)
	}
	// users is declared by sync_users, but outside the run: a parameter of
	// the extracted helper, not a local rename.
	if d, ok := byValues["[users orders]"]; !ok || d.Local {
		t.Errorf("users | orders should be a parameter (declared outside the run): %+v", c.Differences)
	}
}

func TestBlockClonesIgnoreSmallRuns(t *testing.T) {
	units := append(rustUnits(t, "a.rs", blockA), rustUnits(t, "b.rs", blockB)...)
	if got := BlockClones(units, BlockSize{MinNodes: 1000}, nil); len(got) != 0 {
		t.Errorf("got %d block clones above any run's size, want 0", len(got))
	}
}

func TestBlockClonesSkipWholeFunctionCopies(t *testing.T) {
	units := append(rustUnits(t, "a.rs", blockA), rustUnits(t, "c.rs", blockA)...)
	var reported [][]unit.Unit
	for _, g := range Groups(units, 20) {
		reported = append(reported, g.Units)
	}
	if got := BlockClones(units, BlockSize{MinNodes: 20}, reported); len(got) != 0 {
		t.Errorf("whole-function copies were reported as block clones: %s", occurrenceLines(got[0]))
	}
}

func TestBlockClonesFindRepeatWithinOneFunction(t *testing.T) {
	src := `fn twice(client: &Client) -> Result<(), Error> {
    let first = client.render(1)?;
    client.send(&first)?;
    client.flush()?;
    log("between");
    let second = client.render(2)?;
    client.send(&second)?;
    client.flush()?;
    Ok(())
}
`
	units := rustUnits(t, "a.rs", src)
	// Even when the function is already reported with others, a repeat
	// inside it is news.
	got := BlockClones(units, BlockSize{MinNodes: 15}, [][]unit.Unit{units})
	if len(got) != 1 || occurrenceLines(got[0]) != "[a.rs:2-4 twice a.rs:6-8 twice]" {
		t.Errorf("block clones = %d, first %v; want one at lines 2-4 and 6-8", len(got), got)
	}
}

func TestBlockClonesNeverOverlap(t *testing.T) {
	src := `fn retry(client: &Client) {
    client.send(1).unwrap_or_default();
    client.send(1).unwrap_or_default();
    client.send(1).unwrap_or_default();
    client.send(1).unwrap_or_default();
}
`
	got := BlockClones(rustUnits(t, "a.rs", src), BlockSize{MinNodes: 10}, nil)
	if len(got) == 0 {
		t.Fatal("found no repeats; the test needs some")
	}
	for _, c := range got {
		var spans [][2]int
		for _, o := range c.Occurrences {
			spans = append(spans, [2]int{o.Start, o.End})
		}
		slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
		for i := 1; i < len(spans); i++ {
			if spans[i][0] < spans[i-1][1] {
				t.Errorf("overlapping occurrences %v", spans)
			}
		}
		if len(c.Occurrences) < 2 {
			t.Errorf("block clone with %d occurrences", len(c.Occurrences))
		}
	}
}

func TestBlockClonesSkipRunsOfFunctionsReportedTogether(t *testing.T) {
	units := append(rustUnits(t, "a.rs", blockA), rustUnits(t, "b.rs", blockB)...)

	if got := BlockClones(units, BlockSize{MinNodes: 20}, [][]unit.Unit{units}); len(got) != 0 {
		t.Errorf("got %d block clones between functions already reported together, want 0", len(got))
	}
	other := [][]unit.Unit{{units[0]}, {units[1]}}
	if got := BlockClones(units, BlockSize{MinNodes: 20}, other); len(got) != 1 {
		t.Errorf("got %d block clones between functions reported apart, want 1", len(got))
	}
}

// guarded returns n methods that each start with the same small guard and
// then do something of their own.
func guarded(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `fn op%d(&mut self) -> Result<(), NotCopied> {
    if !self.usable {
        return Err(NotCopied::Unavailable("unusable".to_owned()));
    }
    self.usable = false;
    self.step%d(%d)
}
`, i, i, i)
	}
	return b.String()
}

func TestBlockClonesKeepSmallRunsCopiedManyTimes(t *testing.T) {
	size := BlockSize{MinNodes: 100, FrequentNodes: 20, FrequentDuplicated: 300}
	many := BlockClones(rustUnits(t, "a.rs", guarded(12)), size, nil)
	if len(many) != 1 || len(many[0].Occurrences) != 12 {
		t.Fatalf("got %d block clones, want the guard with 12 occurrences", len(many))
	}
	if many[0].Nodes >= size.MinNodes || many[0].Nodes < size.FrequentNodes {
		t.Fatalf("the guard has %d nodes; the test needs it between %d and %d", many[0].Nodes, size.FrequentNodes, size.MinNodes)
	}
	if few := BlockClones(rustUnits(t, "a.rs", guarded(2)), size, nil); len(few) != 0 {
		t.Errorf("a small run copied twice was reported: %s", occurrenceLines(few[0]))
	}
}
