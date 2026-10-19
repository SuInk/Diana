// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestConsoleGroupNaturalSplitOverrideRoundTrip(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "memory"
		if persistent {
			name = "persistent"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			on, off := true, false
			base := assistant.DefaultBotConfig()
			base.NaturalReplySplitEnabled = &on
			runtime := assistant.NewRuntime(base, consoleGroupListChannel{result: map[string]any{
				"items": []any{map[string]any{"group_id": "10001"}},
			}}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
			handler := NewBotHandler(ctx, runtime)
			var store BotGroupConfigStore = NewMemoryBotGroupConfigStore()
			var db *storage.SQLiteStore
			if persistent {
				var err error
				db, err = storage.NewSQLiteStore(filepath.Join(t.TempDir(), "groups.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				store, err = NewPersistentBotGroupConfigStore(ctx, db)
				if err != nil {
					t.Fatal(err)
				}
			}
			handler.SetGroupConfigStore(store)
			router := botTestRouter(handler)
			assertSplit := func(where string, got, want *bool) {
				t.Helper()
				if (got == nil) != (want == nil) || (got != nil && want != nil && *got != *want) {
					gotJSON, _ := json.Marshal(got)
					wantJSON, _ := json.Marshal(want)
					t.Errorf("%s: natural split=%s, want %s", where, gotJSON, wantJSON)
				}
			}
			for _, value := range []*bool{nil, &off, &on, nil} {
				body, err := json.Marshal(consoleGroupSavePayload{Config: assistant.GroupConfig{
					GroupID:                  "10001",
					NaturalReplySplitEnabled: value,
					ReplyPreserveLineBreaks:  value,
				}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, "/api/assistant/groups", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
				}
				var response consoleGroupSavePayload
				if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
					t.Fatal(err)
				}
				assertSplit("save response", response.Config.NaturalReplySplitEnabled, value)
				assertSplit("layout save response", response.Config.ReplyPreserveLineBreaks, value)
				if persistent {
					store, err = NewPersistentBotGroupConfigStore(ctx, db)
					if err != nil {
						t.Fatal(err)
					}
					handler.SetGroupConfigStore(store)
				}
				saved, ok := store.ConfigForGroup(response.Config.BotProfileID, "10001")
				if !ok {
					t.Fatal("saved group missing")
				}
				assertSplit("stored config", saved.NaturalReplySplitEnabled, value)
				assertSplit("layout stored config", saved.ReplyPreserveLineBreaks, value)
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/groups", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
				}
				var list consoleGroupsResponse
				if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
					t.Fatal(err)
				}
				if len(list.Groups) != 1 {
					t.Fatalf("listed groups=%d, want 1", len(list.Groups))
				}
				assertSplit("list response", list.Groups[0].NaturalReplySplitEnabled, value)
				assertSplit("layout list response", list.Groups[0].ReplyPreserveLineBreaks, value)
			}
		})
	}
}

func TestGroupChatModelPersistsAndResets(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPersistentBotGroupConfigStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	base := assistant.BotConfig{ID: "bot"}.WithDefaults()
	cfg := assistant.GroupConfig{BotProfileID: "bot", GroupID: "100", ChatModel: &assistant.GroupChatModel{ProfileID: "provider", Model: "model"}}
	if _, err = store.SaveGroupConfig(cfg, base); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewPersistentBotGroupConfigStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := reloaded.ConfigForGroup("bot", "100")
	if !ok || saved.ChatModel == nil || *saved.ChatModel != *cfg.ChatModel {
		t.Fatalf("lost model: %+v", saved.ChatModel)
	}
	runtime := assistant.NewRuntime(base, consoleGroupListChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandler(ctx, runtime)
	handler.SetGroupConfigStore(reloaded)
	omitted := cfg
	omitted.ChatModel = nil
	preserved, err := handler.sanitizeGroupConfigPayload(omitted, "100")
	if err != nil || preserved.ChatModel == nil || *preserved.ChatModel != *cfg.ChatModel {
		t.Fatalf("old client cleared model: %+v, %v", preserved.ChatModel, err)
	}
	cfg.ChatModel = &assistant.GroupChatModel{}
	reset, err := handler.sanitizeGroupConfigPayload(cfg, "100")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reloaded.SaveGroupConfig(reset, base); err != nil {
		t.Fatal(err)
	}
	final, err := NewPersistentBotGroupConfigStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = final.ConfigForGroup("bot", "100")
	if saved.ChatModel == nil || saved.ChatModel.ProfileID != "" || saved.ChatModel.Model != "" {
		t.Fatal("reset not persisted")
	}
	cfg.ChatModel = &assistant.GroupChatModel{ProfileID: "provider"}
	if _, err := handler.sanitizeGroupConfigPayload(cfg, "100"); err == nil {
		t.Fatal("accepted incomplete binding")
	}
	cfg.ChatModel.Model = "model"
	if _, err := handler.sanitizeGroupConfigPayload(cfg, "100"); err == nil {
		t.Fatal("accepted unavailable provider")
	}
}

func TestGroupChatModelFieldSaveAndSeedRepair(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPersistentBotGroupConfigStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	base := assistant.BotConfig{ID: "a"}.WithDefaults()
	for _, id := range []string{"a", "b"} {
		_, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: id, GroupID: "g", SystemPrompt: "keep", ChatModel: &assistant.GroupChatModel{ProfileID: "seed-old", Model: "old"}}, base)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveGroupChatModel("a", "g", assistant.GroupChatModel{ProfileID: "seed-old", Model: "new"}, base); err != nil {
		t.Fatal(err)
	}
	a, _ := store.ConfigForGroup("a", "g")
	b, _ := store.ConfigForGroup("b", "g")
	if a.SystemPrompt != "keep" || a.ChatModel.Model != "new" || b.ChatModel.Model != "old" {
		t.Fatal("field update changed unrelated configuration")
	}
	if err := RepairSeedLLMProfileRefs(ctx, db, "seed-new"); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewPersistentBotGroupConfigStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = loaded.ConfigForGroup("a", "g")
	if a.ChatModel.ProfileID != "seed-new" || a.ChatModel.Model != "new" {
		t.Fatal("seed repair lost group model")
	}
}
