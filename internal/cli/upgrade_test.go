package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// latestServer answers only the latest-release redirect.
func latestServer(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			t.Errorf("unexpected request %s: nothing should be downloaded", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	repo, client := upgradeRepo, upgradeClient
	upgradeRepo, upgradeClient = srv.URL, srv.Client()
	t.Cleanup(func() { upgradeRepo, upgradeClient = repo, client })
}

func asVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestUpgradeRefusesDevelopmentBuildsWithoutForce(t *testing.T) {
	asVersion(t, "v2026.11.11_2-4-geb25f25-dirty")
	code, _, stderr := run("upgrade")
	if code != 2 || !strings.Contains(stderr, "--force") {
		t.Errorf("exit code %d, stderr %q; want 2 and a hint about --force", code, stderr)
	}
}

func TestUpgradeCheckReportsANewerRelease(t *testing.T) {
	asVersion(t, "v2026.11.11_2")
	latestServer(t, "v2026.11.12_0")
	code, stdout, stderr := run("upgrade", "--check")
	if code != 0 || !strings.Contains(stdout, "v2026.11.12_0 is available (this is v2026.11.11_2)") {
		t.Errorf("exit code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestUpgradeDoesNothingWhenUpToDate(t *testing.T) {
	asVersion(t, "v2026.11.12_0")
	latestServer(t, "v2026.11.12_0")
	code, stdout, _ := run("upgrade")
	if code != 0 || !strings.Contains(stdout, "already up to date") {
		t.Errorf("exit code %d, stdout %q; want 0 and already up to date", code, stdout)
	}
}
