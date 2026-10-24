// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestAssistantUserMemorySourceShowsOriginalMessageAndContext(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).Unix()
	session := "qq:group:100"
	for index := 0; index < 10; index++ {
		text := "闲聊 " + strconv.Itoa(index)
		if index == 6 {
			text = "我对花生过敏"
		}
		if err := store.AppendMessageEvent(ctx, session, assistant.MessageEvent{
			ProfileID: "qq", Kind: assistant.EventKindGroup, GroupID: "100", UserID: "200", SenderName: "小明",
			MessageID: "m" + strconv.Itoa(index), Time: base + int64(index*10), RawMessage: text,
			Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
		Session: session, EventKind: assistant.EventKindGroup, GroupID: "100", SubjectUserID: "200",
		SourceMessageID: "m6", SourceEventTime: time.Unix(base+60, 0),
		Candidates: []assistant.MemoryCandidate{{
			Key: "fact.allergy", Kind: assistant.MemoryKindFact, Topic: "过敏", Content: "小明对花生过敏",
			Evidence: "我对花生过敏", Visibility: assistant.MemoryVisibilityUser, Confidence: 0.95, Importance: 0.8,
		}},
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/200/memories/"+items[0].ID+"/source?profile=qq", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("source: %d %s", rec.Code, rec.Body.String())
	}
	var got memorySourceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Origin != "message" || got.Evidence != "我对花生过敏" {
		t.Fatalf("response = %#v", got)
	}
	if len(got.Messages) != memorySourceBefore+1+memorySourceAfter {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if source := got.Messages[memorySourceBefore]; !source.Source || source.MessageID != "m6" || source.Text != "我对花生过敏" {
		t.Fatalf("source message = %#v", source)
	}

	// 别的机器人名下查不到这条记忆。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/200/memories/"+items[0].ID+"/source?profile=tg", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross profile: %d %s", rec.Code, rec.Body.String())
	}
}

// 摘要提炼的记忆没有单条原消息，退回到来源时间前后的聊天。
func TestAssistantUserMemorySourceFallsBackToTimeWindowForSummary(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).Unix()
	session := "qq:group:100"
	for index := 0; index < 3; index++ {
		if err := store.AppendMessageEvent(ctx, session, assistant.MessageEvent{
			ProfileID: "qq", Kind: assistant.EventKindGroup, GroupID: "100", UserID: "200",
			MessageID: "s" + strconv.Itoa(index), Time: base + int64(index), RawMessage: "第 " + strconv.Itoa(index) + " 句",
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
		Session: session, EventKind: assistant.EventKindGroup, GroupID: "100", SubjectUserID: "200",
		SourceMessageID: "summary:job-1", SourceEventTime: time.Unix(base+1, 0),
		Candidates: []assistant.MemoryCandidate{{
			Key: "fact.x", Kind: assistant.MemoryKindFact, Topic: "作息", Content: "小明常熬夜",
			Visibility: assistant.MemoryVisibilityUser, Confidence: 0.9, Importance: 0.6,
		}},
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/200/memories/"+items[0].ID+"/source?profile=qq", nil))
	var got memorySourceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("source: %d %s", rec.Code, rec.Body.String())
	}
	if got.Origin != "summary" || len(got.Messages) != 3 || got.Note == "" {
		t.Fatalf("response = %#v", got)
	}
	for _, message := range got.Messages {
		if message.Source {
			t.Fatalf("summary memory marked a source message: %#v", message)
		}
	}
}
