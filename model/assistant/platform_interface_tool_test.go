// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/applog"
)

// moderationTestChannel 既记录 CallAPI（用于校验动词映射到的原生动作），又实现
// GroupMemberChannel（用于让管理员前置校验拿到机器人自己的角色），从而在不接真实
// 平台的情况下把破坏性操作的整条链路跑通。
type moderationTestChannel struct {
	*recordingChannel
	selfRole  string
	memberErr error
}

func newModerationTestChannel(selfRole string) *moderationTestChannel {
	return &moderationTestChannel{recordingChannel: &recordingChannel{apiResponses: map[string]map[string]any{}}, selfRole: selfRole}
}

func (c *moderationTestChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	if c.memberErr != nil {
		return OneBotGroupMemberInfo{}, c.memberErr
	}
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Role: c.selfRole, MembershipVerified: true}, nil
}

func (c *moderationTestChannel) GroupMembers(_ context.Context, groupID string) (GroupMemberDirectory, error) {
	return GroupMemberDirectory{Members: []OneBotGroupMemberInfo{{GroupID: groupID, UserID: "10086", Role: "member", MembershipVerified: true}}, Complete: false, Source: "test"}, nil
}

func platformToolFor(t *testing.T, cfg BotConfig, channel Channel, event MessageEvent) (*dianaPlatformTool, *Runtime, *captureAppLogs) {
	t.Helper()
	logs := &captureAppLogs{}
	runtime := NewRuntime(cfg, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	return newDianaPlatformTool(runtime, event), runtime, logs
}

func TestDefaultPluginManagerIncludesPlatformInterface(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(platformInterfacePluginID)
	if !ok || !state.Installed || !state.Enabled || !state.Manifest.Official || !state.Manifest.BuiltIn {
		t.Fatalf("platform interface plugin state = %#v, found = %v", state, ok)
	}
	// 旧的 OneBot v11 插件必须彻底消失。
	if _, ok := manager.Get("official.onebot-v11-skill"); ok {
		t.Fatal("legacy onebot-v11 plugin is still registered")
	}
}

func TestPlatformToolOwnerMutesAndKicksOnOneBot(t *testing.T) {
	channel := newModerationTestChannel("admin")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, logs := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)

	if _, err := tool.Run(context.Background(), map[string]any{"operation": "mute", "user_id": "555", "duration": 600}); err != nil {
		t.Fatalf("mute error = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "555", "reject_add_request": true}); err != nil {
		t.Fatalf("kick error = %v", err)
	}

	ban := recordedCallsByAction(channel.callsSnapshot(), "set_group_ban")
	if len(ban) != 1 || intFromAny(ban[0].params["duration"]) != 600 {
		t.Fatalf("set_group_ban calls = %#v", ban)
	}
	kick := recordedCallsByAction(channel.callsSnapshot(), "set_group_kick")
	if len(kick) != 1 || kick[0].params["reject_add_request"] != true {
		t.Fatalf("set_group_kick calls = %#v", kick)
	}
	entries := logs.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %#v", entries)
	}
	for _, entry := range entries {
		if entry.Kind != applog.KindOperation || entry.Metadata["owner"] != true || entry.Metadata["destructive"] != true {
			t.Fatalf("audit entry = %#v", entry)
		}
	}
}

func TestPlatformToolOwnerMutesAndUnmutesOnTelegram(t *testing.T) {
	channel := newModerationTestChannel("admin")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "42", Platform: PlatformTelegram}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "42", Platform: PlatformTelegram}, channel, event)

	if _, err := tool.Run(context.Background(), map[string]any{"operation": "mute", "user_id": "555", "duration": 600}); err != nil {
		t.Fatalf("mute error = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "unmute", "user_id": "555"}); err != nil {
		t.Fatalf("unmute error = %v", err)
	}
	restricts := recordedCallsByAction(channel.callsSnapshot(), "restrictChatMember")
	if len(restricts) != 2 {
		t.Fatalf("restrictChatMember calls = %#v", restricts)
	}
	muted, _ := restricts[0].params["permissions"].(map[string]any)
	if muted["can_send_messages"] != false {
		t.Fatalf("mute permissions = %#v", restricts[0].params)
	}
	if _, ok := restricts[0].params["until_date"]; !ok {
		t.Fatalf("mute missing until_date = %#v", restricts[0].params)
	}
	lifted, _ := restricts[1].params["permissions"].(map[string]any)
	if lifted["can_send_messages"] != true {
		t.Fatalf("unmute permissions = %#v", restricts[1].params)
	}
	if _, ok := restricts[1].params["until_date"]; ok {
		t.Fatalf("unmute must not carry until_date = %#v", restricts[1].params)
	}
}

