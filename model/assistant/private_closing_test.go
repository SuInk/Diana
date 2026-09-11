// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// privateClosingProvider 把发送前审核这一次调用和其它调用分开：收尾判断是审核
// 顺带做的一项，测试要能逐轮给出不同的审核结论。
type privateClosingProvider struct {
	mu         sync.Mutex
	audits     []string
	auditCalls int
	replies    []string
	replyCalls int
}

func (p *privateClosingProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if requestMessagesContain(req.Messages, "你是机器人回复的发送前审核器") {
		text := `{"send_confidence":0.99,"account_safe":true,"count_refusal":false}`
		if p.auditCalls < len(p.audits) {
			text = p.audits[p.auditCalls]
		}
		p.auditCalls++
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: text}, nil
	}
	if requestMessagesContain(req.Messages, "功能路由器") {
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"none","prompt":""}`}, nil
	}
	text := "好的，拜拜~"
	if p.replyCalls < len(p.replies) {
		text = p.replies[p.replyCalls]
	}
	p.replyCalls++
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: text}, nil
}

func (p *privateClosingProvider) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.auditCalls, p.replyCalls
}

const (
	auditClosing    = `{"send_confidence":0.99,"account_safe":true,"count_refusal":false,"conversation_closing":true,"stop_requested":false,"closing_confidence":0.96,"closing_reason":"双方都只在道别"}`
	auditStop       = `{"send_confidence":0.99,"account_safe":true,"count_refusal":false,"conversation_closing":true,"stop_requested":true,"closing_confidence":0.97,"closing_reason":"对方说别回了"}`
	auditSubstance  = `{"send_confidence":0.99,"account_safe":true,"count_refusal":false,"conversation_closing":false,"stop_requested":false,"closing_confidence":0.95,"closing_reason":"对方提出了新问题"}`
	auditLowClosing = `{"send_confidence":0.99,"account_safe":true,"count_refusal":false,"conversation_closing":true,"stop_requested":false,"closing_confidence":0.4,"closing_reason":"拿不准"}`
)

// rememberPrivateBotReply 在会话历史里放一条机器人刚说过的话，好让私聊的收尾
// 判断够得着时间门槛。
func rememberPrivateBotReply(runtime *Runtime, userID, messageID string, at time.Time) {
	// 私聊里机器人自己那条历史保留对端的 UserID（会话键按对端算），靠 Outbound
	// 标记身份——见 outgoingHistoryEvent。
	runtime.remember(MessageEvent{
		Kind: EventKindPrivate, UserID: userID, SelfID: "42", MessageID: messageID, Outbound: true,
		Time: at.Unix(), RawMessage: "拜拜~",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "拜拜~"}}},
	})
}

func privateEvent(userID, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindPrivate, UserID: userID, MessageID: messageID, ToMe: true,
		Time: time.Now().Unix(), RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// TestPrivateClosingAnswersGraceThenWithholds 复现那次私聊的形状：前两句告别
// 照常回答，第三句只剩告别时不再追加。
func TestPrivateClosingAnswersGraceThenWithholds(t *testing.T) {
	provider := &privateClosingProvider{audits: []string{auditClosing, auditClosing, auditClosing}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner", PrivateClosingGrace: 2},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	texts := []string{"嗯 拜", "拜拜", "嗯 晚安"}
	outcomes := make([]string, 0, len(texts))
	for index, text := range texts {
		rememberPrivateBotReply(runtime, "380726517", "bot-"+text, time.Now())
		event := privateEvent("380726517", "m"+string(rune('1'+index)), text)
		outcome, err := runtime.replyAndRecord(context.Background(), event, text, "replied")
		if err != nil {
			t.Fatalf("turn %d: %v", index+1, err)
		}
		outcomes = append(outcomes, outcome)
	}
	if outcomes[0] != "replied" || outcomes[1] != "replied" {
		t.Fatalf("first two closing turns were not answered: %#v", outcomes)
	}
	if outcomes[2] != "ignored_conversation_closed" {
		t.Fatalf("third closing turn outcome = %q, want ignored_conversation_closed", outcomes[2])
	}
	if len(channel.sent) != 2 {
		t.Fatalf("sent %d replies, want exactly the two answered closers: %#v", len(channel.sent), channel.sent)
	}
	// 收尾拦截不开暂停：对方只是在道别，不是要求停止。
	if _, blocked := runtime.activeReplySuppression(privateEvent("380726517", "x", "x"), time.Now()); blocked {
		t.Fatal("closing withholding must not activate a 30 minute suppression")
	}
}

// TestPrivateClosingCountResetsOnSubstantiveMessage 确认一句有内容的话能把
// 计数清零：对话又活过来了，宽限次数应该重新给满。
func TestPrivateClosingCountResetsOnSubstantiveMessage(t *testing.T) {
	provider := &privateClosingProvider{audits: []string{auditClosing, auditClosing, auditSubstance, auditClosing, auditClosing}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner", PrivateClosingGrace: 2},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	turns := []string{"嗯 拜", "拜拜", "等下 明天几点开会", "行 拜", "嗯 晚安"}
	for index, text := range turns {
		rememberPrivateBotReply(runtime, "380726517", "bot-"+text, time.Now())
		event := privateEvent("380726517", "reset-m"+string(rune('1'+index)), text)
		outcome, err := runtime.replyAndRecord(context.Background(), event, text, "replied")
		if err != nil {
			t.Fatalf("turn %d: %v", index+1, err)
		}
		if outcome != "replied" {
			t.Fatalf("turn %d outcome = %q, want replied; a substantive message must restore the full grace", index+1, outcome)
		}
	}
	if len(channel.sent) != len(turns) {
		t.Fatalf("sent = %d, want %d", len(channel.sent), len(turns))
	}
}

// TestPrivateStopRequestWithholdsAndSuppresses 明确叫停不给宽限次数，并且复用
// 现有的 30 分钟响应限制。
func TestPrivateStopRequestWithholdsAndSuppresses(t *testing.T) {
	provider := &privateClosingProvider{audits: []string{auditStop}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner", PrivateClosingGrace: 2},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	rememberPrivateBotReply(runtime, "380726517", "bot-1", time.Now())
	event := privateEvent("380726517", "stop-1", "别回了 睡了")
	outcome, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "ignored_stop_requested" {
		t.Fatalf("outcome = %q, want ignored_stop_requested", outcome)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("stop request still sent a reply: %#v", channel.sent)
	}
	item, blocked := runtime.activeReplySuppression(event, time.Now())
	if !blocked {
		t.Fatal("stop request did not activate the existing response suppression")
	}
	if remaining := time.Until(item.Until); remaining < 29*time.Minute || remaining > 31*time.Minute {
		t.Fatalf("suppression duration = %s, want about 30m", remaining)
	}
}

// TestPrivateStopRequestFromOwnerSkipsSuppression 主人说「别回了」照样收声，
// 但绝不能把主人锁在自己的机器人外面——解除限制的命令恰恰要主人发。
func TestPrivateStopRequestFromOwnerSkipsSuppression(t *testing.T) {
	provider := &privateClosingProvider{audits: []string{auditStop}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "10001", PrivateClosingGrace: 2},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	rememberPrivateBotReply(runtime, "10001", "bot-owner", time.Now())
	event := privateEvent("10001", "owner-stop", "别回了")
	outcome, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "ignored_stop_requested" {
		t.Fatalf("outcome = %q, want ignored_stop_requested", outcome)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("owner stop request still sent a reply: %#v", channel.sent)
	}
	if _, blocked := runtime.activeReplySuppression(event, time.Now()); blocked {
		t.Fatal("owner must never be locked out by a 30 minute suppression")
	}
}

// TestPrivateFirstMessagePaysNoClosingAudit 私聊里的第一句不为收尾判断多花
// 一次模型调用：机器人还没说过话，这里不可能是第 N 次道别。
func TestPrivateFirstMessagePaysNoClosingAudit(t *testing.T) {
	provider := &privateClosingProvider{replies: []string{"你好呀"}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	event := privateEvent("380726517", "first", "在吗")
	if _, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied"); err != nil {
		t.Fatal(err)
	}
	if audits, _ := provider.counts(); audits != 0 {
		t.Fatalf("audit calls = %d, want 0 for the first message in a private chat", audits)
	}
}

// TestPrivateClosingAuditSkippedAfterLongSilence 隔了很久之后的第一句同样不判：
// 那是新的一段对话。
func TestPrivateClosingAuditSkippedAfterLongSilence(t *testing.T) {
	provider := &privateClosingProvider{replies: []string{"早呀"}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	rememberPrivateBotReply(runtime, "380726517", "old", time.Now().Add(-privateClosingAuditWindow-time.Minute))
	event := privateEvent("380726517", "next-day", "早")
	if _, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied"); err != nil {
		t.Fatal(err)
	}
	if audits, _ := provider.counts(); audits != 0 {
		t.Fatalf("audit calls = %d, want 0 after a silence longer than %s", audits, privateClosingAuditWindow)
	}
}

// TestPrivateClosingIgnoresLowConfidence 置信度不够时照常回复：少答一句的代价
// 远小于该答不答。
func TestPrivateClosingIgnoresLowConfidence(t *testing.T) {
	provider := &privateClosingProvider{audits: []string{auditLowClosing, auditLowClosing, auditLowClosing}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner", PrivateClosingGrace: 1},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	for index, text := range []string{"嗯 拜", "拜拜", "晚安"} {
		rememberPrivateBotReply(runtime, "380726517", "bot-low-"+text, time.Now())
		event := privateEvent("380726517", "low-m"+string(rune('1'+index)), text)
		outcome, err := runtime.replyAndRecord(context.Background(), event, text, "replied")
		if err != nil {
			t.Fatal(err)
		}
		if outcome != "replied" {
			t.Fatalf("turn %d outcome = %q, want replied for a low-confidence closing verdict", index+1, outcome)
		}
	}
}

func TestPrivateClosingGraceFallsBackToDefault(t *testing.T) {
	if got := privateClosingGrace(BotConfig{}); got != defaultPrivateClosingGrace {
		t.Fatalf("privateClosingGrace(zero) = %d, want %d", got, defaultPrivateClosingGrace)
	}
	if got := privateClosingGrace(BotConfig{PrivateClosingGrace: 5}); got != 5 {
		t.Fatalf("privateClosingGrace(5) = %d", got)
	}
	if got := privateClosingReason(2); !strings.Contains(got, "已互相道别 2 次") {
		t.Fatalf("privateClosingReason(2) = %q", got)
	}
}

func TestParseProactiveReplyQualityDecisionReadsClosingFields(t *testing.T) {
	decision, ok := parseProactiveReplyQualityDecision(auditStop)
	if !ok {
		t.Fatal("audit payload with closing fields did not parse")
	}
	if !decision.ConversationClosing || !decision.StopRequested {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.ClosingConfidence != 0.97 || decision.ClosingReason != "对方说别回了" {
		t.Fatalf("closing confidence/reason not parsed: %#v", decision)
	}
	if !decision.stopCounts() || !decision.closingCounts() {
		t.Fatalf("high-confidence closing verdict did not count: %#v", decision)
	}

	// 旧提示词生成的回答里没有这几个字段，必须按「没有收尾」解出来，而不是整条作废。
	legacy, ok := parseProactiveReplyQualityDecision(`{"send_confidence":0.95,"account_safe":true}`)
	if !ok {
		t.Fatal("legacy audit payload stopped parsing")
	}
	if legacy.ConversationClosing || legacy.StopRequested || legacy.closingCounts() || legacy.stopCounts() {
		t.Fatalf("missing closing fields must default to false: %#v", legacy)
	}
}

func TestReplyAuditPromptDescribesClosingContract(t *testing.T) {
	for _, want := range []string{
		"closing_check=true",
		"conversation_closing",
		"stop_requested",
		"closing_confidence",
		"sender_marked_as_bot",
		"累计几次由运行时自己数",
	} {
		if !strings.Contains(proactiveReplyQualityPrompt, want) {
			t.Fatalf("send audit prompt missing %q", want)
		}
	}
}

// TestOwnerReleaseClearsClosingCount 「解除响应限制」要是干净的一笔勾销：
// 留着收尾计数会让机器人下一句又闭嘴。
func TestOwnerReleaseClearsClosingCount(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "10001"},
		nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := privateEvent("380726517", "closed-1", "拜拜")
	now := time.Now()
	if got := runtime.notePrivateClosingExchange(event, now); got != 1 {
		t.Fatalf("closing count = %d, want 1", got)
	}
	if got := runtime.notePrivateClosingExchange(event, now); got != 2 {
		t.Fatalf("closing count = %d, want 2", got)
	}
	if _, ok := runtime.activateReplySuppression(event, "test", now); !ok {
		t.Fatal("activateReplySuppression() = false")
	}
	if _, ok := runtime.clearReplySuppression(event, "380726517"); !ok {
		t.Fatal("clearReplySuppression() = false")
	}
	if got := runtime.privateClosingCount(event, now); got != 0 {
		t.Fatalf("closing count after owner release = %d, want 0", got)
	}
}

// TestPrivateClosingCountExpires 计数跟着对话过期：隔了半小时再说话是新的一段。
func TestPrivateClosingCountExpires(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := privateEvent("380726517", "expire-1", "拜拜")
	start := time.Now()
	runtime.notePrivateClosingExchange(event, start)
	runtime.notePrivateClosingExchange(event, start)
	if got := runtime.privateClosingCount(event, start.Add(privateClosingRetention-time.Minute)); got != 2 {
		t.Fatalf("count inside the retention window = %d, want 2", got)
	}
	if got := runtime.privateClosingCount(event, start.Add(privateClosingRetention+time.Minute)); got != 0 {
		t.Fatalf("count after %s = %d, want 0", privateClosingRetention, got)
	}
}
