// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// friendRosterChannel 让测试决定 get_friend_list 回答什么：名册里有谁，或者根本
// 答不上来（不支持这个接口的 OneBot 实现）。
type friendRosterChannel struct {
	*recordingChannel
	friends     []string
	unsupported bool
	// groups 非空时连接被当成在线，get_group_list 按它作答——群成员核验只认
	// 在线账号给出的完整列表。
	groups []string
	// sendFailure 非空时每次发送都失败。recordingChannel 自带的 sendErr 是
	// 「失败一次就自愈」的，测不了「这条通道一直发不出去」。
	sendFailure error
}

func newFriendRosterChannel(friends ...string) *friendRosterChannel {
	return &friendRosterChannel{recordingChannel: &recordingChannel{}, friends: friends}
}

func (c *friendRosterChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	if c.sendFailure != nil {
		return c.sendFailure
	}
	return c.recordingChannel.Send(ctx, msg)
}

func (c *friendRosterChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if c.sendFailure != nil {
		return nil, c.sendFailure
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func (c *friendRosterChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: len(c.groups) > 0}
}

func (c *friendRosterChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	response, err := c.recordingChannel.CallAPI(ctx, action, params)
	switch action {
	case "get_friend_list":
		if c.unsupported {
			return nil, errors.New("unsupported action")
		}
		return oneBotDataMap(oneBotIDListItems("user_id", c.friends)), nil
	case "get_group_list":
		return oneBotDataMap(oneBotIDListItems("group_id", c.groups)), nil
	}
	return response, err
}

func oneBotIDListItems(field string, ids []string) []any {
	items := make([]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, map[string]any{field: id})
	}
	return items
}

func crossSessionTestRuntime(channel Channel, cfg BotConfig) *Runtime {
	if strings.TrimSpace(cfg.Platform) == "" {
		cfg.Platform = PlatformOneBotV11
	}
	if strings.TrimSpace(cfg.BotAccount) == "" {
		cfg.BotAccount = "10000"
	}
	return NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
}

func crossSessionGroupSource(userID string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, UserID: userID, GroupID: "123",
		SelfID: "10000", Platform: PlatformOneBotV11, MessageID: "9001",
	}
}

// 群里说「私聊发给我」，内容必须当场落进私聊窗口，而不是发回群里。
func TestCrossSessionToolSendsToSpeakerPrivateChat(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	out, err := tool.Run(context.Background(), map[string]any{"message": "你的口癖整理如下"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("tool result = %s", out)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v", sent)
	}
	if sent[0].UserID != "555" || sent[0].GroupID != "" || sent[0].Text != "你的口癖整理如下" {
		t.Fatalf("消息没发进私聊：%#v", sent[0])
	}
	// 是好友，就不该借临时会话；引用的是群里那条消息，挂到私聊上只会指错地方。
	if sent[0].TempSessionGroupID != "" {
		t.Fatalf("好友不该走临时会话：%q", sent[0].TempSessionGroupID)
	}
	if sent[0].ReplyMessageID != "" {
		t.Fatalf("私聊不该带群消息引用：%q", sent[0].ReplyMessageID)
	}
}

// 不是好友时借发起那条群走临时会话，send_private_msg 必须带上 group_id。
func TestCrossSessionToolUsesTempSessionForNonFriend(t *testing.T) {
	channel := newFriendRosterChannel("888")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	if _, err := tool.Run(context.Background(), map[string]any{"message": "整理好了"}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].UserID != "555" || sent[0].TempSessionGroupID != "123" {
		t.Fatalf("非好友没有走临时会话：%#v", sent)
	}
}

// 临时会话最终要变成 send_private_msg 的 group_id 参数，正反向连接都得带上——
// 两条连接各写一份组包逻辑时，这类新参数很容易只加到其中一份上。
func TestOneBotSendPrivateMessageCarriesTempSessionGroup(t *testing.T) {
	var calls []recordingAPICall
	call := func(_ context.Context, action string, params map[string]any) (map[string]any, error) {
		calls = append(calls, recordingAPICall{action: action, params: params})
		return nil, nil
	}
	msg := OutgoingMessage{UserID: "555", Text: "整理好了", TempSessionGroupID: "123"}
	if _, err := sendOneBotMessage(context.Background(), msg, call); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].action != "send_private_msg" {
		t.Fatalf("calls = %#v", calls)
	}
	if got := stringFromAny(calls[0].params["group_id"]); got != "123" {
		t.Fatalf("临时会话没带上共同群：group_id = %q", got)
	}
	if got := stringFromAny(calls[0].params["user_id"]); got != "555" {
		t.Fatalf("user_id = %q", got)
	}

	calls = nil
	msg.TempSessionGroupID = ""
	if _, err := sendOneBotMessage(context.Background(), msg, call); err != nil {
		t.Fatal(err)
	}
	if _, exists := calls[0].params["group_id"]; exists {
		t.Fatalf("普通私聊不该带 group_id：%#v", calls[0].params)
	}
}