func TestPlatformToolKickOnTelegramBanUnbanSemantics(t *testing.T) {
	// reject_add_request=false 是「踢出但允许再加」：先 ban 再 unban。
	channel := newModerationTestChannel("owner")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "42", Platform: PlatformTelegram}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "42", Platform: PlatformTelegram}, channel, event)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "555"}); err != nil {
		t.Fatalf("kick error = %v", err)
	}
	calls := channel.callsSnapshot()
	if len(recordedCallsByAction(calls, "banChatMember")) != 1 || len(recordedCallsByAction(calls, "unbanChatMember")) != 1 {
		t.Fatalf("kick(not ban) calls = %#v", calls)
	}

	// reject_add_request=true 是「踢出并封禁」：只 ban，不 unban。
	channel2 := newModerationTestChannel("owner")
	tool2, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "42", Platform: PlatformTelegram}, channel2, event)
	if _, err := tool2.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "555", "reject_add_request": true}); err != nil {
		t.Fatalf("ban error = %v", err)
	}
	calls2 := channel2.callsSnapshot()
	if len(recordedCallsByAction(calls2, "banChatMember")) != 1 || len(recordedCallsByAction(calls2, "unbanChatMember")) != 0 {
		t.Fatalf("kick(ban) calls = %#v", calls2)
	}
}

// moderationRoles 是层级用例的群：机器人和 admin1、admin2 是管理员，gowner 是群主。
func moderationRoles() map[string]string {
	return map[string]string{"10000": "admin", "admin1": "admin", "admin2": "admin", "gowner": "owner"}
}

func moderatorToolFor(t *testing.T, actor, senderRole string) (*dianaPlatformTool, *roleTestChannel, *captureAppLogs) {
	t.Helper()
	channel := newRoleTestChannel(moderationRoles())
	event := MessageEvent{Kind: EventKindGroup, UserID: actor, GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11, SenderRole: senderRole}
	tool, _, logs := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	return tool, channel, logs
}

// 群管理员可以用群管操作，身份以平台实时返回为准：事件里没带身份也照样认。
func TestPlatformToolGroupAdminCanModerate(t *testing.T) {
	tool, channel, logs := moderatorToolFor(t, "admin1", "")
	for _, input := range []map[string]any{
		{"operation": "kick", "user_id": "555"},
		{"operation": "mute", "user_id": "555", "duration": 60},
		{"operation": "mute_all"},
		{"operation": "announce", "content": "群规"},
		{"operation": "set_card", "user_id": "555", "card": "新人"},
	} {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	calls := channel.callsSnapshot()
	for _, action := range []string{"set_group_kick", "set_group_ban", "set_group_whole_ban", "_send_group_notice", "set_group_card"} {
		if len(recordedCallsByAction(calls, action)) != 1 {
			t.Fatalf("%s calls = %#v", action, calls)
		}
	}
	for _, entry := range logs.entriesSnapshot() {
		if entry.Metadata["access"] != "group_admin" || entry.Metadata["owner"] != false {
			t.Fatalf("audit entry = %#v", entry)
		}
	}
}

// 普通成员一律拒绝；事件自称的 admin 不作数，只认平台实时查到的身份。
func TestPlatformToolMemberCannotModerateEvenIfClaimingAdmin(t *testing.T) {
	for _, claimed := range []string{"", "admin", "owner"} {
		tool, channel, logs := moderatorToolFor(t, "555", claimed)
		_, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "666"})
		if err == nil || !strings.Contains(err.Error(), "主人、群主或群管理员") {
			t.Fatalf("claimed %q error = %v", claimed, err)
		}
		entries := logs.entriesSnapshot()
		if len(entries) != 1 || entries[0].Kind != applog.KindError || entries[0].Metadata["owner"] != false {
			t.Fatalf("claimed %q denial log = %#v", claimed, entries)
		}
		if got := recordedCallsByAction(channel.callsSnapshot(), "set_group_kick"); len(got) != 0 {
			t.Fatalf("member reached set_group_kick: %#v", got)
		}
	}
}

