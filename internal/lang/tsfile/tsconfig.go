package tsfile

import (
	"bytes"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/heikki-laitala/ddslop/internal/config"
)

// Config is what a tsconfig.json says of the module paths its files
// import: BaseURL, the directory a non-relative path may be under, "" for
// none, and Paths, the targets each pattern ("@/*") maps to, "*" standing
// for what the pattern matched, in the order the file declares them. The
// targets are as written, read from the effective BaseURL, else from
// PathsDir, the directory of the file declaring them, as tsc reads them:
// an inherited pattern follows the baseUrl of the file extending it. The
// directories are slashed paths from the analysed root.
type Config struct {
	BaseURL  string
	Paths    []Pattern
	PathsDir string
	baseSet  bool // the file or one it extends sets baseUrl, null included
	// Projects are the configs a solution tsconfig.json references (Vite's
	// tsconfig.app.json and tsconfig.node.json): a file reads the first
	// whose include covers it (see project), else this one.
	Projects []Config
	// include, exclude and files are the globs and files the config's
	// include, exclude and files name, from the analysed root (see
	// covers); none of include and files covers all.
	include, exclude, files []string
}

// Pattern is one entry of a tsconfig's paths: the pattern and its
// targets, as written.
type Pattern struct {
	Pattern string
	Targets []string
}

// ReadConfig reads data, the tsconfig.json of dir, reporting false when
// it is no JSON. Its comments and trailing commas aside, it is JSON. A
// relative extends ("../../tsconfig.base.json"), or each of an array of
// them in turn, gives, read with read, what the file does not say itself,
// through a chain of any length that ends where a file would extend
// itself; a package's (@tsconfig/node20) is not read. Paths resolve from
// the baseUrl, the file's own or inherited, else from the directory of
// the file declaring them. Each config its references name (a file, or a
// directory's tsconfig.json) is read as a project of its own (see
// Config.Projects).
func ReadConfig(dir string, data []byte, read func(file string) ([]byte, error)) (Config, bool) {
	c, ok := readConfig(dir, data, read, map[string]bool{})
	var raw struct {
		References []struct {
			Path string `json:"path"`
		} `json:"references"`
	}
	if !ok || !decode(data, &raw) {
		return c, ok
	}
	for _, ref := range raw.References {
		file := path.Join(dir, slashed(ref.Path))
		if !strings.HasSuffix(file, ".json") {
			file = path.Join(file, "tsconfig.json")
		}
		if data, err := read(file); err == nil {
			if p, ok := readConfig(path.Dir(file), data, read, map[string]bool{file: true}); ok {
				if p.include == nil && p.files == nil {
					p.include = []string{path.Join(path.Dir(file), "**/*")} // tsc's default: all of its directory
				}
				c.Projects = append(c.Projects, p)
			}
		}
	}
	return c, true
}

// decode reads a tsconfig into v: JSON with comments and trailing commas,
// perhaps after a byte order mark.
func decode(data []byte, v any) bool {
	return json.Unmarshal(stripJSONC(bytes.TrimPrefix(data, []byte("\ufeff"))), v) == nil
}

// fromDir returns the paths or globs a config written in dir names, from
// the analysed root.
func fromDir(dir string, patterns []string) []string {
	out := []string{}
	for _, p := range patterns {
		out = append(out, path.Join(dir, slashed(p)))
	}
	return out
}

// project returns the config a file reads: the first of the projects
// whose include covers it (see Projects), else c itself.
func (c Config) project(file string) Config {
	for _, p := range c.Projects {
		if p.covers(file) {
			return p
		}
	}
	return c
}

// covers reports whether a config covers a file, as tsc decides it: its
// files list it, or an include pattern matches it and no exclude one
// does; a config with no include and no files covers all.
func (c Config) covers(file string) bool {
	if c.include == nil && c.files == nil {
		return true
	}
	matches := func(p string) bool { return matchGlob(p, file) }
	return slices.Contains(c.files, file) || slices.ContainsFunc(c.include, matches) && !slices.ContainsFunc(c.exclude, matches)
}

// matchGlob reports whether a tsconfig include or exclude pattern
// matches a file: * and ? within a segment, ** for any number of
// directories, and a pattern with no wildcard the file itself or a
// directory holding it.
func matchGlob(pattern, file string) bool {
	if pattern == "." {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return file == pattern || strings.HasPrefix(file, pattern+"/")
	}
	return config.MatchGlob(pattern, file)
}

