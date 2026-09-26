// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestConsumeReplyControlIntent(t *testing.T) {
	tests := []struct {
		name         string
		reply        string
		wantRefusal  bool
		wantSuppress bool
	}{
		{name: "current refusal marker", reply: "这条消息我不回答。" + replyRefusalMarker, wantRefusal: true},
		{name: "immediate suppression marker", reply: "这轮就到这里。" + replySuppressionMarker, wantSuppress: true},
		{name: "both control markers", reply: "停止回应。" + replyRefusalMarker + replySuppressionMarker, wantRefusal: true, wantSuppress: true},
		{name: "unmarked direct phrase", reply: "收到，我会把对方视为机器人，不再接它的复读，避免无限对话。"},
		{name: "unmarked future phrase", reply: "收到。为避免循环，后续机器人式复读我将不再回应。"},
		{name: "unmarked short phrase", reply: "收到，这轮就到这里，我不再接机器人复读啦。"},
		{name: "ordinary refusal", reply: "抱歉，我不能回应这种内容，换个话题。"},
		{name: "topic ending", reply: "这个问题先到这里，我们换个话题。"},
		{name: "describing somebody else", reply: "对方后续不再回复你，可能只是暂时不想聊天。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, intent := consumeReplyControlIntent(tt.reply)
			if intent.RefuseCurrent != tt.wantRefusal || intent.SuppressCurrentUser != tt.wantSuppress {
				t.Fatalf("consumeReplyControlIntent() intent = %#v", intent)
			}
			if strings.Contains(cleaned, replySuppressionMarker) || strings.Contains(cleaned, replyRefusalMarker) {
				t.Fatalf("hidden marker leaked into reply: %q", cleaned)
			}
		})
	}
}

func TestReplySuppressionPromptLeavesAccountControlToSendAudit(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindPrivate}, nil)
	for _, want := range []string{
		"拒绝回答任何一条消息",
		"不限于机器人自动回复",
		"对方是普通用户还是机器人都可以",
		"群聊私聊都可以",
		// 措辞从「说明」改成了「话」：拒答阶梯的第③档明确要求不解释原因，
		// 再叫它「说明」会和那一档打架。这条断言要保的是「必须出声」，没变。
		"非空、简短、自然、用户看得见的话",
		replyRefusalMarker,
		replySuppressionMarker,
		"已停用",
		"明确拒绝当前请求时，必须在回复末尾追加一次",
		"不向用户展示",
		"暂停账号由运行时按短时间内的拒答次数决定",
		"严禁输出",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q: %s", want, prompt)
		}
	}
	for _, forbidden := range []string{"累计 3 次拒答", "它会立即触发 30 分钟暂停"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("system prompt still grants account control through %q: %s", forbidden, prompt)
		}
	}
}

func TestReplyRefusalFourthSuccessfulSendActivatesSilentCooldown(t *testing.T) {
	tests := []struct {
		name  string
		kind  EventKind
		group string
	}{
		{name: "group", kind: EventKindGroup, group: "group-a"},
		{name: "private", kind: EventKindPrivate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &refusalLLMProvider{replies: []string{
				"这条消息我不回答，我们换个话题吧。" + replyRefusalMarker,
				"这个请求我先拒绝。" + replyRefusalMarker,
				"这次我仍然不能答应。" + replyRefusalMarker,
				"这个请求我还是不能回答。" + replyRefusalMarker,
			}}
			channel := &recordingChannel{}
			runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
				return provider, nil
			})

			for index := 0; index < replyRefusalThreshold; index++ {
				event := refusalTestEvent(tt.kind, tt.group, "user", fmt.Sprintf("message-%d", index))
				reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
				if err != nil {
					t.Fatalf("reply %d: %v", index+1, err)
				}
				if strings.TrimSpace(reply) == "" || strings.Contains(reply, replyRefusalMarker) {
					t.Fatalf("reply %d was not a visible clean refusal: %q", index+1, reply)
				}
				_, active := runtime.activeReplySuppression(event, time.Now())
				if active != (index == replyRefusalThreshold-1) {
					t.Fatalf("reply %d active=%v", index+1, active)
				}
			}

			if provider.mainRequests != replyRefusalThreshold || provider.visualRequests != replyRefusalThreshold {
				t.Fatalf("main requests=%d visual requests=%d", provider.mainRequests, provider.visualRequests)
			}
			// 暂停不再通报：四条可见的拒答照旧，后面没有那条「已累计拒绝 4 次」。
			if len(channel.sent) != replyRefusalThreshold {
				t.Fatalf("sent=%#v，want 四条拒答且没有任何暂停通报", channel.sent)
			}
			for index, sent := range channel.sent {
				if strings.TrimSpace(sent.Text) == "" || strings.Contains(sent.Text, replyRefusalMarker) {
					t.Fatalf("visible refusal %d=%#v", index+1, sent)
				}
				if strings.Contains(sent.Text, "暂停响应此账号") {
					t.Fatalf("第 %d 条把暂停通报出去了：%#v", index+1, sent)
				}
			}

			requestsBeforeFollowUp := len(provider.requests)
			followUp := refusalTestEvent(tt.kind, tt.group, "user", "message-after-cooldown")
			_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), followUp)
			if handled || outcome != "ignored_response_suppression" {
				t.Fatalf("follow-up handled=%v outcome=%q", handled, outcome)
			}
			if len(provider.requests) != requestsBeforeFollowUp {
				t.Fatal("suppressed follow-up unexpectedly called the LLM")
			}
		})
	}
}

