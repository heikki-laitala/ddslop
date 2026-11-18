package tsfile

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
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
}

// Pattern is one entry of a tsconfig's paths: the pattern and its
// targets, as written.
type Pattern struct {
	Pattern string
	Targets []string
}

// maxExtends bounds a chain of extends, which a loop would make endless.
const maxExtends = 8

// ReadConfig reads data, the tsconfig.json of dir, reporting false when
// it is no JSON. Its comments and trailing commas aside, it is JSON. A
// relative extends ("../../tsconfig.base.json") gives, read with read,
// what the file does not say itself; a package's (@tsconfig/node20) is
// not read. Paths resolve from the baseUrl, the file's own or inherited,
// else from the directory of the file declaring them.
func ReadConfig(dir string, data []byte, read func(file string) ([]byte, error)) (Config, bool) {
	return readConfig(dir, data, read, maxExtends)
}

func readConfig(dir string, data []byte, read func(file string) ([]byte, error), extends int) (Config, bool) {
	var raw struct {
		Extends         any `json:"extends"`
		CompilerOptions struct {
			BaseURL *string         `json:"baseUrl"`
			Paths   json.RawMessage `json:"paths"`
		} `json:"compilerOptions"`
	}
	data = stripJSONC(bytes.TrimPrefix(data, []byte("\ufeff")))
	if json.Unmarshal(data, &raw) != nil {
		return Config{}, false
	}
	paths, ok := orderedPaths(raw.CompilerOptions.Paths)
	if !ok {
		return Config{}, false
	}
	var c Config
	if parent, ok := raw.Extends.(string); ok && relative(parent) && extends > 0 {
		if !strings.HasSuffix(parent, ".json") {
			parent += ".json"
		}
		file := path.Join(dir, parent)
		if data, err := read(file); err == nil {
			c, _ = readConfig(path.Dir(file), data, read, extends-1)
		}
	}
	if b := raw.CompilerOptions.BaseURL; b != nil {
		c.BaseURL = path.Join(dir, *b)
	}
	if paths != nil {
		c.Paths, c.PathsDir = paths, dir
	}
	return c, true
}

// orderedPaths decodes a tsconfig's paths in the order it declares them:
// tsc takes the first of two patterns matching alike. ok is false when
// they are no object of string arrays; none (nil) is no paths.
func orderedPaths(raw json.RawMessage) ([]Pattern, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
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
		out = append(out, Pattern{Pattern: pattern, Targets: targets})
	}
	return out, true
}

// targets returns the paths, without extension, a non-relative module
// path may name under c, in tsc's order: those of the exact pattern of
// the path, else of the wildcard one of the longest prefix matching it,
// else, with no pattern matching, the path under the baseUrl.
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
			out = append(out, trimExtension(path.Join(from, strings.Replace(t, "*", star, 1))))
		}
	} else if c.BaseURL != "" {
		out = append(out, trimExtension(path.Join(c.BaseURL, module)))
	}
	return out
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
