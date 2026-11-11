// Package config reads .ddslop.yaml, a repository's ddslop settings.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is the configuration file's name at the analysed root.
const File = ".ddslop.yaml"

// Config holds a repository's settings. A nil threshold is not set: the
// flag's default applies. A flag given on the command line overrides the
// file.
type Config struct {
	MinNodes      *int     `yaml:"min-nodes"`
	MinBlockNodes *int     `yaml:"min-block-nodes"`
	MinNearNodes  *int     `yaml:"min-near-nodes"`
	MinSimilarity *float64 `yaml:"min-similarity"`
	Tests         *bool    `yaml:"tests"`
	// Exclude lists glob patterns of files to leave out, matched against
	// paths relative to the root with "/" separators. "*" matches within
	// one path segment, "**" any number of segments.
	Exclude []string `yaml:"exclude"`
}

// Load reads root's configuration file. Without one, nothing is set.
// Unknown keys are errors, so a misspelt setting is not silently ignored.
func Load(root string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(root, File)) //nolint:gosec // the analysed repository's own file
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%s: %w", File, err)
	}
	for _, p := range c.Exclude {
		if _, err := path.Match(strings.ReplaceAll(p, "**", "*"), ""); err != nil {
			return Config{}, fmt.Errorf("%s: exclude %q: %w", File, p, err)
		}
	}
	return c, nil
}

// Excluded reports whether a path (relative to the root, "/" separators)
// matches one of the exclude patterns.
func (c Config) Excluded(file string) bool {
	segments := strings.Split(file, "/")
	for _, p := range c.Exclude {
		if match(strings.Split(p, "/"), segments) {
			return true
		}
	}
	return false
}

// match matches path segments against pattern segments, where "**"
// matches any number of segments.
func match(pattern, segments []string) bool {
	if len(pattern) == 0 {
		return len(segments) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(segments); i++ {
			if match(pattern[1:], segments[i:]) {
				return true
			}
		}
		return false
	}
	if len(segments) == 0 {
		return false
	}
	ok, _ := path.Match(pattern[0], segments[0]) // patterns are checked in Load
	return ok && match(pattern[1:], segments[1:])
}
