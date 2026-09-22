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
	if err != nil || !found || p.DisplayName != "Original" || p.Favorability != 12 {
		t.Fatalf("other bot modified: %+v %v", p, err)
	}
}

// TestAssistantUserManualPortraitIsNormalized 控制台手填的画像要和模型写进来的
// 走同一道归一：补上栏目名、按栏校验取值。时区尤其不能放过——存进一句「在德国」
// 不会报错，但它换算不出时间，跨时区那条链路只会当成「没记过」。
func TestAssistantUserManualPortraitIsNormalized(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	ctx := context.Background()
	if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{ProfileID: "bot-a", UserID: "10001", SenderName: "Original", RawMessage: "hi"}, assistant.UserMemoryUpdate{FavorabilityDelta: 1}); err != nil {
		t.Fatal(err)
	}
	save := func(traits []assistant.UserPortraitTrait) *httptest.ResponseRecorder {
		t.Helper()
		profile, found, err := store.GetUserMemoryExact(ctx, "bot-a", "10001")
		if err != nil || !found {
			t.Fatalf("profile: %v %v", found, err)
		}
		profile.Portrait = traits
		body, _ := json.Marshal(map[string]any{"profile": profile})
		r := httptest.NewRequest(http.MethodPut, "/api/assistant/users/10001?profile=bot-a", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	// 换算不了的时区一律打回，并且要说清楚该怎么填。
	rejected := save([]assistant.UserPortraitTrait{{Field: assistant.PortraitFieldTimezone, Value: "在德国", Source: assistant.PortraitSourceManual}})
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "Europe/Berlin") {
		t.Fatalf("时区没被打回或没给出可照做的提示：%d %s", rejected.Code, rejected.Body.String())
	}

	// 合法的 IANA 名收下，栏目名由后端补齐，手动来源保留。
	if w := save([]assistant.UserPortraitTrait{{Field: assistant.PortraitFieldTimezone, Value: "Europe/Berlin", Source: assistant.PortraitSourceManual}}); w.Code != http.StatusOK {
		t.Fatalf("手填时区被拒：%d %s", w.Code, w.Body.String())
	}
	saved, _, err := store.GetUserMemoryExact(ctx, "bot-a", "10001")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Portrait) != 1 {
		t.Fatalf("画像 = %+v", saved.Portrait)
	}
	trait := saved.Portrait[0]
	if trait.Value != "Europe/Berlin" || trait.Label != "时区" || trait.Source != assistant.PortraitSourceManual || trait.UpdatedAt.IsZero() {
		t.Fatalf("手填的画像没有被归一：%+v", trait)
	}
	// 手填的时区立刻能拿来换算，不必等后台再评估一次。
	if location := assistant.PortraitTimezone(saved.Portrait); location == nil || location.String() != "Europe/Berlin" {
		t.Fatalf("手填的时区换算不出来：%v", location)
	}
}
