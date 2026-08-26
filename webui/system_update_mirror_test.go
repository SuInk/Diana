// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/ghmirror"
	"github.com/SuInk/diana/model/updater"
)

type recordingMirrorSelector struct {
	mu       sync.Mutex
	mode     string
	probedAt []string
	results  []ghmirror.ProbeResult
}

func (s *recordingMirrorSelector) SetMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
}

func (s *recordingMirrorSelector) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *recordingMirrorSelector) Base(_ context.Context, _ string) string {
	return "https://ghfast.top"
}

func (s *recordingMirrorSelector) Probe(_ context.Context, probeURL string) []ghmirror.ProbeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probedAt = append(s.probedAt, probeURL)
	return s.results
}

func (s *recordingMirrorSelector) LastProbe() []ghmirror.ProbeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results
}

// 界面上改了加速策略，下一次下载就得按新策略走——所以保存策略必须传到选择器。
func TestSaveUpdatePolicyAppliesMirrorMode(t *testing.T) {
	store := &memoryUpdatePolicyStore{}
	selector := &recordingMirrorSelector{}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	if err := handler.SetUpdatePolicyStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	handler.SetGitHubMirrorSelector(selector)
	router := systemUpdateTestRouter(handler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/system/update/policy",
		strings.NewReader(`{"auto_download":true,"auto_install":false,"github_mirror":"https://ghfast.top/"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save policy = %d %s", recorder.Code, recorder.Body.String())
	}
	if store.policy.GitHubMirror != "https://ghfast.top" {
		t.Fatalf("落库的加速策略 = %q", store.policy.GitHubMirror)
	}
	if selector.Mode() != "https://ghfast.top" {
		t.Fatalf("选择器没有收到新策略：%q", selector.Mode())
	}

	// 重启之后要按存下来的策略跑，别退回默认的自动模式。
	restarted := NewSystemUpdateHandler(fakeSystemUpdater{})
	restartedSelector := &recordingMirrorSelector{}
	if err := restarted.SetUpdatePolicyStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	restarted.SetGitHubMirrorSelector(restartedSelector)
	if restartedSelector.Mode() != "https://ghfast.top" {
		t.Fatalf("重启后的策略 = %q", restartedSelector.Mode())
	}
}

// 坏地址不落库：否则每次下载都要先撞一次失败再回落。
func TestSaveUpdatePolicyRejectsInvalidMirror(t *testing.T) {
	store := &memoryUpdatePolicyStore{}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	if err := handler.SetUpdatePolicyStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	router := systemUpdateTestRouter(handler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/system/update/policy",
		strings.NewReader(`{"auto_download":true,"github_mirror":"http://插件市场.example"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save policy = %d %s", recorder.Code, recorder.Body.String())
	}
	if store.policy.GitHubMirror != ghmirror.ModeAuto {
		t.Fatalf("坏地址被存下来了：%q", store.policy.GitHubMirror)
	}
}

// 测速拿真实的校验清单当样本：几 KB，又确实是更新时要下的东西。
func TestMirrorTestUsesLatestChecksumAsProbe(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z","assets":[` +
			`{"name":"SHA256SUMS","browser_download_url":"https://github.com/SuInk/Diana/releases/download/v1.3.0/SHA256SUMS"}]}]`))
	}))
	defer github.Close()

	selector := &recordingMirrorSelector{results: []ghmirror.ProbeResult{
		{Name: "ghfast.top", BaseURL: "https://ghfast.top", OK: true, LatencyMS: 120},
		{Name: "直连 GitHub", Direct: true, OK: false, Error: "dial tcp: i/o timeout"},
	}}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.githubAPIBase = github.URL
	handler.SetGitHubMirrorSelector(selector)
	router := systemUpdateTestRouter(handler)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/system/update/mirrors/test", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("mirror test = %d %s", recorder.Code, recorder.Body.String())
	}
	var response githubMirrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.LastProbe) != 2 || !response.LastProbe[0].OK {
		t.Fatalf("实测结果 = %#v", response.LastProbe)
	}
	if len(response.Mirrors) == 0 {
		t.Fatal("没有把候选线路一起返回，界面就没法选")
	}
	if len(selector.probedAt) != 1 || !strings.HasSuffix(selector.probedAt[0], "/SHA256SUMS") {
		t.Fatalf("测速样本 = %#v", selector.probedAt)
	}
}

// 没有注入选择器时说清楚，而不是返回一份空的假结果。
func TestMirrorTestWithoutSelector(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	router := systemUpdateTestRouter(handler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/system/update/mirrors/test", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("mirror test = %d %s", recorder.Code, recorder.Body.String())
	}
}
