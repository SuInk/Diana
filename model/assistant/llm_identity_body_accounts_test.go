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
	"time"
)

// bodyAccountChannel 按名单回答成员查询，并记下问过谁。
type bodyAccountChannel struct {
	nilChannel
	mu      sync.Mutex
	members map[string]OneBotGroupMemberInfo
	// failures 里的号按给定的错误失败，模拟重连、限流这类临时故障。
	failures map[string]error
	asked    []string
}

func (c *bodyAccountChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, userID)
	if err := c.failures[userID]; err != nil {
		return OneBotGroupMemberInfo{}, err
	}
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
	cfg.BotAccount = "20002"
	cfg.OwnerID = "10001"
	return NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
}

func bodyAccountEvent(text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "20002",
		GroupID: "50005", UserID: "30003", MessageID: "90001", RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 正文里写的是本群成员的号，就和元数据里的号一样换成别名，模型填回别名能还原；
// 不是成员的数字原样保留。
func TestBodyAccountOfGroupMemberIsAliasedAndRestored(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member", Card: "小王"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	event := bodyAccountEvent("70007 是谁？顺便看下订单 13800138000 和 88888")

	ctx := runtime.withReplyIdentityPrivacyContext(context.Background(), event, nil)
	scope := identityPrivacyScopeFromContext(ctx)
	protected := scope.protectText(event.RawMessage)

	if strings.Contains(protected, "70007") {
		t.Fatalf("成员账号应换成别名: %q", protected)
	}
	alias := scope.register("70007", "user")
	if !strings.HasPrefix(alias, identityAlias("user")) || !strings.Contains(protected, alias) {
		t.Fatalf("正文里应出现 user 别名 %q: %q", alias, protected)
	}
	if !strings.Contains(protected, "13800138000") || !strings.Contains(protected, "88888") {
		t.Fatalf("非成员数字不应被当成账号换掉: %q", protected)
	}
	if got := scope.restoreText("查一下 " + alias); got != "查一下 70007" {
		t.Fatalf("别名没有还原成真实账号: %q", got)
	}
	// 11 位的手机号超出 QQ 号长度，不去平台问。
	if asked := channel.askedIDs(); slices.Contains(asked, "13800138000") {
		t.Fatalf("超长数字不该去平台核实: %v", asked)
	}
}

// 核实结果要跨轮记住：下一轮这条消息进了历史，同一个号还是同一个别名，也不再问平台。
func TestBodyAccountVerdictIsCachedAcrossTurns(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	first := bodyAccountEvent("70007 和 54321 是谁")

	ctx := runtime.withReplyIdentityPrivacyContext(context.Background(), first, nil)
	firstText := identityPrivacyScopeFromContext(ctx).protectText(first.RawMessage)
	asked := len(channel.askedIDs())
	if asked != 2 {
		t.Fatalf("第一轮应各问一次平台，实际 %v", channel.askedIDs())
	}

	next := bodyAccountEvent("好的")
	next.MessageID = "90002"
	ctx = runtime.withReplyIdentityPrivacyContext(context.Background(), next, []MessageEvent{first})
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
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	old := bodyAccountEvent("70007 来了")
	old.MessageID = "80001"

	runtime.withReplyIdentityPrivacyContext(context.Background(), bodyAccountEvent("你好"), []MessageEvent{old})
	if asked := channel.askedIDs(); len(asked) != 0 {
		t.Fatalf("历史号码不应触发平台查询: %v", asked)
	}
}

// 一条消息贴一串号码时，每轮只问有限几次，不能把主链路拖住。
func TestBodyAccountLookupsAreBoundedPerTurn(t *testing.T) {
	channel := &bodyAccountChannel{}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	runtime.withReplyIdentityPrivacyContext(context.Background(), bodyAccountEvent("11111 22222 33333 44444 55555 66666"), nil)
	if asked := channel.askedIDs(); len(asked) != bodyAccountLookupsPerTurn {
		t.Fatalf("每轮最多问 %d 次，实际 %v", bodyAccountLookupsPerTurn, asked)
	}
}

// 正文映射可以单独关掉；隐私代理整体关掉时它也跟着不起作用。
func TestBodyAccountMappingCanBeDisabled(t *testing.T) {
	for name, cfg := range map[string]BotConfig{
		"正文映射关": {LLMIdentityMaskingEnabled: boolPointer(true), LLMIdentityBodyAccounts: boolPointer(false)},
		"隐私代理关": {LLMIdentityMaskingEnabled: boolPointer(false)},
	} {
		t.Run(name, func(t *testing.T) {
			channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}}}
			runtime := newBodyAccountRuntime(cfg, channel)
			event := bodyAccountEvent("70007 是谁")
			ctx := runtime.withReplyIdentityPrivacyContext(context.Background(), event, nil)
			if asked := channel.askedIDs(); len(asked) != 0 {
				t.Fatalf("关掉后不应查询平台: %v", asked)
			}
			if scope := identityPrivacyScopeFromContext(ctx); scope != nil {
				if got := scope.protectText(event.RawMessage); !strings.Contains(got, "70007") {
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
	cfg := ConfigFromPayload(ConfigPayload{LLMIdentityBodyAccounts: boolPointer(false)}, BotConfig{})
	if llmIdentityBodyAccountMappingEnabled(cfg) {
		t.Fatal("显式关闭应保留")
	}
	payload := PayloadFromConfig(cfg)
	if payload.LLMIdentityBodyAccounts == nil || *payload.LLMIdentityBodyAccounts {
		t.Fatalf("配置回读丢了显式 false: %#v", payload.LLMIdentityBodyAccounts)
	}
}

func TestBodyAccountCandidates(t *testing.T) {
	text := "找 70007，链接 https://x.com/status/12345 版本 v1.23456 文件 a_12345.png 编号 012345 群号:90009"
	got := bodyAccountCandidates(text, 5, 10, true)
	want := []string{"70007", "90009"}
	if !slices.Equal(got, want) {
		t.Fatalf("候选 = %v, want %v", got, want)
	}
	if _, _, _, ok := bodyAccountDigitsAllowed(PlatformFeishu); ok {
		t.Fatal("账号不是数字的平台不应扫描正文数字")
	}
}

// 「QQ70007」「qq70008是谁」「Q70009」是在报 QQ 号；别的字母紧贴的数字仍然不算，
// 非 QQ 平台也不放行这个前缀。
func TestBodyAccountCandidatesAcceptQQPrefix(t *testing.T) {
	text := "QQ70007 qq70008是谁 Q70009 abQQ70010 QQQ70011 id70012 x70013"
	got := bodyAccountCandidates(text, 5, 10, true)
	if want := []string{"70007", "70008", "70009"}; !slices.Equal(got, want) {
		t.Fatalf("QQ 平台候选 = %v, want %v", got, want)
	}
	if got := bodyAccountCandidates(text, 5, 13, false); len(got) != 0 {
		t.Fatalf("非 QQ 平台不应放行字母前缀: %v", got)
	}
	_, _, qq, _ := bodyAccountDigitsAllowed(PlatformOneBotV11)
	_, _, tg, _ := bodyAccountDigitsAllowed(PlatformTelegram)
	if !qq || tg {
		t.Fatalf("QQ 前缀只该在 OneBot 平台放行: onebot=%v telegram=%v", qq, tg)
	}
}

// 路由之前那次每条群消息都会经过，大多数不回复，不能为它们向平台查询；
// 确定要回复时才查。
func TestBodyAccountRoutingNeverQueriesPlatform(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}}}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	event := bodyAccountEvent("70007 在吗")

	ctx := runtime.withIdentityPrivacyContext(context.Background(), event, nil)
	if asked := channel.askedIDs(); len(asked) != 0 {
		t.Fatalf("路由阶段不应查询平台: %v", asked)
	}
	if got := identityPrivacyScopeFromContext(ctx).protectText(event.RawMessage); !strings.Contains(got, "70007") {
		t.Fatalf("还没核实的号不应被换掉: %q", got)
	}

	ctx = runtime.withReplyIdentityPrivacyContext(ctx, event, nil)
	if asked := channel.askedIDs(); !slices.Equal(asked, []string{"70007"}) {
		t.Fatalf("进入回复后应核实一次: %v", asked)
	}
	if got := identityPrivacyScopeFromContext(ctx).protectText(event.RawMessage); strings.Contains(got, "70007") {
		t.Fatalf("核实后应换成别名: %q", got)
	}
}

