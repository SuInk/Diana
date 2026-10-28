// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

// 关掉账号安全审核后，只剩空转、疲劳这些作用在以后的判断，不该再让回复在发送前等审核；
// 开着时照旧先审后发。
func TestReplyAuditDefersWhenNothingBlocksSend(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"account_safe":0.98}`}
	runtime := topicTestRuntime(provider)
	event := botReplyLoopEvent(runtime, "defer", "23456", 0, time.Now().Add(-time.Minute), time.Second, "哈哈")
	cfg := runtime.effectiveConfigForEvent(event)
	cfg.ReplySafetyMasterEnabled = boolPointer(false)

	prepared := runtime.prepareReplyAuditOrDefer(context.Background(), event, "哈哈", "哈哈哈", cfg, false, true)
	if !prepared.deferred || !(prepared.need.Loop || prepared.need.Fatigue) {
		t.Fatalf("关了账号安全、只剩空转或疲劳时应当挪到发送后：deferred=%v need=%+v", prepared.deferred, prepared.need)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("挪后的审核不该在发送前调模型：%d 次", len(provider.requests))
	}
	if blocked := runtime.prepareReplyAuditOrDefer(context.Background(), event, "哈哈", "哈哈哈", cfg, false, false); blocked.deferred {
		t.Fatal("调用方不允许挪后时（转发卡片）照旧先审")
	}

	cfg.ReplySafetyMasterEnabled = boolPointer(true)
	if sync := runtime.prepareReplyAuditOrDefer(context.Background(), event, "哈哈", "哈哈哈", cfg, false, true); sync.deferred {
		t.Fatal("账号安全开着时要在发送前审")
	}
	if len(provider.requests) == 0 {
		t.Fatal("账号安全开着时应当在发送前调审核模型")
	}
}

// 私聊收尾决定这一条发不发（对方说了别回），不挪。
func TestPrivateClosingStaysBeforeSend(t *testing.T) {
	if !(replyAuditNeed{Closing: true}).blocksSend() || !(replyAuditNeed{Quality: true}).blocksSend() || !(replyAuditNeed{AccountSafety: true}).blocksSend() {
		t.Fatal("账号安全、主动插话、私聊收尾都要在发送前判")
	}
	if (replyAuditNeed{Loop: true, Fatigue: true, GroupStop: true}).blocksSend() {
		t.Fatal("空转、疲劳、群叫停只影响以后的消息，不该挡发送")
	}
}

// 挪后的审核照样落账：空转累计达标就开暂停，只是当时那条已经发出去了。
func TestDeferredReplyAuditStillSuppressesLoop(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"account_safe":0.98,"reply_loop_meaningless":true,"reply_loop_purposeless":false,"reply_loop_confidence":0.99,"reply_loop_reason":"机械复读"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "42", ReplySafetyMasterEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	start := time.Now().Add(-25 * time.Minute).Truncate(time.Second)
	var last MessageEvent
	for i := 0; i < defaultBotReplyLoopThreshold+1; i++ {
		event := botReplyLoopEvent(runtime, "deferloop", "23456", i, start.Add(time.Duration(i)*5*time.Minute), time.Minute, "在吗")
		if _, active := runtime.activeReplySuppression(event, time.Now()); active {
			return
		}
		cfg := runtime.effectiveConfigForEvent(event)
		prepared := runtime.prepareReplyAuditOrDefer(context.Background(), event, "在吗", "在的", cfg, false, true)
		if !prepared.deferred || !prepared.need.Loop {
			t.Fatalf("round %d: 关了账号安全的空转候选应当挪到发送后：deferred=%v need=%+v", i, prepared.deferred, prepared.need)
		}
		runtime.runDeferredReplyAudit(context.Background(), event, cfg, prepared, "在的", false)
		deadline := time.Now().Add(3 * time.Second)
		for func() bool { provider.mu.Lock(); defer provider.mu.Unlock(); return len(provider.requests) <= i }() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		last = event
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, active := runtime.activeReplySuppression(last, time.Now()); active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("挪后的审核累计判到空转后应当开暂停")
}
