// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/osc"
	"github.com/SuInk/diana/model/vrchat"
)

func vrchatPluginFrom(t *testing.T, manager *PluginManager) *VRChatPlugin {
	t.Helper()
	plugin, _, ok := manager.PluginForConfiguration(vrchatPluginID)
	if !ok {
		t.Fatal("VRChat 插件没有注册")
	}
	typed, ok := plugin.(*VRChatPlugin)
	if !ok {
		t.Fatalf("plugin type = %T", plugin)
	}
	t.Cleanup(typed.bridge.Close)
	return typed
}

// 绝大多数部署没有 VRChat，装好就开会白占一个 UDP 端口。
func TestVRChatPluginIsBuiltInAndDefaultDisabled(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(vrchatPluginID)
	if !ok {
		t.Fatal("VRChat 插件没有注册")
	}
	if !state.Manifest.BuiltIn || !state.Manifest.DefaultDisabled || state.Enabled {
		t.Fatalf("state = %#v", state)
	}
	manager.Restore(nil)
	if manager.EnabledWithOverrides(vrchatPluginID, nil) {
		t.Fatal("恢复空状态后仍应保持关闭")
	}
	if vrchatPluginFrom(t, manager).bridge.Status(0).Enabled {
		t.Fatal("插件关闭时桥不能在跑")
	}
}

func TestVRChatConfigFromSettings(t *testing.T) {
	defaults := effectivePluginSettings(NewVRChatPlugin().Manifest().Settings, nil)
	cfg, problems := vrchatConfigFromSettings(defaults)
	if len(problems) != 0 {
		t.Fatalf("默认映射表不该有问题：%v", problems)
	}
	if cfg.SendAddress != "127.0.0.1:9000" || cfg.ListenAddress != "127.0.0.1:9001" {
		t.Fatalf("addresses = %q / %q", cfg.SendAddress, cfg.ListenAddress)
	}
	if cfg.ChatboxInterval != vrchat.DefaultChatboxInterval || cfg.InputMaxHold != vrchat.DefaultInputHold {
		t.Fatalf("durations = %v / %v", cfg.ChatboxInterval, cfg.InputMaxHold)
	}
	if _, ok := cfg.Expressions.Lookup("趴桌"); !ok {
		t.Fatal("默认映射表缺少趴桌")
	}

	custom, problems := vrchatConfigFromSettings(SettingValues{
		vrchatSettingHost:            "192.168.1.20",
		vrchatSettingSendPort:        float64(9100),
		vrchatSettingListenEnabled:   false,
		vrchatSettingChatboxInterval: 2.5,
		vrchatSettingInputMaxSeconds: 1.5,
		vrchatSettingExpressions:     "笑 = Smile:true\n坏行",
	})
	if custom.SendAddress != "192.168.1.20:9100" || custom.ListenAddress != "" {
		t.Fatalf("custom addresses = %q / %q", custom.SendAddress, custom.ListenAddress)
	}
	if custom.ChatboxInterval != 2500*time.Millisecond || custom.InputMaxHold != 1500*time.Millisecond {
		t.Fatalf("custom durations = %v / %v", custom.ChatboxInterval, custom.InputMaxHold)
	}
	if len(problems) != 1 || !slices.Equal(custom.Expressions.Names(), []string{"笑"}) {
		t.Fatalf("problems = %v, names = %v", problems, custom.Expressions.Names())
	}
}

