package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// fakeParse records which files it parsed and turns each line of a file
// into a unit named after the line.
type fakeParse struct {
	mu     sync.Mutex
	parsed []string
}

// parseLocked is parse, safe to call from several goroutines.
func (f *fakeParse) parseLocked(path string, src []byte) (Parsed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parse(path, src)
}

func (f *fakeParse) parse(path string, src []byte) (Parsed, error) {
	f.parsed = append(f.parsed, path)
	text := string(src)
	if strings.HasPrefix(text, "GENERATED") {
		return Parsed{Generated: true}, nil
	}
	if strings.HasPrefix(text, "BROKEN") {
		return Parsed{}, errors.New("cannot parse " + path)
	}
	var units []unit.Unit
	for i, line := range strings.Split(strings.TrimSpace(text), "\n") {
		units = append(units, unit.Unit{
			File: path, Name: line, StartLine: i + 1, EndLine: i + 1,
			Shape: uint64(len(line)), Nodes: 10 * len(line), Test: line == "test",
			Leaves: []unit.Leaf{{Kind: unit.Identifier, Text: line, Line: i + 1, Declares: true}},
		})
	}
	return Parsed{Units: units}, nil
}

type fixture struct {
	t     *testing.T
	root  string
	db    *DB
	fake  *fakeParse
	paths []string
}

func newFixture(t *testing.T, files map[string]string) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir(), fake: &fakeParse{}}
	for name, content := range files {
		f.write(name, content)
		f.paths = append(f.paths, name)
	}
	f.db = f.open()
	return f
}

func (f *fixture) open() *DB {
	f.t.Helper()
	db, err := Open(filepath.Join(f.root, ".ddslop", "index.db"))
	if err != nil {
		f.t.Fatalf("Open: %v", err)
	}
	f.t.Cleanup(func() { _ = db.Close() })
	return db
}

// write creates or changes a file and moves its mtime forward, so the change
// is visible even within the filesystem's timestamp resolution.
func (f *fixture) write(name, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		f.t.Fatal(err)
	}
	var mtime time.Time
	if info, err := os.Stat(p); err == nil {
		mtime = info.ModTime().Add(2 * time.Second)
	} else {
		mtime = time.Now().Add(-time.Hour)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sync(version string) Stats {
	f.t.Helper()
	f.fake.parsed = nil
	stats, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: version, Parse: f.fake.parse})
	if err != nil {
		f.t.Fatalf("Sync: %v", err)
	}
	return stats
}

func (f *fixture) files() []File {
	f.t.Helper()
	files, err := f.db.Files()
	if err != nil {
		f.t.Fatalf("Files: %v", err)
	}
	return files
}

func TestFirstSyncParsesEveryFileAndStoresUnits(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha\ntest", "b.rs": "beta", "gen.rs": "GENERATED"})

	stats := f.sync("v1")

	if stats.Parsed != 3 || stats.Unchanged != 0 || stats.Deleted != 0 {
		t.Errorf("stats = %+v, want 3 parsed", stats)
	}
	files := f.files()
	if len(files) != 3 || files[0].Path != "a.rs" || files[1].Path != "b.rs" || files[2].Path != "gen.rs" {
		t.Fatalf("files = %+v, want a.rs, b.rs, gen.rs in order", files)
	}
	a := files[0]
	if len(a.Units) != 2 {
		t.Fatalf("a.rs units = %+v, want 2", a.Units)
	}
	want := unit.Unit{
		File: "a.rs", Name: "test", StartLine: 2, EndLine: 2, Shape: 4, Nodes: 40, Test: true,
		Leaves: []unit.Leaf{{Kind: unit.Identifier, Text: "test", Line: 2, Declares: true}},
	}
	got := a.Units[1]
	if got.File != want.File || got.Name != want.Name || got.StartLine != want.StartLine ||
		got.EndLine != want.EndLine || got.Shape != want.Shape || got.Nodes != want.Nodes ||
		got.Test != want.Test || len(got.Leaves) != 1 || got.Leaves[0] != want.Leaves[0] {
		t.Errorf("stored unit = %+v, want %+v", got, want)
	}
	if !files[2].Generated || len(files[2].Units) != 0 {
		t.Errorf("gen.rs = %+v, want generated with no units", files[2])
	}
}