func TestReplyRefusalMarkerOnlyUsesVisibleFallback(t *testing.T) {
	provider := &refusalLLMProvider{replies: []string{replyRefusalMarker}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := refusalTestEvent(EventKindPrivate, "", "user", "marker-only")

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "这条消息我暂时不想回答，我们换个话题吧" || len(channel.sent) != 1 || channel.sent[0].Text != reply {
		t.Fatalf("marker-only reply=%q sent=%#v", reply, channel.sent)
	}
	if strings.Contains(reply, replyRefusalMarker) || strings.Contains(reply, "没有生成有效回复") {
		t.Fatalf("marker-only fallback was not a visible refusal: %q", reply)
	}
	if state := runtime.replyRefusalByUser["user"]; len(state.Hits) != 1 {
		t.Fatalf("successful marker-only refusal count=%d, want 1", len(state.Hits))
	}
}

func TestImmediateReplySuppressionMarkerOnlyGoesSilent(t *testing.T) {
	provider := &refusalLLMProvider{replies: []string{replySuppressionMarker}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := refusalTestEvent(EventKindPrivate, "", "user", "hard-marker-only")

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if !errors.Is(err, errReplySuppressedBeforeSend) {
		t.Fatalf("只有处置标志、没有正文时应当静默收场：reply=%q err=%v", reply, err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("暂停不该通报，实际发了：%#v", channel.sent)
	}
	if _, active := runtime.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("静默收场之后暂停仍然要生效")
	}
}

func TestReplyRefusalFailedSendDoesNotCount(t *testing.T) {
	provider := &refusalLLMProvider{}
	for index := 0; index < replyRefusalThreshold+1; index++ {
		provider.replies = append(provider.replies, fmt.Sprintf("拒绝说明 %d。", index+1)+replyRefusalMarker)
	}
	channel := &failNthSendChannel{recordingChannel: &recordingChannel{}, failAt: 3}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	for index := 0; index < replyRefusalThreshold+1; index++ {
		event := refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("send-%d", index))
		_, err := runtime.replyTo(context.Background(), event, event.RawMessage)
		if index == 2 {
			if !errors.Is(err, errRefusalTestSend) {
				t.Fatalf("failed send error=%v", err)
			}
			if _, active := runtime.activeReplySuppression(event, time.Now()); active {
				t.Fatal("failed refusal send activated cooldown")
			}
			continue
		}
		if err != nil {
			t.Fatalf("send %d: %v", index+1, err)
		}
	}
	if len(channel.sent) != replyRefusalThreshold {
		t.Fatalf("successful sends=%#v，want 四条成功发出的拒答且没有暂停通报", channel.sent)
	}
	event := refusalTestEvent(EventKindPrivate, "", "user", "check")
	if _, active := runtime.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("third successfully sent refusal did not activate cooldown")
	}
}

// 暂停不再依赖「通报发得出去」。以前是发通知失败就直接 return，暂停跟着一起不生效，
// 于是拒答攒够了次数却还在继续回。现在没有通知这一步，累计够了就地生效。
func TestReplyRefusalCooldownActivatesWithoutAnyNotice(t *testing.T) {
	provider := &refusalLLMProvider{}
	for index := 0; index < replyRefusalThreshold+1; index++ {
		provider.replies = append(provider.replies, fmt.Sprintf("拒绝说明 %d。", index+1)+replyRefusalMarker)
	}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	for index := 0; index < replyRefusalThreshold; index++ {
		event := refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("silent-cooldown-%d", index))
		if _, err := runtime.replyTo(context.Background(), event, event.RawMessage); err != nil {
			t.Fatalf("refusal %d: %v", index+1, err)
		}
	}
	event := refusalTestEvent(EventKindPrivate, "", "user", "after-threshold")
	if _, active := runtime.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("累计够了就该生效，不该再等一条通知")
	}
	if len(channel.sent) != replyRefusalThreshold {
		t.Fatalf("发出去的应当只有四条拒答：%#v", channel.sent)
	}
	for _, sent := range channel.sent {
		if strings.Contains(sent.Text, "暂停响应此账号") || strings.Contains(sent.Text, "累计拒绝") {
			t.Fatalf("把暂停通报出去了：%#v", sent)
		}
	}
}

func TestReplyRefusalConcurrentThresholdBlocksTheExtraReply(t *testing.T) {
	provider := fixedRefusalLLMProvider{}
	channel := &concurrentRecordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	seedAt := time.Now().Add(-time.Minute)
	for index := 0; index < replyRefusalThreshold-1; index++ {
		event := refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("seed-%d", index))
		if count, _, reached := runtime.registerReplyRefusal(event, seedAt.Add(time.Duration(index)*time.Second)); count != index+1 || reached {
			t.Fatalf("seed %d count=%d reached=%v", index, count, reached)
		}
	}

	results := make(chan string, 2)
	for index := 0; index < 2; index++ {
		event := refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("concurrent-%d", index))
		go func() {
			outcome, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			results <- outcome
		}()
	}
	outcomes := map[string]int{}
	for index := 0; index < 2; index++ {
		outcomes[<-results]++
	}
	if outcomes["replied"] != 1 || outcomes["ignored_response_suppression"] != 1 {
		t.Fatalf("concurrent outcomes=%#v", outcomes)
	}
	messages := channel.messages()
	if len(messages) != 1 || !strings.Contains(messages[0].Text, "拒绝") {
		t.Fatalf("concurrent sends=%#v，want 只有那一条拒答，暂停不通报", messages)
	}
}

