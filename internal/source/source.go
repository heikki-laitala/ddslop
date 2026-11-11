// Package source finds the files to analyse.
package source

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Files returns the paths, relative to root and sorted, of files under root
// whose names end in one of exts. Inside a Git work tree it lists tracked and
// untracked-but-not-ignored files, so .gitignore is respected. Elsewhere it
// walks the directory, skipping hidden directories and "target".
func Files(root string, exts ...string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	var files []string
	if insideGit(root) {
		files, err = gitFiles(root)
	} else {
		files, err = walkFiles(root)
	}
	if err != nil {
		return nil, err
	}

	var out []string
	for _, f := range files {
		if slices.ContainsFunc(exts, func(ext string) bool { return strings.HasSuffix(f, ext) }) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out, nil
}

func insideGit(root string) bool {
	out, err := git(root, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func gitFiles(root string) ([]string, error) {
	out, err := git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			files = append(files, filepath.FromSlash(f))
		}
	}
	return files, nil
}

func walkFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "target") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// git runs a git subcommand in dir and returns its standard output.
func git(dir string, args ...string) ([]byte, error) {
	// No shell is involved and dir is always the value of -C, so neither can
	// inject options or commands.
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // see above
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", args[0], dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Revision names the Git commit checked out under root, short, with
// "-dirty" when tracked files have uncommitted changes; "" outside Git.
func Revision(root string) string {
	if !insideGit(root) {
		return ""
	}
	head, err := git(root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "" // a repository without commits
	}
	rev := strings.TrimSpace(string(head))
	if status, err := git(root, "status", "--porcelain", "--untracked-files=no"); err == nil && len(bytes.TrimSpace(status)) > 0 {
		rev += "-dirty"
	}
	return rev
}
