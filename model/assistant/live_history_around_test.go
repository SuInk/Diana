// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// 真实数据回放，默认跳过：DIANA_LIVE_HISTORY_DB 指向一份数据库副本（不要指向线上库），
// DIANA_LIVE_HISTORY_NAMESPACE / _GROUP / _MESSAGE 指定当前群和一条在别的群里的消息。
func TestLiveHistoryAroundLocatesOtherGroupOnRealData(t *testing.T) {
	path := os.Getenv("DIANA_LIVE_HISTORY_DB")
	if path == "" {
		t.Skip("set DIANA_LIVE_HISTORY_DB to a copy of a production database")
	}
	namespace, group, messageID := os.Getenv("DIANA_LIVE_HISTORY_NAMESPACE"), os.Getenv("DIANA_LIVE_HISTORY_GROUP"), os.Getenv("DIANA_LIVE_HISTORY_MESSAGE")
	store, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	enabled := true
	r := assistant.NewRuntime(assistant.BotConfig{CrossGroupMemoryEnabled: &enabled}, nil, assistant.NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(store)
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: group, ContextNamespace: namespace, ProfileID: namespace, Platform: assistant.PlatformOneBotV11}
	raw, err := assistant.RunChatHistoryToolForTest(r, event, map[string]any{"operation": "around", "message_id": messageID, "before": 2, "after": 2})
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	var result struct {
		Message string `json:"message"`
		Items   []struct {
			MessageID string `json:"message_id"`
			Sender    string `json:"sender"`
			Text      string `json:"text"`
			GroupID   string `json:"group_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	t.Logf("message=%s", result.Message)
	found := false
	for _, item := range result.Items {
		t.Logf("[%s] %s: %s", item.GroupID, item.Sender, strings.ReplaceAll(item.Text, "\n", " / "))
		if item.MessageID == messageID {
			found = true
		}
	}
	if !found {
		t.Fatal("结果里没有目标消息")
	}
}