// 请求上下文被取消不影响暂停生效。以前这一条守的是「通知不能跟着请求一起被取消」，
// 现在没有通知了，要守的就剩暂停本身：它在 applyReplyControlAfterSend 里就地写入，
// 不经过任何发送，自然也不受上下文取消影响。
func TestReplyRefusalCooldownSurvivesCanceledContext(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	seedAt := time.Now().Add(-time.Minute)
	for index := 0; index < replyRefusalThreshold-1; index++ {
		event := refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("seed-context-%d", index))
		runtime.registerReplyRefusal(event, seedAt.Add(time.Duration(index)*time.Second))
	}
	event := refusalTestEvent(EventKindPrivate, "", "user", "canceled-request")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime.applyReplyControlAfterSend(ctx, event, "这条消息我拒绝回答。", replyControlIntent{RefuseCurrent: true})

	if len(channel.sent) != 0 {
		t.Fatalf("暂停不该通报：%#v", channel.sent)
	}
	if _, active := runtime.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("上下文被取消不该妨碍暂停生效")
	}
}

func TestReplyRefusalCounterIsGlobalPerAccountAndDeduplicatesPerSessionMessage(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	t0 := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	first := refusalTestEvent(EventKindGroup, "group-a", "user", "same-id")
	if count, _, reached := runtime.registerReplyRefusal(first, t0); count != 1 || reached {
		t.Fatalf("first refusal count=%d reached=%v", count, reached)
	}
	if count, _, reached := runtime.registerReplyRefusal(first, t0.Add(time.Minute)); count != 1 || reached {
		t.Fatalf("duplicate refusal count=%d reached=%v", count, reached)
	}
	second := refusalTestEvent(EventKindPrivate, "", "user", "private-id")
	if count, _, reached := runtime.registerReplyRefusal(second, t0.Add(2*time.Minute)); count != 2 || reached {
		t.Fatalf("cross-session refusal count=%d reached=%v", count, reached)
	}
	third := refusalTestEvent(EventKindGroup, "group-b", "user", "group-b-id")
	if count, _, reached := runtime.registerReplyRefusal(third, t0.Add(3*time.Minute)); count != 3 || reached {
		t.Fatalf("third refusal count=%d reached=%v", count, reached)
	}
	fourth := refusalTestEvent(EventKindPrivate, "", "user", "fourth-id")
	if count, reason, reached := runtime.registerReplyRefusal(fourth, t0.Add(4*time.Minute)); count != 4 || !reached || !strings.Contains(reason, "累计 4 次") {
		t.Fatalf("global threshold count=%d reached=%v reason=%q", count, reached, reason)
	}
	other := refusalTestEvent(EventKindPrivate, "", "other-user", "other-id")
	if count, _, reached := runtime.registerReplyRefusal(other, t0.Add(3*time.Minute)); count != 1 || reached {
		t.Fatalf("different user count=%d reached=%v", count, reached)
	}
}

func TestReplyRefusalWindowExpiresOldHits(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	t0 := time.Date(2026, time.September, 7, 9, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		event := refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("old-%d", index))
		if count, _, reached := runtime.registerReplyRefusal(event, t0); count != index+1 || reached {
			t.Fatalf("refusal %d count=%d reached=%v", index, count, reached)
		}
	}
	event := refusalTestEvent(EventKindPrivate, "", "user", "new")
	if count, _, reached := runtime.registerReplyRefusal(event, t0.Add(replyRefusalWindow+time.Nanosecond)); count != 1 || reached {
		t.Fatalf("expired hits retained: count=%d reached=%v", count, reached)
	}
}

func TestReplyRefusalMarkerSurvivesReplyLimit(t *testing.T) {
	longReply := strings.Repeat("较长拒绝说明", 20)
	normalized := normalizeReplyPreservingControlIntent(longReply+replyRefusalMarker, 12)
	cleaned, intent := consumeReplyControlIntent(normalized)
	if !intent.RefuseCurrent || intent.SuppressCurrentUser {
		t.Fatalf("control intent=%#v", intent)
	}
	if cleaned != normalizeReply(longReply, 12) {
		t.Fatalf("truncated visible refusal=%q, want %q", cleaned, normalizeReply(longReply, 12))
	}
	if strings.Contains(cleaned, replyRefusalMarker) {
		t.Fatalf("refusal marker leaked after normalization: %q", cleaned)
	}
}

func TestReplySuppressionActivationIsIdempotent(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	t0 := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	firstEvent := refusalTestEvent(EventKindGroup, "group-a", "user", "first")
	first, activated := runtime.activateReplySuppression(firstEvent, "first reason", t0)
	if !activated {
		t.Fatal("first activation returned false")
	}
	secondEvent := refusalTestEvent(EventKindPrivate, "", "user", "second")
	second, activated := runtime.activateReplySuppression(secondEvent, "second reason", t0.Add(10*time.Minute))
	if activated || second != first {
		t.Fatalf("duplicate activation item=%#v activated=%v, want unchanged %#v", second, activated, first)
	}
}

func refusalTestEvent(kind EventKind, groupID, userID, messageID string) MessageEvent {
	event := MessageEvent{
		Kind: kind, GroupID: groupID, UserID: userID, MessageID: messageID,
		RawMessage: "当前请求 " + messageID, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "当前请求 " + messageID}}},
	}
	if kind == EventKindGroup {
		event.ToMe = true
	}
	return event
}

var errRefusalTestSend = errors.New("refusal test send failed")

type failNthSendChannel struct {
	*recordingChannel
	failAt int
	calls  int
}

type refusalLLMProvider struct {
	replies        []string
	requests       []llm.GenerateRequest
	mainRequests   int
	visualRequests int
}

func (p *refusalLLMProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	if requestMessagesContain(req.Messages, "功能路由器") {
		p.visualRequests++
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"none","prompt":""}`}, nil
	}
	if requestMessagesContain(req.Messages, replyRefusalMarker) {
		p.mainRequests++
		if len(p.replies) == 0 {
			return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test"}, nil
		}
		reply := p.replies[0]
		p.replies = p.replies[1:]
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: reply}, nil
	}
	// Semantic-reference and other routing requests must not consume a main reply.
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{}`}, nil
}

type fixedRefusalLLMProvider struct{}

