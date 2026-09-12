// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// markedBotRuntime 里 380726517 标在机器人级：那是「这个账号在哪儿都是机器人」的
// 表达，所以私聊也算。群里另标一个 700000001，它只在那个群生效，用来钉住作用域。
func markedBotRuntime(t *testing.T, provider LLMProvider) *Runtime {
	t.Helper()
	store := &testWritableGroupConfigStore{}
	if _, err := store.SaveGroupConfig(GroupConfig{
		BotProfileID: "a", GroupID: "1049765710", Enabled: true, EnabledSet: true,
		MarkedBotIDs: []string{"700000001"},
	}, BotConfig{ID: "a", BotAccount: "42"}); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(BotConfig{ID: "a", BotAccount: "42", OwnerID: "owner", MarkedBotIDs: []string{"380726517"}},
		nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetGroupConfigStore(store)
	return runtime
}

// 标记的作用域就是它被填在哪儿：机器人级那份到处生效，群里那份只管那个群。
//
// 这里一度是跨群汇总：任意一个群标下的账号，在这台机器人的所有会话里都算机器人。
// 代价是标记串门——在 A 群标的账号到 B 群照样被抑制，而 B 群的编辑页两处都看不见
// 它，管理员无从知道为什么不理人。
func TestMarkerScopeFollowsWhereItWasSet(t *testing.T) {
	runtime := markedBotRuntime(t, &capturingLLMProvider{reply: `{"needs_reply":false}`})

	private := MessageEvent{Kind: EventKindPrivate, ProfileID: "a", UserID: "380726517", MessageID: "p1"}
	if !runtime.accountMarkedAsBot(private) {
		t.Fatal("机器人级标记应当在私聊里也生效")
	}
	// 只标在某个群里的账号：那个群里算机器人，别的群和私聊都不算。
	inGroup := MessageEvent{Kind: EventKindGroup, ProfileID: "a", GroupID: "1049765710", UserID: "700000001", MessageID: "g1"}
	if !runtime.accountMarkedAsBot(inGroup) {
		t.Fatal("群级标记应当在本群生效")
	}
	otherGroup := inGroup
	otherGroup.GroupID = "791503570"
	if runtime.accountMarkedAsBot(otherGroup) {
		t.Fatal("群级标记串到了别的群")
	}
	privateOnlyGroupMarked := private
	privateOnlyGroupMarked.UserID = "700000001"
	if runtime.accountMarkedAsBot(privateOnlyGroupMarked) {
		t.Fatal("群级标记串进了私聊")
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
	// 原先这里还验「另一台机器人的群配置不该污染这一台」。那条防的是跨群汇总时
	// 按 bot_profile_id 过滤有没有做对；汇总去掉之后群标记根本不出本群，比按归属
	// 过滤更强，这条断言也就没有对象了。
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