// 层级：管理员动不了群主、别的管理员、主人和机器人；群主可以动管理员。
func TestPlatformToolModerationHierarchy(t *testing.T) {
	admin, adminChannel, _ := moderatorToolFor(t, "admin1", "admin")
	for _, input := range []map[string]any{
		{"operation": "kick", "user_id": "admin2"},
		{"operation": "mute", "user_id": "gowner", "duration": 60},
		{"operation": "set_card", "user_id": "admin2", "card": "x"},
	} {
		if _, err := admin.Run(context.Background(), input); err == nil || !strings.Contains(err.Error(), "群主或其他管理员") {
			t.Fatalf("admin %v on %v error = %v", input["operation"], input["user_id"], err)
		}
	}
	if _, err := admin.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "owner"}); err == nil || !strings.Contains(err.Error(), "机器人主人") {
		t.Fatalf("admin kick bot owner error = %v", err)
	}
	if _, err := admin.Run(context.Background(), map[string]any{"operation": "set_card", "user_id": "10000", "card": "x"}); err == nil || !strings.Contains(err.Error(), "机器人自己") {
		t.Fatalf("admin set_card on bot error = %v", err)
	}
	calls := adminChannel.callsSnapshot()
	for _, action := range []string{"set_group_kick", "set_group_ban", "set_group_card"} {
		if got := recordedCallsByAction(calls, action); len(got) != 0 {
			t.Fatalf("refused target reached %s: %#v", action, got)
		}
	}

	groupOwner, ownerChannel, _ := moderatorToolFor(t, "gowner", "owner")
	if _, err := groupOwner.Run(context.Background(), map[string]any{"operation": "mute", "user_id": "admin2", "duration": 60}); err != nil {
		t.Fatalf("group owner mute admin error = %v", err)
	}
	if got := recordedCallsByAction(ownerChannel.callsSnapshot(), "set_group_ban"); len(got) != 1 {
		t.Fatalf("group owner set_group_ban = %#v", got)
	}
}

// 群管理员只能管当前这个群；私聊里没有群上下文，不替非主人执行。
func TestPlatformToolGroupAdminScopedToCurrentGroup(t *testing.T) {
	admin, channel, _ := moderatorToolFor(t, "admin1", "admin")
	if _, err := admin.Run(context.Background(), map[string]any{"operation": "mute_all", "group_id": "999"}); err == nil || !strings.Contains(err.Error(), "当前这个群") {
		t.Fatalf("other group error = %v", err)
	}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "admin1", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, private)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "mute_all", "group_id": "123"}); err == nil || !strings.Contains(err.Error(), "只能在群里") {
		t.Fatalf("private error = %v", err)
	}
	if got := recordedCallsByAction(channel.callsSnapshot(), "set_group_whole_ban"); len(got) != 0 {
		t.Fatalf("out-of-scope call reached platform: %#v", got)
	}
}

// schema 只对主人和（事件或平台显示的）群主、群管理员列出群管操作。
func TestPlatformToolSchemaShowsModerationToGroupAdmins(t *testing.T) {
	for role, want := range map[string]bool{"admin": true, "owner": true, "member": false} {
		tool, _, _ := moderatorToolFor(t, "someone", role)
		if got := strings.Contains(fmt.Sprint(tool.InputSchema()), platformOpMuteAll); got != want {
			t.Fatalf("role %s schema shows moderation = %v", role, got)
		}
	}
}

