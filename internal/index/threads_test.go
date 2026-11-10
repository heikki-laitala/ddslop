package index

import (
	"fmt"
	"testing"
)

func TestResolveThreadsDefaultsToHalfTheCores(t *testing.T) {
	for cores, want := range map[int]int{1: 1, 2: 1, 3: 1, 8: 4, 16: 8} {
		if got := ResolveThreads("", cores); got != want {
			t.Errorf("ResolveThreads(\"\", %d) = %d, want %d", cores, got, want)
		}
	}
}

func TestResolveThreadsHonoursValidOverride(t *testing.T) {
	cases := map[string]int{"3": 3, "1": 1, "0": 4, "-2": 4, "many": 4}
	for env, want := range cases {
		if got := ResolveThreads(env, 8); got != want {
			t.Errorf("ResolveThreads(%q, 8) = %d, want %d", env, got, want)
		}
	}
}

func TestParallelSyncMatchesFileCount(t *testing.T) {
	files := map[string]string{}
	for i := range 50 {
		files[fmt.Sprintf("f%02d.rs", i)] = fmt.Sprintf("unit%d", i)
	}
	f := newFixture(t, files)

	stats, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: "v1", Parse: f.fake.parseLocked, Threads: 8})
	if err != nil {
		t.Fatal(err)
	}

	if stats.Parsed != 50 {
		t.Errorf("parsed %d files, want 50", stats.Parsed)
	}
	got := f.files()
	if len(got) != 50 {
		t.Fatalf("indexed %d files, want 50", len(got))
	}
	for i, file := range got {
		if want := fmt.Sprintf("unit%d", i); file.Units[0].Name != want {
			t.Errorf("%s holds %q, want %q", file.Path, file.Units[0].Name, want)
		}
	}
}