// 开关按机器人分，但桥是进程级的：任何一台机器人开着就要跑，全关了就停。
func TestVRChatBridgeFollowsPluginSwitch(t *testing.T) {
	vrchatClient, err := osc.Listen("127.0.0.1:0", func(osc.Message, *net.UDPAddr) {})
	if err != nil {
		t.Fatal(err)
	}
	defer vrchatClient.Close()
	port := float64(vrchatClient.LocalAddr().Port)

	manager := NewDefaultPluginManager()
	plugin := vrchatPluginFrom(t, manager)
	if _, err := manager.UpdateSettings(vrchatPluginID, map[string]any{
		vrchatSettingSendPort:      port,
		vrchatSettingListenEnabled: false,
		vrchatSettingExpressions:   "开心 = Happy:true\n坏行",
	}); err != nil {
		t.Fatal(err)
	}
	if plugin.bridge.Status(0).Enabled {
		t.Fatal("只改设置不该启动桥")
	}
	if _, err := manager.SetEnabledForProfile(vrchatPluginID, "bot-a", true); err != nil {
		t.Fatal(err)
	}
	status := plugin.Status()
	if !status.Enabled || status.SendAddress != vrchatClient.LocalAddr().String() {
		t.Fatalf("status = %+v", status)
	}
	if len(status.MappingProblems) != 1 {
		t.Fatalf("映射表问题要能在界面上看到：%v", status.MappingProblems)
	}
	if _, err := manager.SetEnabledForProfile(vrchatPluginID, "bot-b", false); err != nil {
		t.Fatal(err)
	}
	if !plugin.bridge.Status(0).Enabled {
		t.Fatal("bot-a 仍开着，桥不能停")
	}
	if _, err := manager.SetEnabledForProfile(vrchatPluginID, "bot-a", false); err != nil {
		t.Fatal(err)
	}
	if plugin.bridge.Status(0).Enabled {
		t.Fatal("全部关掉后桥要停")
	}

	// 重启时按存下来的状态恢复。
	restored := NewDefaultPluginManager()
	restoredPlugin := vrchatPluginFrom(t, restored)
	restored.Restore(map[string]PersistedPluginState{vrchatPluginID: {
		Installed:             true,
		ProfileEnabled:        map[string]bool{"bot-a": true},
		ProfileConfigMigrated: true,
		Settings:              map[string]any{vrchatSettingSendPort: port, vrchatSettingListenEnabled: false},
	}})
	if !restoredPlugin.bridge.Status(0).Enabled {
		t.Fatal("恢复出开着的状态后桥要启动")
	}
}

func toolNames(tools []agent.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return names
}

func TestVRChatToolsRespectControlPermission(t *testing.T) {
	plugin := NewVRChatPlugin()
	defaults := effectivePluginSettings(plugin.Manifest().Settings, nil)

	memberTools, denied := newDianaVRChatTools(plugin, defaults, false)
	if !slices.Equal(toolNames(memberTools), []string{dianaVRChatStatusToolName}) {
		t.Fatalf("成员默认只能查状态：%v", toolNames(memberTools))
	}
	if !slices.Equal(denied, vrchatControlToolNames) {
		t.Fatalf("denied = %v", denied)
	}

	ownerTools, denied := newDianaVRChatTools(plugin, defaults, true)
	if len(ownerTools) != 4 || len(denied) != 0 {
		t.Fatalf("主人应拿到全部工具：%v / %v", toolNames(ownerTools), denied)
	}
	opened := SettingValues{vrchatSettingMemberControl: true}
	if tools, _ := newDianaVRChatTools(plugin, opened, false); len(tools) != 4 {
		t.Fatalf("放开后成员应拿到全部工具：%v", toolNames(tools))
	}
	allowed := RelationshipPolicy{}.allowedAgentToolNames()
	for _, tool := range ownerTools {
		if !allowed[tool.Name()] {
			t.Fatalf("%s 不在成员放行名单里，放开操控也用不了", tool.Name())
		}
	}

	// 表情枚举来自映射表，模型不会去猜不存在的名字。
	for _, tool := range ownerTools {
		if tool.Name() != dianaVRChatExpressionToolName {
			continue
		}
		schema, _ := json.Marshal(tool.(interface{ InputSchema() map[string]any }).InputSchema())
		for _, name := range []string{"开心", "疑惑", "屑", "趴桌"} {
			if !strings.Contains(string(schema), name) {
				t.Fatalf("schema 缺少 %s：%s", name, schema)
			}
		}
	}
}

