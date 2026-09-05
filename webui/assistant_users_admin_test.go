// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestAssistantUserEditsAndDeletesExactProfile(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	ctx := context.Background()
	for _, id := range []string{"", "bot-a", "bot-b"} {
		_, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{ProfileID: id, UserID: "10001", SenderName: "Original", RawMessage: "remember me"}, assistant.UserMemoryUpdate{FavorabilityDelta: 2})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", "bot-a"} {
		p, found, err := store.GetUserMemoryExact(ctx, id, "10001")
		if err != nil || !found {
			t.Fatalf("profile: %v %v", found, err)
		}
		p.DisplayName, p.Favorability, p.Memories = "Edited", 42, nil
		body, _ := json.Marshal(map[string]any{"profile": p})
		request := func(method, suffix string, want int) {
			t.Helper()
			r := httptest.NewRequest(method, "/api/assistant/users/10001"+suffix, strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("%s %s: %d %s", method, suffix, w.Code, w.Body.String())
			}
		}
		request(http.MethodPut, "", http.StatusBadRequest)
		request(http.MethodPut, "?profile=bot-b", http.StatusBadRequest)
		request(http.MethodPut, "?profile="+id, http.StatusOK)
		request(http.MethodPut, "?profile="+id, http.StatusConflict)
		request(http.MethodDelete, "?profile="+id, http.StatusConflict)
		after, _, err := store.GetUserMemoryExact(ctx, id, "10001")
		if err != nil || after.DisplayName != "Edited" || after.Favorability != 42 || len(after.Memories) != 0 || after.MessageCount != p.MessageCount || !after.LastSeenAt.Equal(p.LastSeenAt) {
			t.Fatalf("after: %+v %v", after, err)
		}
		changes, err := store.ListUserFavorabilityChangesExact(ctx, id, "10001", 50)
		if err != nil || len(changes) != 2 || changes[0].Source != "manual" {
			t.Fatalf("changes: %+v %v", changes, err)
		}
		body, _ = json.Marshal(map[string]any{"profile": after})
		request(http.MethodDelete, "?profile="+id, http.StatusOK)
		if _, found, err := store.GetUserMemoryExact(ctx, id, "10001"); err != nil || found {
			t.Fatalf("deleted: %v %v", found, err)
		}
		changes, err = store.ListUserFavorabilityChangesExact(ctx, id, "10001", 50)
		if err != nil || len(changes) != 0 {
			t.Fatalf("remaining changes: %+v %v", changes, err)
		}
	}
	p, found, err := store.GetUserMemoryExact(ctx, "bot-b", "10001")
	if err != nil || !found || p.DisplayName != "Original" || p.Favorability != 2 {
		t.Fatalf("other bot modified: %+v %v", p, err)
	}
}