// 非好友又没有共同群时不白发一次：临时会话根本开不了。内容的去向由托管决定，
// 见 pending_direct_message_test.go。
func TestCrossSessionToolDoesNotBlindSendWithoutTempSession(t *testing.T) {
	channel := newFriendRosterChannel("888")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "999"})
	owner := MessageEvent{Kind: EventKindPrivate, UserID: "999", SelfID: "10000", Platform: PlatformOneBotV11}
	tool := newDianaCrossSessionTool(runtime, owner, true)
	if _, err := tool.Run(context.Background(), map[string]any{"message": "喂", "user_id": "555"}); err == nil {
		t.Fatal("没有托管存储时应当报错，而不是假装发出去了")
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("临时会话开不了时不该白发一次：发出了 %d 条", got)
	}
}

// 名册问不出来时按普通私聊发，让平台自己给拒绝原因，不要替它猜一个。
func TestCrossSessionToolFallsBackWhenFriendListUnsupported(t *testing.T) {
	channel := newFriendRosterChannel()
	channel.unsupported = true
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	if _, err := tool.Run(context.Background(), map[string]any{"message": "整理好了"}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].TempSessionGroupID != "" {
		t.Fatalf("问不出好友关系时不该自作主张走临时会话：%#v", sent)
	}
}

// 群里的第三个人没有开口，谁都不能借机器人的手给他发私信——主人除外。
func TestCrossSessionToolOnlyOwnerCanTargetSomeoneElse(t *testing.T) {
	channel := newFriendRosterChannel("777")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	member := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	if _, err := member.Run(context.Background(), map[string]any{"message": "喂", "user_id": "777"}); err == nil {
		t.Fatal("普通成员指定别人应当被拒绝")
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("被拒绝后仍然发出了 %d 条消息", got)
	}

	ownerChannel := newFriendRosterChannel("777")
	ownerRuntime := crossSessionTestRuntime(ownerChannel, BotConfig{OwnerID: "555"})
	owner := newDianaCrossSessionTool(ownerRuntime, crossSessionGroupSource("555"), true)
	if _, err := owner.Run(context.Background(), map[string]any{"message": "喂", "user_id": "777"}); err != nil {
		t.Fatal(err)
	}
	sent := ownerChannel.sentSnapshot()
	if len(sent) != 1 || sent[0].UserID != "777" || sent[0].GroupID != "" {
		t.Fatalf("主人指定的目标没收到私聊：%#v", sent)
	}
}

// 主人可以让机器人在别的群发言，普通成员不行。
func TestCrossSessionToolGroupTargetIsOwnerOnly(t *testing.T) {
	memberChannel := newFriendRosterChannel("555")
	memberRuntime := crossSessionTestRuntime(memberChannel, BotConfig{})
	member := newDianaCrossSessionTool(memberRuntime, crossSessionGroupSource("555"), false)
	if _, err := member.Run(context.Background(), map[string]any{"message": "喂", "group_id": "456"}); err == nil {
		t.Fatal("普通成员往群里发应当被拒绝")
	}
	if got := len(memberChannel.sentSnapshot()); got != 0 {
		t.Fatalf("被拒绝后仍然发出了 %d 条消息", got)
	}

	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	owner := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), true)
	if _, err := owner.Run(context.Background(), map[string]any{"message": "大家好", "group_id": "456"}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].GroupID != "456" || sent[0].UserID != "" {
		t.Fatalf("消息没发进目标群：%#v", sent)
	}
	// 主动在别的群说话不点名任何人。
	if sent[0].MentionUserID != "" {
		t.Fatalf("跨群发言不该 @ 人：%q", sent[0].MentionUserID)
	}
}

