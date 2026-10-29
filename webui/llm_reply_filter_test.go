// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postJSON(router http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestReplyFilterAssistReturnsFirstValidRule(t *testing.T) {
	client := &echoPersonaClient{reply: "```regex\n^\\[reply:[^\\]]*\\]\\s*\n```"}
	rec := postJSON(personaRouterWithClient(client), "/api/llm/reply-filter-assist", `{"request":"删掉开头的 [reply:xxx]","sample":"[reply:...] 你好"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Rule string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Rule != `^\[reply:[^\]]*\]\s*` {
		t.Fatalf("rule = %q", resp.Rule)
	}
	if user := client.captured.Messages[1].Content; !strings.Contains(user, "示例回复") || !strings.Contains(user, "[reply:...] 你好") {
		t.Fatalf("user message = %q", user)
	}
}

func TestReplyFilterAssistRejectsEmptyAndUnusableRules(t *testing.T) {
	for reply, body := range map[string]string{
		`^\[x\]`: `{"request":""}`,
		"[":      `{"request":"删括号"}`,
		"x*":     `{"request":"删 x"}`,
	} {
		if rec := postJSON(personaRouterWithClient(&echoPersonaClient{reply: reply}), "/api/llm/reply-filter-assist", body); rec.Code == http.StatusOK {
			t.Fatalf("reply %q body %s unexpectedly succeeded: %s", reply, body, rec.Body.String())
		}
	}
}

func TestReplyFilterTestRunsRulesWithGoRegexp(t *testing.T) {
	rec := postJSON(personaRouterWithClient(&echoPersonaClient{}), "/api/assistant/reply-filter/test", `{"rules":["喵$","("],"text":"好耶喵\n再见"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result string
		Steps  []struct {
			Removed []string
			Error   string
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Result != "好耶\n再见" || len(resp.Steps) != 2 || resp.Steps[0].Removed[0] != "喵" || resp.Steps[1].Error == "" {
		t.Fatalf("resp = %#v", resp)
	}
}
