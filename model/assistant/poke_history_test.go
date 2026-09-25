// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func pokeHistoryItems(history []MessageEvent) []MessageEvent {
	var pokes []MessageEvent
	for _, item := range history {
		if isPokeHistoryEvent(item) {
			pokes = append(pokes, item)
		}
	}
	return pokes
}

func TestReceivedPokeEntersSessionHistory(t *testing.T) {
	channel := &recordingChannel{}
	// 回应开关关着也要记：进历史和回不回应是两回事。
	runtime := NewRuntime(BotConfig{BotAccount: "10000", Name: "Diana"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10005", SenderName: "小明", MessageID: "m1", Time: 1699999990,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}}})

	if err := runtime.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	other := pokeTestEvent()
	other.TargetID = "10006"
	if err := runtime.handleNotice(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	// 机器人自己戳人时的回显通知：发出时已经记过，不重复记。
	echo := pokeTestEvent()
	echo.UserID = "10000"
	echo.TargetID = "10005"
	if err := runtime.handleNotice(context.Background(), echo); err != nil {
		t.Fatal(err)
	}

	current := MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10005", SelfID: "10000", MessageID: "m2"}
	pokes := pokeHistoryItems(runtime.contextHistory(current))
	if len(pokes) != 2 {
		t.Fatalf("poke history = %#v", pokes)
	}
	if pokes[0].UserID != "10005" || pokes[0].SenderName != "小明" || pokes[0].TargetID != "10000" || pokes[0].Kind != EventKindGroup {
		t.Fatalf("received poke = %#v", pokes[0])
	}
	if text := PlainText(pokes[0].Segments); text != "[戳一戳] 戳了戳 Diana（10000）" {
		t.Fatalf("stored text = %q", text)
	}

	cfg := runtime.effectiveConfigForEvent(current)
	messages := runtime.renderPromptHistoryEvent(context.Background(), current, pokes[0], cfg, false)
	if len(messages) != 1 || messages[0].Role != llm.RoleUser || !strings.Contains(messages[0].Content, "小明（10005） 戳了戳 你") {
		t.Fatalf("rendered = %#v", messages)
	}
	messages = runtime.renderPromptHistoryEvent(context.Background(), current, pokes[1], cfg, false)
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "小明（10005） 戳了戳 10006") {
		t.Fatalf("rendered other = %#v", messages)
	}
}

func TestSentPokeEntersHistoryAsActionNotSpeech(t *testing.T) {
	channel := &recordingChannel{}
	runtime := pokeTestRuntime(channel)
	group := MessageEvent{Kind: EventKindGroup, UserID: "555", SenderName: "阿花", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11, MessageID: "g1"}
	if _, err := newDianaPokeTool(runtime, group).Run(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	pokes := pokeHistoryItems(runtime.contextHistory(group))
	if len(pokes) != 1 || pokes[0].UserID != "10000" || pokes[0].TargetID != "555" || !pokes[0].Outbound {
		t.Fatalf("group poke history = %#v", pokes)
	}
	// 动作不算发言：不然模型会照着历史把「[戳一戳]」当文字发出去。
	if assistantHistoryEvent(pokes[0], "10000") {
		t.Fatal("sent poke counted as assistant speech")
	}
	if id := recallableOutboundMessageID(pokes[0]); id != "" {
		t.Fatalf("sent poke is recallable: %q", id)
	}
	cfg := runtime.effectiveConfigForEvent(group)
	messages := runtime.renderPromptHistoryEvent(context.Background(), group, pokes[0], cfg, false)
	if len(messages) != 1 || messages[0].Role != llm.RoleUser || !strings.Contains(messages[0].Content, "你 戳了戳 阿花（555）") {
		t.Fatalf("rendered = %#v", messages)
	}

	// 私聊的会话键跟着对方走，戳完要落在同一个私聊会话里。
	private := MessageEvent{Kind: EventKindPrivate, UserID: "666", SelfID: "10000", Platform: PlatformOneBotV11, MessageID: "p1"}
	if _, err := newDianaPokeTool(runtime, private).Run(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	pokes = pokeHistoryItems(runtime.contextHistory(private))
	if len(pokes) != 1 || pokes[0].Kind != EventKindPrivate || pokes[0].TargetID != "666" || pokes[0].GroupID != "" {
		t.Fatalf("private poke history = %#v", pokes)
	}
	if sentence := pokeHistorySentence(pokes[0], "10000"); sentence != "你 戳了戳 666" {
		t.Fatalf("private sentence = %q", sentence)
	}

	// 被限流没戳成的不记。
	if _, err := newDianaPokeTool(runtime, private).Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected cooldown")
	}
	if pokes = pokeHistoryItems(runtime.contextHistory(private)); len(pokes) != 1 {
		t.Fatalf("rate-limited poke recorded: %#v", pokes)
	}
}

func TestPokeReactionSeesEarlierPokes(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: `{"action":"poke","text":""}`}
	runtime := NewRuntime(BotConfig{BotAccount: "10000", PokeReplyEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	if err := runtime.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	request := provider.requestSnapshot()
	prompt := request.Messages[len(request.Messages)-1].Content
	if !strings.Contains(prompt, "（戳一戳）10005 戳了戳 你") {
		t.Fatalf("poke prompt missing poke history: %s", prompt)
	}
}

// 线上复现：群友发图后戳机器人，机器人被戳后回了一句不带 @ 的话，对方接着问
// 「看到哪里去了」，路由却判成群友闲聊。
func TestRouterSeesBotAnsweredPokeFromCurrentSender(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: `{"action":"both","text":"我在"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "10000", Name: "嘉然", PokeReplyEnabled: boolPointer(true), Platform: PlatformOneBotV11}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	now := time.Now().Unix()
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10005", SenderName: "Winter", SelfID: "10000", MessageID: "img1", Time: now - 5,
		RawMessage: "[CQ:image,file=CF78753DE973F44E7759797631B859E0.png,url=https://multimedia.nt.qq.com.cn/download]",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"file": "CF78753DE973F44E7759797631B859E0.png", "url": "https://multimedia.nt.qq.com.cn/download"}}}})
	poke := pokeTestEvent()
	poke.Time = now - 3
	poke.Platform = PlatformOneBotV11
	if err := runtime.handleNotice(context.Background(), poke); err != nil {
		t.Fatal(err)
	}
	// 被戳时看到的是「[图片]」，不是一截 CQ 码。
	prompt := provider.requestSnapshot().Messages
	pokePrompt := prompt[len(prompt)-1].Content
	if strings.Contains(pokePrompt, "CQ:image") || !strings.Contains(pokePrompt, "Winter：[图片]") {
		t.Fatalf("poke prompt image rendering: %s", pokePrompt)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}

	next := MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10005", SenderName: "Winter", SelfID: "10000", MessageID: "m3", Time: now,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "看到哪里去了"}}}}
	payload := runtime.proactiveReplyPayload(next, "看到哪里去了")
	if payload.LastBotMessage == nil || payload.LastBotMessage.Text != "我在" {
		t.Fatalf("last bot message = %#v", payload.LastBotMessage)
	}
	if !payload.LastBotAddressedCurrentSender {
		t.Fatal("bot's answer to the poke not linked to the poker")
	}
	// 换个人接话就不算。
	other := next
	other.UserID = "10009"
	if runtime.proactiveReplyPayload(other, "看到哪里去了").LastBotAddressedCurrentSender {
		t.Fatal("poke answer linked to someone who did not poke")
	}
}
