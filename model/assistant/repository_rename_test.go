// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 草稿写的是改名前的 acme/old：REST 对旧名回 301，GraphQL 悄悄跟到 acme/demo，返回的
// 链接对不上旧名。以前这会报成「无法解析的响应」，确认码回多少遍都停在同一处。
func TestRepositoryIssueApproveFollowsRenamedRepository(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/graphql":
			if r.Header.Get("Authorization") != "Bearer "+repositoryPublishTestToken {
				http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"issues": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false},
				"nodes": []map[string]any{{
					"number": 5, "title": "旧问题", "state": "OPEN", "url": "https://github.com/acme/demo/issues/5",
					"updatedAt": "2026-01-01T00:00:00Z",
				}},
			}}}})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/old"):
			http.Redirect(w, r, server.URL+"/repositories/7", http.StatusMovedPermanently)
		case r.URL.Path == "/repositories/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "acme/demo"})
		default:
			github.handler(w, r)
		}
	}))
	defer server.Close()

	tool := repositoryPublishTestTool(server, "给 acme/old 提个 issue", nil)
	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation": "create", "repository": "acme/old", "title": "登录失败", "body": "重置密码后无法登录。",
	})
	if !draft.OK || draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	tool.event.RawMessage = repositoryIssueConfirmationCode(draft.Draft.ID)

	stuck := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "approve", "draft_id": draft.Draft.ID})
	if stuck.FailureCode != "repository_renamed" || stuck.RedirectRepository != "acme/demo" || !strings.Contains(stuck.Message, "acme/demo") {
		t.Fatalf("approve on renamed repository=%#v", stuck)
	}

	// 改投只认 GitHub 给的新名字，模型随手填的别的仓库不行。
	wrong := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "approve", "draft_id": draft.Draft.ID, "repository": "acme/other"})
	if wrong.FailureCode != "redirect_mismatch" || github.count(http.MethodPost) != 0 {
		t.Fatalf("retarget to unrelated repository=%#v posts=%d", wrong, github.count(http.MethodPost))
	}

	approved := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "approve", "draft_id": draft.Draft.ID, "repository": "acme/demo"})
	if !approved.OK || approved.Outcome != "created" || approved.Repository != "acme/demo" || approved.Issue == nil {
		t.Fatalf("retargeted approve=%#v", approved)
	}
	if posted := github.last(http.MethodPost); posted.Path != "/repos/acme/demo/issues" {
		t.Fatalf("posted to %q", posted.Path)
	}
	stored, ok, err := tool.plugin.findResolvedDraft(t.Context(), "private:owner", draft.Draft.ID)
	if err != nil || !ok || stored.Repository != "acme/demo" {
		t.Fatalf("stored draft=%#v ok=%v err=%v", stored, ok, err)
	}
}

// 没改名的仓库照常报原来的失败，不多出一个 redirect_repository。
func TestRepositoryIssueInvalidResponseWithoutRenameStaysUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/acme/demo" {
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "acme/demo"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "", nil)
	result := tool.explainRepositoryRename(t.Context(), repositoryIssueResult{Repository: "acme/demo"}.fail("invalid_response", "x"))
	if result.FailureCode != "invalid_response" || result.RedirectRepository != "" {
		t.Fatalf("result=%#v", result)
	}
}
