package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heikki-laitala/ddslop/internal/testutil"

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

// A second sync parses only the files that changed since the first, or
// everything when the parser's version changed; a third sync after it
// parses nothing.
func TestASyncParsesOnlyWhatChanged(t *testing.T) {
	for name, tc := range map[string]struct {
		change  func(f *fixture)
		version string
		stats   string
		parsed  []string
		files   []string
	}{
		"nothing changed": {
			version: "v1",
			stats:   "0 parsed, 2 unchanged, 0 deleted",
			files:   []string{"a.rs alpha", "b.rs beta"},
		},
		"a modified file": {
			change:  func(f *fixture) { f.write("b.rs", "gamma") },
			version: "v1",
			stats:   "1 parsed, 1 unchanged, 0 deleted",
			parsed:  []string{"b.rs"},
			files:   []string{"a.rs alpha", "b.rs gamma"},
		},
		"a deleted file": {
			change:  func(f *fixture) { f.paths = []string{"a.rs"} },
			version: "v1",
			stats:   "0 parsed, 1 unchanged, 1 deleted",
			files:   []string{"a.rs alpha"},
		},
		"a new version": {
			version: "v2",
			stats:   "2 parsed, 0 unchanged, 0 deleted",
			parsed:  []string{"a.rs", "b.rs"},
			files:   []string{"a.rs alpha", "b.rs beta"},
		},
		"a reopened index": {
			change: func(f *fixture) {
				if err := f.db.Close(); err != nil {
					f.t.Fatal(err)
				}
				f.db = f.open()
			},
			version: "v1",
			stats:   "0 parsed, 2 unchanged, 0 deleted",
			files:   []string{"a.rs alpha", "b.rs beta"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
			f.sync("v1")
			if tc.change != nil {
				tc.change(f)
			}
			s := f.sync(tc.version)
			if got := fmt.Sprintf("%d parsed, %d unchanged, %d deleted", s.Parsed, s.Unchanged, s.Deleted); got != tc.stats {
				t.Errorf("stats = %s, want %s", got, tc.stats)
			}
			parsed := slices.Sorted(slices.Values(f.fake.parsed))
			testutil.Lines(t, "parsed", parsed, tc.parsed)
			files := testutil.Format(f.files(), func(file File) string {
				return file.Path + " " + file.Units[0].Name
			})
			testutil.Lines(t, "files", files, tc.files)
			if again := f.sync(tc.version); again.Parsed != 0 {
				t.Errorf("stats after re-sync = %+v, want nothing parsed", again)
			}
		})
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

func TestParseErrorFailsSyncAndKeepsPreviousIndex(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha", "b.rs": "beta"})
	f.sync("v1")
	f.write("a.rs", "delta")
	f.write("b.rs", "BROKEN")

	_, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: "v1", Parse: f.fake.parse})

	if err == nil || !strings.Contains(err.Error(), "b.rs") { // ddslop:ignore asserts the message the user reads
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

func TestTraitsAndImportsAreStored(t *testing.T) {
	f := newFixture(t, map[string]string{"a.rs": "alpha"})
	parse := func(path string, src []byte) (Parsed, error) {
		return Parsed{Generated: true, Traits: []string{"LedgerApi", "HealthApi"}, Imports: map[string]string{"fill": "super::helpers::fill"}, TestModules: []string{"real_ledger"}}, nil
	}
	if _, err := f.db.Sync(Options{Root: f.root, Paths: f.paths, Version: "v1", Parse: parse}); err != nil {
		t.Fatal(err)
	}

	files := f.files()

	if len(files) != 1 || fmt.Sprint(files[0].Traits) != "[LedgerApi HealthApi]" || !files[0].Generated {
		t.Errorf("files = %+v, want a.rs generated with its two traits", files)
	}
	if got := files[0].Imports["fill"]; got != "super::helpers::fill" {
		t.Errorf("imports = %v, want fill -> super::helpers::fill", files[0].Imports)
	}
	if fmt.Sprint(files[0].TestModules) != "[real_ledger]" {
		t.Errorf("test modules = %v, want [real_ledger]", files[0].TestModules)
	}
}

func TestHealthHistoryRecordsOnlyChanges(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	record := func(minutes int, version, revision, data string) {
		t.Helper()
		if err := db.RecordHealth(HealthEntry{At: at.Add(time.Duration(minutes) * time.Minute), Version: version, Revision: revision, Data: []byte(data)}); err != nil {
			t.Fatal(err)
		}
	}
	record(0, "v1", "a1", `{"duplication_percent":7.6}`)
	record(1, "v1", "a1", `{"duplication_percent":7.6}`) // an agent checking again: nothing new
	record(2, "v1", "b2", `{"duplication_percent":7.6}`) // a new commit
	record(3, "v1", "b2", `{"duplication_percent":6.9}`) // a cleanup
	record(4, "v2", "b2", `{"duplication_percent":6.9}`) // an upgrade

	got, err := db.HealthHistory(10)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range got {
		lines = append(lines, fmt.Sprintf("%s %s %s %s", e.At.Format("15:04"), e.Version, e.Revision, e.Data))
	}
	want := []string{
		`10:00 v1 a1 {"duplication_percent":7.6}`,
		`10:02 v1 b2 {"duplication_percent":7.6}`,
		`10:03 v1 b2 {"duplication_percent":6.9}`,
		`10:04 v2 b2 {"duplication_percent":6.9}`,
	}
	testutil.Lines(t, "history", lines, want)
	if last, _ := db.HealthHistory(2); len(last) != 2 || last[1].Version != "v2" {
		t.Errorf("HealthHistory(2) = %+v, want the two latest, oldest first", last)
	}
}