func runVRChatTool(t *testing.T, tool agent.Tool, input map[string]any) vrchatToolResult {
	t.Helper()
	output, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result vrchatToolResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("output %q: %v", output, err)
	}
	return result
}

func TestVRChatToolsDriveBridge(t *testing.T) {
	received := make(chan osc.Message, 16)
	vrchatClient, err := osc.Listen("127.0.0.1:0", func(message osc.Message, _ *net.UDPAddr) { received <- message })
	if err != nil {
		t.Fatal(err)
	}
	defer vrchatClient.Close()

	plugin := NewVRChatPlugin()
	defer plugin.bridge.Close()
	settings := effectivePluginSettings(plugin.Manifest().Settings, map[string]any{
		vrchatSettingSendPort:        float64(vrchatClient.LocalAddr().Port),
		vrchatSettingListenEnabled:   false,
		vrchatSettingInputMaxSeconds: 0.5,
	})
	tools, _ := newDianaVRChatTools(plugin, settings, true)
	byName := map[string]agent.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}

	// 桥没启动时如实告诉模型，而不是报系统错误。
	if result := runVRChatTool(t, byName[dianaVRChatMoveToolName], map[string]any{"action": "forward"}); result.OK || !strings.Contains(result.Message, "没有在运行") {
		t.Fatalf("disabled move = %+v", result)
	}
	if result := runVRChatTool(t, byName[dianaVRChatStatusToolName], nil); result.OK {
		t.Fatalf("disabled status = %+v", result)
	}

	plugin.PluginStateChanged(true, settings)
	expect := func(address string) osc.Message {
		t.Helper()
		select {
		case message := <-received:
			if message.Address != address {
				t.Fatalf("got %s, want %s", message.Address, address)
			}
			return message
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", address)
		}
		return osc.Message{}
	}

	if result := runVRChatTool(t, byName[dianaVRChatExpressionToolName], map[string]any{"expression": "疑惑"}); !result.OK {
		t.Fatalf("expression = %+v", result)
	}
	if message := expect("/avatar/parameters/Expression"); message.Args[0] != int32(2) {
		t.Fatalf("expression args = %#v", message.Args)
	}
	if result := runVRChatTool(t, byName[dianaVRChatExpressionToolName], map[string]any{"expression": "大笑"}); result.OK {
		t.Fatalf("unknown expression should fail softly: %+v", result)
	}

	result := runVRChatTool(t, byName[dianaVRChatMoveToolName], map[string]any{"action": "forward", "seconds": 30})
	if !result.OK || !strings.Contains(result.Message, "0.5 秒") {
		t.Fatalf("move = %+v", result)
	}
	if message := expect("/input/MoveForward"); message.Args[0] != int32(1) {
		t.Fatalf("press = %#v", message.Args)
	}
	if message := expect("/input/MoveForward"); message.Args[0] != int32(0) {
		t.Fatalf("release = %#v", message.Args)
	}

	if result := runVRChatTool(t, byName[dianaVRChatChatboxToolName], map[string]any{"operation": "send", "text": "大家好"}); !result.OK {
		t.Fatalf("chatbox = %+v", result)
	}
	if message := expect("/chatbox/input"); message.Args[0] != "大家好" {
		t.Fatalf("chatbox args = %#v", message.Args)
	}

	status := runVRChatTool(t, byName[dianaVRChatStatusToolName], nil)
	detail, _ := json.Marshal(status.Detail)
	if !status.OK || !strings.Contains(string(detail), `"expression":"疑惑"`) || !strings.Contains(string(detail), "大家好") {
		t.Fatalf("status = %s", detail)
	}
}

func TestVRChatMoodExpression(t *testing.T) {
	cases := map[float64]string{
		moodHappyThreshold: vrchat.ExpressionHappy,
		0:                  vrchat.ExpressionNeutral,
		moodLowThreshold:   vrchat.ExpressionLow,
		-moodScoreLimit:    vrchat.ExpressionLow,
	}
	for score, want := range cases {
		if got := vrchatMoodExpression(score); got != want {
			t.Fatalf("score %v = %q, want %q", score, got, want)
		}
	}
}

