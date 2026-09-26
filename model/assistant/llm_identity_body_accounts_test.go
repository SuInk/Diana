// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// bodyAccountChannel 按名单回答成员查询，并记下问过谁。
type bodyAccountChannel struct {
	nilChannel
	mu      sync.Mutex
	members map[string]OneBotGroupMemberInfo
	asked   []string
}

func (c *bodyAccountChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, userID)
	if member, ok := c.members[userID]; ok {
		member.GroupID, member.UserID = groupID, userID
		return member, nil
	}
	return OneBotGroupMemberInfo{}, errors.New("not in group")
}

func (c *bodyAccountChannel) GroupMembers(context.Context, string) (GroupMemberDirectory, error) {
	return GroupMemberDirectory{}, nil
}

func (c *bodyAccountChannel) askedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

func newBodyAccountRuntime(cfg BotConfig, channel *bodyAccountChannel) *Runtime {
	cfg.Platform = PlatformOneBotV11
	cfg.BotAccount = "200002"
	cfg.OwnerID = "100001"
	return NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
}

func bodyAccountEvent(text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "200002",
		GroupID: "500005", UserID: "300003", MessageID: "90001", RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 正文里写的是本群成员的号，就和元数据里的号一样换成别名，模型填回别名能还原；
// 不是成员的数字原样保留。
func TestBodyAccountOfGroupMemberIsAliasedAndRestored(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"765432109": {Role: "member", Card: "小王"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	event := bodyAccountEvent("765432109 是谁？顺便看下订单 13800138000 和 88888")

	ctx := runtime.withIdentityPrivacyContext(context.Background(), event, nil)
	scope := identityPrivacyScopeFromContext(ctx)
	protected := scope.protectText(event.RawMessage)

	if strings.Contains(protected, "765432109") {
		t.Fatalf("成员账号应换成别名: %q", protected)
	}
	alias := scope.register("765432109", "user")
	if !strings.HasPrefix(alias, identityAlias("user")) || !strings.Contains(protected, alias) {
		t.Fatalf("正文里应出现 user 别名 %q: %q", alias, protected)
	}
	if !strings.Contains(protected, "13800138000") || !strings.Contains(protected, "88888") {
		t.Fatalf("非成员数字不应被当成账号换掉: %q", protected)
	}
	if got := scope.restoreText("查一下 " + alias); got != "查一下 765432109" {
		t.Fatalf("别名没有还原成真实账号: %q", got)
	}
	// 11 位的手机号超出 QQ 号长度，不去平台问。
	if asked := channel.askedIDs(); slices.Contains(asked, "13800138000") {
		t.Fatalf("超长数字不该去平台核实: %v", asked)
	}
}

// 核实结果要跨轮记住：下一轮这条消息进了历史，同一个号还是同一个别名，也不再问平台。
func TestBodyAccountVerdictIsCachedAcrossTurns(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"765432109": {Role: "member"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	first := bodyAccountEvent("765432109 和 54321 是谁")

	ctx := runtime.withIdentityPrivacyContext(context.Background(), first, nil)
	firstText := identityPrivacyScopeFromContext(ctx).protectText(first.RawMessage)
	asked := len(channel.askedIDs())
	if asked != 2 {
		t.Fatalf("第一轮应各问一次平台，实际 %v", channel.askedIDs())
	}

	next := bodyAccountEvent("好的")
	next.MessageID = "90002"
	ctx = runtime.withIdentityPrivacyContext(context.Background(), next, []MessageEvent{first})
	secondText := identityPrivacyScopeFromContext(ctx).protectText(first.RawMessage)
	if secondText != firstText {
		t.Fatalf("同一段历史两轮渲染不同，前缀缓存会失效:\n%q\n%q", firstText, secondText)
	}
	if got := len(channel.askedIDs()); got != asked {
		t.Fatalf("历史里的号码不应再问平台: %v", channel.askedIDs())
	}
}

// 历史消息里的新号码只看缓存，不去平台问；只有当前消息会触发查询。
func TestBodyAccountHistoryNeverTriggersLookup(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"765432109": {Role: "member"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	old := bodyAccountEvent("765432109 来了")
	old.MessageID = "80001"

	runtime.withIdentityPrivacyContext(context.Background(), bodyAccountEvent("你好"), []MessageEvent{old})
	if asked := channel.askedIDs(); len(asked) != 0 {
		t.Fatalf("历史号码不应触发平台查询: %v", asked)
	}
}

// 一条消息贴一串号码时，每轮只问有限几次，不能把主链路拖住。
func TestBodyAccountLookupsAreBoundedPerTurn(t *testing.T) {
	channel := &bodyAccountChannel{}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	runtime.withIdentityPrivacyContext(context.Background(), bodyAccountEvent("11111 22222 33333 44444 55555 66666"), nil)
	if asked := channel.askedIDs(); len(asked) != bodyAccountLookupsPerTurn {
		t.Fatalf("每轮最多问 %d 次，实际 %v", bodyAccountLookupsPerTurn, asked)
	}
}

// 正文映射可以单独关掉；隐私代理整体关掉时它也跟着不起作用。
func TestBodyAccountMappingCanBeDisabled(t *testing.T) {
	for name, cfg := range map[string]BotConfig{
		"正文映射关": {LLMIdentityMaskingEnabled: boolPointer(true), LLMIdentityBodyAccountMappingEnabled: boolPointer(false)},
		"隐私代理关": {LLMIdentityMaskingEnabled: boolPointer(false)},
	} {
		t.Run(name, func(t *testing.T) {
			channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"765432109": {Role: "member"}}}
			runtime := newBodyAccountRuntime(cfg, channel)
			event := bodyAccountEvent("765432109 是谁")
			ctx := runtime.withIdentityPrivacyContext(context.Background(), event, nil)
			if asked := channel.askedIDs(); len(asked) != 0 {
				t.Fatalf("关掉后不应查询平台: %v", asked)
			}
			if scope := identityPrivacyScopeFromContext(ctx); scope != nil {
				if got := scope.protectText(event.RawMessage); !strings.Contains(got, "765432109") {
					t.Fatalf("关掉正文映射后正文号码应原样保留: %q", got)
				}
			}
		})
	}
}

func TestBodyAccountMappingDefaultsOn(t *testing.T) {
	if !llmIdentityBodyAccountMappingEnabled(BotConfig{}) {
		t.Fatal("正文映射默认应开启")
	}
	cfg := ConfigFromPayload(ConfigPayload{LLMIdentityBodyAccountMappingEnabled: boolPointer(false)}, BotConfig{})
	if llmIdentityBodyAccountMappingEnabled(cfg) {
		t.Fatal("显式关闭应保留")
	}
	payload := PayloadFromConfig(cfg)
	if payload.LLMIdentityBodyAccountMappingEnabled == nil || *payload.LLMIdentityBodyAccountMappingEnabled {
		t.Fatalf("配置回读丢了显式 false: %#v", payload.LLMIdentityBodyAccountMappingEnabled)
	}
}

func TestBodyAccountCandidates(t *testing.T) {
	text := "找 765432109，链接 https://x.com/status/123456789 版本 v1.23456 文件 a_12345.png 编号 012345 群号:987654321"
	got := bodyAccountCandidates(text, 5, 10)
	want := []string{"765432109", "987654321"}
	if !slices.Equal(got, want) {
		t.Fatalf("候选 = %v, want %v", got, want)
	}
	if _, _, ok := bodyAccountDigitsAllowed(PlatformFeishu); ok {
		t.Fatal("账号不是数字的平台不应扫描正文数字")
	}
}

// 用户拿号码问「这是谁」，identity_check 开了群身份核验时要带回群名片。
func TestIdentityCheckReturnsDisplayNameForGroupMember(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"765432109": {Role: "admin", Card: "小王", Nickname: "wang"}}}
	runtime := newBodyAccountRuntime(BotConfig{}, channel)
	got := runIdentityCheck(t, runtime, bodyAccountEvent("765432109 是谁"), map[string]any{"user_id": "765432109", "check_group_role": true})
	if got.DisplayName != "小王" || got.GroupRole != string(GroupRoleAdmin) {
		t.Fatalf("应返回群名片和群身份: %+v", got)
	}
}