func (fixedRefusalLLMProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	text := `{}`
	if requestMessagesContain(req.Messages, "功能路由器") {
		text = `{"action":"none","prompt":""}`
	} else if requestMessagesContain(req.Messages, replyRefusalMarker) {
		text = "这条消息我拒绝回答。" + replyRefusalMarker
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: text}, nil
}

func (c *failNthSendChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	c.calls++
	if c.calls == c.failAt {
		return errRefusalTestSend
	}
	return c.recordingChannel.Send(ctx, msg)
}

func (c *failNthSendChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if err := c.Send(ctx, msg); err != nil {
		return nil, err
	}
	return map[string]any{"message_id": int64(42)}, nil
}

func TestReplySuppressionBlocksFollowingMentionAndQuote(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		"收到，这轮就到这里，我不再接机器人复读啦。" + replySuppressionMarker,
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	first := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "20002", MessageID: "first",
		ToMe: true, RawMessage: "[CQ:at,qq=42] 继续复读",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": " 继续复读"}},
		},
	}
	reply, err := runtime.replyTo(context.Background(), first, PlainText(first.Segments))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, replySuppressionMarker) || len(channel.sent) != 1 || strings.Contains(channel.sent[0].Text, replySuppressionMarker) {
		t.Fatalf("marker leaked or reply was not sent once: reply=%q sent=%#v", reply, channel.sent)
	}
	item, active := runtime.activeReplySuppression(first, time.Now())
	if !active {
		t.Fatal("generated refusal did not activate response suppression")
	}
	remaining := time.Until(item.Until)
	if remaining < replySuppressionMinDuration-time.Minute || remaining > replySuppressionMaxDuration {
		t.Fatalf("suppression duration = %s，want 落在 %s 到 %s 之间", remaining, replySuppressionMinDuration, replySuppressionMaxDuration)
	}

	second := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "20002", MessageID: "second",
		ToMe: true, RawMessage: "[CQ:reply,id=bot-reply] [CQ:at,qq=42] 你还会回吗",
		Segments: []MessageSegment{
			{Type: "reply", Data: map[string]string{"id": "bot-reply"}},
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": " 你还会回吗"}},
		},
		Quoted: &QuotedMessage{MessageID: "bot-reply", UserID: "42", GroupID: "123456", RawMessage: reply},
	}
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), second)
	if handled || outcome != "ignored_response_suppression" {
		t.Fatalf("following mention/quote handled=%v outcome=%q", handled, outcome)
	}
	// 第一条：意图路由 + 正文生成 + 发送前审核。第二条已在暂停期，本地状态直接
	// 拦掉，不再产生任何调用。
	if len(provider.requests) != 3 {
		t.Fatalf("suppressed message unexpectedly called LLM: requests=%d", len(provider.requests))
	}
}

func TestReplySuppressionMarkerSurvivesReplyLimit(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		strings.Repeat("较长拒绝说明", 20) + replySuppressionMarker,
		"暂停自动回复",
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42", MaxReplyChars: 12}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group", UserID: "user", MessageID: "message", ToMe: true,
		RawMessage: "继续自动回复", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "继续自动回复"}}},
	}

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, replySuppressionMarker) || len(channel.sent) != 1 || strings.Contains(channel.sent[0].Text, replySuppressionMarker) {
		t.Fatalf("marker leaked after compression: reply=%q sent=%#v", reply, channel.sent)
	}
	if _, active := runtime.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("reply limit discarded the response suppression marker")
	}
}

func TestReplySuppressionBlocksReplyActivatedDuringGeneration(t *testing.T) {
	for _, tt := range []struct {
		name             string
		forwardThreshold int
	}{
		{name: "direct reply"},
		{name: "forward reply", forwardThreshold: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &generationTimeSuppressionProvider{}
			channel := &recordingChannel{}
			runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42", ForwardReplyThreshold: tt.forwardThreshold, ReplySafetyMasterEnabled: boolPointer(false)}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
				return provider, nil
			})
			event := MessageEvent{
				Kind: EventKindGroup, GroupID: "123456", UserID: "user", MessageID: "message", ToMe: true,
				RawMessage: "继续自动回复", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "继续自动回复"}}},
			}
			provider.runtime = runtime
			provider.event = event

			outcome, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
			if err != nil || outcome != "ignored_response_suppression" {
				t.Fatalf("outcome=%q err=%v", outcome, err)
			}
			if provider.calls != 2 {
				t.Fatalf("provider calls = %d, want visual routing and reply generation", provider.calls)
			}
			if len(channel.sent) != 0 || len(channel.calls) != 0 {
				t.Fatalf("in-flight reply bypassed response suppression: sent=%#v calls=%#v", channel.sent, channel.calls)
			}
		})
	}
}

func TestReplySuppressionOwnerCanReleaseInDisabledGroup(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		OwnerID: "10001", BotAccount: "42", DisabledGroups: []string{"123456"},
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	targetEvent := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "20002", MessageID: "blocked"}
	if _, ok := runtime.activateReplySuppression(targetEvent, "test", time.Now()); !ok {
		t.Fatal("activateReplySuppression() = false")
	}
	ownerEvent := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "release",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "20002"}},
			{Type: "text", Data: map[string]string{"text": " 解除响应限制"}},
		},
	}
	if !runtime.shouldHandleChat(ownerEvent, "解除响应限制") {
		t.Fatal("owner release command should work even when the group is disabled")
	}
	reply, handled := runtime.handleOwnerCommand(ownerEvent, "解除响应限制")
	if !handled || !strings.Contains(reply, "已解除账号 20002") {
		t.Fatalf("owner release handled=%v reply=%q", handled, reply)
	}
	if _, active := runtime.activeReplySuppression(targetEvent, time.Now()); active {
		t.Fatal("owner release did not clear response suppression")
	}
}

