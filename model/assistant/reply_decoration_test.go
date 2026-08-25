// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 归一化要吃掉大小写和空白，没写或写错都按 on 处理。
func TestNormalizeReplyDecorationMode(t *testing.T) {
	// 没写和写错都按 auto，和 DefaultBotConfig 的默认值一致：同一份没填的配置
	// 不该在两条路径上得到两种行为。
	cases := map[ReplyDecorationMode]ReplyDecorationMode{
		"":         ReplyDecorationAuto,
		"on":       ReplyDecorationOn,
		"off":      ReplyDecorationOff,
		" AUTO ":   ReplyDecorationAuto,
		"nonsense": ReplyDecorationAuto,
	}
	for input, want := range cases {
		if got := normalizeReplyDecorationMode(input); got != want {
			t.Fatalf("normalizeReplyDecorationMode(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSendAutoModeLeavesReferenceAndMentionToModel 验证 auto 档运行时不再自动补装饰件。
func TestSendAutoModeLeavesReferenceAndMentionToModel(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{
		ReplyReferenceMode: ReplyDecorationAuto,
		MentionUserMode:    ReplyDecorationAuto,
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1244393238"}

	if err := runtime.send(context.Background(), event, "你好"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if channel.sent[0].ReplyMessageID != "" || channel.sent[0].MentionUserID != "" {
		t.Fatalf("auto mode still decorated the reply: %#v", channel.sent[0])
	}
}

// TestSendAutoModeKeepsModelWrittenReplyMarker 验证 auto 档下模型自己写的引用标记仍然生效。
func TestSendAutoModeKeepsModelWrittenReplyMarker(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{
		ReplyReferenceMode: ReplyDecorationAuto,
		MentionUserMode:    ReplyDecorationAuto,
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1244393238"}

	if err := runtime.send(context.Background(), event, replyMarkerPrefix+"1244393238] 你好"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if channel.sent[0].ReplyMessageID != "1244393238" {
		t.Fatalf("model written reply marker was dropped: %#v", channel.sent[0])
	}
	if strings.Contains(channel.sent[0].Text, replyMarkerPrefix) {
		t.Fatalf("reply marker leaked into the message text: %#v", channel.sent[0])
	}
}

func TestReplyDecorationPromptOnlyGuidesAutoMode(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1244393238"}
	// on 和 off 都是运行时说了算，不需要也不该告诉模型怎么判断。
	for _, mode := range []ReplyDecorationMode{ReplyDecorationOn, ReplyDecorationOff} {
		decided := BotConfig{ReplyReferenceMode: mode, MentionUserMode: mode}.WithDefaults()
		if prompt := replyDecorationPrompt(decided, event, nil); prompt != "" {
			t.Fatalf("mode %s should not emit decoration guidance: %q", mode, prompt)
		}
	}
	// 默认就是 auto，所以默认配置反过来必须带上这份判断依据。
	if prompt := replyDecorationPrompt(BotConfig{}.WithDefaults(), event, nil); prompt == "" {
		t.Fatal("default config is auto now, it must carry the decoration guidance")
	}

	cfg := BotConfig{ReplyReferenceMode: ReplyDecorationAuto, MentionUserMode: ReplyDecorationAuto}.WithDefaults()
	prompt := replyDecorationPrompt(cfg, event, nil)
	if !strings.Contains(prompt, replyMarkerPrefix+"1244393238]") {
		t.Fatalf("auto prompt is missing the current message marker: %q", prompt)
	}
	if !strings.Contains(prompt, "[diana-at:10001]") {
		t.Fatalf("auto prompt is missing the sender mention hint: %q", prompt)
	}
	// 私聊没有引用和 @ 的概念，不该占用上下文。
	if direct := replyDecorationPrompt(cfg, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, nil); direct != "" {
		t.Fatalf("private chat should not emit decoration guidance: %q", direct)
	}
}

// 追发合并后合并回复没有锚点指向前一条,发的人会觉得前一条被跳过。
// 连发未回时提示词必须点出那条消息并建议引用它。
func TestReplyDecorationPromptAnchorsPendingEarlierMessage(t *testing.T) {
	cfg := BotConfig{ReplyReferenceMode: ReplyDecorationAuto}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u1", MessageID: "222", Time: 10000,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "以及pr合并"}}}}
	earlier := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u1", MessageID: "111", Time: 9995,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "commit提交也不用每次都推"}}}}

	prompt := replyDecorationPrompt(cfg, current, []MessageEvent{earlier, current})
	if !strings.Contains(prompt, "你还没有回复") || !strings.Contains(prompt, "commit提交也不用每次都推") {
		t.Fatalf("连发未回时应点出上一条:%s", prompt)
	}
	// 承接靠措辞完成,不建议引用——引用框太重,连发场景里真人也只是接着说。
	if strings.Contains(prompt, replyMarkerPrefix+"111]") {
		t.Fatalf("不该建议引用更早那条消息:%s", prompt)
	}

	// 中间隔着机器人回复,说明上一条已经回过,不算连发未回。
	botReply := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "bot", MessageID: "150", Time: 9997, Outbound: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "收到"}}}}
	prompt = replyDecorationPrompt(cfg, current, []MessageEvent{earlier, botReply, current})
	if strings.Contains(prompt, "你还没有回复") {
		t.Fatalf("已回复过的不该再提示承接:%s", prompt)
	}

	// 隔太久的两条消息是两个话题,不绑在一起。
	stale := earlier
	stale.Time = current.Time - int64(pendingEarlierMessageWindow/time.Second) - 1
	prompt = replyDecorationPrompt(cfg, current, []MessageEvent{stale, current})
	if strings.Contains(prompt, "你还没有回复") {
		t.Fatalf("超出连发窗口不该提示:%s", prompt)
	}

	// 别人的消息不算自己的连发。
	other := earlier
	other.UserID = "u2"
	prompt = replyDecorationPrompt(cfg, current, []MessageEvent{other, current})
	if strings.Contains(prompt, "你还没有回复") {
		t.Fatalf("他人消息不该触发承接提示:%s", prompt)
	}
}

// 订阅推送和聊天回复对 @ 的诉求相反：聊天里每句都 @ 很烦人，所以有 auto/off；
// 但提醒和订阅是过了很久之后主动找某个人，正文是模板或后台任务生成的，没有模型
// 帮它写 @，被那个开关连坐的结果就是订阅者在群里永远收不到点名。
func TestSubscriberNoticeMentionsIgnoringChatMentionMode(t *testing.T) {
	withFastSendTiming(t)
	for _, mode := range []ReplyDecorationMode{ReplyDecorationAuto, ReplyDecorationOff, ReplyDecorationOn} {
		channel := &scriptedChannel{}
		runtime := NewRuntime(BotConfig{
			ReplyReferenceMode: mode,
			MentionUserMode:    mode,
		}, channel, NewPluginManager(), nil, nil, nil, nil)
		event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}

		if err := runtime.sendSubscriberNotice(context.Background(), event, "提醒你：该喝水了"); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if len(channel.sent) != 1 {
			t.Fatalf("mode %q sent = %#v", mode, channel.sent)
		}
		if channel.sent[0].MentionUserID != "10001" {
			t.Fatalf("mode %q did not mention the subscriber: %#v", mode, channel.sent[0])
		}
		// 触发它的那条消息可能是几天前的，引用没有意义。
		if channel.sent[0].ReplyMessageID != "" {
			t.Fatalf("mode %q attached a reply reference: %#v", mode, channel.sent[0])
		}
	}
}

// 「群里有没有别人在说话」是算得出来的，不该让模型从历史文本里猜。
func TestOtherSpeakersBeforeCountsInterveningPeople(t *testing.T) {
	now := int64(1_700_000_000)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", MessageID: "m5", Time: now}
	history := []MessageEvent{
		{GroupID: "g", UserID: "1", MessageID: "m1", Time: now - 60},
		{GroupID: "g", UserID: "2", MessageID: "m2", Time: now - 40},
		{GroupID: "g", UserID: "3", MessageID: "m3", Time: now - 20},
		{GroupID: "g", UserID: "2", MessageID: "m4", Time: now - 10},
		event,
	}
	if got := otherSpeakersBefore(history, event); got != 2 {
		t.Fatalf("otherSpeakersBefore = %d, want 2", got)
	}
}

func TestOtherSpeakersBeforeIgnoresSelfBotAndStaleTurns(t *testing.T) {
	now := int64(1_700_000_000)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", MessageID: "m9", Time: now}
	history := []MessageEvent{
		// 窗口之外的插话不算：那是上一个话题了。
		{GroupID: "g", UserID: "7", MessageID: "old", Time: now - int64(mentionCrowdWindow.Seconds()) - 1},
		// 机器人自己的发言不算。
		{GroupID: "g", UserID: "bot", MessageID: "m7", Time: now - 30, Outbound: true},
		// 发送者自己连发不算「有人插话」。
		{GroupID: "g", UserID: "1", MessageID: "m8", Time: now - 10},
		event,
	}
	if got := otherSpeakersBefore(history, event); got != 0 {
		t.Fatalf("otherSpeakersBefore = %d, want 0", got)
	}
}

// 冷清和热闹给的是两条不同的规则，不是同一句话加个数字。
func TestMentionDecorationRuleFollowsCrowd(t *testing.T) {
	quiet := mentionDecorationRule("123456", 0, false)
	if !strings.Contains(quiet, "不用 @") {
		t.Fatalf("一对一时应当明说不用 @：%s", quiet)
	}
	busy := mentionDecorationRule("123456", 3, false)
	// 写法要和「群聊真实提及规则」一致，都用平台中立的标记：既不教裸 @数字，
	// 也不教 OneBot 方言的 CQ 码——Telegram 群里那只会把字面量发出去。
	if !strings.Contains(busy, "3 个人") || !strings.Contains(busy, "[diana-at:123456]") {
		t.Fatalf("热闹时应当给出人数并要求用提及标记点名：%s", busy)
	}
	for _, unwanted := range []string{"写 @123456", "[CQ:at"} {
		if strings.Contains(busy, unwanted) {
			t.Fatalf("不该再教平台方言写法 %q：%s", unwanted, busy)
		}
	}
}

// 私聊本来就只有他一个人，@ 没有意义，也不该冒出一个 at 段。
func TestSubscriberNoticeDoesNotMentionInPrivateChat(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{MentionUserMode: ReplyDecorationOn}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	if err := runtime.sendSubscriberNotice(context.Background(), event, "提醒你：该喝水了"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "" {
		t.Fatalf("private notice = %#v", channel.sent)
	}
}

// 仓库订阅走的是另一条投递函数（通知卡片不按人设分条），@ 也要跟上。
func TestRepositoryNotificationMentionsSubscriber(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{MentionUserMode: ReplyDecorationOff}, channel, NewPluginManager(), nil, nil, nil, nil)

	group := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}
	if err := runtime.sendNotification(context.Background(), group, "仓库有新动态"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "10001" {
		t.Fatalf("repository notice = %#v", channel.sent)
	}

	// 纯群目标（没记订阅人）没有可 @ 的对象，就老实不 @。
	channel.sent = nil
	anonymous := MessageEvent{Kind: EventKindGroup, GroupID: "123456"}
	if err := runtime.sendNotification(context.Background(), anonymous, "仓库有新动态"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "" {
		t.Fatalf("anonymous group notice = %#v", channel.sent)
	}
}

// 普通聊天回复不受这次改动影响：off 就是一条都不 @。
func TestChatReplyStillFollowsMentionMode(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{MentionUserMode: ReplyDecorationOff}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "42"}

	if err := runtime.send(context.Background(), event, "你好"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "" {
		t.Fatalf("chat reply = %#v", channel.sent)
	}
}

// auto 模式下这条规则要真的进到提示词里，并且带上算出来的人数。
func TestReplyDecorationPromptCarriesMentionCrowd(t *testing.T) {
	now := int64(1_700_000_000)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", MessageID: "m3", Time: now}
	history := []MessageEvent{
		{GroupID: "g", UserID: "2", MessageID: "m1", Time: now - 30},
		{GroupID: "g", UserID: "3", MessageID: "m2", Time: now - 20},
		event,
	}
	cfg := BotConfig{MentionUserMode: ReplyDecorationAuto, ReplyReferenceMode: ReplyDecorationOff}
	prompt := replyDecorationPrompt(cfg, event, history)
	if !strings.Contains(prompt, "2 个人") {
		t.Fatalf("提示词里没有算出来的插话人数：%s", prompt)
	}
	// 关掉和总是 @ 两种模式不需要这条规则，模型不该被告知怎么判断。
	for _, mode := range []ReplyDecorationMode{ReplyDecorationOn, ReplyDecorationOff} {
		off := replyDecorationPrompt(BotConfig{MentionUserMode: mode, ReplyReferenceMode: ReplyDecorationOff}, event, history)
		if strings.Contains(off, "个人在说话") {
			t.Fatalf("mode %s 不该带 @ 判断规则：%s", mode, off)
		}
	}
}

// 两段关于 @ 的提示词不能各说各的。
//
// 「群聊真实提及规则」曾经写死一句「发送层会引用并 @ 当前发言者，这部分不需要
// 你输出 CQ at」——只有 on 档成立。auto 档发送层一个装饰件都不加，另一段却在请
// 模型自己写 @：模型两段都收到，前一段是陈述句、后一段是选择题，于是按前一段
// 办，@ 就消失了。
func TestMentionPromptAgreesWithDecorationMode(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "123456", UserID: "10001",
		MessageID: "m1", ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}},
	}

	cases := []struct {
		mode   ReplyDecorationMode
		want   string
		reject string
	}{
		// on：发送层确实会自己加，这句话成立，模型不该重复输出。
		{mode: ReplyDecorationOn, want: "不需要你输出 CQ at", reject: "发送层不会自动 @ 任何人"},
		// auto：发送层什么都不加，必须说清楚，并把判断交回给另一段规则。
		{mode: ReplyDecorationAuto, want: "按本轮单独给出的那条规则判断", reject: "不需要你输出 CQ at"},
		// off：发送层同样不加，但也没有另一段规则，需要点名就自己写。
		{mode: ReplyDecorationOff, want: "需要点名时自己写", reject: "不需要你输出 CQ at"},
	}
	for _, tc := range cases {
		cfg := BotConfig{BotAccount: "42", MentionUserMode: tc.mode, ReplyReferenceMode: tc.mode}.WithDefaults()
		prompt := runtime.replyMentionPrompt(cfg, event, nil)
		if prompt == "" {
			t.Fatalf("mode %s: 提及规则不该为空", tc.mode)
		}
		if !strings.Contains(prompt, tc.want) {
			t.Fatalf("mode %s 缺少 %q：%s", tc.mode, tc.want, prompt)
		}
		if strings.Contains(prompt, tc.reject) {
			t.Fatalf("mode %s 不该出现 %q：%s", tc.mode, tc.reject, prompt)
		}
	}
}

// 「发送层会取消自动引用和 @」这句只有真的加了才成立。
func TestMentionPromptDropsSendLayerClausesWhenNothingIsAdded(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "123456", UserID: "10001",
		MessageID: "m1", ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}},
	}
	quiet := BotConfig{BotAccount: "42", MentionUserMode: ReplyDecorationAuto, ReplyReferenceMode: ReplyDecorationAuto}.WithDefaults()
	prompt := runtime.replyMentionPrompt(quiet, event, nil)
	for _, unwanted := range []string{"会取消对触发者的自动引用", "自动避免把触发者误当成回应对象"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("auto 档不该承诺发送层的动作 %q：%s", unwanted, prompt)
		}
	}
	// 引用还开着的时候，「会取消自动引用」仍然成立，不能一起删掉。
	mixed := BotConfig{BotAccount: "42", MentionUserMode: ReplyDecorationAuto, ReplyReferenceMode: ReplyDecorationOn}.WithDefaults()
	if !strings.Contains(runtime.replyMentionPrompt(mixed, event, nil), "会取消对触发者的自动引用") {
		t.Fatal("引用仍为 on 时应当保留取消说明")
	}
}

