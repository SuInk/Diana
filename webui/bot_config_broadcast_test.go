package webui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// 聊天里换模型改的是 WebUI 这同一份机器人配置，改完必须播出去，否则开着的页面
// 停在旧值。回调里回读一次配置：通知要发生在存储的锁外面，读回来才不会卡死。
func TestModelRoleSaveNotifiesOpenConsoles(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	bot := assistant.BotConfig{ID: "bot", OwnerID: "11"}
	if err := store.SaveProfiles(assistant.ProfileSet{Profiles: []assistant.BotConfig{bot}}); err != nil {
		t.Fatal(err)
	}
	notified := 0
	seen := ""
	store.SetChangeListener(func() {
		notified++
		seen = store.Profiles().Profiles[0].ModelRoles["chat"].Model
	})
	if _, err := store.SaveModelRole(bot, "chat", assistant.ModelRole{ProfileID: "two", Model: "new-chat"}); err != nil {
		t.Fatal(err)
	}
	if notified != 1 || seen != "new-chat" {
		t.Fatalf("model role change not broadcast: notified=%d model=%q", notified, seen)
	}
	// 屏蔽名单、禁用群这些聊天指令走的是另一条写入路径，同样要播。
	if err := store.SaveDisabledGroups("bot", []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if notified != 2 {
		t.Fatalf("profile field change not broadcast: notified=%d", notified)
	}
	// 没改成什么就不该通知，否则页面被没发生的变更来回刷。
	if _, err := store.SaveModelRole(assistant.BotConfig{ID: "missing"}, "chat", assistant.ModelRole{Model: "x"}); err == nil {
		t.Fatal("saving an unknown bot should fail")
	}
	if notified != 2 {
		t.Fatalf("failed save broadcast a change: notified=%d", notified)
	}
}

func TestPublishConfigChangedFrame(t *testing.T) {
	hub := NewEventHub()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)
	hub.PublishConfigChanged("bot")
	message := <-sub
	if message.Event != "config_changed" {
		t.Fatalf("unexpected event %q", message.Event)
	}
	payload := map[string]string{}
	if err := json.Unmarshal(message.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "bot" {
		t.Fatalf("unexpected payload %s", message.Data)
	}
}