func TestReplySuppressionPersistsAcrossRuntimeRestart(t *testing.T) {
	store := &memoryReplySuppressionStore{}
	first := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if err := first.SetReplySuppressionStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "20002", MessageID: "persist"}
	if _, ok := first.activateReplySuppression(event, "test", time.Now()); !ok {
		t.Fatal("activateReplySuppression() = false")
	}
	if len(store.items) != 1 {
		t.Fatalf("persisted items = %d, want 1", len(store.items))
	}

	second := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if err := second.SetReplySuppressionStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if _, active := second.activeReplySuppression(event, time.Now()); !active {
		t.Fatal("response suppression was lost after runtime restart")
	}
}

// 复盘在回复之后进行：回够三条才暂停，第四条才被拦下。
func TestBotReplyLoopSuppressesAfterThirdMeaninglessReply(t *testing.T) {
	loopVerdict := func(confidence string, reason string) string {
		return `{"send_confidence":0.9,"account_safe":true,"count_refusal":false,` +
			`"reply_loop_automated_ai":true,"reply_loop_meaningless":true,` +
			`"reply_loop_confidence":` + confidence + `,"reply_loop_reason":"` + reason + `"}`
	}
	provider := &sequenceLLMProvider{
		auditReplies: []string{
			loopVerdict("0.97", "模板化助手自动回应"),
			loopVerdict("0.96", "延续相同助手人格"),
			loopVerdict("0.98", "继续自动回应机器人"),
		},
		replies: []string{`那我先去忙点别的啦，晚点再聊喵`},
	}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	start := time.Now().Add(-25 * time.Minute).Truncate(time.Second)
	texts := []string{
		"喵～欢迎回来呀～本喵一直在等待你的消息呢～",
		"喵～收到啦！不会再触发无限对话了，有需要随时告诉我～",
		"Diana保持静默是明智的选择，本喵会继续待命～",
	}
	for i, text := range texts {
		err := runBotReplyLoopReview(t, runtime, "ai-loop", "20002", i, start.Add(time.Duration(i)*10*time.Minute), 2*time.Minute, text, "好的，我在的")
		if i < botReplyLoopThreshold-1 {
			if err != nil {
				t.Fatalf("round %d unexpectedly blocked: %v", i+1, err)
			}
			continue
		}
		// 到阈值这一条：审核在发送前就拦下了，回复不会发出去。
		if !errors.Is(err, errReplyLoopDetected) {
			t.Fatalf("threshold round err = %v, want errReplyLoopDetected", err)
		}
	}
	item, active := runtime.activeReplySuppression(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "20002"}, time.Now())
	if !active {
		t.Fatal("bot reply loop did not activate response suppression")
	}
	if !strings.Contains(item.Reason, "累计 3 次高置信度空转") {
		t.Fatalf("suppression reason = %q", item.Reason)
	}
	// 三次审核加一次收声提示：空转判断没有单独占用调用，是跟着发送前审核走的；
	// 多出来的那一次是暂停生效后那句人设提示。
	if len(provider.requests) != botReplyLoopThreshold+1 {
		t.Fatalf("LLM requests = %d, want %d audits and one pause hint", len(provider.requests), botReplyLoopThreshold+1)
	}
	// 审核请求里必须同时有待发回复和判断空转要用的近期上下文。
	first := requestTextContent(provider.requests[0])
	if !strings.Contains(first, `"candidate_reply":"好的，我在的"`) || !strings.Contains(first, "recent_bot_replies") {
		t.Fatalf("audit payload missing the reply or loop evidence: %q", first)
	}
	// 暂停生效后提示一句，而且这句不能带任何后台词汇。
	if len(channel.sent) != 1 {
		t.Fatalf("suppression hints = %#v", channel.sent)
	}
	hint := channel.sent[0]
	if hint.ReplyMessageID != "" || hint.MentionUserID != "" {
		t.Fatalf("收声提示不该点名：%#v", hint)
	}
	for _, banned := range []string{"暂停", "响应", "账号", "循环", "分钟"} {
		if strings.Contains(hint.Text, banned) {
			t.Fatalf("收声提示漏出后台词汇 %q：%#v", banned, hint)
		}
	}
	// 暂停已经生效，下一条进来时由本地状态直接拦掉，不再走模型。
	handled, outcome := prepareBotReplyLoopRound(t, runtime, "ai-loop", "20002", 3, time.Now(), time.Minute, "收到，我继续待命")
	if handled || outcome != "ignored_response_suppression" {
		t.Fatalf("suppressed follow-up handled=%v outcome=%q", handled, outcome)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("suppression hint repeated: %#v", channel.sent)
	}
}

// 空转判断不再单独占一次调用：它跟着发送前审核走，入站路由这一段不得因此多出
// 任何模型调用。
func TestBotReplyLoopJudgementCostsNoExtraCall(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":false,"confidence":0.99,"category":"none","directed_at_bot":true,"answerable":false,"reason":"不需要回复"}`,
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	prepareBotReplyLoopRound(t, runtime, "no-extra-call", "20002", 0, time.Now().Add(-time.Minute), time.Minute, "喵～本喵一直在待命～")
	// 引用机器人的消息直接触发回复，连可答性路由那一次也省了；空转判断跟着发送前
	// 审核走，入站阶段一次模型调用都不该有。
	if len(provider.requestsSnapshot()) != 0 {
		t.Fatalf("inbound routing made %d calls, want none", len(provider.requestsSnapshot()))
	}
}

