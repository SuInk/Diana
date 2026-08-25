// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
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
