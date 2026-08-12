package assistant

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/applog"
)

func TestDefaultPluginManagerIncludesOneBotV11Skill(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(oneBotV11PluginID)
	if !ok || !state.Installed || !state.Enabled || !state.Manifest.Official || !state.Manifest.BuiltIn {
		t.Fatalf("OneBot v11 plugin state = %#v, found = %v", state, ok)
	}
	for _, permission := range []string{"onebot:read", "onebot:write:owner", "onebot:credentials:owner"} {
		if !containsString(state.Manifest.Permissions, permission) {
			t.Fatalf("manifest permissions = %#v, missing %q", state.Manifest.Permissions, permission)
		}
	}
}

func TestDianaOneBotV11ToolOwnerCanCallEveryAction(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"set_group_name":          {"updated": true},
		"custom_extension_action": {"extension": "ok"},
		"get_credentials":         {"token": "owner-secret"},
	}}
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformNapCat}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	tool := newDianaOneBotV11Tool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner", Platform: PlatformNapCat})

	tests := []struct {
		action string
		params map[string]any
		want   string
	}{
		{action: "set_group_name", params: map[string]any{"group_id": 123, "group_name": "new-name"}, want: `"updated":true`},
		{action: "custom_extension_action", params: map[string]any{"mode": "all"}, want: `"extension":"ok"`},
		{action: "get_credentials", params: map[string]any{"domain": "example.com"}, want: `"token":"owner-secret"`},
	}
	for _, test := range tests {
		output, err := tool.Run(context.Background(), map[string]any{"action": test.action, "params": test.params})
		if err != nil {
			t.Fatalf("action %q error = %v", test.action, err)
		}
		if !strings.Contains(output, `"access":"owner_full"`) || !strings.Contains(output, test.want) {
			t.Fatalf("action %q output = %s", test.action, output)
		}
	}

	calls := channel.callsSnapshot()
	if len(calls) != len(tests) {
		t.Fatalf("calls = %#v", calls)
	}
	entries := logs.entriesSnapshot()
	if len(entries) != len(tests) {
		t.Fatalf("logs = %#v", entries)
	}
	credentialLog := entries[len(entries)-1]
	if credentialLog.Kind != applog.KindOperation || credentialLog.Metadata["sensitive"] != true {
		t.Fatalf("credential log = %#v", credentialLog)
	}
	encodedLog := credentialLog.Detail + credentialLog.Message
	if strings.Contains(encodedLog, "owner-secret") || strings.Contains(encodedLog, "example.com") {
		t.Fatalf("audit log leaked values: %#v", credentialLog)
	}
}

func TestDianaOneBotV11ToolMemberCanOnlyCallReadAllowlist(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info_async": {"group_name": "Diana users"},
	}}
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformNapCat}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	tool := newDianaOneBotV11Tool(runtime, MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "123", Platform: PlatformNapCat})

	output, err := tool.Run(context.Background(), map[string]any{
		"action": "get_group_info_async",
		"params": `{"group_id":123}`,
	})
	if err != nil {
		t.Fatalf("read action error = %v", err)
	}
	if !strings.Contains(output, `"access":"member_read_only"`) || !strings.Contains(output, "Diana users") {
		t.Fatalf("read output = %s", output)
	}

	for _, action := range []string{
		"send_group_msg",
		"set_group_name",
		"get_credentials",
		"get_group_msg_history",
		"get_custom_state",
		"get_group_info_async_async",
		"get_group_info_async_async_send_group_msg",
	} {
		_, err := tool.Run(context.Background(), map[string]any{"action": action, "params": map[string]any{"value": "must-not-leak"}})
		if err == nil || !strings.Contains(err.Error(), "需要主人权限") {
			t.Fatalf("action %q error = %v", action, err)
		}
	}
	if calls := channel.callsSnapshot(); len(calls) != 1 || calls[0].action != "get_group_info_async" {
		t.Fatalf("member calls = %#v", calls)
	}
	entries := logs.entriesSnapshot()
	if len(entries) != 8 {
		t.Fatalf("logs = %#v", entries)
	}
	for _, entry := range entries[1:] {
		if entry.Kind != applog.KindError || entry.Metadata["access"] != "member_read_only" {
			t.Fatalf("denial log = %#v", entry)
		}
		if strings.Contains(entry.Detail, "must-not-leak") {
			t.Fatalf("denial log leaked parameter value: %#v", entry)
		}
	}
}