// 机器人上一句就在回这个人时,这一轮是同一段对话的下一句——运行时算得出来,
// 不该让模型从历史文本里认。
func TestBotJustAnsweredSenderDetectsFollowUp(t *testing.T) {
	now := int64(1_700_000_000)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", MessageID: "m3", Time: now}
	history := []MessageEvent{
		{GroupID: "g", UserID: "1", MessageID: "m1", Time: now - 40},
		{GroupID: "g", UserID: "bot", MessageID: "m2", Time: now - 20, Outbound: true},
		event,
	}
	if !botJustAnsweredSender(history, event) {
		t.Fatal("上一条是回这个人的,应当认出是紧接着的下一句")
	}
}

func TestBotJustAnsweredSenderRejectsOtherCases(t *testing.T) {
	now := int64(1_700_000_000)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", MessageID: "m9", Time: now}

	cases := map[string][]MessageEvent{
		// 机器人上一句回的是别人,当前这位没被回过。
		"answered someone else": {
			{GroupID: "g", UserID: "2", MessageID: "m1", Time: now - 40},
			{GroupID: "g", UserID: "bot", MessageID: "m2", Time: now - 20, Outbound: true},
			event,
		},
		// 机器人回完之后别人又说了话,当前这条不再紧挨着那句回复。
		"someone spoke after the reply": {
			{GroupID: "g", UserID: "1", MessageID: "m1", Time: now - 60},
			{GroupID: "g", UserID: "bot", MessageID: "m2", Time: now - 40, Outbound: true},
			{GroupID: "g", UserID: "3", MessageID: "m3", Time: now - 20},
			event,
		},
		// 隔了太久就是重新起了个话头,不算补充。
		"stale reply": {
			{GroupID: "g", UserID: "1", MessageID: "m1", Time: now - int64(botFollowUpWindow.Seconds()) - 60},
			{GroupID: "g", UserID: "bot", MessageID: "m2", Time: now - int64(botFollowUpWindow.Seconds()) - 1, Outbound: true},
			event,
		},
		// 机器人还没开过口。
		"no reply yet": {
			{GroupID: "g", UserID: "1", MessageID: "m1", Time: now - 20},
			event,
		},
	}
	for name, history := range cases {
		if botJustAnsweredSender(history, event) {
			t.Fatalf("%s: 不该判成紧接着的下一句", name)
		}
	}
}

