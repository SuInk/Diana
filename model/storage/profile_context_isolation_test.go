package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestLegacySharedContextConfigPreservesProfilesAndHistory(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacy := map[string]any{
		"active_id":                 "qq",
		"isolate_platform_contexts": false,
		"profiles": []assistant.BotConfig{
			{ID: "qq", Name: "QQ bot", Platform: assistant.PlatformOneBotV11},
			{ID: "tg", Name: "Telegram bot", Platform: assistant.PlatformTelegram},
		},
	}
	if err := store.saveJSON(ctx, botProfilesKey, legacy); err != nil {
		t.Fatal(err)
	}
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "100", UserID: "200", MessageID: "legacy-message", RawMessage: "Legacy shared context"}
	if err := store.AppendMessageEvent(ctx, "group:100", event); err != nil {
		t.Fatal(err)
	}
	set, found, err := store.LoadBotProfiles(ctx)
	if err != nil || !found {
		t.Fatalf("load legacy profiles: found=%v err=%v", found, err)
	}
	set = set.WithDefaults()
	if set.ActiveID != "qq" || len(set.Profiles) != 2 || set.Profiles[0].ID != "qq" || set.Profiles[1].ID != "tg" {
		t.Fatalf("legacy profile identity changed: %#v", set)
	}
	if err := store.SaveBotProfiles(ctx, set); err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if found, err := store.loadJSON(ctx, botProfilesKey, &stored); err != nil || !found {
		t.Fatalf("reload profiles: found=%v err=%v", found, err)
	}
	if _, found := stored["isolate_platform_contexts"]; found {
		t.Fatal("obsolete shared-context setting was persisted again")
	}
	for _, session := range []string{"group:100", "qq:group:100", "tg:group:100"} {
		history, err := store.ListRecentMessageEvents(ctx, session, 10)
		if err != nil {
			t.Fatal(err)
		}
		if session == "group:100" {
			if len(history) != 1 || history[0].MessageID != event.MessageID {
				t.Fatalf("legacy history was modified: %#v", history)
			}
		} else if len(history) != 0 {
			t.Fatalf("legacy shared history was assigned to %q: %#v", session, history)
		}
	}
}