// 群消息走完整条入站链路但不需要回复时，也不能触发平台查询。
func TestBodyAccountUnrepliedGroupMessageNeverQueriesPlatform(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}}}
	cfg := BotConfig{LLMIdentityMaskingEnabled: boolPointer(true), GroupTriggerMode: AliasTriggerStrict}
	runtime := newBodyAccountRuntime(cfg, channel)
	event := bodyAccountEvent("70007 今天没来")

	_, _, handled, _ := runtime.routeMessageEvent(context.Background(), event)
	if handled {
		t.Fatal("没有点名机器人的群消息不应进入回复")
	}
	if asked := channel.askedIDs(); len(asked) != 0 {
		t.Fatalf("不回复的消息不应查询平台: %v", asked)
	}
}

// 平台临时出错不能当成「不在群」记十分钟：过了短暂的冷却就会再问。
func TestBodyAccountTransientErrorIsNotCachedAsAbsent(t *testing.T) {
	channel := &bodyAccountChannel{
		members:  map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}},
		failures: map[string]error{"70007": errors.New("onebot websocket reconnecting")},
	}
	runtime := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
	now := time.Unix(1_700_000_000, 0)
	runtime.now = func() time.Time { return now }
	event := bodyAccountEvent("70007 是谁")

	runtime.withReplyIdentityPrivacyContext(context.Background(), event, nil)
	runtime.withReplyIdentityPrivacyContext(context.Background(), event, nil)
	if asked := channel.askedIDs(); len(asked) != 1 {
		t.Fatalf("冷却期内不应反复查询: %v", asked)
	}

	channel.mu.Lock()
	channel.failures = nil
	channel.mu.Unlock()
	now = now.Add(bodyAccountTransientTTL + time.Second)
	ctx := runtime.withReplyIdentityPrivacyContext(context.Background(), event, nil)
	if got := identityPrivacyScopeFromContext(ctx).protectText(event.RawMessage); strings.Contains(got, "70007") {
		t.Fatalf("临时故障恢复后应换成别名: %q", got)
	}
}

