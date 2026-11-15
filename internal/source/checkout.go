package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckoutAt checks out the tree of ref into a temporary worktree of the
// Git repository holding root and returns the worktree and root's
// counterpart in it (which may not exist there), so the
// ref's code can be analysed exactly as a checkout of it would be (its
// tracked files, listed by Git). The returned function removes the worktree
// and its registration; call it whatever happens. Hooks are not run.
func CheckoutAt(root, ref string) (tree, dir string, cleanup func(), err error) {
	if !insideGit(root) {
		return "", "", nil, fmt.Errorf("--baseline-ref needs a Git repository, and %s is not in one", root)
	}
	if _, err := git(root, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return "", "", nil, fmt.Errorf("--baseline-ref %s is not a commit in the Git repository at %s", ref, root)
	}
	top, err := git(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", nil, err
	}
	prefix, err := git(root, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", nil, err
	}
	repo := strings.TrimSpace(string(top))
	tmp, err := os.MkdirTemp("", "ddslop-baseline-")
	if err != nil {
		return "", "", nil, err
	}
	tree = filepath.Join(tmp, "tree")
	cleanup = func() {
		_, _ = git(repo, "worktree", "remove", "--force", tree)
		_ = os.RemoveAll(tmp)
		_, _ = git(repo, "worktree", "prune")
	}
	if _, err := git(repo, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--quiet", "--detach", tree, ref); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return tree, filepath.Join(tree, filepath.FromSlash(strings.TrimSpace(string(prefix)))), cleanup, nil
}
