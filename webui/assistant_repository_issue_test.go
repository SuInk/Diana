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
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, manager, nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel {
		return fakeChannel{}
	})
	router := botTestRouter(handler)

	body := []byte(`{"repository":"acme/demo","title":"WebUI issue","body":"details","labels":["bug"]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repository-publish/issues", bytes.NewReader(body))
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