func TestSecondSyncSkipsUnchangedFiles(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
	f.sync("v1")

	stats := f.sync("v1")

	if stats.Parsed != 0 || stats.Unchanged != 2 {
		t.Errorf("stats = %+v, want 0 parsed, 2 unchanged", stats)
	}
	if len(f.fake.parsed) != 0 {
		t.Errorf("parsed %v, want nothing", f.fake.parsed)
	}
	if files := f.files(); len(files) != 2 || files[0].Units[0].Name != "alpha" {
		t.Errorf("files after second sync = %+v", files)
	}
}

func TestModifiedFileIsParsedAgain(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
	f.sync("v1")
	f.write("b.rs", "gamma")

	stats := f.sync("v1")

	if stats.Parsed != 1 || stats.Unchanged != 1 || len(f.fake.parsed) != 1 || f.fake.parsed[0] != "b.rs" {
		t.Errorf("stats = %+v, parsed %v; want only b.rs parsed", stats, f.fake.parsed)
	}
	if files := f.files(); files[1].Units[0].Name != "gamma" {
		t.Errorf("b.rs units = %+v, want the new content", files[1].Units)
	}
}

func TestSameMtimeButDifferentSizeIsParsedAgain(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha"})
	f.sync("v1")
	p := filepath.Join(f.root, "a.rs")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("alphabet"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	if stats := f.sync("v1"); stats.Parsed != 1 {
		t.Errorf("stats = %+v, want the resized file parsed", stats)
	}
}

func TestDeletedFileIsRemoved(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
	f.sync("v1")
	f.paths = []string{"a.rs"}

	stats := f.sync("v1")

	if stats.Deleted != 1 || stats.Unchanged != 1 || stats.Parsed != 0 {
		t.Errorf("stats = %+v, want 1 deleted, 1 unchanged", stats)
	}
	if files := f.files(); len(files) != 1 || files[0].Path != "a.rs" {
		t.Errorf("files = %+v, want only a.rs", files)
	}
}

func TestVersionChangeParsesEverythingAgain(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
	f.sync("v1")

	stats := f.sync("v2")

	if stats.Parsed != 2 || stats.Unchanged != 0 {
		t.Errorf("stats = %+v, want everything parsed after a version change", stats)
	}
	if again := f.sync("v2"); again.Parsed != 0 {
		t.Errorf("stats after re-sync = %+v, want the new version remembered", again)
	}
}

func TestIndexPersistsAcrossReopen(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha"})
	f.sync("v1")
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db = f.open()

	if stats := f.sync("v1"); stats.Parsed != 0 || stats.Unchanged != 1 {
		t.Errorf("stats after reopen = %+v, want the file unchanged", stats)
	}
}

func TestParseErrorFailsSyncAndKeepsPreviousIndex(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
	f.sync("v1")
	f.write("a.rs", "delta")
	f.write("b.rs", "BROKEN")

	_, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: "v1", Parse: f.fake.parse})

	if err == nil || !strings.Contains(err.Error(), "b.rs") {
		t.Fatalf("Sync error = %v, want an error naming b.rs", err)
	}
	if files := f.files(); files[0].Units[0].Name != "alpha" {
		t.Errorf("a.rs = %+v, want the previous index kept after a failed sync", files[0].Units)
	}
}

func TestMissingFileFailsSync(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha"})
	f.paths = append(f.paths, "missing.rs")

	if _, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: "v1", Parse: f.fake.parse}); err == nil {
		t.Error("Sync with a missing file returned no error")
	}
}

func TestTraitsAreStored(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha"})
	parse := func(path string, src []byte) (Parsed, error) {
		return Parsed{Generated: true, Traits: []string{"LedgerApi", "HealthApi"}}, nil
	}
	if _, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: "v1", Parse: parse}); err != nil {
		t.Fatal(err)
	}

	files := f.files()

	if len(files) != 1 || fmt.Sprint(files[0].Traits) != "[LedgerApi HealthApi]" || !files[0].Generated {
		t.Errorf("files = %+v, want a.rs generated with its two traits", files)
	}
}