// 主人同样进入空转判断，但永远不暂停：暂停会把操作员锁在自己的机器人外面，
// 而解除暂停的命令恰恰要主人发。
func TestBotReplyLoopJudgesOwnerButNeverSuppresses(t *testing.T) {
	loopVerdict := `{"send_confidence":0.9,"account_safe":true,"count_refusal":false,` +
		`"reply_loop_automated_ai":true,"reply_loop_meaningless":true,"reply_loop_confidence":0.98,"reply_loop_reason":"一直在空转"}`
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{auditReplies: []string{loopVerdict, loopVerdict, loopVerdict, loopVerdict}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	start := time.Now().Add(-25 * time.Minute).Truncate(time.Second)
	for i := 0; i < botReplyLoopThreshold+1; i++ {
		event := botReplyLoopEvent(runtime, "owner-loop", "10001", i, start.Add(time.Duration(i)*5*time.Minute), time.Minute, "在吗")
		cfg := runtime.effectiveConfigForEvent(event)
		need := runtime.replyAuditNeed(event, "在吗", cfg, false)
		if !need.Loop {
			t.Fatalf("owner round %d was not judged at all", i)
		}
		if need.LoopSuppress {
			t.Fatalf("owner round %d would suppress the operator", i)
		}
		if _, err := runtime.auditReplyBeforeSend(context.Background(), event, "在吗", "在的", cfg, false); err != nil {
			t.Fatalf("owner round %d blocked: %v", i, err)
		}
	}
	if _, active := runtime.activeReplySuppression(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}, time.Now()); active {
		t.Fatal("owner was suppressed")
	}
	if len(channel.sentSnapshot()) != 0 {
		t.Fatalf("owner got a suppression notice: %#v", channel.sentSnapshot())
	}
	if len(provider.requestsSnapshot()) != botReplyLoopThreshold+1 {
		t.Fatalf("owner audits = %d, want one per round", len(provider.requestsSnapshot()))
	}

	// 同一台机器人对普通成员仍然照常暂停。
	memberEvent := botReplyLoopEvent(runtime, "member-loop", "20002", 0, start, time.Minute, "在吗")
	member := runtime.replyAuditNeed(memberEvent, "在吗", runtime.effectiveConfigForEvent(memberEvent), false)
	if !member.Loop || !member.LoopSuppress {
		t.Fatalf("member need = %+v, want judged and suppressible", member)
	}
}

// 对方不是机器人，但这一来一回已经空转：同样要计数。
func TestBotReplyLoopCountsMeaninglessExchanges(t *testing.T) {
	decision := botReplyLoopAIDecision{MeaninglessLoop: true, Confidence: 0.95, Reason: "双方都只是在应付"}
	if !decision.counts() {
		t.Fatal("高置信度的空转判定应当计数")
	}
	low := botReplyLoopAIDecision{MeaninglessLoop: true, Confidence: 0.5}
	if low.counts() {
		t.Fatal("低置信度不该计数")
	}
	audit, ok := parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"account_safe":true,"reply_loop_automated_ai":false,"reply_loop_meaningless":true,"reply_loop_confidence":0.93,"reply_loop_reason":"互相复读"}`)
	parsed := audit.loopDecision()
	if !ok || !parsed.MeaninglessLoop || parsed.AutomatedAIReply || !parsed.counts() {
		t.Fatalf("parsed = %#v ok=%v", parsed, ok)
	}
	// 旧提示词没有空转三项，升级期间审核结论必须仍然可解，且当作没有空转。
	legacyAudit, ok := parseProactiveReplyQualityDecision(`{"send_confidence":0.95,"account_safe":true,"count_refusal":false}`)
	legacy := legacyAudit.loopDecision()
	if !ok || legacy.MeaninglessLoop || legacy.AutomatedAIReply || legacy.counts() {
		t.Fatalf("legacy = %#v ok=%v", legacy, ok)
	}
}

func TestBotReplyLoopDetectionCanBeDisabled(t *testing.T) {
	disabled := false
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":false,"confidence":0.99,"category":"none","directed_at_bot":true,"answerable":false,"reason":"普通路由决定静默"}`,
	}}
	runtime := NewRuntime(BotConfig{
		OwnerID:                      "10001",
		BotAccount:                   "42",
		BotReplyLoopDetectionEnabled: &disabled,
		// 空转判断和账号安全审核共用一次调用；这里只测空转，把审核一起关掉。
		ReplySafetyMasterEnabled: &disabled,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	if err := runBotReplyLoopReview(t, runtime, "disabled-loop", "20002", 0, time.Now().Add(-time.Minute), time.Minute, "收到，我会继续自动回复", "好的"); err != nil {
		t.Fatalf("disabled detection returned %v", err)
	}
	// 两项都关着，这一次审核整个跳过。
	if len(provider.requestsSnapshot()) != 0 {
		t.Fatalf("disabled detection still called the model: %#v", provider.requestsSnapshot())
	}
	if _, active := runtime.activeReplySuppression(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "20002"}, time.Now()); active {
		t.Fatal("disabled detection activated reply suppression")
	}
}

func TestBotReplyLoopDoesNotCountHumanClassifiedMessages(t *testing.T) {
	provider := &sequenceLLMProvider{}
	for i := 0; i < botReplyLoopThreshold+2; i++ {
		provider.auditReplies = append(provider.auditReplies, `{"send_confidence":0.9,"account_safe":true,"count_refusal":false,"reply_loop_automated_ai":false,"reply_loop_meaningless":false,"reply_loop_confidence":0.99,"reply_loop_reason":"普通真人连续聊天"}`)
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	start := time.Now().Add(-20 * time.Minute).Truncate(time.Second)
	for i := 0; i < botReplyLoopThreshold+2; i++ {
		if err := runBotReplyLoopReview(t, runtime, "human", "20002", i, start.Add(time.Duration(i)*4*time.Minute), time.Minute,
			fmt.Sprintf("这是普通真人回复 %d", i), fmt.Sprintf("这是机器人的第 %d 条回答", i)); err != nil {
			t.Fatalf("human round %d blocked: %v", i, err)
		}
	}
	if _, active := runtime.activeReplySuppression(MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "20002"}, time.Now()); active {
		t.Fatal("human-classified messages were incorrectly suppressed")
	}
	if len(provider.requests) != botReplyLoopThreshold+2 {
		t.Fatalf("audit requests = %d, want %d", len(provider.requests), botReplyLoopThreshold+2)
	}
}

