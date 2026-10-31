// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
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
	// forwardErr 非空时原生单条转发失败，用来测退回照原文重发。
	forwardErr error
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
	case "forward_group_single_msg", "forward_friend_single_msg":
		if c.forwardErr != nil {
			return nil, c.forwardErr
		}
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
	// 投递测试显式启用私聊；准入限制用例可传入其他模式覆盖。
	if cfg.PrivateAdmission.Mode == "" {
		cfg.PrivateAdmission.Mode = PrivateAdmissionAll
	}
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
		{"私聊关闭", BotConfig{OwnerID: "555", PrivateAdmission: PrivateAdmission{Mode: PrivateAdmissionDisabled}}},
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
	if _, err := runtime.sendCrossSessionMessage(ctx, source, target("1"), crossSessionPayload{message: "第一条"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.sendCrossSessionMessage(ctx, source, target("1"), crossSessionPayload{message: "再来一条"}); !errors.Is(err, errCrossSessionRateLimited) {
		t.Fatalf("同一个目标的冷却没生效：%v", err)
	}
	for _, id := range []string{"2", "3", "4", "5"} {
		if _, err := runtime.sendCrossSessionMessage(ctx, source, target(id), crossSessionPayload{message: "第一条"}); err != nil {
			t.Fatalf("发给 %s：%v", id, err)
		}
	}
	if _, err := runtime.sendCrossSessionMessage(ctx, source, target("6"), crossSessionPayload{message: "第一条"}); !errors.Is(err, errCrossSessionRateLimited) {
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

// 主人在私聊里发图、引用一个文件、再带一段语音，说「转到群里」：附件要一个不落地
// 发进目标群，只转附件时正文可以留空。
func TestCrossSessionToolForwardsMediaFromPrivateToGroup(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	source := MessageEvent{
		Kind: EventKindPrivate, UserID: "555", SelfID: "10000", Platform: PlatformOneBotV11, MessageID: "9002",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "转到群里"}},
			{Type: "image", Data: map[string]string{"url": "https://example.test/a.png"}},
			{Type: "video", Data: map[string]string{"url": "https://example.test/b.mp4"}},
		},
		Quoted: &QuotedMessage{MessageID: "9001", Segments: []MessageSegment{
			{Type: "file", Data: map[string]string{"file": "/tmp/report.pdf", "name": "report.pdf"}},
		}},
	}
	tool := newDianaCrossSessionTool(runtime, source, true)
	out, err := tool.Run(context.Background(), map[string]any{"group_id": "456", "forward_media": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"media":3`) {
		t.Fatalf("tool result = %s", out)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 3 {
		t.Fatalf("sent = %#v", sent)
	}
	for _, msg := range sent {
		if msg.GroupID != "456" || msg.UserID != "" {
			t.Fatalf("附件没发进目标群：%#v", msg)
		}
	}
	if len(sent[0].ImageURLs) != 1 || len(sent[1].VideoURLs) != 1 {
		t.Fatalf("图片、视频没发出：%#v", sent)
	}
	if len(sent[2].Segments) != 1 || sent[2].Segments[0].Type != "file" || sent[2].Segments[0].Data["name"] != "report.pdf" {
		t.Fatalf("引用里的文件没发出：%#v", sent[2])
	}
}

// 不要求转附件时，消息里带的图不会被顺手发出去。
func TestCrossSessionToolDoesNotForwardMediaUnlessAsked(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	source := crossSessionGroupSource("555")
	source.Segments = []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.test/a.png"}}}
	tool := newDianaCrossSessionTool(runtime, source, false)
	if _, err := tool.Run(context.Background(), map[string]any{"message": "整理好了"}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || len(sent[0].ImageURLs) != 0 {
		t.Fatalf("sent = %#v", sent)
	}
	empty := newDianaCrossSessionTool(runtime, crossSessionGroupSource("556"), false)
	if _, err := empty.Run(context.Background(), map[string]any{"forward_media": true}); err == nil || !strings.Contains(err.Error(), "没有能转发") {
		t.Fatalf("没有附件也没有正文应当被拒绝：%v", err)
	}
}

// 换平台转发时只留链接和本机文件，Telegram file_id 这种平台内引用要丢掉。
func TestCrossSessionMediaPortableDropsPlatformRefs(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{
		{Type: "image", Data: map[string]string{"file_id": "AgACAgUAAx"}},
		{Type: "image", Data: map[string]string{"cached_file": "/data/media/a.png", "file_id": "AgACAgUAAy"}},
		{Type: "record", Data: map[string]string{"url": "https://example.test/v.ogg"}},
		{Type: "file", Data: map[string]string{"file_id": "BQACAgUAAz", "name": "a.zip"}},
	}}
	same := crossSessionMediaFromEvent(event, false)
	if same.count() != 4 {
		t.Fatalf("同平台应当全部保留：%#v", same)
	}
	portable := crossSessionMediaFromEvent(event, true)
	if len(portable.images) != 1 || portable.images[0] != "/data/media/a.png" || len(portable.audios) != 1 || len(portable.files) != 0 {
		t.Fatalf("portable = %#v", portable)
	}
}

// 转发的是一段成品（帖子、整理好的长文），对面收到的必须和原文逐字一致。它曾经走
// 聊天分条：每行拆成一个气泡、句号被删、段落空行被收掉，行数一多又被打包成合并
// 转发卡片，看上去就是「转发多出了一堆分行」。
func TestCrossSessionToolDeliversMessageVerbatim(t *testing.T) {
	message := "识别：示例帖子\n作者：示例作者 (@example)\n文案：今天整理了三步。\n\n" +
		"第一步：拿码\n登录后等邮件，里面有专属链接。\n\n" +
		"第二步：改设置\n先加一个，再减一个。\n动作要快。\n\n" +
		"第三步：付款\n大部分卡过不去。\n链接：https://example.com/post/1"
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	if _, err := tool.Run(context.Background(), map[string]any{"message": message}); err != nil {
		t.Fatal(err)
	}
	for _, call := range channel.callsSnapshot() {
		if strings.Contains(call.action, "forward") {
			t.Fatalf("一条正文不该被打包成合并转发：%#v", call)
		}
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("正文被拆成了 %d 条：%#v", len(sent), sent)
	}
	if sent[0].Text != message {
		t.Fatalf("转发内容和原文不一致：\n got: %q\nwant: %q", sent[0].Text, message)
	}
}

// 模型显式写的 <diana-message/> 仍是分条信号，分出来的每条同样保留原文排版。
func TestCrossSessionToolSplitsOnlyOnExplicitMarker(t *testing.T) {
	first := "第一篇：标题\n\n正文第一段。\n正文第二段。"
	second := "第二篇：标题\n只有一段。"
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	if _, err := tool.Run(context.Background(), map[string]any{"message": first + notificationSplitMarker + second}); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || sent[0].Text != first || sent[1].Text != second {
		t.Fatalf("应当按 <diana-message/> 原样分成两条：%#v", sent)
	}
}

func TestSplitVerbatimReplyKeepsLayout(t *testing.T) {
	text := "第一行。\r\n\r\n第二行<diana-br/>第三行。"
	got := splitVerbatimReply(text)
	want := "第一行。\n\n第二行\n第三行。"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("splitVerbatimReply = %q, want %q", got, want)
	}
	long := strings.Repeat("长", notificationChunkSize*2)
	if chunks := splitVerbatimReply(long); len(chunks) < 2 || strings.Join(chunks, "") != long {
		t.Fatalf("超长正文应按平台长度兜底切开且不丢字：%d 段", len(chunks))
	}
}

// 正文超过合并转发字数阈值时仍打包成卡片，但卡片里同样是原文：一个节点装完整段，
// 而不是一行一个节点。
func TestCrossSessionToolForwardCardKeepsMessageVerbatim(t *testing.T) {
	message := "标题：示例\n\n第一段，句号要留着。\n第二行。\n\n第三段。"
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{ForwardReplyThreshold: 10})
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	if _, err := tool.Run(context.Background(), map[string]any{"message": message}); err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	for _, call := range channel.callsSnapshot() {
		if strings.Contains(call.action, "forward_msg") {
			nodes, _ = call.params["messages"].([]map[string]any)
		}
	}
	if len(nodes) != 1 {
		t.Fatalf("卡片应当只有一个节点，实际 %d 个；已发 %#v", len(nodes), channel.sentSnapshot())
	}
	data, _ := nodes[0]["data"].(map[string]any)
	if got := forwardNodeContentText(data["content"]); got != message {
		t.Fatalf("卡片节点和原文不一致：\n got: %q\nwant: %q", got, message)
	}
}

// crossSessionForwardSource 是主人引用自己发的一条链接说「转到 456 群」的那一轮。
func crossSessionForwardSource() MessageEvent {
	source := crossSessionGroupSource("555")
	source.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "转到 456 群里"}}}
	source.Quoted = &QuotedMessage{MessageID: "70001", UserID: "555", Segments: []MessageSegment{
		{Type: "reply", Data: map[string]string{"id": "69999"}},
		{Type: "text", Data: map[string]string{"text": "http://x.com/example/status/1 看这个。"}},
		{Type: "image", Data: map[string]string{"url": "https://example.test/c.png"}},
	}}
	return source
}

func crossSessionNativeForwardCalls(channel *friendRosterChannel) []recordingAPICall {
	var calls []recordingAPICall
	for _, call := range channel.callsSnapshot() {
		if strings.HasPrefix(call.action, "forward_") && strings.HasSuffix(call.action, "_single_msg") {
			calls = append(calls, call)
		}
	}
	return calls
}

// 按 ID 转发时同一台 OneBot 机器人走接入端原生转发：发出去的就是原消息，模型一个
// 字都不经手。
func TestCrossSessionToolForwardsExistingMessageNatively(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	tool := newDianaCrossSessionTool(runtime, crossSessionForwardSource(), true)
	out, err := tool.Run(context.Background(), map[string]any{"group_id": "456", "message_ids": []any{"70001"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"forwarded":1`) || strings.Contains(out, `"replayed"`) {
		t.Fatalf("tool result = %s", out)
	}
	calls := crossSessionNativeForwardCalls(channel)
	if len(calls) != 1 || calls[0].action != "forward_group_single_msg" {
		t.Fatalf("没有走原生转发：%#v", channel.callsSnapshot())
	}
	if calls[0].params["message_id"] != int64(70001) || calls[0].params["group_id"] != int64(456) {
		t.Fatalf("原生转发参数不对：%#v", calls[0].params)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("原生转发之外不该再自己发一遍：%#v", sent)
	}
}

// 私聊目的地用好友转发接口。
func TestCrossSessionToolForwardsExistingMessageToFriend(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	tool := newDianaCrossSessionTool(runtime, crossSessionForwardSource(), false)
	if _, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{"70001"}}); err != nil {
		t.Fatal(err)
	}
	calls := crossSessionNativeForwardCalls(channel)
	if len(calls) != 1 || calls[0].action != "forward_friend_single_msg" || calls[0].params["user_id"] != int64(555) {
		t.Fatalf("私聊没有走好友转发：%#v", calls)
	}
}