func TestGroupMemberAbsentDistinguishesTransientErrors(t *testing.T) {
	for _, err := range []error{
		&oneBotActionError{retCode: 200, message: "群成员不存在"},
		&oneBotActionError{retCode: 100, message: "获取群成员信息失败"},
		errors.New("telegram: user is not a current chat member (status left)"),
	} {
		if !groupMemberAbsent(err) {
			t.Errorf("应算作不在群: %v", err)
		}
	}
	for _, err := range []error{
		&oneBotActionError{retCode: 200, message: "NTEvent timeout"},
		errors.New("onebot websocket reconnecting"),
		errors.New("429 Too Many Requests"),
		context.DeadlineExceeded,
	} {
		if groupMemberAbsent(err) {
			t.Errorf("临时故障不应算作不在群: %v", err)
		}
	}
}

// bodyAccountHistoryStore 同时是历史存储和正文账号存储：运行时从 messageStore 上
// 做类型断言取它，测试替身走同一条路。
type bodyAccountHistoryStore struct {
	mu      sync.Mutex
	members map[string][]string
}

func (*bodyAccountHistoryStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (*bodyAccountHistoryStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *bodyAccountHistoryStore) LoadIdentityBodyAccounts(_ context.Context, platform, profileID, groupID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.members[bodyAccountGroupKey(platform, profileID, groupID)]...), nil
}

func (s *bodyAccountHistoryStore) SaveIdentityBodyAccount(_ context.Context, platform, profileID, groupID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members == nil {
		s.members = map[string][]string{}
	}
	key := bodyAccountGroupKey(platform, profileID, groupID)
	if !slices.Contains(s.members[key], userID) {
		s.members[key] = append(s.members[key], userID)
	}
	return nil
}

// 重启后内存缓存全空，历史里之前换成别名的号必须还是同一个别名，不能变回真号。
func TestBodyAccountAliasSurvivesRestart(t *testing.T) {
	store := &bodyAccountHistoryStore{}
	salt := &memoryAliasSaltStore{}
	newRuntime := func(channel *bodyAccountChannel) *Runtime {
		r := newBodyAccountRuntime(BotConfig{LLMIdentityMaskingEnabled: boolPointer(true)}, channel)
		r.SetMessageHistoryStore(struct {
			*bodyAccountHistoryStore
			*memoryAliasSaltStore
		}{store, salt})
		return r
	}
	first := bodyAccountEvent("70007 是谁")

	before := newRuntime(&bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "member"}}})
	ctx := before.withReplyIdentityPrivacyContext(context.Background(), first, nil)
	firstText := identityPrivacyScopeFromContext(ctx).protectText(first.RawMessage)
	if strings.Contains(firstText, "70007") {
		t.Fatalf("第一轮应换成别名: %q", firstText)
	}

	// 重启：新的 Runtime，平台这次什么都不回答，只能靠落库的结论。
	restartedChannel := &bodyAccountChannel{}
	after := newRuntime(restartedChannel)
	next := bodyAccountEvent("好的")
	next.MessageID = "90002"
	ctx = after.withIdentityPrivacyContext(context.Background(), next, []MessageEvent{first})
	if got := identityPrivacyScopeFromContext(ctx).protectText(first.RawMessage); got != firstText {
		t.Fatalf("重启后历史渲染变了:\n%q\n%q", firstText, got)
	}
	if asked := restartedChannel.askedIDs(); len(asked) != 0 {
		t.Fatalf("重启后历史号码不应重新查询: %v", asked)
	}
}

// 用户拿号码问「这是谁」，identity_check 开了群身份核验时要带回群名片。
func TestIdentityCheckReturnsDisplayNameForGroupMember(t *testing.T) {
	channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"70007": {Role: "admin", Card: "小王", Nickname: "wang"}}}
	runtime := newBodyAccountRuntime(BotConfig{}, channel)
	got := runIdentityCheck(t, runtime, bodyAccountEvent("70007 是谁"), map[string]any{"user_id": "70007", "check_group_role": true})
	if got.DisplayName != "小王" || got.GroupRole != string(GroupRoleAdmin) {
		t.Fatalf("应返回群名片和群身份: %+v", got)
	}
}