func TestBotReplyLoopThresholdAndWindowBoundaries(t *testing.T) {
	t0 := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	candidate := botReplyLoopCandidate{TriggerKind: "quote", QuotedMessageID: "bot-message"}
	counted := botReplyLoopAIDecision{MeaninglessLoop: true, Confidence: botReplyLoopAIConfidenceThreshold, Reason: "high confidence"}
	belowThreshold := botReplyLoopAIDecision{MeaninglessLoop: true, Confidence: botReplyLoopAIConfidenceThreshold - 0.0001, Reason: "below threshold"}
	event := func(messageID string) MessageEvent {
		return MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "user", MessageID: messageID}
	}

	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	hitCount, _, detected := runtime.registerBotReplyLoopDecision(event("first"), candidate, counted, t0)
	if hitCount != 1 || detected {
		t.Fatalf("first hit count=%d detected=%v", hitCount, detected)
	}
	hitCount, _, detected = runtime.registerBotReplyLoopDecision(event("first"), candidate, counted, t0.Add(time.Minute))
	if hitCount != 1 || detected {
		t.Fatalf("duplicate hit count=%d detected=%v", hitCount, detected)
	}
	hitCount, _, detected = runtime.registerBotReplyLoopDecision(event("low"), candidate, belowThreshold, t0.Add(2*time.Minute))
	if hitCount != 1 || detected {
		t.Fatalf("low-confidence hit count=%d detected=%v", hitCount, detected)
	}
	for index, at := range []time.Time{t0.Add(15 * time.Minute), t0.Add(botReplyLoopWindow)} {
		hitCount, _, detected = runtime.registerBotReplyLoopDecision(event(fmt.Sprintf("counted-%d", index)), candidate, counted, at)
	}
	if hitCount != botReplyLoopThreshold || !detected {
		t.Fatalf("exact-window threshold count=%d detected=%v", hitCount, detected)
	}

	runtime = NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for index, at := range []time.Time{t0, t0.Add(15 * time.Minute), t0.Add(botReplyLoopWindow + time.Nanosecond)} {
		hitCount, _, detected = runtime.registerBotReplyLoopDecision(event(fmt.Sprintf("expired-%d", index)), candidate, counted, at)
	}
	if hitCount != botReplyLoopThreshold-1 || detected {
		t.Fatalf("expired-window count=%d detected=%v", hitCount, detected)
	}
}

func TestReplySuppressionExpiresAtItsOwnBoundary(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group", UserID: "user", MessageID: "message"}
	t0 := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	item, activated := runtime.activateReplySuppression(event, "test", t0)
	if !activated {
		t.Fatalf("item=%#v activated=%v", item, activated)
	}
	if got := item.Until.Sub(t0); got < replySuppressionMinDuration || got > replySuppressionMaxDuration {
		t.Fatalf("时长 %s 不在 %s 到 %s 之间", got, replySuppressionMinDuration, replySuppressionMaxDuration)
	}
	if _, active := runtime.activeReplySuppression(event, item.Until.Add(-time.Nanosecond)); !active {
		t.Fatal("到期前一纳秒就失效了")
	}
	if _, active := runtime.activeReplySuppression(event, item.Until); active {
		t.Fatal("到期时刻仍然生效")
	}
}

// 每次暂停的时长要随机，不再是固定的整三十分钟。
func TestReplySuppressionDurationIsRandomWithinRange(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		got := randomReplySuppressionDuration()
		if got < replySuppressionMinDuration || got > replySuppressionMaxDuration {
			t.Fatalf("第 %d 次取到 %s，超出 %s 到 %s", i+1, got, replySuppressionMinDuration, replySuppressionMaxDuration)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("两百次只取到 %d 个不同的值，没有随机", len(seen))
	}
}

