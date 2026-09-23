// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

type capturingLogWriter struct {
	mu      sync.Mutex
	entries []storage.AppLogEntry
}

func (w *capturingLogWriter) AppendLog(_ context.Context, entry storage.AppLogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
	return nil
}

func (w *capturingLogWriter) actions() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	actions := make([]string, 0, len(w.entries))
	for _, entry := range w.entries {
		actions = append(actions, entry.Action)
	}
	return actions
}

func countAction(actions []string, action string) int {
	count := 0
	for _, candidate := range actions {
		if candidate == action {
			count++
		}
	}
	return count
}

// 对外 API 是公网打得到的端点：拿错密钥一直刷的时候，同一来源同一原因一分钟只记
// 一条，不能把运行日志刷满；换了原因要另记。
func TestOpenAPIRejectionsAreLoggedAndThrottled(t *testing.T) {
	router, handler, _, _ := newOpenAPITestRouter(t)
	logs := &capturingLogWriter{}
	handler.SetLogStore(logs)
	post := func(auth string) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/openapi/v1/messages", strings.NewReader(`{}`))
		if auth != "" {
			request.Header.Set("Authorization", auth)
		}
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for range 3 {
		if code := post("Bearer wrong-key"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d", code)
		}
	}
	post("")
	if got := countAction(logs.actions(), "openapi_auth_rejected"); got != 2 {
		t.Fatalf("rejection logs = %v, want one per reason within a minute", logs.actions())
	}
	for _, entry := range logs.entries {
		if strings.Contains(entry.Message+entry.Detail, "wrong-key") {
			t.Fatalf("rejection log leaked the presented key: %#v", entry)
		}
	}
}

// 登录有审计、退出没有的话，会话少了一台设备时查不出是主动退出还是过期。
func TestLogoutIsAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewAuthHandler(NewAuthManager(&memoryAuthStore{}))
	logs := &capturingLogWriter{}
	handler.SetLogStore(logs)
	router := gin.New()
	handler.Register(router)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if recorder.Code != http.StatusOK || countAction(logs.actions(), "auth_logout") != 1 {
		t.Fatalf("status = %d actions = %v", recorder.Code, logs.actions())
	}
}

type failingMediaBaseURLStore struct{}

func (failingMediaBaseURLStore) LoadLocalMediaBaseURL(context.Context) (string, bool, error) {
	return "", false, nil
}
func (failingMediaBaseURLStore) SaveLocalMediaBaseURL(context.Context, string) error {
	return errors.New("database is locked")
}

// 设置保存失败返回 500，以前错误日志里一条都没有。
func TestSettingSaveFailureIsLogged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, err := NewMediaBaseURLHandler(context.Background(), failingMediaBaseURLStore{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := &capturingLogWriter{}
	handler.SetLogStore(logs)
	router := gin.New()
	handler.Register(router)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/system/media-base-url", strings.NewReader(`{"base_url":""}`)))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if countAction(logs.actions(), "system_media_base_url_save") != 1 || logs.entries[0].Kind != storage.LogKindError {
		t.Fatalf("entries = %#v", logs.entries)
	}
}

type failingProfileStore struct{ BotProfileStore }

func (failingProfileStore) SaveProfileConfig(assistant.BotConfig) error {
	return errors.New("disk full")
}

// 机器人指令改的配置没存下来，重启后会变回去；以前只打到终端。
func TestRuntimeConfigPersistFailureIsLogged(t *testing.T) {
	persistor := NewRuntimePersistor(failingProfileStore{})
	logs := &capturingLogWriter{}
	persistor.SetAppLogWriter(logs)
	persistor.SaveBotConfig(assistant.BotConfig{ID: "bot-a"})
	if countAction(logs.actions(), "runtime_config_persist") != 1 || logs.entries[0].Target != "bot-a" {
		t.Fatalf("entries = %#v", logs.entries)
	}
}
