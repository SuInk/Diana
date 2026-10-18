// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postRSSAssist(router http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/llm/rss-assist", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRSSAssistBuildsEachSupportedDraft(t *testing.T) {
	for _, kind := range []string{"regex", "judge", "reply"} {
		t.Run(kind, func(t *testing.T) {
			reply := "整理后的要求"
			if kind == "regex" {
				reply = `(?i)release|发布`
			}
			client := &echoPersonaClient{reply: reply}
			rec := postRSSAssist(personaRouterWithClient(client), `{"kind":"`+kind+`","request":"只看正式版本"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if len(client.captured.Messages) != 2 || !strings.Contains(client.captured.Messages[0].Content, rssAssistPrompts[kind]) || client.captured.Messages[1].Content != "只看正式版本" {
				t.Fatalf("messages = %#v", client.captured.Messages)
			}
		})
	}
}

func TestRSSAssistRejectsEmptyUnknownAndInvalidRegex(t *testing.T) {
	router := personaRouterWithClient(&echoPersonaClient{reply: "["})
	for _, body := range []string{
		`{"kind":"reply","request":""}`,
		`{"kind":"unknown","request":"帮我写"}`,
		`{"kind":"regex","request":"匹配发布"}`,
	} {
		if rec := postRSSAssist(router, body); rec.Code == http.StatusOK {
			t.Fatalf("body %s unexpectedly succeeded: %s", body, rec.Body.String())
		}
	}
}

func TestNormalizeRSSAssistStripsFenceAndLimitsLength(t *testing.T) {
	if got := normalizeRSSAssist("```regex\n(?i)release\n```"); got != "(?i)release" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeRSSAssist(strings.Repeat("字", rssAssistMaxRunes+5)); len([]rune(got)) != rssAssistMaxRunes {
		t.Fatalf("length = %d", len([]rune(got)))
	}
}