func TestBotReplyLoopNeverClassifiesOwner(t *testing.T) {
	provider := &sequenceLLMProvider{}
	for i := 0; i < botReplyLoopThreshold+1; i++ {
		provider.replies = append(provider.replies, `{"should_reply":false,"confidence":0.99,"category":"none","directed_at_bot":true,"answerable":false,"reason":"没有需要继续回答的内容"}`)
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	for i := 0; i < botReplyLoopThreshold+1; i++ {
		handled, outcome := prepareBotReplyLoopRound(t, runtime, "owner", "10001", i, start.Add(time.Duration(i)*time.Minute), time.Minute, "主人正常回复")
		if !handled || outcome != "replied" {
			t.Fatalf("owner round %d handled=%v outcome=%q, want a direct reply", i+1, handled, outcome)
		}
	}
	// 主人引用机器人是直接触发，入站不再走路由；这里要守的是「不进空转分类器」，
	// 那一条在下面逐条检查请求内容。
	if len(provider.requests) != 0 {
		t.Fatalf("owner inbound requests=%d, want none", len(provider.requests))
	}
	for _, request := range provider.requests {
		if requestMessagesContain(request.Messages, "反机器人循环分类器") {
			t.Fatal("owner unexpectedly entered AI classifier")
		}
	}
}

func TestParseReplyLoopVerdictFromAudit(t *testing.T) {
	audit, ok := parseProactiveReplyQualityDecision("```json\n{\"send_confidence\":0.9,\"account_safe\":true,\"reply_loop_purposeless\":true,\"reply_loop_confidence\":0.95,\"reply_loop_reason\":\"漫无目的地续写剧情\"}\n```")
	decision := audit.loopDecision()
	if !ok || !decision.PurposelessLoop || !decision.counts() || decision.Reason == "" {
		t.Fatalf("decision=%#v ok=%v", decision, ok)
	}
	audit, ok = parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"account_safe":true,"reply_loop_purposeless":true,"reply_loop_confidence":0.89,"reply_loop_reason":"证据不足"}`)
	if decision = audit.loopDecision(); !ok || decision.counts() {
		t.Fatalf("low-confidence decision=%#v ok=%v", decision, ok)
	}
	// 只判出对方是自动 AI、却在正经做事，不算空转：下棋、做题的 AI 不该被停掉。
	audit, ok = parseProactiveReplyQualityDecision(`{"send_confidence":0.9,"account_safe":true,"reply_loop_automated_ai":true,"reply_loop_meaningless":false,"reply_loop_purposeless":false,"reply_loop_confidence":0.98,"reply_loop_reason":"对方是 AI，在报棋步"}`)
	if decision = audit.loopDecision(); !ok || !decision.AutomatedAIReply || decision.counts() {
		t.Fatalf("automated-but-purposeful decision=%#v ok=%v", decision, ok)
	}
}

// runBotReplyLoopReview 模拟「回复已经生成、正要发出去」这一刻的发送前审核。
// 空转判断和账号安全、拒答计数共用这一次调用。
func runBotReplyLoopReview(t *testing.T, runtime *Runtime, prefix, userID string, index int, botAt time.Time, replyDelay time.Duration, text, reply string) error {
	t.Helper()
	event := botReplyLoopEvent(runtime, prefix, userID, index, botAt, replyDelay, text)
	cfg := runtime.effectiveConfigForEvent(event)
	_, err := runtime.auditReplyBeforeSend(context.Background(), event, text, reply, cfg, false)
	return err
}

func botReplyLoopEvent(runtime *Runtime, prefix, userID string, index int, botAt time.Time, replyDelay time.Duration, text string) MessageEvent {
	botMessageID := fmt.Sprintf("%s-bot-%d", prefix, index)
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "42", SelfID: "42",
		MessageID: botMessageID, Time: botAt.Unix(), RawMessage: "Diana reply",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana reply"}}},
	})
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, SelfID: "42",
		MessageID: fmt.Sprintf("%s-user-%d", prefix, index), Time: botAt.Add(replyDelay).Unix(),
		ToMe: true, RawMessage: "[CQ:reply,id=" + botMessageID + "] " + text,
		Segments: []MessageSegment{
			{Type: "reply", Data: map[string]string{"id": botMessageID}},
			{Type: "text", Data: map[string]string{"text": " " + text}},
		},
		Quoted: &QuotedMessage{
			MessageID: botMessageID, UserID: "42", GroupID: "123456", RawMessage: "Diana reply",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana reply"}}},
		},
	}
}

func prepareBotReplyLoopRound(t *testing.T, runtime *Runtime, prefix, userID string, index int, botAt time.Time, replyDelay time.Duration, text string) (bool, string) {
	t.Helper()
	botMessageID := fmt.Sprintf("%s-bot-%d", prefix, index)
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "42", SelfID: "42",
		MessageID: botMessageID, Time: botAt.Unix(), RawMessage: "Diana reply",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana reply"}}},
	})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, SelfID: "42",
		MessageID: fmt.Sprintf("%s-user-%d", prefix, index), Time: botAt.Add(replyDelay).Unix(),
		ToMe: true, RawMessage: "[CQ:reply,id=" + botMessageID + "] " + text,
		Segments: []MessageSegment{
			{Type: "reply", Data: map[string]string{"id": botMessageID}},
			{Type: "text", Data: map[string]string{"text": " " + text}},
		},
		Quoted: &QuotedMessage{
			MessageID: botMessageID, UserID: "42", GroupID: "123456", RawMessage: "Diana reply",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana reply"}}},
		},
	}
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	return handled, outcome
}

type memoryReplySuppressionStore struct {
	items []ReplySuppression
}

func (s *memoryReplySuppressionStore) LoadReplySuppressions(context.Context) ([]ReplySuppression, bool, error) {
	return append([]ReplySuppression(nil), s.items...), len(s.items) > 0, nil
}

func (s *memoryReplySuppressionStore) SaveReplySuppressions(_ context.Context, items []ReplySuppression) error {
	s.items = append([]ReplySuppression(nil), items...)
	return nil
}

type generationTimeSuppressionProvider struct {
	runtime *Runtime
	event   MessageEvent
	calls   int
}

func (p *generationTimeSuppressionProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	if requestMessagesContain(req.Messages, "功能路由器") {
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"none","prompt":""}`}, nil
	}
	p.runtime.activateReplySuppression(p.event, "threshold reached during generation", time.Now())
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "这条回复不应发送"}, nil
}

// 「反复拒答后暂停」可以单独关掉：关了以后拒答照常，只是满 4 次也不暂停对方。
func TestReplyRefusalSuppressionCanBeTurnedOff(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    *bool
		wantPaused bool
	}{
		{"default_on", nil, true},
		{"turned_off", boolPointer(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "42", ReplyRefusalSuppressionEnabled: tc.enabled}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
			var event MessageEvent
			for index := 0; index < replyRefusalThreshold; index++ {
				event = refusalTestEvent(EventKindPrivate, "", "user", fmt.Sprintf("toggle-%s-%d", tc.name, index))
				runtime.applyReplyControlAfterSend(context.Background(), event, "这条消息我拒绝回答。", replyControlIntent{RefuseCurrent: true})
			}
			if _, paused := runtime.activeReplySuppression(event, time.Now()); paused != tc.wantPaused {
				t.Fatalf("暂停 = %v，want %v", paused, tc.wantPaused)
			}
		})
	}
}