// 接入端转不了时退回照原文重发：链接一字不差、配图跟着同一条发，回复引用丢掉，
// 并且在工具结果里说清楚退回了。
func TestCrossSessionToolReplaysExistingMessageWhenNativeForwardFails(t *testing.T) {
	channel := newFriendRosterChannel("555")
	channel.forwardErr = errors.New("message not found: 70001")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	tool := newDianaCrossSessionTool(runtime, crossSessionForwardSource(), true)
	out, err := tool.Run(context.Background(), map[string]any{
		"group_id": "456", "message_ids": []any{"70001"}, "message": "主人让我转过来的",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"replayed":1`) || !strings.Contains(out, "照原文重发") {
		t.Fatalf("工具结果没说退回了：%s", out)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 {
		t.Fatalf("sent = %#v", sent)
	}
	original := sent[0]
	if original.GroupID != "456" || original.Text != "http://x.com/example/status/1 看这个。" || !original.LiteralText {
		t.Fatalf("原消息没有照原文发出：%#v", original)
	}
	if len(original.ImageURLs) != 1 || original.ImageURLs[0] != "https://example.test/c.png" {
		t.Fatalf("原消息的图片没跟着发：%#v", original)
	}
	if original.ReplyMessageID != "" {
		t.Fatalf("原会话里的回复引用不该带过去：%q", original.ReplyMessageID)
	}
	if sent[1].Text != "主人让我转过来的" {
		t.Fatalf("附言应当在原消息之后：%#v", sent[1])
	}
}

// 原生转发结果不明时可能已经发出去了，再照原文发一遍就是重复。
func TestCrossSessionToolDoesNotReplayWhenForwardOutcomeUnknown(t *testing.T) {
	channel := newFriendRosterChannel("555")
	channel.forwardErr = &outboundOutcomeUnknownError{action: "forward_group_single_msg", cause: context.DeadlineExceeded}
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	tool := newDianaCrossSessionTool(runtime, crossSessionForwardSource(), true)
	if _, err := tool.Run(context.Background(), map[string]any{"group_id": "456", "message_ids": []any{"70001"}}); err == nil {
		t.Fatal("结果不明应当报错")
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("结果不明时不该照原文重发：%#v", sent)
	}
}

// 历史里的消息也能按 ID 转；模型把 ID 写成数字时照样认。
func TestCrossSessionToolForwardsMessageFromHistory(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	source := crossSessionGroupSource("555")
	runtime.history[sessionKey(source)] = []MessageEvent{{
		Kind: EventKindGroup, GroupID: "123", UserID: "10000", MessageID: "70002",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "机器人之前发的"}}},
	}}
	tool := newDianaCrossSessionTool(runtime, source, true)
	if _, err := tool.Run(context.Background(), map[string]any{"group_id": "456", "message_ids": []any{float64(70002)}}); err != nil {
		t.Fatal(err)
	}
	calls := crossSessionNativeForwardCalls(channel)
	if len(calls) != 1 || calls[0].params["message_id"] != int64(70002) {
		t.Fatalf("历史消息没有被转发：%#v", calls)
	}
}

// 只能转当前会话里的消息：找不到的 ID、还原不出来的别名都不交给接入端去别处翻。
func TestCrossSessionToolRejectsForwardOutsideSession(t *testing.T) {
	for _, id := range []string{"70009", "im_msg_deadbeef"} {
		channel := newFriendRosterChannel("555")
		runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
		tool := newDianaCrossSessionTool(runtime, crossSessionForwardSource(), true)
		if _, err := tool.Run(context.Background(), map[string]any{"group_id": "456", "message_ids": []any{id}}); err == nil {
			t.Fatalf("%s 应当被拒绝", id)
		}
		if calls := crossSessionNativeForwardCalls(channel); len(calls) != 0 {
			t.Fatalf("%s 被拒绝后仍调用了转发：%#v", id, calls)
		}
		if sent := channel.sentSnapshot(); len(sent) != 0 {
			t.Fatalf("%s 被拒绝后仍发出了消息：%#v", id, sent)
		}
	}
}

// 被引用的那条已经整条转发时，forward_media 不该把它的图再单独发一遍。
func TestCrossSessionToolForwardMediaSkipsForwardedMessage(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "555"})
	tool := newDianaCrossSessionTool(runtime, crossSessionForwardSource(), true)
	out, err := tool.Run(context.Background(), map[string]any{
		"group_id": "456", "message_ids": []any{"70001"}, "forward_media": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"media"`) {
		t.Fatalf("附件被重复计入：%s", out)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("原生转发之外又单独发了附件：%#v", sent)
	}
}

// 隐私代理给消息 ID 起的别名放在数组里也要还原成真实 ID。
func TestIdentityPrivacyRestoresMessageIDArray(t *testing.T) {
	scope := newIdentityPrivacyScopeWithSalt("test")
	alias := scope.registerMessageID("70001")
	if alias == "70001" {
		t.Fatal("消息 ID 没有起别名")
	}
	calls := scope.restoreToolCalls([]llm.ToolCall{{Name: dianaCrossSessionToolName, Arguments: map[string]any{
		"message_ids": []any{alias},
	}}})
	ids, _ := calls[0].Arguments["message_ids"].([]any)
	if len(ids) != 1 || ids[0] != "70001" {
		t.Fatalf("别名没有还原：%#v", calls[0].Arguments)
	}
}