// 插件关着时模型看不到任何 vrchat_* 工具；开着时主人能看到。
func TestVRChatToolRegistration(t *testing.T) {
	toolPromptFor := func(t *testing.T, enabled bool) string {
		t.Helper()
		provider := &agentSequenceLLMProvider{responses: []string{
			`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
			`{"action":"final","content":"好"}`,
		}}
		plugins := NewDefaultPluginManager()
		plugin := vrchatPluginFrom(t, plugins)
		if enabled {
			if _, err := plugins.UpdateSettings(vrchatPluginID, map[string]any{vrchatSettingListenEnabled: false}); err != nil {
				t.Fatal(err)
			}
			if _, err := plugins.SetEnabledForProfile(vrchatPluginID, "qq", true); err != nil {
				t.Fatal(err)
			}
			if !plugin.bridge.Status(0).Enabled {
				t.Fatal("bridge should be running")
			}
		}
		runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true, ReplySafetyMasterEnabled: boolPointer(false)}, nilChannel{}, plugins, nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		if _, err := runtime.replyTo(context.Background(), MessageEvent{
			Kind: EventKindPrivate, UserID: "owner", MessageID: "message-1", ProfileID: "qq",
		}, "你在 VRChat 干嘛"); err != nil {
			t.Fatal(err)
		}
		if len(provider.requests) == 0 {
			t.Fatal("provider was not called")
		}
		return provider.requests[len(provider.requests)-1].Messages[0].Content
	}
	if prompt := toolPromptFor(t, false); strings.Contains(prompt, "vrchat_") {
		t.Fatalf("插件关闭时不该挂 VRChat 工具：%s", prompt)
	}
	prompt := toolPromptFor(t, true)
	for _, name := range []string{dianaVRChatStatusToolName, dianaVRChatChatboxToolName, dianaVRChatExpressionToolName, dianaVRChatMoveToolName} {
		if !strings.Contains(prompt, name) {
			t.Fatalf("启用后主人应能看到 %s", name)
		}
	}
}

// 回复后按心情换表情；群回复可同步到聊天框，私聊回复永不同步。
func TestVRChatAfterReplyHook(t *testing.T) {
	received := make(chan osc.Message, 16)
	vrchatClient, err := osc.Listen("127.0.0.1:0", func(message osc.Message, _ *net.UDPAddr) { received <- message })
	if err != nil {
		t.Fatal(err)
	}
	defer vrchatClient.Close()

	plugins := NewDefaultPluginManager()
	vrchatPluginFrom(t, plugins)
	if _, err := plugins.UpdateSettings(vrchatPluginID, map[string]any{
		vrchatSettingSendPort:      float64(vrchatClient.LocalAddr().Port),
		vrchatSettingListenEnabled: false,
		vrchatSettingMirrorReply:   true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SetEnabledForProfile(vrchatPluginID, "qq", true); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", MoodEnabled: boolPointer(true)}, nilChannel{}, plugins, nil, nil, nil, nil)
	runtime.bumpMood("qq", 5, runtime.clock())

	runtime.afterReplyVRChat(MessageEvent{Kind: EventKindPrivate, UserID: "u", ProfileID: "qq"}, "私聊里的话")
	select {
	case message := <-received:
		// 默认模板里 开心 = Expression:1
		if message.Address != "/avatar/parameters/Expression" || message.Args[0] != int32(1) {
			t.Fatalf("mood message = %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mood expression never arrived")
	}

	runtime.afterReplyVRChat(MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", ProfileID: "qq"}, "群里的话")
	select {
	case message := <-received:
		if message.Address != "/chatbox/input" || message.Args[0] != "群里的话" {
			t.Fatalf("private reply leaked or group reply missing: %#v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("group reply never mirrored")
	}
}
