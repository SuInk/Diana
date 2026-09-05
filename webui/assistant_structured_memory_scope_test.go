package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestAssistantStructuredMemoriesFollowExactProfile(t *testing.T) {
	ctx := context.Background()
	store, router := newAssistantUsersTestRouter(t)
	expected := map[string]int{"": 1, "qq": 2, "qq-other": 3, "tg": 4, "id_%": 5}
	for profileID, count := range expected {
		if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
			ProfileID: profileID, Kind: assistant.EventKindGroup, GroupID: "100", UserID: "200",
			SenderName: "Person " + profileID, MessageID: "message-" + profileID, RawMessage: "A message", Time: time.Now().Unix(),
		}, assistant.UserMemoryUpdate{}); err != nil {
			t.Fatal(err)
		}
		prefix := ""
		if profileID != "" {
			prefix = profileID + ":"
		}
		for index := 0; index < count; index++ {
			_, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
				Session: prefix + "group:100", EventKind: assistant.EventKindGroup, GroupID: "100", SubjectUserID: "200",
				SourceMessageID: "memory-" + profileID + "-" + strconv.Itoa(index), SourceEventTime: time.Now(),
				Candidates: []assistant.MemoryCandidate{{
					Key: "fact." + strconv.Itoa(index), Kind: assistant.MemoryKindFact, Topic: "Preferences",
					Content: "Profile " + profileID + " memory " + strconv.Itoa(index), Visibility: assistant.MemoryVisibilityUser,
					Confidence: 0.95, Importance: 0.8,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for profileID, count := range expected {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users/200?profile="+url.QueryEscape(profileID), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("detail %q: %d %s", profileID, rec.Code, rec.Body.String())
		}
		var detail assistantUserDetailResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if len(detail.StructuredMemories) != count {
			t.Fatalf("profile %q got %d memories, want %d", profileID, len(detail.StructuredMemories), count)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/users", nil))
	var list assistantUsersResponse
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Users) != len(expected) {
		t.Fatalf("got %d profiles, want %d", len(list.Users), len(expected))
	}
	for _, user := range list.Users {
		if user.StructuredMemoryCount != expected[user.BotProfileID] {
			t.Fatalf("profile %q count=%d, want %d", user.BotProfileID, user.StructuredMemoryCount, expected[user.BotProfileID])
		}
	}
}