func TestDianaOneBotV11ToolRejectsInvalidInputAndUnavailablePlugin(t *testing.T) {
	tests := []struct {
		name    string
		config  BotConfig
		plugins *PluginManager
		input   map[string]any
		want    string
	}{
		{
			name:    "invalid action",
			config:  BotConfig{Platform: PlatformNapCat},
			plugins: NewDefaultPluginManager(),
			input:   map[string]any{"action": "get_status\nset_group_name"},
			want:    "invalid OneBot v11 action name",
		},
		{
			name:    "invalid params",
			config:  BotConfig{Platform: PlatformNapCat},
			plugins: NewDefaultPluginManager(),
			input:   map[string]any{"action": "get_status", "params": []any{"bad"}},
			want:    "params must be an object",
		},
		{
			name:    "non OneBot platform",
			config:  BotConfig{Platform: PlatformTelegram},
			plugins: NewDefaultPluginManager(),
			input:   map[string]any{"action": "get_status"},
			want:    "当前消息不来自 OneBot 平台",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime(test.config, &recordingChannel{}, test.plugins, nil, nil, nil, nil)
			_, err := newDianaOneBotV11Tool(runtime, MessageEvent{Platform: test.config.Platform, UserID: "member"}).Run(context.Background(), test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	plugins := NewDefaultPluginManager()
	if _, err := plugins.SetEnabled(oneBotV11PluginID, false); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{Platform: PlatformNapCat}, &recordingChannel{}, plugins, nil, nil, nil, nil)
	_, err := newDianaOneBotV11Tool(runtime, MessageEvent{Platform: PlatformNapCat, UserID: "member"}).Run(context.Background(), map[string]any{"action": "get_status"})
	if err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("disabled plugin error = %v", err)
	}
}

func TestDianaOneBotV11ToolRoutesToSourceProfile(t *testing.T) {
	qqChannel := &recordingChannel{apiResponses: map[string]map[string]any{"get_status": {"online": true}}}
	tgChannel := &recordingChannel{}
	multi := NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq-main", Platform: PlatformNapCat, Channel: qqChannel},
		{ProfileID: "tg-main", Platform: PlatformTelegram, Channel: tgChannel},
	})
	runtime := NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformNapCat}, multi, NewDefaultPluginManager(), nil, nil, nil, nil)
	tool := newDianaOneBotV11Tool(runtime, MessageEvent{ProfileID: "qq-main", Platform: PlatformNapCat, UserID: "owner"})
	if _, err := tool.Run(context.Background(), map[string]any{"action": "get_status"}); err != nil {
		t.Fatal(err)
	}
	if len(qqChannel.callsSnapshot()) != 1 || len(tgChannel.callsSnapshot()) != 0 {
		t.Fatalf("qq calls=%#v tg calls=%#v", qqChannel.callsSnapshot(), tgChannel.callsSnapshot())
	}
}

func TestOneBotV11BuiltinSkillFollowsPluginAndPlatform(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := NewRuntime(BotConfig{Platform: PlatformNapCat}, &recordingChannel{}, plugins, nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformNapCat}
	if skills := runtime.oneBotV11BuiltinSkills(event); len(skills) != 1 || !strings.Contains(skills[0].Content, "Access Boundary") {
		t.Fatalf("enabled skills = %#v", skills)
	}
	if skills := runtime.oneBotV11BuiltinSkills(MessageEvent{Platform: PlatformTelegram}); len(skills) != 0 {
		t.Fatalf("Telegram skills = %#v", skills)
	}
	if _, err := plugins.SetEnabled(oneBotV11PluginID, false); err != nil {
		t.Fatal(err)
	}
	if skills := runtime.oneBotV11BuiltinSkills(event); len(skills) != 0 {
		t.Fatalf("disabled skills = %#v", skills)
	}
}

func TestMemberAgentRegistryRetainsOneBotReadTool(t *testing.T) {
	workDir := t.TempDir()
	cfg := DefaultBotConfig()
	cfg.AgentWorkDir = workDir
	cfg.AgentSkillRoots = []string{filepath.Join(workDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(workDir, "missing-mcp.json")
	event := MessageEvent{Kind: EventKindPrivate, UserID: "member", Platform: PlatformNapCat}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformNapCat}, &recordingChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	registry, err := runtime.newAgentRegistry(
		context.Background(),
		cfg.WithDefaults(),
		event,
		RelationshipPolicy{Tier: RelationshipAcquaintance},
		newDianaOneBotV11Tool(runtime, event),
		newDianaLLMConfigTool(runtime, event),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get(dianaOneBotV11ToolName); !ok {
		t.Fatal("member OneBot read tool is missing")
	}
	if _, ok := registry.Get("diana.llm_config"); ok {
		t.Fatal("member registry exposed owner-only LLM configuration")
	}
}

func TestOneBotV11AuditRedactsFailureDetail(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	runtime.recordOneBotV11Action(
		MessageEvent{UserID: "owner"},
		"custom_extension_action",
		"owner_full",
		true,
		[]string{"secret_param"},
		context.Canceled,
	)
	entries := logs.entriesSnapshot()
	if len(entries) != 1 || entries[0].Kind != applog.KindError {
		t.Fatalf("entries = %#v", entries)
	}
	if strings.Contains(entries[0].Detail, context.Canceled.Error()) {
		t.Fatalf("audit detail leaked adapter error: %#v", entries[0])
	}
}
