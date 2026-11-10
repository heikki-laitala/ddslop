package flow

import (
	"fmt"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/lang/rust"
)

const features = `impl FeatureApi for Service {
    async fn create(&self, id: u64) -> Result<u64, E> {
        let v = validate_create(id)?;
        let f = fingerprint_create(v);
        persist_create(f)
    }

    async fn update(&self, key: u64) -> Result<u64, E> {
        let checked = validate_update(key)?;
        let print = fingerprint_update(checked);
        persist_update(print)
    }

    async fn health(&self) -> u64 {
        1
    }
}

fn validate_create(id: u64) -> Result<u64, E> {
    if id == 0 {
        return Err(E::Zero);
    }
    if id > 1000 {
        return Err(E::Large);
    }
    Ok(id)
}

fn validate_update(key: u64) -> Result<u64, E> {
    if key == 0 {
        return Err(E::Missing);
    }
    if key > 5000 {
        return Err(E::TooLarge);
    }
    Ok(key)
}

fn fingerprint_create(v: u64) -> String {
    let mut out = String::new();
    for b in v.to_be_bytes() {
        out.push_str(&format!("{b:02x}"));
    }
    out
}

fn fingerprint_update(v: u64) -> String {
    let mut text = String::new();
    for byte in v.to_be_bytes() {
        text.push_str(&format!("{byte:02x}"));
    }
    text
}

fn persist_create(f: String) -> Result<u64, E> {
    let key = format!("create:{f}");
    store_write(&key)?;
    Ok(f.len() as u64)
}

fn persist_update(f: String) -> Result<u64, E> {
    let key = format!("update:{f}");
    store_write(&key)?;
    Ok(f.len() as u64)
}

fn store_write(key: &str) -> Result<(), E> {
    if key.is_empty() {
        return Err(E::Empty);
    }
    Ok(())
}
`

func featureGraph(t *testing.T) (*Graph, []int) {
	t.Helper()
	units, err := rust.Units("svc/src/lib.rs", []byte(features))
	if err != nil {
		t.Fatal(err)
	}
	g := Build(nil, units)
	return g, g.Entries(map[string]bool{"FeatureApi": true})
}

var testOptions = ParallelOptions{Depth: 6, MinNodes: 10, MinSteps: 3, MinCover: 0.2, MinStepSim: 0.7, SharedFraction: 1}

func names(g *Graph, ids []int) []string {
	var out []string
	for _, id := range ids {
		if id < 0 {
			out = append(out, "-")
			continue
		}
		out = append(out, g.Units[id].Name)
	}
	return out
}

func TestParallelFindsFeaturesRunningTheSamePipeline(t *testing.T) {
	g, entries := featureGraph(t)

	flows := g.Parallel(entries, testOptions)

	if len(flows) != 1 {
		t.Fatalf("got %d parallel flows, want 1", len(flows))
	}
	f := flows[0]
	if got := fmt.Sprint(names(g, f.Entries)); got != "[Service::create Service::update]" {
		t.Errorf("entries = %s", got)
	}
	var steps []string
	for _, s := range f.Steps {
		steps = append(steps, fmt.Sprint(names(g, s.Units)))
		if s.Similarity[0] != 1 || s.Similarity[1] < 0.7 {
			t.Errorf("step %v similarities = %v, want 1 for the anchor and >= 0.7", names(g, s.Units), s.Similarity)
		}
	}
	want := "[[Service::create Service::update] [validate_create validate_update] [fingerprint_create fingerprint_update] [persist_create persist_update]]"
	if fmt.Sprint(steps) != want {
		t.Errorf("steps = %v\nwant %s (store_write is reused by both, not parallel)", steps, want)
	}
	if f.MatchedNodes == 0 || f.Cover < 0.8 {
		t.Errorf("matched nodes = %d, cover = %v; want most of the code aligned", f.MatchedNodes, f.Cover)
	}
}

func TestParallelNeedsEnoughAlignedSteps(t *testing.T) {
	g, entries := featureGraph(t)
	opts := testOptions
	opts.MinSteps = 5

	if flows := g.Parallel(entries, opts); len(flows) != 0 {
		t.Errorf("got %d flows with only 4 aligned steps and MinSteps 5", len(flows))
	}
}

func TestSharedInfrastructureNeedsMoreThanTwoFeatures(t *testing.T) {
	// With 3 entry points, a quarter of them is less than one feature: a
	// step one feature uses must not count as shared infrastructure.
	g, entries := featureGraph(t)
	opts := testOptions
	opts.SharedFraction = 0.25

	if flows := g.Parallel(entries, opts); len(flows) != 1 || len(flows[0].Steps) != 4 {
		t.Errorf("got %d flows with SharedFraction 0.25, want the create/update flow with 4 steps", len(flows))
	}
}