func TestPlatformToolRefusesWhenBotIsNotAdmin(t *testing.T) {
	channel := newModerationTestChannel("member")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	_, err := tool.Run(context.Background(), map[string]any{"operation": "mute", "user_id": "555", "duration": 60})
	if err == nil || !strings.Contains(err.Error(), "不是这个群的管理员") {
		t.Fatalf("precondition error = %v", err)
	}
	if calls := channel.callsSnapshot(); len(recordedCallsByAction(calls, "set_group_ban")) != 0 {
		t.Fatalf("must not call destructive API when bot is a plain member: %#v", calls)
	}
}

func TestPlatformToolRefusesOwnerAndSelfTargets(t *testing.T) {
	channel := newModerationTestChannel("admin")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)

	if _, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "owner"}); err == nil || !strings.Contains(err.Error(), "机器人主人") {
		t.Fatalf("owner target error = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "10000"}); err == nil || !strings.Contains(err.Error(), "机器人自己") {
		t.Fatalf("self target error = %v", err)
	}
	if len(channel.callsSnapshot()) != 0 {
		t.Fatalf("protected targets must not reach the API: %#v", channel.callsSnapshot())
	}
}

func TestPlatformToolRejectsNicknameTarget(t *testing.T) {
	channel := newModerationTestChannel("admin")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	_, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "小明"})
	if err == nil || !strings.Contains(err.Error(), "不能使用昵称") {
		t.Fatalf("nickname target error = %v", err)
	}
}

func TestPlatformToolMuteDurationValidation(t *testing.T) {
	channel := newModerationTestChannel("admin")
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)

	if _, err := tool.Run(context.Background(), map[string]any{"operation": "mute", "user_id": "555", "duration": 0}); err == nil || !strings.Contains(err.Error(), "正的时长") {
		t.Fatalf("zero-duration error = %v", err)
	}
	// 超过平台上限时按上限执行。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "mute", "user_id": "555", "duration": oneBotMaxMuteSeconds + 100000}); err != nil {
		t.Fatalf("over-cap mute error = %v", err)
	}
	ban := recordedCallsByAction(channel.callsSnapshot(), "set_group_ban")
	if len(ban) != 1 || intFromAny(ban[0].params["duration"]) != oneBotMaxMuteSeconds {
		t.Fatalf("over-cap duration was not clamped: %#v", ban)
	}
}

func TestPlatformToolUnsupportedPlatformForModeration(t *testing.T) {
	channel := &recordingChannel{}
	event := MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "bot", Platform: PlatformFeishu}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "bot", Platform: PlatformFeishu}, channel, event)
	_, err := tool.Run(context.Background(), map[string]any{"operation": "kick", "user_id": "555"})
	if err == nil || !strings.Contains(err.Error(), "当前平台暂不支持此操作") {
		t.Fatalf("unsupported platform error = %v", err)
	}
	if len(channel.callsSnapshot()) != 0 {
		t.Fatalf("unsupported platform must not call any API: %#v", channel.callsSnapshot())
	}
}

func TestPlatformToolReadsWorkForMembers(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info":        {"group_name": "Diana users", "member_count": 3},
		"get_group_member_info": {"user_id": "555", "nickname": "Someone", "role": "member"},
		"get_group_member_list": {"items": []any{
			map[string]any{"group_id": "123", "user_id": "555", "nickname": "Someone"},
		}},
	}}
	event := MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "123", Platform: PlatformOneBotV11}
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", Platform: PlatformOneBotV11}, channel, event)

	info, err := tool.Run(context.Background(), map[string]any{"operation": "group_info"})
	if err != nil || !strings.Contains(info, "Diana users") || !strings.Contains(info, `"access":"member_read_only"`) {
		t.Fatalf("group_info = %s err = %v", info, err)
	}
	member, err := tool.Run(context.Background(), map[string]any{"operation": "member_info", "user_id": "555"})
	if err != nil || !strings.Contains(member, "555") {
		t.Fatalf("member_info = %s err = %v", member, err)
	}
	list, err := tool.Run(context.Background(), map[string]any{"operation": "member_list"})
	if err != nil || !strings.Contains(list, "555") || !strings.Contains(list, `"operation":"member_list"`) {
		t.Fatalf("member_list = %s err = %v", list, err)
	}
}

