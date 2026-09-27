// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/updater"
)

func TestDockerUpdateSupportRequiresMatchingMutableTag(t *testing.T) {
	h := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	h.SetDockerDeployment(NewDockerUpdateTrigger("ghcr.io/suink/diana:canary-slim", "secret"))
	if got := h.dockerUpdateSupport("canary", "v1.2.4-canary.3"); !got.Supported {
		t.Fatalf("matching canary image should update: %+v", got)
	}
	for _, tc := range []struct{ image, channel, latest string }{
		{"ghcr.io/suink/diana:latest", "canary", "v1.2.4-canary.3"},
		{"ghcr.io/suink/diana:canary", "canary", "v1.2.4"},
		{"ghcr.io/suink/diana:v1.2.3", "release", "v1.2.4"},
		{"example.com/other:latest", "release", "v1.2.4"},
	} {
		h.dockerUpdater.Image = tc.image
		if got := h.dockerUpdateSupport(tc.channel, tc.latest); got.Supported || got.Reason == "" {
			t.Fatalf("must reject image=%s channel=%s release=%s: %+v", tc.image, tc.channel, tc.latest, got)
		}
	}
}

func TestDockerUpdateRequestUsesPrivateTokenAndReturnsBeforeRestart(t *testing.T) {
	github := releaseTestServer(t, "v9.9.9")
	defer github.Close()
	called := make(chan struct{}, 1)
	helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/update" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected helper request: %s %s %s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		called <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer helper.Close()
	trigger := NewDockerUpdateTrigger("ghcr.io/suink/diana:latest", "secret")
	trigger.URL = helper.URL + "/v1/update"
	h := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	h.SetDockerDeployment(trigger)
	h.SetBuildVersion("v1.0.0")
	h.githubAPIBase = github.URL
	router := systemUpdateTestRouter(h)

	check := httptest.NewRecorder()
	router.ServeHTTP(check, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"deployment_mode":"docker"`) || !strings.Contains(check.Body.String(), `"update_supported":true`) {
		t.Fatalf("Docker check = %d %s", check.Code, check.Body.String())
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"confirmation":"apply-update"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	var result updater.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || !result.Updated || result.TargetCommit != "v9.9.9" {
		t.Fatalf("accepted result = %+v, err = %v", result, err)
	}
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("update helper was not called")
	}
}

func TestDockerAutomaticUpdateRequiresInstallSwitch(t *testing.T) {
	called := make(chan struct{}, 1)
	helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		called <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer helper.Close()
	trigger := NewDockerUpdateTrigger("ghcr.io/suink/diana:latest", "secret")
	trigger.URL = helper.URL + "/v1/update"
	h := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	h.SetDockerDeployment(trigger)
	h.SetBuildVersion("v1.0.0")
	// A previously saved Release-package setting must not arm Docker updates.
	h.policy.AutoInstall = true
	h.runAutoDockerUpdate("v1.0.1")
	if h.dockerUpdateTarget != "" {
		t.Fatal("automatic Docker update started with the install switch off")
	}
	h.policy.DockerAutoInstall = true
	h.runAutoDockerUpdate("v1.0.1")
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic Docker update did not trigger after opt-in")
	}
}
