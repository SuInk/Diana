// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func markedBotRuntime(t *testing.T, provider LLMProvider) *Runtime {
	t.Helper()
	store := &testWritableGroupConfigStore{}
	if _, err := store.SaveGroupConfig(GroupConfig{
		BotProfileID: "a", GroupID: "1049765710", Enabled: true, EnabledSet: true,
		MarkedBotIDs: []string{"380726517"},
	}, BotConfig{ID: "a", BotAccount: "42"}); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{ID: "a", BotAccount: "42", OwnerID: "owner"},
		nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetGroupConfigStore(store)
	return runtime
}

// TestGroupMarkerAppliesInPrivateChat 群里标的机器人，进私聊也还是机器人。
// 以前 effectiveConfigForEventLocked 只在群事件上合并群级标记（runtime.go:1293），
// 机器人级那份又常常是空的，于是同一个账号一进私聊就变回了人。
func TestGroupMarkerAppliesInPrivateChat(t *testing.T) {
	runtime := markedBotRuntime(t, &capturingLLMProvider{reply: `{"needs_reply":false}`})

	private := MessageEvent{Kind: EventKindPrivate, ProfileID: "a", UserID: "380726517", MessageID: "p1"}
	if !runtime.accountMarkedAsBot(private) {
		t.Fatal("group-level marker did not reach private chat")
	}
	if !runtime.requiresTelegramBotMentionJudgment(private) {
		t.Fatal("marked bot in DM did not require the semantic gate")
	}
	// 没被标记的账号完全不受影响。
	other := private
	other.UserID = "999"
	if runtime.accountMarkedAsBot(other) || runtime.requiresTelegramBotMentionJudgment(other) {
		t.Fatal("unmarked account was treated as a bot")
	}
	// 另一台机器人的群配置不该污染这一台。
	foreign := private
	foreign.ProfileID = "b"
	if runtime.accountMarkedAsBot(foreign) {
		t.Fatal("marker leaked across bot profiles")
	}
}

// TestMarkedBotPrivateMessageSuppressedUnlessAddressed 保留群里那条「语义上向
// 本机接话时仍可回应」的例外：私聊里换成「这条确实在要一个回答」。
func TestMarkedBotPrivateMessageSuppressedUnlessAddressed(t *testing.T) {
	declining := &capturingLLMProvider{reply: `{"needs_reply":false}`}
	runtime := markedBotRuntime(t, declining)
	event := MessageEvent{
		Kind: EventKindPrivate, ProfileID: "a", UserID: "380726517", MessageID: "p1",
		RawMessage: "【系统通知】您的订阅已更新", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "【系统通知】您的订阅已更新"}}},
	}
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	if handled || outcome != "ignored_bot_message" {
		t.Fatalf("handled=%v outcome=%q, want ignored_bot_message", handled, outcome)
	}
	if len(declining.requestSnapshot().Messages) == 0 {
		t.Fatal("private marked-bot gate never asked the model")
	}

	accepting := &capturingLLMProvider{reply: `{"needs_reply":true}`}
	allowed := markedBotRuntime(t, accepting)
	_, _, handled, outcome = allowed.prepareMessageEvent(context.Background(), event)
	if !handled || outcome == "ignored_bot_message" {
		t.Fatalf("semantically addressed DM was suppressed: handled=%v outcome=%q", handled, outcome)
	}
}

func TestMarkedBotPrivatePromptRejectsMentionHeuristics(t *testing.T) {
	for _, want := range []string{"默认不回应", "「有没有点到你」不是判据", "needs_reply"} {
		if !strings.Contains(markedBotPrivateMessagePrompt, want) {
			t.Fatalf("private marked-bot prompt missing %q", want)
		}
	}
}

// TestBotReplyLoopCandidateCoversPrivate 空转判断以前按事件类型挡掉私聊
// （reply_suppression.go:341），于是那次 57 条私聊里一次都没跑过。
func TestBotReplyLoopCandidateCoversPrivate(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"},
		nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := privateEvent("380726517", "loop-1", "嗯 拜")

	// 机器人还没在这个会话里说过话：谈不上空转，不判。
	if _, ok := runtime.botReplyLoopCandidate(event, event.RawMessage); ok {
		t.Fatal("private loop candidate fired before the bot had replied at all")
	}

	rememberPrivateBotReply(runtime, "380726517", "bot-loop", time.Now())
	candidate, ok := runtime.botReplyLoopCandidate(event, event.RawMessage)
	if !ok {
		t.Fatal("private message right after a bot reply is not a loop candidate")
	}
	if candidate.TriggerKind != "private" {
		t.Fatalf("trigger kind = %q, want private", candidate.TriggerKind)
	}

	// 隔了很久的那一句同样不判。
	stale := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rememberPrivateBotReply(stale, "380726517", "bot-old", time.Now().Add(-privateClosingAuditWindow-time.Minute))
	if _, ok := stale.botReplyLoopCandidate(event, event.RawMessage); ok {
		t.Fatal("private loop candidate fired after a long silence")
	}
}

// TestReplyAuditPassesMarkerEvidenceForLoop 标记证据只在判空转时带上，别的时候
// 不塞进审核载荷。
func TestReplyAuditPassesMarkerEvidenceForLoop(t *testing.T) {
	runtime := markedBotRuntime(t, &capturingLLMProvider{reply: `{"needs_reply":true}`})
	event := MessageEvent{
		Kind: EventKindPrivate, ProfileID: "a", UserID: "380726517", MessageID: "p2", ToMe: true,
		Time: time.Now().Unix(), RawMessage: "嗯 拜",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "嗯 拜"}}},
	}
	rememberPrivateBotReply(runtime, "380726517", "bot-marker", time.Now())
	need := runtime.replyAuditNeed(event, event.RawMessage, runtime.effectiveConfigForEvent(event), false)
	if !need.Loop {
		t.Fatal("marked bot DM right after a bot reply is not a loop candidate")
	}
	if !need.MarkedBot {
		t.Fatal("marker evidence was not passed into the audit payload")
	}
	if !need.Closing {
		t.Fatal("closing check should also run for this follow-up")
	}
}