func TestPlatformToolMemberSchemaHidesModerationOps(t *testing.T) {
	channel := &recordingChannel{}
	memberTool := newDianaPlatformTool(NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformOneBotV11}, channel, NewDefaultPluginManager(), nil, nil, nil, nil), MessageEvent{Kind: EventKindGroup, UserID: "member", GroupID: "123", Platform: PlatformOneBotV11})
	ownerTool := newDianaPlatformTool(NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformOneBotV11}, channel, NewDefaultPluginManager(), nil, nil, nil, nil), MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", Platform: PlatformOneBotV11})

	if ops := platformSchemaOperations(t, memberTool); containsString(ops, "mute") || containsString(ops, "kick") || containsString(ops, "unmute") {
		t.Fatalf("member schema exposed moderation ops: %#v", ops)
	}
	if ops := platformSchemaOperations(t, ownerTool); !containsString(ops, "mute") || !containsString(ops, "kick") || !containsString(ops, "unmute") {
		t.Fatalf("owner schema missing moderation ops: %#v", ops)
	}
}

func platformSchemaOperations(t *testing.T, tool *dianaPlatformTool) []string {
	t.Helper()
	schema := tool.InputSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema)
	}
	operation, ok := properties["operation"].(map[string]any)
	if !ok {
		t.Fatalf("operation property = %#v", properties)
	}
	values, ok := operation["enum"].([]string)
	if !ok {
		t.Fatalf("operation enum = %#v", operation)
	}
	return values
}

func TestPlatformToolMemberRegistryRetainsReadTool(t *testing.T) {
	workDir := t.TempDir()
	cfg := DefaultBotConfig()
	cfg.AgentSkillRoots = []string{filepath.Join(workDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(workDir, "missing-mcp.json")
	event := MessageEvent{Kind: EventKindPrivate, UserID: "member", Platform: PlatformOneBotV11}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformOneBotV11}, &recordingChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	registry, err := runtime.newAgentRegistry(
		context.Background(),
		cfg.WithDefaults(),
		event,
		RelationshipPolicy{Score: 10},
		newDianaPlatformTool(runtime, event),
		newDianaLLMConfigTool(runtime, event),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get(dianaPlatformToolName); !ok {
		t.Fatal("member platform read tool is missing")
	}
	if _, ok := registry.Get("diana.onebot_v11"); ok {
		t.Fatal("legacy diana.onebot_v11 tool is still registered")
	}
	if _, ok := registry.Get("llm_config"); ok {
		t.Fatal("member registry exposed owner-only LLM configuration")
	}
}

func TestPlatformInterfaceBuiltinSkillFollowsPluginAndPlatform(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, &recordingChannel{}, plugins, nil, nil, nil, nil)
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		if skills := runtime.platformInterfaceBuiltinSkills(MessageEvent{Platform: platform}); len(skills) != 1 || !strings.Contains(skills[0].Content, "Access Boundary") {
			t.Fatalf("%s skills = %#v", platform, skills)
		}
	}
	if _, err := plugins.SetEnabledForProfile(platformInterfacePluginID, "qq", false); err != nil {
		t.Fatal(err)
	}
	if skills := runtime.platformInterfaceBuiltinSkills(MessageEvent{Platform: PlatformOneBotV11}); len(skills) != 0 {
		t.Fatalf("disabled skills = %#v", skills)
	}
}

func TestPlatformInterfaceAuditRedactsFailureDetail(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	runtime.recordPlatformInterfaceOperation(
		MessageEvent{UserID: "owner"},
		"kick",
		"owner_full",
		true,
		"555",
		fmt.Errorf("adapter rejected owner-secret token"),
	)
	entries := logs.entriesSnapshot()
	if len(entries) != 1 || entries[0].Kind != applog.KindError {
		t.Fatalf("entries = %#v", entries)
	}
	if strings.Contains(entries[0].Detail, "owner-secret") {
		t.Fatalf("audit detail leaked adapter error: %#v", entries[0])
	}
}
