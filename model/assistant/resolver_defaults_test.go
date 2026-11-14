// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"slices"
	"testing"
)

// 链接解析新装、没存过状态、新加的机器人都默认关；为某个机器人打开过的保持打开。
func TestResolverPluginDefaultsToDisabled(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := pluginStateByID(manager.List(), resolverPluginID)
	if !ok {
		t.Fatal("链接解析没有注册进默认插件管理器")
	}
	if !state.Installed || state.Enabled || !state.Manifest.DefaultDisabled {
		t.Fatalf("新装时链接解析应当装好但关闭：installed=%v enabled=%v", state.Installed, state.Enabled)
	}

	manager.Restore(map[string]PersistedPluginState{})
	if manager.EnabledWithOverrides(resolverPluginID, manager.ProfileOverrides("qq")) {
		t.Fatal("没有存过状态时链接解析不该开启")
	}

	manager.Restore(map[string]PersistedPluginState{
		resolverPluginID: {Installed: true, ProfileEnabled: map[string]bool{"qq": true}, ProfileConfigMigrated: true},
	})
	if !manager.EnabledWithOverrides(resolverPluginID, manager.ProfileOverrides("qq")) {
		t.Fatal("机器人显式打开的链接解析被关掉了")
	}
	if manager.EnabledWithOverrides(resolverPluginID, manager.ProfileOverrides("tg")) {
		t.Fatal("没设置过的机器人不该跟着打开链接解析")
	}
}

func TestResolverPlatformDefaultsExcludeGitHub(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, _ := pluginStateByID(manager.List(), resolverPluginID)
	settings := SettingValues(effectivePluginSettings(state.Manifest.Settings, state.Settings))
	platforms := settings.StringSlice(resolverSettingEnabledPlatforms)
	if slices.Contains(platforms, "github") {
		t.Fatalf("GitHub 默认不该启用：%v", platforms)
	}
	for _, key := range []string{"bilibili", "x", "douyin"} {
		if !slices.Contains(platforms, key) {
			t.Fatalf("默认启用平台少了 %s：%v", key, platforms)
		}
	}
	if !settings.Bool(resolverSettingMergedForward, false) {
		t.Fatal("合并转发应当默认开启")
	}

	// 用户显式勾过 GitHub 的照常解析。
	explicit := SettingValues{resolverSettingEnabledPlatforms: []any{"github"}}
	if !slices.Contains(explicit.StringSlice(resolverSettingEnabledPlatforms), "github") {
		t.Fatal("显式勾选的 GitHub 丢了")
	}
}

// 没有配置启用平台时 Handle 也按默认值跳过 GitHub。
func TestResolverSkipsGitHubWhenPlatformsUnset(t *testing.T) {
	plugin := NewResolverPlugin(nil)
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:     "https://github.com/SuInk/Diana",
		Event:    MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
		Settings: SettingValues{resolverSettingFetchTitle: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil {
		t.Fatalf("GitHub 默认不该解析：%#v", resp)
	}
}

// 只有文字的解析结果默认也走合并转发，关掉开关才发普通消息。
func TestResolverTextOnlyResultUsesMergedForward(t *testing.T) {
	plugin := NewResolverPlugin(nil)
	settings := SettingValues{
		resolverSettingFetchTitle:       false,
		resolverSettingEnabledPlatforms: []any{"zhihu"},
		resolverSettingDownloadMedia:    false,
	}
	req := PluginRequest{
		Text:     "https://www.zhihu.com/question/1",
		Event:    MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
		Settings: settings,
	}
	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !resp.Forward || len(resp.ForwardMessages) != 1 || resp.ForwardMessages[0].Text != resp.Reply {
		t.Fatalf("纯文字结果没有走合并转发：%#v", resp)
	}

	settings[resolverSettingMergedForward] = false
	resp, err = plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.Forward || len(resp.ForwardMessages) != 0 || resp.Reply == "" {
		t.Fatalf("关掉合并转发后应发普通消息：%#v", resp)
	}
}
