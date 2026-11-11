package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/heikki-laitala/ddslop/internal/upgrade"
)

const upgradeUsage = `usage: ddslop upgrade [flags]

Replaces this ddslop with the latest release from GitHub, or the one
--version names, after verifying the download against the release's
checksums. This is the only command that uses the network. Development
builds are not replaced unless --force is given, so an upgrade never
overwrites a build of your own changes.

Flags:
`

// upgradeRepo and upgradeClient are where releases are fetched from;
// tests point them at a local server.
var (
	upgradeRepo   = upgrade.Repository
	upgradeClient = &http.Client{Timeout: 2 * time.Minute}
)

func runUpgrade(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("upgrade", upgradeUsage, stderr)
	want := fs.String("version", "", "install this release, such as v2026.11.11_2 (default: the latest)")
	check := fs.Bool("check", false, "only report whether a newer release exists")
	force := fs.Bool("force", false, "replace a development build too")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		return fail(stderr, errors.New("upgrade takes no arguments"))
	}
	current := versionString()
	if !upgrade.IsRelease(current) && !*force && !*check {
		return fail(stderr, fmt.Errorf("this is a development build (%s); use --force to replace it with a release", current))
	}
	tag := *want
	if tag == "" {
		latest, err := upgrade.Latest(upgradeClient, upgradeRepo)
		if err != nil {
			return fail(stderr, err)
		}
		tag = latest
	}
	if tag == current {
		fmt.Fprintf(stdout, "ddslop %s is already up to date\n", current)
		return exitClean
	}
	if *check {
		fmt.Fprintf(stdout, "ddslop %s is available (this is %s); run ddslop upgrade\n", tag, current)
		return exitClean
	}
	asset, err := upgrade.Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fail(stderr, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return fail(stderr, err)
	}
	if err := upgrade.Install(upgradeClient, upgradeRepo, tag, asset, exe); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "upgraded %s from %s to %s\n", exe, current, tag)
	return exitClean
}
