// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/ghmirror"
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
	if store.policy.GitHubMirror != ghmirror.ModeDirect {
		t.Fatalf("坏地址被存下来了：%q", store.policy.GitHubMirror)
	}
}

// 测速拿真实的校验清单当样本：几 KB，又确实是更新时要下的东西。
// 测速样本要取安装包本身。校验清单只有几 KB，还没进入稳定传输就读完了，
// 拿它测出来的是握手耗时不是下载速度——线路一旦是「秒回应答头然后限速」，
// 用清单测就永远看不出来。
