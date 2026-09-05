// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestConsoleDeleteGroupConfig(t *testing.T) {
	runtime := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(context.Background(), runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	if _, err := h.groupConfigs.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "a", GroupID: "10001"}, runtime.Config()); err != nil {
		t.Fatal(err)
	}
	router := botTestRouter(h)
	for _, test := range []struct {
		suffix string
		status int
	}{{"", 400}, {"?profile=b", 404}, {"?profile=a", 200}, {"?profile=a", 404}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/assistant/groups/10001"+test.suffix, nil))
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.suffix, w.Code, w.Body.String())
		}
	}
}

func TestDeleteGroupConfigPersistsAndIsolatesProfiles(t *testing.T) {
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	persistent, err := NewPersistentBotGroupConfigStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []BotGroupConfigStore{NewMemoryBotGroupConfigStore(), persistent} {
		for _, id := range []string{"", "a", "b"} {
			if _, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: id, GroupID: "10001"}, assistant.BotConfig{}); err != nil {
				t.Fatal(err)
			}
		}
		if found, err := store.DeleteGroupConfig("a", "10001"); err != nil || !found {
			t.Fatalf("delete: %v %v", found, err)
		}
		if found, err := store.DeleteGroupConfig("a", "10001"); err != nil || found {
			t.Fatalf("repeat delete: %v %v", found, err)
		}
		if len(store.Groups().Groups) != 2 {
			t.Fatalf("groups: %+v", store.Groups())
		}
	}
	reloaded, err := NewPersistentBotGroupConfigStore(context.Background(), db)
	if err != nil || len(reloaded.Groups().Groups) != 2 || len(reloaded.Groups().GroupsForProfile("a")) != 0 {
		t.Fatalf("reload: %+v %v", reloaded, err)
	}
}
