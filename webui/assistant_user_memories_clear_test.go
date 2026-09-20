// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func seedMemories(t *testing.T, store interface {
	ApplyMemoryCandidates(context.Context, assistant.MemoryWriteRequest) ([]assistant.StructuredMemoryItem, error)
	UpdateUserMemory(context.Context, assistant.MessageEvent, assistant.UserMemoryUpdate) (assistant.UserMemoryProfile, error)
}, profileID, userID string, count int) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
		ProfileID: profileID, Kind: assistant.EventKindGroup, GroupID: "100", UserID: userID,
		SenderName: "Person", MessageID: "m-" + profileID + userID, RawMessage: "hi", Time: time.Now().Unix(),
	}, assistant.UserMemoryUpdate{}); err != nil {
		t.Fatal(err)
	}
	prefix := ""
	if profileID != "" {
		prefix = profileID + ":"
	}
	for index := 0; index < count; index++ {
		if _, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			Session: prefix + "group:100", EventKind: assistant.EventKindGroup, GroupID: "100", SubjectUserID: userID,
			SourceMessageID: "mem-" + profileID + userID + strconv.Itoa(index), SourceEventTime: time.Now(),
			Candidates: []assistant.MemoryCandidate{{
				Key: "fact." + strconv.Itoa(index), Kind: assistant.MemoryKindFact, Topic: "偏好",
				Content: "第 " + strconv.Itoa(index) + " 条", Visibility: assistant.MemoryVisibilityUser,
				Confidence: 0.95, Importance: 0.8,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func memoryCount(t *testing.T, router http.Handler, profileID, userID string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/"+userID+"?profile="+profileID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	var detail assistantUserDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	return len(detail.StructuredMemories)
}

func TestClearAssistantUserMemories(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	seedMemories(t, store, "qq", "200", 3)
	seedMemories(t, store, "tg", "200", 2)

	if got := memoryCount(t, router, "qq", "200"); got != 3 {
		t.Fatalf("清空前 = %d", got)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/assistant/users/200/memories?profile=qq", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		OK      bool  `json:"ok"`
		Cleared int64 `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Cleared != 3 {
		t.Fatalf("result = %#v", result)
	}
	if got := memoryCount(t, router, "qq", "200"); got != 0 {
		t.Fatalf("清空后还剩 %d 条", got)
	}
	// 同号账号在别的机器人名下的记忆不受影响。
	if got := memoryCount(t, router, "tg", "200"); got != 2 {
		t.Fatalf("清到了别的机器人：tg 剩 %d 条", got)
	}
}

func TestClearSingleAssistantUserMemory(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	seedMemories(t, store, "qq", "200", 3)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/200?profile=qq", nil))
	var detail assistantUserDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	target := detail.StructuredMemories[0].ID

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/assistant/users/200/memories/"+target+"?profile=qq", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete one: %d %s", rec.Code, rec.Body.String())
	}
	if got := memoryCount(t, router, "qq", "200"); got != 2 {
		t.Fatalf("删一条之后剩 %d 条", got)
	}
}

// 清空不可逆，作用域留空在底层是「只匹配没有命名空间的旧记录」，不能靠猜。
func TestClearAssistantUserMemoriesRequiresProfileScope(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	seedMemories(t, store, "qq", "200", 2)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/assistant/users/200/memories", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing scope should be rejected: %d %s", rec.Code, rec.Body.String())
	}
	if got := memoryCount(t, router, "qq", "200"); got != 2 {
		t.Fatalf("被误清了：剩 %d 条", got)
	}
}

// 删掉人员记录时，他身上的长期记忆也要一起收掉——否则控制台上人没了，机器人
// 却还记得他说过什么。
func TestDeletingUserAlsoClearsStructuredMemories(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	seedMemories(t, store, "qq", "200", 3)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/200?profile=qq", nil))
	var detail assistantUserDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"profile": detail.Profile})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/assistant/users/200?profile=qq", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete user: %d %s", rec.Code, rec.Body.String())
	}

	items, err := store.ListStructuredMemoriesBySubject(context.Background(), "qq", "200", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("人删了，长期记忆还剩 %d 条", len(items))
	}
}