func readConfig(dir string, data []byte, read func(file string) ([]byte, error), seen map[string]bool) (Config, bool) {
	var raw struct {
		Extends         any      `json:"extends"`
		Include         []string `json:"include"`
		Exclude         []string `json:"exclude"`
		Files           []string `json:"files"`
		CompilerOptions struct {
			BaseURL json.RawMessage `json:"baseUrl"`
			Paths   json.RawMessage `json:"paths"`
		} `json:"compilerOptions"`
	}
	if !decode(data, &raw) {
		return Config{}, false
	}
	paths, ok := orderedPaths(raw.CompilerOptions.Paths)
	if !ok {
		return Config{}, false
	}
	var c Config
	for _, parent := range extendsOf(raw.Extends) {
		parent = slashed(parent)
		if !relative(parent) {
			continue
		}
		// as written, else with .json added, as tsc reads it
		file := path.Join(dir, parent)
		data, err := read(file)
		if err != nil && !strings.HasSuffix(file, ".json") {
			file += ".json"
			data, err = read(file)
		}
		if err != nil || seen[file] {
			continue // missing, or a file extending itself through the chain read so far
		}
		seen[file] = true
		if p, ok := readConfig(path.Dir(file), data, read, seen); ok {
			c = p.over(c)
		}
		delete(seen, file) // another branch may extend it as well
	}
	switch b := raw.CompilerOptions.BaseURL; {
	case string(b) == "null":
		c.BaseURL, c.baseSet = "", true // cleared: inherited paths read from where they are declared
	case len(b) > 0:
		var base string
		if json.Unmarshal(b, &base) != nil {
			return Config{}, false
		}
		c.BaseURL, c.baseSet = path.Join(dir, slashed(base)), true
	}
	if paths != nil {
		c.Paths, c.PathsDir = paths, dir
	}
	if raw.Include != nil {
		c.include = fromDir(dir, raw.Include)
	}
	if raw.Exclude != nil {
		c.exclude = fromDir(dir, raw.Exclude)
	}
	if raw.Files != nil {
		c.files = fromDir(dir, raw.Files)
	}
	return c, true
}

// slashed returns a path a tsconfig writes with slashes: tsc reads a
// Windows separator (.\base.json) as one on every system.
func slashed(p string) string {
	return strings.ReplaceAll(p, `\`, "/")
}

// extendsOf returns the configs an extends names: one, or an array of them.
func extendsOf(extends any) []string {
	switch e := extends.(type) {
	case string:
		return []string{e}
	case []any:
		var out []string
		for _, v := range e {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// over returns c's settings over base's: each one c sets replaces base's.
func (c Config) over(base Config) Config {
	if c.baseSet {
		base.BaseURL, base.baseSet = c.BaseURL, true
	}
	if c.Paths != nil {
		base.Paths, base.PathsDir = c.Paths, c.PathsDir
	}
	if c.include != nil {
		base.include = c.include
	}
	if c.exclude != nil {
		base.exclude = c.exclude
	}
	if c.files != nil {
		base.files = c.files
	}
	return base
}

// orderedPaths decodes a tsconfig's paths in the order it declares them:
// tsc takes the first of two patterns matching alike. ok is false when
// they are no object of string arrays; none (nil) is no paths, and null
// an empty set, clearing those inherited.
func orderedPaths(raw json.RawMessage) ([]Pattern, bool) {
	switch string(raw) {
	case "":
		return nil, true
	case "null":
		return []Pattern{}, true // cleared: no pattern, over any inherited
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	out := []Pattern{}
	for d.More() {
		key, err := d.Token()
		pattern, isString := key.(string)
		var targets []string
		if err != nil || !isString || d.Decode(&targets) != nil {
			return nil, false
		}
		for i, t := range targets {
			targets[i] = slashed(t)
		}
		out = append(out, Pattern{Pattern: pattern, Targets: targets})
	}
	return out, true
}

// targets returns the paths, without extension, a non-relative module
// path may name under c, in tsc's order: those of the exact pattern of
// the path, else of the wildcard one of the longest prefix matching it,
// else, with no pattern matching, the path under the baseUrl. A target
// ending in a slash keeps it: it names a directory's index file.
func (c Config) targets(module string) []string {
	var matched []string
	star, best := "", -1
	for _, p := range c.Paths {
		prefix, suffix, wild := strings.Cut(p.Pattern, "*")
		switch {
		case !wild && p.Pattern == module:
			matched, star, best = p.Targets, "", len(module)+1
		case wild && len(prefix) > best && len(module) >= len(prefix)+len(suffix) &&
			strings.HasPrefix(module, prefix) && strings.HasSuffix(module, suffix):
			matched, star, best = p.Targets, module[len(prefix):len(module)-len(suffix)], len(prefix)
		}
	}
	from := c.BaseURL
	if from == "" {
		from = c.PathsDir
	}
	var out []string
	if best >= 0 {
		for _, t := range matched {
			out = append(out, under(from, strings.Replace(t, "*", star, 1)))
		}
	} else if c.BaseURL != "" {
		out = append(out, under(c.BaseURL, module))
	}
	return out
}

// under returns a path a tsconfig resolves under dir, without extension,
// ending in a slash as p does: a directory, whose index file it names.
func under(dir, p string) string {
	resolved := trimExtension(path.Join(dir, p))
	if strings.HasSuffix(p, "/") {
		resolved += "/"
	}
	return resolved
}

// stripJSONC turns JSON with comments and trailing commas, as tsconfig.json
// is written, into JSON: comments become spaces, and a comma followed only
// by space before a closing bracket goes.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch {
		case inString:
			if b == '\\' && i+1 < len(data) {
				out = append(out, b)
				i++
				b = data[i]
			} else if b == '"' {
				inString = false
			}
		case b == '"':
			inString = true
		case b == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			b = '\n'
		case b == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			i += end + 3
			b = ' '
		case b == '}' || b == ']':
			trimmed := bytes.TrimRight(out, " \t\r\n")
			if bytes.HasSuffix(trimmed, []byte(",")) {
				out = trimmed[:len(trimmed)-1]
			}
		}
		out = append(out, b)
	}
	return out
}
