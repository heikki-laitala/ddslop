package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release builds a release archive holding a binary with the given
// content, as the release workflow packages it.
func release(t *testing.T, asset, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{asset + "/ddslop": content, asset + "/LICENSE": "MIT"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGitHub serves a repository's latest-release redirect and one
// release's assets, with checksums that can be broken.
func fakeGitHub(t *testing.T, tag, asset string, archive []byte, badChecksum bool) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	if badChecksum {
		sum[0] ^= 0xff
	}
	checksums := fmt.Sprintf("%s  %s.tar.gz\n%s  other.tar.gz\n", hex.EncodeToString(sum[:]), asset, strings.Repeat("0", 64))
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+asset+".tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestReadsTheTagFromTheRedirect(t *testing.T) {
	srv := fakeGitHub(t, "v1.1.0", "ddslop-linux-x86_64", nil, false)
	got, err := Latest(srv.Client(), srv.URL)
	if err != nil || got != "v1.1.0" {
		t.Errorf("Latest = %q, %v; want v1.1.0", got, err)
	}
}

func TestInstallReplacesTheBinaryWithAVerifiedRelease(t *testing.T) {
	asset := "ddslop-linux-x86_64"
	srv := fakeGitHub(t, "v1.1.0", asset, release(t, asset, "new binary"), false)
	bin := filepath.Join(t.TempDir(), "ddslop")
	if err := os.WriteFile(bin, []byte("old binary"), 0o755); err != nil { //nolint:gosec // an executable test file
		t.Fatal(err)
	}

	if err := Install(srv.Client(), srv.URL, "v1.1.0", asset, bin); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := os.ReadFile(bin) //nolint:gosec // the test's own file
	info, _ := os.Stat(bin)
	if string(got) != "new binary" || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary = %q, mode %v; want the new, executable binary", got, info.Mode())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(bin), ".ddslop-upgrade-*")); len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestInstallRefusesAReleaseWhoseChecksumDoesNotMatch(t *testing.T) {
	asset := "ddslop-linux-x86_64"
	srv := fakeGitHub(t, "v1.1.0", asset, release(t, asset, "tampered binary"), true)
	bin := filepath.Join(t.TempDir(), "ddslop")
	if err := os.WriteFile(bin, []byte("old binary"), 0o755); err != nil { //nolint:gosec // an executable test file
		t.Fatal(err)
	}

	err := Install(srv.Client(), srv.URL, "v1.1.0", asset, bin)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("Install with a bad checksum: %v, want a checksum error", err)
	}
	if got, _ := os.ReadFile(bin); string(got) != "old binary" { //nolint:gosec // the test's own file
		t.Errorf("binary = %q after a failed upgrade, want it untouched", got)
	}
}

func TestAssetNamesTheReleaseArchiveForThePlatform(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:  "ddslop-linux-x86_64",
		{"linux", "arm64"}:  "ddslop-linux-aarch64",
		{"darwin", "arm64"}: "ddslop-macos-aarch64",
		{"darwin", "amd64"}: "ddslop-macos-x86_64",
	}
	for in, want := range cases {
		if got, err := Asset(in[0], in[1]); err != nil || got != want {
			t.Errorf("Asset(%s, %s) = %q, %v; want %s", in[0], in[1], got, err, want)
		}
	}
	if _, err := Asset("windows", "amd64"); err == nil {
		t.Error("Asset(windows) should fail: there is no Windows release")
	}
}

func TestIsReleaseTellsReleasesFromDevelopmentBuilds(t *testing.T) {
	for v, want := range map[string]bool{
		"v1.0.0":                             true,
		"v1.12.3":                            true,
		"v1.0.0-4-geb25f25-dirty":            false,
		"v1.0.0-rc.1":                        false,
		"v2026.11.11_2":                      false,
		"dev":                                false,
		"v0.0.0-20261111094512-eb25f2551a3f": false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", v, got, want)
		}
	}
}
