// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

type repositoryIssueTestTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func TestBotHandlerListsPersistentRepositoryIssueDrafts(t *testing.T) {
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.SaveRepositoryIssueDraft(context.Background(), assistant.RepositoryIssueDraft{
		ID: "draft-web-1", GroupID: "group-1", Repository: "acme/demo",
		RequesterID: "member", RequesterName: "Alice", Status: "pending",
		Input:     map[string]any{"title": "Login fails", "body": "Detailed steps"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	handler := &BotHandler{sqlite: store}
	router := gin.New()
	router.GET("/api/assistant/plugins/repository-publish/drafts", handler.listRepositoryIssueDrafts)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins/repository-publish/drafts?status=all", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Detailed steps") || !strings.Contains(recorder.Body.String(), "Alice") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// 过期是按时间分的：同一批 pending 草稿，status=pending 只给还能确认的，
// status=expired 只给过了有效期的。
func TestListRepositoryIssueDraftsSplitsExpired(t *testing.T) {
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	drafts := []assistant.RepositoryIssueDraft{
		{ID: "fresh", GroupID: "group-1", Repository: "acme/demo", RequesterID: "member", Status: "pending",
			Input: map[string]any{"title": "还能确认"}, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(48 * time.Hour)},
		{ID: "stale", GroupID: "group-1", Repository: "acme/demo", RequesterID: "member", Status: "pending",
			Input: map[string]any{"title": "已经过期"}, CreatedAt: now.Add(-9 * 24 * time.Hour), UpdatedAt: now.Add(-9 * 24 * time.Hour), ExpiresAt: now.Add(-2 * 24 * time.Hour)},
	}
	for _, draft := range drafts {
		if err := store.SaveRepositoryIssueDraft(context.Background(), draft); err != nil {
			t.Fatal(err)
		}
	}
	handler := &BotHandler{sqlite: store}
	router := gin.New()
	router.GET("/api/assistant/plugins/repository-publish/drafts", handler.listRepositoryIssueDrafts)

	body := func(status string) string {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins/repository-publish/drafts?status="+status, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%s code=%d body=%s", status, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	if pending := body("pending"); !strings.Contains(pending, "还能确认") || strings.Contains(pending, "已经过期") {
		t.Fatalf("pending 列表不对：%s", pending)
	}
	if expired := body("expired"); !strings.Contains(expired, "已经过期") || strings.Contains(expired, "还能确认") {
		t.Fatalf("expired 列表不对：%s", expired)
	}
	if all := body("all"); !strings.Contains(all, "还能确认") || !strings.Contains(all, "已经过期") {
		t.Fatalf("all 列表应当两条都在：%s", all)
	}
}

func (t repositoryIssueTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clonedURL := *request.URL
	clonedURL.Scheme = t.target.Scheme
	clonedURL.Host = t.target.Host
	clone.URL = &clonedURL
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func TestBotHandlerCreatesRepositoryIssueThroughPublishingPlugin(t *testing.T) {
	var mu sync.Mutex
	methods := make([]string, 0, 2)
	authorizations := make([]string, 0, 2)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		// 只模拟 REST：GraphQL 查询直接 404、不计入请求记录，工具会退回 REST。
		if request.URL.Path == "/graphql" {
			http.NotFound(w, request)
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":42,"title":"WebUI issue","body":"details","state":"open","html_url":"https://github.com/acme/demo/issues/42"}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer github.Close()
	target, err := url.Parse(github.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: repositoryIssueTestTransport{base: github.Client().Transport, target: target}}
	manager := assistant.NewPluginManager(assistant.NewRepositoryPublishPlugin(client))
	if _, err := manager.UpdateSettings(assistant.RepositoryPublishPluginID, map[string]any{
		"github_token":         "test-issue-token",
		"allowed_repositories": "acme/demo",
	}); err != nil {
		t.Fatal(err)
	}
	runtime := assistant.NewRuntime(assistant.BotConfig{ID: "issue-bot"}, fakeChannel{}, manager, nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	body := []byte(`{"repository":"acme/demo","title":"WebUI issue","body":"details","labels":["bug"]}`)
	if err := handler.profiles.SaveProfiles(assistant.ProfileSet{Profiles: []assistant.BotConfig{runtime.ProfileConfig("")}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repository-publish/issues?profile=issue-bot", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result assistant.RepositoryIssueCreateResult
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Issue == nil || result.Issue.Number != 42 || result.Repository != "acme/demo" {
		t.Fatalf("result=%#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Fatalf("GitHub methods=%v", methods)
	}
	for _, authorization := range authorizations {
		if authorization != "Bearer test-issue-token" {
			t.Fatalf("authorization=%q", authorization)
		}
	}
}
