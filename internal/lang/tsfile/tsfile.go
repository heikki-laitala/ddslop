// Package tsfile knows TypeScript's files by their names alone: which
// are source, which only declare types, and which file a relative import
// names. It holds no parser, so the call graph can use it.
package tsfile

import (
	"encoding/json"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Extensions are the file extensions of TypeScript source.
var Extensions = []string{".ts", ".tsx", ".mts", ".cts"}

// Importers are the extensions of files that may import TypeScript code
// without being TypeScript: JavaScript, and the components of Vue,
// Svelte, Astro and MDX.
var Importers = []string{".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte", ".astro", ".mdx"}

// Configs are the extensions of files that may name a TypeScript export
// as an entry point without importing it: a Lambda handler in
// serverless.yml, a script in package.json.
var Configs = []string{".json", ".yml", ".yaml", ".toml", ".html"}

// IsSource reports whether a file is TypeScript source, by its extension.
func IsSource(file string) bool {
	return slices.ContainsFunc(Extensions, func(ext string) bool { return strings.HasSuffix(file, ext) })
}

// ModulePath returns the path, without extension, that a relative module
// path, as importer imports it ("./store", "../auth/verify.js", ".."),
// names: a file of that stem, written with or without its extension or a
// JavaScript one, or the index file of a directory of that path. A
// package's path names none.
func ModulePath(importer, module string) (string, bool) {
	if !relative(module) {
		return "", false
	}
	return path.Join(path.Dir(filepath.ToSlash(importer)), trimExtension(module)), true
}

// relative reports whether a module path is relative to its importer's
// directory.
func relative(module string) bool {
	return module == "." || module == ".." || strings.HasPrefix(module, "./") || strings.HasPrefix(module, "../")
}

// trimExtension drops a TypeScript or JavaScript extension a module path
// may be written with.
func trimExtension(module string) string {
	if ext := path.Ext(module); slices.Contains(Extensions, ext) || slices.Contains([]string{".js", ".jsx", ".mjs", ".cjs"}, ext) {
		return strings.TrimSuffix(module, ext)
	}
	return module
}

// Stem returns a file's slashed path without its extension.
func Stem(file string) string {
	return strings.TrimSuffix(filepath.ToSlash(file), path.Ext(file))
}

// AliasMayBe reports whether a module path written through a path alias
// ("@/db/schema", "~/server/db") or a package.json import ("#db/schema"),
// which only the project's configuration resolves, may name file: one whose path, without extension,
// ends in the path after the alias, or in its index file. It errs towards
// yes, for uses that may only drop findings.
func AliasMayBe(module, file string) bool {
	rest, imported := strings.CutPrefix(module, "#")
	if imported {
		rest = strings.TrimPrefix(rest, "/")
	} else if alias, after, ok := strings.Cut(module, "/"); ok && (alias == "@" || alias == "~") {
		rest = after
	} else {
		return false
	}
	stem := "/" + Stem(file)
	return strings.HasSuffix(stem, "/"+rest) || strings.HasSuffix(stem, "/"+rest+"/index")
}

// IsDeclarationFile reports whether a file only declares types for code
// elsewhere (.d.ts): it holds no code to compare.
func IsDeclarationFile(file string) bool {
	for _, ext := range Extensions {
		if strings.HasSuffix(file, ".d"+ext) {
			return true
		}
	}
	return false
}

// Manifest is what a package.json says of its package: its name
// (@acme/db), and whether it is private, never published.
type Manifest struct {
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

// ReadManifest reads a package.json, reporting false when it is no JSON.
func ReadManifest(data []byte) (Manifest, bool) {
	var m Manifest
	return m, json.Unmarshal(data, &m) == nil
}

// PackageDir returns the nearest directory above a file that packages
// holds, "." when none does.
func PackageDir[T any](file string, packages map[string]T) string {
	dir := filepath.ToSlash(filepath.Dir(file))
	for {
		if _, ok := packages[dir]; ok || dir == "." || dir == "/" {
			return dir
		}
		dir = path.Dir(dir)
	}
}

// Modules is what resolves the module paths TypeScript files import to
// files of ours.
type Modules struct {
	// Packages maps the directory of each named workspace package to its
	// name (@acme/db).
	Packages map[string]string
	// Configs maps the directory of each tsconfig.json to what it says of
	// module paths.
	Configs map[string]Config
	// Stems are the paths, without extension, of our TypeScript files.
	Stems map[string]bool
}

// Path returns the path, without extension, that a module path names as
// importer imports it: a file of that stem, or the index file of a
// directory of that path. A relative path names one (see ModulePath); a
// non-relative one ("@/lib/db") names the first of the targets the
// nearest tsconfig.json above importer maps it to that is a file of ours
// (see Config), as tsc resolves it. A package's path names none.
func (m Modules) Path(importer, module string) (string, bool) {
	named, ok := m.resolve(importer, module)
	return strings.TrimSuffix(named, "/"), ok
}

// resolve is Path, ending in a slash when the module path or the target
// it resolves through does: a directory's index file, not a file of the
// path, is what it names.
func (m Modules) resolve(importer, module string) (string, bool) {
	if named, ok := relativePath(importer, module); ok {
		return named, true
	}
	for _, target := range m.targets(importer, module) {
		if _, ok := m.pick(target); ok {
			return target, true
		}
	}
	return "", false
}

// File returns the stem of the file of ours a module path names as
// importer imports it (see Path): the file of that path, else its
// directory's index file, which a path ending in a slash ("./db/") names
// alone, as tsc takes them.
func (m Modules) File(importer, module string) (string, bool) {
	named, ok := m.resolve(importer, module)
	if !ok {
		return "", false
	}
	return m.pick(named)
}

// relativePath is ModulePath, ending in a slash when the module path
// names a directory (see resolve): it ends in a slash, or in . or ..
func relativePath(importer, module string) (string, bool) {
	named, ok := ModulePath(importer, module)
	if last := path.Base(module); ok && (strings.HasSuffix(module, "/") || last == "." || last == "..") {
		named += "/"
	}
	return named, ok
}

// pick returns the stem of the file of ours a resolved path names (see
// resolve): the file of the path, else its directory's index file, the
// index alone for a path ending in a slash.
func (m Modules) pick(named string) (string, bool) {
	dir, asDir := strings.CutSuffix(named, "/")
	if !asDir && m.Stems[named] {
		return named, true
	}
	if index := path.Join(dir, "index"); m.Stems[index] {
		return index, true
	}
	return "", false
}

// targets returns the paths the nearest tsconfig.json above importer maps
// a non-relative module path to, in tsc's order (see Config).
func (m Modules) targets(importer, module string) []string {
	c, ok := m.Configs[PackageDir(importer, m.Configs)]
	if !ok {
		return nil
	}
	return c.project(importer).targets(module)
}

// Files reports, for a file, whether it is the module a module path names
// as importer imports it: the one File names, or, our files unknown, the
// file a path names (see Path) or its directory's index file; or, for a
// tsconfig path whose first target of ours is a workspace package's
// directory, one of that package's files, as its name would; else one of
// the workspace package a package path names (@acme/db, @acme/db/query),
// outside the packages nested in it.
func (m Modules) Files(importer, module string) func(file string) bool {
	file := func(named string) func(file string) bool {
		if stem, ok := m.pick(named); ok {
			return func(file string) bool { return Stem(file) == stem }
		}
		dir, asDir := strings.CutSuffix(named, "/")
		return func(file string) bool { // our files unknown: either, a directory's index alone
			stem := Stem(file)
			return !asDir && stem == dir || stem == path.Join(dir, "index")
		}
	}
	inPackage := func(dir string) func(file string) bool {
		return func(file string) bool { return PackageDir(file, m.Packages) == dir }
	}
	if named, ok := relativePath(importer, module); ok {
		return file(named)
	}
	for _, target := range m.targets(importer, module) {
		if _, ok := m.pick(target); ok {
			return file(target)
		}
		if _, ok := m.Packages[strings.TrimSuffix(target, "/")]; ok {
			return inPackage(strings.TrimSuffix(target, "/"))
		}
	}
	if dir, ok := packageNamed(module, m.Packages); ok {
		return inPackage(dir)
	}
	return func(string) bool { return false }
}

// packageNamed returns the directory of the workspace package a module
// path names, itself or a subpath of it, when exactly one package has
// that name.
func packageNamed(module string, packages map[string]string) (string, bool) {
	found, n := "", 0
	for dir, name := range packages {
		if module == name || strings.HasPrefix(module, name+"/") {
			found, n = dir, n+1
		}
	}
	return found, n == 1
}
