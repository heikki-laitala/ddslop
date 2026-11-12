// Package upgrade replaces the running ddslop with a release from GitHub.
// It is the only part of ddslop that uses the network, and only when the
// upgrade command asks it to.
package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Repository is where releases are published.
const Repository = "https://github.com/heikki-laitala/ddslop"

// releaseTag matches a release tag, vMAJOR.MINOR.PATCH.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// IsRelease reports whether a version names a release, rather than a
// development build ("dev", or a git describe or pseudo-version).
func IsRelease(version string) bool { return releaseTag.MatchString(version) }

// Asset names the release archive for an operating system and CPU, as Go
// names them, without its .tar.gz.
func Asset(goos, goarch string) (string, error) {
	osName := map[string]string{"linux": "linux", "darwin": "macos"}[goos]
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("no ddslop release for %s/%s; releases are built for Linux and macOS on x86_64 and aarch64", goos, goarch)
	}
	return "ddslop-" + osName + "-" + arch, nil
}

// Latest returns the latest release's tag. It reads the redirect of the
// repository's releases/latest page rather than the API, whose limit for
// requests without a login shared CI runners hit.
func Latest(client *http.Client, repo string) (string, error) {
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Get(repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	location := resp.Header.Get("Location")
	_, tag, found := strings.Cut(location, "/releases/tag/")
	if resp.StatusCode/100 != 3 || !found || tag == "" {
		return "", fmt.Errorf("could not find the latest release (HTTP %d); see %s/releases", resp.StatusCode, repo)
	}
	return tag, nil
}

// Install downloads release tag's archive for asset, verifies it against
// the release's checksums and atomically replaces the binary at target
// with the one inside. On any error, target is left as it was.
func Install(client *http.Client, repo, tag, asset, target string) error {
	base := repo + "/releases/download/" + tag + "/"
	archive, err := fetch(client, base+asset+".tar.gz")
	if err != nil {
		return err
	}
	sums, err := fetch(client, base+"checksums.txt")
	if err != nil {
		return err
	}
	if err := verify(archive, sums, asset+".tar.gz"); err != nil {
		return err
	}
	binary, err := extract(archive, asset+"/ddslop")
	if err != nil {
		return err
	}
	return replace(target, binary)
}

// maxDownload bounds a download; release archives are a few megabytes.
const maxDownload = 200 << 20

func fetch(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// verify checks data against the checksum listed for name.
func verify(data, checksums []byte, name string) error {
	for line := range strings.Lines(string(checksums)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != fields[0] {
				return fmt.Errorf("checksum mismatch for %s: the download is not the published release", name)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum for %s in the release", name)
}

// extract returns the file at name inside a .tar.gz archive.
func extract(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s is missing from the release archive", path.Base(name))
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// replace writes data next to target and renames it over target, so a
// failed upgrade never leaves a half-written binary.
func replace(target string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".ddslop-upgrade-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() //nolint:gosec // our temporary file; gone after the rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // an executable
		return err
	}
	return os.Rename(tmp.Name(), target) //nolint:gosec // our temporary file over the running executable
}