// 机器人根本不在那个群里时直接说，而不是发一条注定失败的消息。
func TestCrossSessionToolRefusesGroupTheBotIsNotIn(t *testing.T) {
	channel := newFriendRosterChannel("555")
	channel.groups = []string{"123", "789"}
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	owner := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), true)
	if _, err := owner.Run(context.Background(), map[string]any{"message": "大家好", "group_id": "456"}); err == nil {
		t.Fatal("不在的群应当被拒绝")
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("被拒绝后仍然发出了 %d 条消息", got)
	}
	if _, err := owner.Run(context.Background(), map[string]any{"message": "大家好", "group_id": "789"}); err != nil {
		t.Fatalf("在的群应当发得出去：%v", err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].GroupID != "789" {
		t.Fatalf("消息没发进目标群：%#v", sent)
	}
}

// 关掉或不准入的群不发：换条通道绕过开关不算遵守开关。
func TestCrossSessionToolRefusesGroupOutsideAdmission(t *testing.T) {
	channel := newFriendRosterChannel("555")
	cfg := BotConfig{OwnerID: "555", GroupAdmission: GroupAdmission{Mode: GroupAdmissionWhitelist, AllowedGroups: []string{"123"}}}
	runtime := crossSessionTestRuntime(channel, cfg)
	owner := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), true)
	if _, err := owner.Run(context.Background(), map[string]any{"message": "大家好", "group_id": "456"}); err == nil {
		t.Fatal("不准入的群应当被拒绝")
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("被拒绝后仍然发出了 %d 条消息", got)
	}
}

// 目的地是当前这条会话时一律拒绝：这一条让工具没法被拿来代替正常回复。
func TestCrossSessionToolRejectsCurrentSession(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	group := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), true)
	if _, err := group.Run(context.Background(), map[string]any{"message": "喂", "group_id": "123"}); err == nil {
		t.Fatal("发回当前这个群应当被拒绝")
	}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "555", SelfID: "10000", Platform: PlatformOneBotV11}
	if _, err := newDianaCrossSessionTool(runtime, private, true).Run(context.Background(), map[string]any{"message": "喂"}); err == nil {
		t.Fatal("私聊里对着同一个人调用应当被拒绝")
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("被拒绝后仍然发出了 %d 条消息", got)
	}
}

// 私聊准入和屏蔽名单都是「不要找他」的意思，换条通道绕过去不算遵守。
func TestCrossSessionToolRespectsPrivateAdmissionAndBlocklist(t *testing.T) {
	for _, item := range []struct {
		name string
		cfg  BotConfig
	}{
		{"屏蔽名单", BotConfig{ReplyGate: &ReplyGate{BlockedUsers: []string{"555"}}}},
		{"私聊准入", BotConfig{OwnerID: "999", PrivateAdmission: PrivateAdmission{Mode: PrivateAdmissionOwnerOnly}}},
	} {
		channel := newFriendRosterChannel("555")
		runtime := crossSessionTestRuntime(channel, item.cfg)
		tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
		if _, err := tool.Run(context.Background(), map[string]any{"message": "喂"}); err == nil {
			t.Fatalf("%s：应当被拒绝", item.name)
		}
		if got := len(channel.sentSnapshot()); got != 0 {
			t.Fatalf("%s：被拒绝后仍然发出了 %d 条消息", item.name, got)
		}
	}
}

func TestCrossSessionToolRejectsBadInput(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), true)
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"空正文", map[string]any{"message": "   "}},
		{"超长正文", map[string]any{"message": strings.Repeat("字", crossSessionMaxRunes+1)}},
		{"发给机器人自己", map[string]any{"message": "喂", "user_id": "10000"}},
		{"昵称当账号", map[string]any{"message": "喂", "user_id": "糖宝"}},
		{"群名当群号", map[string]any{"message": "喂", "group_id": "摸鱼群"}},
		{"人和群同时填", map[string]any{"message": "喂", "user_id": "777", "group_id": "456"}},
	}
	for _, item := range cases {
		if _, err := tool.Run(context.Background(), item.input); err == nil {
			t.Fatalf("%s 应当被拒绝", item.name)
		}
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("被拒绝后仍然发出了 %d 条消息", got)
	}
}

