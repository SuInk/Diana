package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/updater"
	"github.com/gin-gonic/gin"
)

func TestUpdateChannelSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately unsorted; flags alone must not allow beta into stable.
		_, _ = w.Write([]byte(`[
   {"tag_name":"v1.2.0-beta.2","prerelease":true},
   {"tag_name":"v1.1.0"},
   {"tag_name":"v1.2.0-beta.10","prerelease":true},
   {"tag_name":"v1.1.1-rc.1","prerelease":true},
   {"tag_name":"v1.3.0-beta.1","draft":true},
   {"tag_name":"v9.0.0-alpha.1","prerelease":true},
   {"tag_name":"v1.2.0-rc.1"},
   {"tag_name":"v1.3.0-beta.1"},
   {"tag_name":"v9.0.0","prerelease":true}
  ]`))
	}))
	defer server.Close()
	h := NewSystemUpdateHandler(fakeSystemUpdater{})
	h.githubAPIBase = server.URL
	for _, tc := range []struct{ channel, want string }{
		{"", "v1.1.0"}, {"release", "v1.1.0"}, {"beta", "v1.3.0-beta.1"},
	} {
		h.policy.Channel = tc.channel
		got, err := h.latestChannelRelease(context.Background(), "")
		if err != nil || got.Tag != tc.want {
			t.Fatalf("channel %q: got %s, %v; want %s", tc.channel, got.Tag, err, tc.want)
		}
	}
}

func TestPrereleaseVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"v1.2.0-beta.2", "v1.2.0-beta.10", true},
		{"v1.2.0-beta.10", "v1.2.0-beta.2", false},
		{"v1.2.0-beta.10", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.1", "v1.2.0", true},
		{"v1.2.0", "v1.2.0-rc.1", false},
		{"v1.2.0-beta.1", "v1.1.9", false},
		{"v1.2.0-rc.1+abc", "v1.2.0-rc.1+def", false},
	} {
		got, err := updateAvailableAgainst(tc.current, tc.latest)
		if err != nil || got != tc.want {
			t.Errorf("%s -> %s: got %v, %v", tc.current, tc.latest, got, err)
		}
	}
}

func TestChannelPolicyPersistenceAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &memoryUpdatePolicyStore{}
	h := NewSystemUpdateHandler(fakeSystemUpdater{})
	if err := h.SetUpdatePolicyStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	h.Register(router)
	for _, channel := range []string{"beta", "release", "nightly"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/system/update/policy", strings.NewReader(`{"channel":"`+channel+`","auto_download":true}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		want := http.StatusOK
		if channel == "nightly" {
			want = http.StatusBadRequest
		}
		if rec.Code != want {
			t.Fatalf("%s: %d %s", channel, rec.Code, rec.Body.String())
		}
		restarted := NewSystemUpdateHandler(fakeSystemUpdater{})
		if err := restarted.SetUpdatePolicyStore(context.Background(), store); err != nil {
			t.Fatal(err)
		}
		if channel != "nightly" && restarted.currentPolicy().Channel != channel {
			t.Fatal("channel not persisted")
		}
	}
	if got := normalizeUpdatePolicy(updater.UpdatePolicy{}).Channel; got != "release" {
		t.Fatalf("legacy default = %s", got)
	}
}

func TestAutoUpdateDoesNotInstallStaleBetaAfterChannelSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.2.0"}]`))
	}))
	defer server.Close()
	pkg := &recordingReleasePackageUpdater{status: updater.Status{
		NearestTag: "v1.2.0", RunningCommit: "v1.2.0", DownloadReady: true, DownloadedVersion: "v1.3.0-beta.1",
	}}
	h := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	h.SetReleasePackageUpdater(pkg)
	h.githubAPIBase = server.URL
	h.policy = updater.UpdatePolicy{Channel: "release", AutoDownload: true, AutoInstall: true}
	h.runAutoUpdate(context.Background())
	if pkg.installed || pkg.downloaded {
		t.Fatal("stale beta must not install in release channel")
	}
	router := systemUpdateTestRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/install", strings.NewReader(`{"confirmation":"install-restart"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || pkg.installed {
		t.Fatalf("manual stale install: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoUpdateDoesNotDowngradeToCachedStablePackage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.2.0"}]`))
	}))
	defer server.Close()
	pkg := &recordingReleasePackageUpdater{status: updater.Status{
		NearestTag: "v1.3.0-beta.1", RunningCommit: "v1.3.0-beta.1", DownloadReady: true, DownloadedVersion: "v1.2.0",
	}}
	h := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	h.SetReleasePackageUpdater(pkg)
	h.githubAPIBase = server.URL
	h.policy = updater.UpdatePolicy{Channel: "release", AutoDownload: true, AutoInstall: true}
	h.runAutoUpdate(context.Background())
	if pkg.installed || pkg.downloaded {
		t.Fatal("switching to release must not downgrade even when stable package is cached")
	}
}
