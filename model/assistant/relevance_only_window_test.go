// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newRelevanceOnlyRuntime(provider *privateClosingProvider, chatLevel string) *Runtime {
	return NewRuntime(BotConfig{
		BotAccount: "42", OwnerID: "owner", AgentEnabled: false, BotReplyLoopDetectionEnabled: boolPointer(false),
		Participation: &ParticipationPreferences{Desire: 100, RelevanceLevel: "on", ChatLevel: chatLevel},
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
}

// routeCalls 跑一次接话评分，返回模型被调用的次数和沉默理由。
func routeCalls(t *testing.T, runtime *Runtime, provider *privateClosingProvider, messageID string) (int, string) {
	t.Helper()
	event := groupPlainEvent("30001", messageID, "那这个怎么弄")
	_, before := provider.counts()
	routed, _, _, _ := runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
	_, after := provider.counts()
	return after - before, routed.routingReason
}

// TestRelevanceOnlyRoutingNeedsRecentBotReply 闲聊关、回应提问开时，机器人 30 分钟
// 内没在本群说过话就不调接话评分；说过话照常评分。
func TestRelevanceOnlyRoutingNeedsRecentBotReply(t *testing.T) {
	provider := &privateClosingProvider{}
	runtime := newRelevanceOnlyRuntime(provider, "off")
	if calls, reason := routeCalls(t, runtime, provider, "m1"); calls != 0 || !strings.Contains(reason, "30 分钟") {
		t.Fatalf("bot never spoke: calls=%d reason=%q, want no model call", calls, reason)
	}

	rememberGroupBotReply(runtime, "bot-old", time.Now().Add(-relevanceOnlyActiveWindow-time.Minute))
	if calls, _ := routeCalls(t, runtime, provider, "m2"); calls != 0 {
		t.Fatalf("bot spoke over 30 minutes ago: calls=%d, want no model call", calls)
	}

	rememberGroupBotReply(runtime, "bot-recent", time.Now().Add(-10*time.Minute))
	if calls, _ := routeCalls(t, runtime, provider, "m3"); calls == 0 {
		t.Fatal("bot spoke 10 minutes ago: the router must still judge follow-up questions")
	}
}

// TestRelevanceOnlyRoutingUsesSentReplyTime 热闹的群里机器人那句可能已被挤出最近历史，
// 回复发出时记下的时间照样能打开窗口。
func TestRelevanceOnlyRoutingUsesSentReplyTime(t *testing.T) {
	provider := &privateClosingProvider{}
	runtime := newRelevanceOnlyRuntime(provider, "off")
	runtime.markGroupReplied(groupPlainEvent("30001", "asked", "在吗"))
	if calls, _ := routeCalls(t, runtime, provider, "m1"); calls == 0 {
		t.Fatal("a reply was just sent in this group: the router must run")
	}

	other := groupPlainEvent("30001", "m2", "那这个怎么弄")
	other.GroupID = "other"
	if !runtime.relevanceOnlyDormant(other, runtime.effectiveConfigForEvent(other), time.Now()) {
		t.Fatal("a reply in one group must not open the window in another")
	}
}

// TestChatEnabledRoutingIgnoresReplyWindow 闲聊开着时每条消息都要评闲聊分，不受窗口限制。
func TestChatEnabledRoutingIgnoresReplyWindow(t *testing.T) {
	provider := &privateClosingProvider{}
	runtime := newRelevanceOnlyRuntime(provider, "low")
	if calls, _ := routeCalls(t, runtime, provider, "m1"); calls == 0 {
		t.Fatal("chat is on: the router must run even though the bot has been quiet")
	}
}