// 限流挡的是工具循环里的连发：同一个目标不连着收，同一个来源会话有总量上限。
func TestCrossSessionSendRespectsCooldownAndSessionLimit(t *testing.T) {
	channel := newFriendRosterChannel("1", "2", "3", "4", "5", "6")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	source := crossSessionGroupSource("555")
	ctx := context.Background()
	target := func(id string) crossSessionTarget {
		return crossSessionTarget{
			event: crossSessionPrivateEvent(source, id, time.Now()),
			key:   "private|" + id,
			label: "账号 " + id + " 的私聊",
		}
	}
	if _, err := runtime.sendCrossSessionMessage(ctx, source, target("1"), "第一条"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.sendCrossSessionMessage(ctx, source, target("1"), "再来一条"); !errors.Is(err, errCrossSessionRateLimited) {
		t.Fatalf("同一个目标的冷却没生效：%v", err)
	}
	for _, id := range []string{"2", "3", "4", "5"} {
		if _, err := runtime.sendCrossSessionMessage(ctx, source, target(id), "第一条"); err != nil {
			t.Fatalf("发给 %s：%v", id, err)
		}
	}
	if _, err := runtime.sendCrossSessionMessage(ctx, source, target("6"), "第一条"); !errors.Is(err, errCrossSessionRateLimited) {
		t.Fatalf("会话总量上限没生效：%v", err)
	}
	if got := len(channel.sentSnapshot()); got != crossSessionLimit {
		t.Fatalf("实际发出 %d 条，上限是 %d", got, crossSessionLimit)
	}
}

// 工具只在「确实存在另一条会话可发」时才挂：私聊里给普通成员挂上它，模型看得到
// 就会去调，然后只能被拒绝。
func TestCrossSessionToolRegistrationScope(t *testing.T) {
	runtime := crossSessionTestRuntime(newFriendRosterChannel(), BotConfig{OwnerID: "999"})
	cfg := runtime.effectiveConfigForEvent(crossSessionGroupSource("555"))
	for _, item := range []struct {
		name  string
		event MessageEvent
		want  bool
	}{
		{"群里的普通成员", crossSessionGroupSource("555"), true},
		{"私聊里的普通成员", MessageEvent{Kind: EventKindPrivate, UserID: "555", Platform: PlatformOneBotV11}, false},
		{"私聊里的主人", MessageEvent{Kind: EventKindPrivate, UserID: "999", Platform: PlatformOneBotV11}, true},
	} {
		profile, _ := runtime.loadUserMemoryProfile(context.Background(), item.event)
		relationship := RelationshipPolicyForConfig(cfg, profile, item.event.UserID)
		got := item.event.Kind == EventKindGroup || relationship.Owner
		if got != item.want {
			t.Fatalf("%s：挂载与否 = %v，期望 %v", item.name, got, item.want)
		}
	}
}

// 关系等级不该把这件事挡在门外：「私聊发给我」是群里任何人都会提的要求。
// 同时，指定别人和指定群的参数只对主人出现在 schema 里。
func TestCrossSessionToolAvailableToNonOwnersWithNarrowerSchema(t *testing.T) {
	policy := RelationshipPolicyFor(UserMemoryProfile{UserID: "555"}, "999", "555")
	if policy.Owner {
		t.Fatal("测试前提不成立：555 不该是主人")
	}
	if !policy.allowedAgentToolNames()[dianaCrossSessionToolName] {
		t.Fatal("非主人应当也能用跨会话发送")
	}
	runtime := crossSessionTestRuntime(newFriendRosterChannel(), BotConfig{OwnerID: "999"})
	memberSchema := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false).InputSchema()
	properties, _ := memberSchema["properties"].(map[string]any)
	if _, exists := properties["group_id"]; exists {
		t.Fatal("非主人的 schema 里不该出现 group_id")
	}
	if _, exists := properties["user_id"]; exists {
		t.Fatal("非主人的 schema 里不该出现 user_id")
	}
	ownerSchema := newDianaCrossSessionTool(runtime, crossSessionGroupSource("999"), true).InputSchema()
	ownerProperties, _ := ownerSchema["properties"].(map[string]any)
	if _, exists := ownerProperties["group_id"]; !exists {
		t.Fatal("主人的 schema 里应当有 group_id")
	}
}