// 刚回过 TA 是唯一答案确定的一档:不该再 @,也不该把上一条讲过的再讲一遍。
func TestReplyDecorationPromptTellsFollowUpNotToRepeat(t *testing.T) {
	now := int64(1_700_000_000)
	cfg := DefaultBotConfig()
	cfg.MentionUserMode = ReplyDecorationAuto
	cfg.ReplyReferenceMode = ReplyDecorationAuto
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", MessageID: "3003", Time: now}
	history := []MessageEvent{
		// 群里还有别人在说话:没有这一档的话,规则会要求点名 @。
		{GroupID: "g", UserID: "2", MessageID: "3000", Time: now - 50},
		{GroupID: "g", UserID: "1", MessageID: "3001", Time: now - 40},
		{GroupID: "g", UserID: "bot", MessageID: "3002", Time: now - 20, Outbound: true},
		event,
	}
	prompt := replyDecorationPrompt(cfg, event, history)
	if !strings.Contains(prompt, "接着上一条往下说") || !strings.Contains(prompt, "不要再重讲一遍") {
		t.Fatalf("应当要求接着上一条说,而不是重讲：%s", prompt)
	}
	if !strings.Contains(prompt, "不要 @ 发送者") {
		t.Fatalf("刚回过 TA 就不该再 @：%s", prompt)
	}
	if strings.Contains(prompt, mentionMarkerFor("1")) {
		t.Fatalf("这一档不该再给出提及标记的写法：%s", prompt)
	}
}
