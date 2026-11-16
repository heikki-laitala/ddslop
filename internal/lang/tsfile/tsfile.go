// Package tsfile knows TypeScript's files by their names alone: which
// are source, which only declare types, and which file a relative import
// names. It holds no parser, so the call graph can use it.
package tsfile

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Extensions are the file extensions of TypeScript source.
var Extensions = []string{".ts", ".tsx", ".mts", ".cts"}

// IsSource reports whether a file is TypeScript source, by its extension.
func IsSource(file string) bool {
	return slices.ContainsFunc(Extensions, func(ext string) bool { return strings.HasSuffix(file, ext) })
}

// ModuleIs reports whether a relative module path, as importer imports
// it ("./store", "../auth/verify.js", ".."), names file: the file itself,
// written with or without its extension or a JavaScript one, or its
// directory's index file. A package's path names no file of ours.
func ModuleIs(importer, module, file string) bool {
	if module != "." && module != ".." && !strings.HasPrefix(module, "./") && !strings.HasPrefix(module, "../") {
		return false
	}
	if ext := path.Ext(module); slices.Contains(Extensions, ext) || slices.Contains([]string{".js", ".jsx", ".mjs", ".cjs"}, ext) {
		module = strings.TrimSuffix(module, ext)
	}
	named := path.Join(path.Dir(filepath.ToSlash(importer)), module)
	stem := strings.TrimSuffix(filepath.ToSlash(file), path.Ext(file))
	return stem == named || stem == named+"/index"
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
