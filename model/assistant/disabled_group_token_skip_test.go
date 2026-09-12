// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// countingRouterProvider records every model call so a test can assert that a
// disabled or non-admitted group triggers none of the token-costing gates
// (Telegram 接话判定、主动回复路由、回复生成都走这条 provider）。
type countingRouterProvider struct {
	mu    sync.Mutex
	calls int
	reply string
}

func (p *countingRouterProvider) Generate(_ context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	reply := p.reply
	if reply == "" {
		// 空 JSON 让主动回复路由解析成「不接话」，测试就不会滚进回复生成。
		reply = "{}"
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: reply}, nil
}

func (p *countingRouterProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type disabledGroupSkipHarness struct {
	runtime    *Runtime
	history    *crossGroupSearchCounter
	memory     *testStructuredMemoryStore
	expression *stubExpressionStore
	provider   *countingRouterProvider
}

// newDisabledGroupSkipHarness wires a runtime with a searchable history store, a
// long-term memory queue, an expression store, and a call-counting model so a
// test can watch which paths a group message actually exercises. base 决定这台
// 机器人是否准入 g1（关掉的群、或不在准入名单里的群，都应当在花 token 之前止步）。
func newDisabledGroupSkipHarness(t *testing.T, base BotConfig, groupEnabled bool) disabledGroupSkipHarness {
	t.Helper()
	base.ID = "a"
	base.BotAccount = "42"
	base.OwnerID = "owner"
	base.CrossGroupMemoryEnabled = boolPointer(true)
	base.ExpressionLearningEnabled = boolPointer(true)
	base.LongTermMemoryEnabled = boolPointer(true)

	provider := &countingRouterProvider{}
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil,
		func() (LLMProvider, error) { return provider, nil })

	history := &crossGroupSearchCounter{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	runtime.SetMessageHistoryStore(history)
	memory := &testStructuredMemoryStore{}
	runtime.SetStructuredMemoryStore(memory)
	expression := &stubExpressionStore{}
	runtime.SetExpressionStyleStore(expression)

	store := &testWritableGroupConfigStore{}
	if _, err := store.SaveGroupConfig(GroupConfig{
		BotProfileID: "a", GroupID: "g1", Enabled: groupEnabled, EnabledSet: true,
	}, base); err != nil {
		t.Fatal(err)
	}
	runtime.SetGroupConfigStore(store)

	return disabledGroupSkipHarness{runtime: runtime, history: history, memory: memory, expression: expression, provider: provider}
}

// disabledGroupPhraseEvent 用一句短口癖：既能被表达学习收下，又是一条正常群消息。
func disabledGroupPhraseEvent() MessageEvent {
	text := "这个转发功能后来修好了吗"
	return MessageEvent{
		Kind: EventKindGroup, Platform: PlatformTelegram,
		ProfileID: "a", GroupID: "g1", UserID: "u1", MessageID: "m1", Time: time.Now().Unix(),
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// disabledGroupSignalEvent 用一句更长、词信号充足的消息，确保开启的群一定会发起
// 跨群检索——短口癖有时凑不够检索词，反而说不清是被跳过还是没命中。
func disabledGroupSignalEvent() MessageEvent {
	text := "上次说的那个转发合并功能后来到底修好了吗"
	return MessageEvent{
		Kind: EventKindGroup, Platform: PlatformTelegram,
		ProfileID: "a", GroupID: "g1", UserID: "u1", MessageID: "m1", Time: time.Now().Unix(),
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func waitForExpressionBump(t *testing.T, store *stubExpressionStore) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(store.bumpSnapshot()) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("group message never reached expression learning")
}

// TestDisabledGroupKeepsBookkeepingSkipsModelCalls 群被关掉时，消息照常进历史、
// 表达学习和长期记忆，但跨群语义检索、Telegram 接话判定和主动回复路由一次都不发。
func TestDisabledGroupKeepsBookkeepingSkipsModelCalls(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, false)
	event := disabledGroupPhraseEvent()

	_, _, handled, outcome := h.runtime.prepareMessageEvent(context.Background(), event)

	if handled || outcome != "ignored_policy" {
		t.Fatalf("handled=%v outcome=%q, want ignored_policy", handled, outcome)
	}
	if got := h.provider.callCount(); got != 0 {
		t.Fatalf("disabled group made %d model calls, want 0", got)
	}
	if h.history.searches != 0 {
		t.Fatalf("disabled group ran %d cross-group searches, want 0", h.history.searches)
	}
	// 仍然进历史。
	if got := len(h.runtime.history[sessionKey(event)]); got == 0 {
		t.Fatal("disabled group message was not persisted to history")
	}
	// 仍然进长期记忆队列。
	if got := len(h.memory.enqueued); got == 0 {
		t.Fatal("disabled group message was not enqueued into long-term memory")
	}
	// 仍然喂给表达学习。
	waitForExpressionBump(t, h.expression)
}

// TestEnabledGroupRunsCrossGroupSearchAndRouter 反向对照：同一条消息，群开着时
// 跨群检索和主动回复路由都要照常跑，别把开着的群一起省掉了。
func TestEnabledGroupRunsCrossGroupSearchAndRouter(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, true)

	_, _, handled, _ := h.runtime.prepareMessageEvent(context.Background(), disabledGroupSignalEvent())

	if handled {
		t.Fatal("router returned {} but the message was still handled")
	}
	if h.history.searches == 0 {
		t.Fatal("enabled group skipped the cross-group search")
	}
	if h.provider.callCount() == 0 {
		t.Fatal("enabled group never reached the proactive reply router")
	}
}

// TestDisabledGroupIgnoresDirectMention 关掉的群里，被 @、被点名也一样不回，而且
// 同样不花一次模型调用——提前判断只是把 admits 的结论挪早，没有改判。
func TestDisabledGroupIgnoresDirectMention(t *testing.T) {
	h := newDisabledGroupSkipHarness(t, BotConfig{}, false)
	event := disabledGroupPhraseEvent()
	event.ToMe = true

	_, _, handled, outcome := h.runtime.prepareMessageEvent(context.Background(), event)

	if handled {
		t.Fatal("mention in a disabled group was answered")
	}
	if outcome != "ignored_policy" {
		t.Fatalf("outcome=%q, want ignored_policy", outcome)
	}
	if got := h.provider.callCount(); got != 0 {
		t.Fatalf("mention in a disabled group made %d model calls, want 0", got)
	}
	if h.history.searches != 0 {
		t.Fatalf("mention in a disabled group ran %d cross-group searches, want 0", h.history.searches)
	}
}

// TestNotAdmittedGroupSkipsModelCalls 白名单外的群和被关掉的群走同一条捷径：
// 记账照旧，模型调用一次不花。
func TestNotAdmittedGroupSkipsModelCalls(t *testing.T) {
	base := BotConfig{GroupAdmission: GroupAdmission{Mode: GroupAdmissionWhitelist, AllowedGroups: []string{"other"}}}
	h := newDisabledGroupSkipHarness(t, base, true) // 群配置开着，但不在准入白名单里。
	event := disabledGroupPhraseEvent()

	_, _, handled, outcome := h.runtime.prepareMessageEvent(context.Background(), event)

	if handled || outcome != "ignored_policy" {
		t.Fatalf("handled=%v outcome=%q, want ignored_policy", handled, outcome)
	}
	if got := h.provider.callCount(); got != 0 {
		t.Fatalf("non-admitted group made %d model calls, want 0", got)
	}
	if h.history.searches != 0 {
		t.Fatalf("non-admitted group ran %d cross-group searches, want 0", h.history.searches)
	}
	if got := len(h.memory.enqueued); got == 0 {
		t.Fatal("non-admitted group message was not enqueued into long-term memory")
	}
}
