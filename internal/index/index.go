// Package index keeps the units extracted from source files in a SQLite
// database, so unchanged files are not parsed again. Modelled on pruner's
// incremental indexer: files are compared by modification time and size,
// only new and modified files are parsed, deleted files are removed, and
// every change is written in one transaction.
package index

import (
	"bytes"
	"database/sql"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3" // SQLite driver

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// Parsed is what a language adapter extracts from one file.
type Parsed struct {
	Generated bool
	Units     []unit.Unit
	// Traits are the traits the file declares.
	Traits []string
}

// ParseFunc extracts units from one file.
type ParseFunc func(path string, src []byte) (Parsed, error)

// Options describe one sync.
type Options struct {
	Root  string   // directory the paths are relative to
	Paths []string // files to index, relative to Root
	// Version identifies the extractor. When it differs from the stored one,
	// every file is parsed again.
	Version string
	Parse   ParseFunc
	// Threads is how many files are parsed in parallel; below 1 means 1.
	// Parse must be safe for concurrent use when Threads is above 1.
	Threads int
}

// Stats says what a sync did.
type Stats struct {
	Parsed    int
	Unchanged int
	Deleted   int
}

// File is one indexed file.
type File struct {
	Path string
	Parsed
}

const schema = `
CREATE TABLE IF NOT EXISTS files (
    path      TEXT PRIMARY KEY,
    mtime     INTEGER NOT NULL,
    size      INTEGER NOT NULL,
    generated INTEGER NOT NULL,
    units     BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

const versionKey = "version"

// stored is what the units column of a file row holds.
type stored struct {
	Units  []unit.Unit
	Traits []string
}

// DB is an open index database.
type DB struct {
	db *sql.DB
}

// Open opens or creates the index database at path, creating its directory.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has one writer; keep pragmas on one connection
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("index %s: %w", path, err)
	}
	return &DB{db: db}, nil
}

// Close closes the database.
func (d *DB) Close() error {
	return d.db.Close()
}

type stamp struct {
	mtime int64
	size  int64
}

type change struct {
	path   string
	stamp  stamp
	parsed Parsed
}

// Sync brings the index up to date with the files in opts.Paths. If any file
// cannot be read or parsed, nothing is written and the previous index stays.
func (d *DB) Sync(opts Options) (Stats, error) {
	var stats Stats

	stored, err := d.stamps()
	if err != nil {
		return stats, err
	}
	version, err := d.metadata(versionKey)
	if err != nil {
		return stats, err
	}
	reset := version != opts.Version
	if reset {
		stored = map[string]stamp{}
	}

	seen := make(map[string]bool, len(opts.Paths))
	var changes []change
	for _, p := range opts.Paths {
		seen[p] = true
		info, err := os.Stat(filepath.Join(opts.Root, p))
		if err != nil {
			return stats, err
		}
		s := stamp{mtime: info.ModTime().UnixNano(), size: info.Size()}
		if old, ok := stored[p]; ok && old == s {
			stats.Unchanged++
			continue
		}
		changes = append(changes, change{path: p, stamp: s})
	}
	var deleted []string
	for p := range stored {
		if !seen[p] {
			deleted = append(deleted, p)
		}
	}

	if err := parseAll(opts, changes); err != nil {
		return stats, err
	}

	if err := d.write(reset, opts.Version, changes, deleted); err != nil {
		return stats, err
	}
	stats.Parsed = len(changes)
	stats.Deleted = len(deleted)
	return stats, nil
}

// parseAll reads and parses the changed files on opts.Threads workers. On
// failure it returns the error of the first failing file in path order, so
// the reported error does not depend on scheduling.
func parseAll(opts Options, changes []change) error {
	errs := make([]error, len(changes))
	next := make(chan int)
	var wg sync.WaitGroup
	for range max(opts.Threads, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				c := &changes[i]
				src, err := os.ReadFile(filepath.Join(opts.Root, c.path))
				if err == nil {
					c.parsed, err = opts.Parse(c.path, src)
					if err != nil {
						err = fmt.Errorf("%s: %w", c.path, err)
					}
				}
				errs[i] = err
			}
		}()
	}
	for i := range changes {
		next <- i
	}
	close(next)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) write(reset bool, version string, changes []change, deleted []string) (err error) {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if reset {
		if _, err = tx.Exec(`DELETE FROM files`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO metadata (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, versionKey, version); err != nil {
			return err
		}
	}
	for _, p := range deleted {
		if _, err = tx.Exec(`DELETE FROM files WHERE path = ?`, p); err != nil {
			return err
		}
	}
	upsert, err := tx.Prepare(`INSERT INTO files (path, mtime, size, generated, units) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET mtime = excluded.mtime, size = excluded.size,
		generated = excluded.generated, units = excluded.units`)
	if err != nil {
		return err
	}
	defer func() { _ = upsert.Close() }()
	for _, c := range changes {
		var blob bytes.Buffer
		if err = gob.NewEncoder(&blob).Encode(stored{c.parsed.Units, c.parsed.Traits}); err != nil {
			return err
		}
		if _, err = upsert.Exec(c.path, c.stamp.mtime, c.stamp.size, c.parsed.Generated, blob.Bytes()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) stamps() (map[string]stamp, error) {
	rows, err := d.db.Query(`SELECT path, mtime, size FROM files`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]stamp{}
	for rows.Next() {
		var p string
		var s stamp
		if err := rows.Scan(&p, &s.mtime, &s.size); err != nil {
			return nil, err
		}
		out[p] = s
	}
	return out, rows.Err()
}

func (d *DB) metadata(key string) (string, error) {
	var value string
	err := d.db.QueryRow(`SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// Files returns every indexed file, sorted by path.
func (d *DB) Files() ([]File, error) {
	rows, err := d.db.Query(`SELECT path, generated, units FROM files ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []File
	for rows.Next() {
		var f File
		var blob []byte
		if err := rows.Scan(&f.Path, &f.Generated, &blob); err != nil {
			return nil, err
		}
		var st stored
		if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&st); err != nil {
			return nil, fmt.Errorf("index: %s: %w", f.Path, err)
		}
		f.Units, f.Traits = st.Units, st.Traits
		out = append(out, f)
	}
	return out, rows.Err()
}
