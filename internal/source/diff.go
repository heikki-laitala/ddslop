package source

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Changes maps files, relative to the analysed root, to their changed
// line ranges (inclusive, 1-based).
type Changes map[string][][2]int

// Touches reports whether lines start..end of file overlap a change.
func (c Changes) Touches(file string, start, end int) bool {
	for _, r := range c[file] {
		if start <= r[1] && r[0] <= end {
			return true
		}
	}
	return false
}

// ChangedLines returns the lines changed under root since ref: committed,
// staged and unstaged changes, plus untracked files in full. A deletion
// marks the lines on both sides of it, since removing code can make two
// functions identical.
func ChangedLines(root, ref string) (Changes, error) {
	if !insideGit(root) {
		return nil, fmt.Errorf("--diff needs a Git repository, and %s is not in one", root)
	}
	out, err := git(root, "diff", "--unified=0", "--no-color", "--no-ext-diff", "--relative", ref, "--")
	if err != nil {
		return nil, err
	}
	changes, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}
	untracked, err := git(root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	for f := range strings.SplitSeq(string(untracked), "\x00") {
		if f != "" {
			changes[filepath.FromSlash(f)] = [][2]int{{1, math.MaxInt}}
		}
	}
	return changes, nil
}

// parseDiff reads the added and modified line ranges of a unified diff
// made with --unified=0.
func parseDiff(diff string) (Changes, error) {
	changes := Changes{}
	file := ""
	for line := range strings.Lines(diff) {
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = ""
			if path := strings.TrimPrefix(line, "+++ "); path != "/dev/null" {
				file = filepath.FromSlash(strings.TrimPrefix(path, "b/"))
			}
		case strings.HasPrefix(line, "@@ ") && file != "":
			start, count, err := newRange(line)
			if err != nil {
				return nil, err
			}
			r := [2]int{start, start + count - 1}
			if count == 0 {
				r = [2]int{max(start, 1), start + 1} // a deletion after line start
			}
			changes[file] = append(changes[file], r)
		}
	}
	return changes, nil
}

// newRange parses the "+start,count" part of a hunk header
// "@@ -a,b +start,count @@".
func newRange(header string) (start, count int, err error) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, errors.New("unexpected diff hunk header: " + header)
	}
	spec := strings.TrimPrefix(fields[2], "+")
	count = 1
	if before, after, ok := strings.Cut(spec, ","); ok {
		spec = before
		if count, err = strconv.Atoi(after); err != nil {
			return 0, 0, fmt.Errorf("diff hunk header %q: %w", header, err)
		}
	}
	if start, err = strconv.Atoi(spec); err != nil {
		return 0, 0, fmt.Errorf("diff hunk header %q: %w", header, err)
	}
	return start, count, nil
}
