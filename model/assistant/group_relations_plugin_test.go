// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/applog"
)

// 关系图必须以插件形式出现在插件页，才能按群开关、才有设置可调。
func TestGroupRelationsPluginIsRegistered(t *testing.T) {
	manager := NewDefaultPluginManager()
	var found *PluginManifest
	for _, state := range manager.List() {
		if state.Manifest.ID == groupRelationsPluginID {
			manifest := state.Manifest
			found = &manifest
		}
	}
	if found == nil {
		t.Fatal("群聊关系图没有注册成内置插件")
	}
	if !found.BuiltIn || !found.Official {
		t.Fatalf("manifest = %#v", *found)
	}
	// 两项设置都要在，否则插件页上是个没得调的空壳。
	keys := map[string]bool{}
	for _, setting := range found.Settings {
		keys[setting.Key] = true
	}
	for _, want := range []string{groupRelationsSettingDefaultRange, groupRelationsSettingMaxMembers} {
		if !keys[want] {
			t.Fatalf("缺少设置项 %s：%#v", want, found.Settings)
		}
	}
	// 声明要用无头浏览器：渲染依赖它，权限清单得说出来。
	var declaresBrowser bool
	for _, permission := range found.Permissions {
		if permission == "browser:headless" {
			declaresBrowser = true
		}
	}
	if !declaresBrowser {
		t.Fatalf("没有声明无头浏览器权限：%#v", found.Permissions)
	}
}

// 这个插件只提供开关和设置，不拦消息——拦了就会把普通聊天吃掉。
func TestGroupRelationsPluginDoesNotHandleMessages(t *testing.T) {
	response, err := NewGroupRelationsPlugin().Handle(context.Background(), PluginRequest{
		Event: MessageEvent{Kind: EventKindGroup, GroupID: "g"},
		Text:  "关系图",
	})
	if err != nil || response != nil {
		t.Fatalf("response = %#v, err = %v，应当完全不处理", response, err)
	}
}

type captureRelationLogs struct {
	mu      sync.Mutex
	entries []applog.Entry
}

func (c *captureRelationLogs) AppendLog(_ context.Context, entry applog.Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
	return nil
}

func (c *captureRelationLogs) find(action string) (applog.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.Action == action {
			return entry, true
		}
	}
	return applog.Entry{}, false
}

// 没有消息存储时要在运行记录里留下一条错误，而不是只在工具返回值里说一句。
// agentRunObserver 记的那条「工具调用完成」在这种情况下和成功长得一模一样。
func TestGroupRelationsToolRecordsFailureInLogs(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureRelationLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "555", UserID: "10001", MessageID: "m1"}

	tool := newDianaGroupRelationsTool(runtime, event, SettingValues{})
	output, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("工具不该抛错：%v", err)
	}
	if !strings.Contains(output, `"ok":false`) {
		t.Fatalf("output = %s", output)
	}
	entry, ok := logs.find("assistant.group_relations")
	if !ok {
		t.Fatalf("运行记录里没有这次失败：%#v", logs.entries)
	}
	if entry.Level != applog.LevelError {
		t.Fatalf("失败应当记成 error：%#v", entry)
	}
	if entry.Metadata["group_id"] != "555" {
		t.Fatalf("metadata 里没带群号：%#v", entry.Metadata)
	}
}

// 私聊里画不了不算故障，不该往运行记录里塞错误。
func TestGroupRelationsToolStaysQuietInPrivateChat(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	logs := &captureRelationLogs{}
	runtime.SetAppLogWriter(logs)
	tool := newDianaGroupRelationsTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, SettingValues{})
	if _, err := tool.Run(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := logs.find("assistant.group_relations"); ok {
		t.Fatalf("私聊不该记错误：%#v", logs.entries)
	}
}
