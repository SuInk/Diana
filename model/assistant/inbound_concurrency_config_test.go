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

func TestInboundConcurrencyDefaultsMatchTheOldConstants(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	if cfg.InboundGroupConcurrency != 3 {
		t.Fatalf("group concurrency default = %d, want 3 (unchanged)", cfg.InboundGroupConcurrency)
	}
	if cfg.InboundPrivateConcurrency != 2 {
		t.Fatalf("private concurrency default = %d, want 2 (private bursts now merge)", cfg.InboundPrivateConcurrency)
	}
	if cfg.PrivateClosingGrace != defaultPrivateClosingGrace {
		t.Fatalf("private closing grace default = %d, want %d", cfg.PrivateClosingGrace, defaultPrivateClosingGrace)
	}
	limits := inboundConcurrencyForConfig(BotConfig{})
	if limits.Group != defaultInboundGroupConcurrency || limits.Private != defaultInboundPrivateConcurrency {
		t.Fatalf("unconfigured limits = %#v", limits)
	}
}

func TestInboundConcurrencyAndClosingGraceRoundTrip(t *testing.T) {
	source := BotConfig{
		InboundGroupConcurrency:   5,
		InboundPrivateConcurrency: 2,
		PrivateClosingGrace:       4,
	}
	restored := ConfigFromPayload(PayloadFromConfig(source), BotConfig{}).WithDefaults()
	if restored.InboundGroupConcurrency != 5 || restored.InboundPrivateConcurrency != 2 {
		t.Fatalf("concurrency did not survive the payload round trip: %#v", restored)
	}
	if restored.PrivateClosingGrace != 4 {
		t.Fatalf("private_closing_grace = %d, want 4", restored.PrivateClosingGrace)
	}
	if limits := inboundConcurrencyForConfig(restored); limits.Group != 5 || limits.Private != 2 {
		t.Fatalf("claim limits = %#v", limits)
	}
}

func TestInboundConcurrencyClampsAbsurdValues(t *testing.T) {
	cfg := BotConfig{InboundGroupConcurrency: 9999, InboundPrivateConcurrency: -3}.WithDefaults()
	if cfg.InboundGroupConcurrency != maxInboundSessionConcurrency {
		t.Fatalf("group concurrency = %d, want clamp to %d", cfg.InboundGroupConcurrency, maxInboundSessionConcurrency)
	}
	if cfg.InboundPrivateConcurrency != defaultInboundPrivateConcurrency {
		t.Fatalf("negative private concurrency = %d, want the default", cfg.InboundPrivateConcurrency)
	}
}

// concurrentBurstProvider 记录每次生成请求开始和结束的时刻，好看出两条私聊
// 是不是真的在并行生成。
type concurrentBurstProvider struct {
	mu       sync.Mutex
	cond     *sync.Cond
	active   int
	maxActiv int
	replies  int
	// wantParallel 是这次希望观察到的同时生成数。每次生成都在这里等，直到
	// 真的凑齐这么多路、或者等到 deadline——不靠 sleep 撞运气。
	wantParallel int
	wait         time.Duration
}

func newConcurrentBurstProvider(wantParallel int, wait time.Duration) *concurrentBurstProvider {
	p := &concurrentBurstProvider{wantParallel: wantParallel, wait: wait}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *concurrentBurstProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if requestMessagesContain(req.Messages, "功能路由器") {
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"action":"none","prompt":""}`}, nil
	}
	if requestMessagesContain(req.Messages, "你是机器人回复的发送前审核器") {
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"send_confidence":0.99,"account_safe":true}`}, nil
	}
	p.mu.Lock()
	p.active++
	p.replies++
	if p.active > p.maxActiv {
		p.maxActiv = p.active
	}
	p.cond.Broadcast()
	deadline := time.Now().Add(p.wait)
	if p.active < p.wantParallel {
		stop := time.AfterFunc(p.wait, func() { p.cond.Broadcast() })
		for p.active < p.wantParallel && time.Now().Before(deadline) {
			p.cond.Wait()
		}
		stop.Stop()
	}
	p.active--
	p.mu.Unlock()
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "回复"}, nil
}

func (p *concurrentBurstProvider) stats() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.replies, p.maxActiv
}

// TestPrivateBurstFoldsIntoActiveDirectReply：私聊连发的补充或重复现在并入正在生成的回复。
func TestPrivateBurstFoldsIntoActiveDirectReply(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"relation":"repeat","confidence":0.95,"reason":"同一个请求再说一遍"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"},
		nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	root := privateEvent("380726517", "burst-1", "戳我一下")
	if key := directReplyMergeKey(root); key == "" {
		t.Fatal("private chat has no direct reply merge key")
	}
	ctx, finish := runtime.beginDirectReply(context.Background(), root)
	defer finish()

	for _, follow := range []string{"burst-2", "burst-3"} {
		event := privateEvent("380726517", follow, "戳我")
		if rootID, merged := runtime.mergeIntoActiveDirectReply(ctx, event, event.RawMessage); !merged || rootID != "burst-1" {
			t.Fatalf("private follow-up %s merged=%v root=%q", follow, merged, rootID)
		}
	}
	// 别人的私聊不会并进这个人的回复。
	if _, merged := runtime.mergeIntoActiveDirectReply(ctx, privateEvent("999", "other", "戳我"), "戳我"); merged {
		t.Fatal("another user's private message merged into this reply")
	}
}

// TestPrivateBurstUnderConcurrency 在真实队列上跑一遍连发三句，只验证并发档位本身。
// 这里的模型不会把连发判成重复或补充，所以每句仍然单独回复；合并由上面那个测试覆盖。
func TestPrivateBurstUnderConcurrency(t *testing.T) {
	t.Run("serial", func(t *testing.T) {
		replies, maxActive, sent := runPrivateBurst(t, 1)
		if replies != 3 || sent != 3 {
			t.Fatalf("serial burst produced %d replies / %d sends, want 3 / 3", replies, sent)
		}
		if maxActive != 1 {
			t.Fatalf("serial burst ran %d generations at once, want 1", maxActive)
		}
	})
	t.Run("parallel", func(t *testing.T) {
		replies, maxActive, sent := runPrivateBurst(t, 2)
		// 并发时后到的那句会先问一次「是不是同一件事」，模型调用数可能多一次。
		if replies < 3 || sent != 3 {
			t.Fatalf("parallel burst produced %d replies / %d sends, want 3 / 3 when follow-ups are not classified as repeats", replies, sent)
		}
		if maxActive < 2 {
			t.Fatalf("max concurrent generations = %d, want at least 2 under private concurrency 2", maxActive)
		}
	})
}

func runPrivateBurst(t *testing.T, privateConcurrency int) (replies, maxActive, sent int) {
	t.Helper()
	store := newMemoryInboundEventStore()
	channel := newQueueTestChannel()
	provider := newConcurrentBurstProvider(privateConcurrency, 2*time.Second)
	runtime := NewRuntime(BotConfig{
		Enabled: true, BotAccount: "42", MaxBotConcurrency: 4, InboundPrivateConcurrency: privateConcurrency,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetInboundEventStore(store)

	ids := make([]string, 0, 3)
	for _, messageID := range []string{"p-1", "p-2", "p-3"} {
		event := MessageEvent{
			Kind: EventKindPrivate, Time: time.Now().Unix(), SelfID: "42", UserID: "380726517",
			MessageID: messageID, RawMessage: "连发一句",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "连发一句"}}},
		}
		id, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
		if err != nil || !inserted {
			t.Fatalf("enqueue %s inserted=%v err=%v", messageID, inserted, err)
		}
		ids = append(ids, id)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		for _, id := range ids {
			if !store.isDone(id) {
				return false
			}
		}
		return true
	})
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}

	replies, maxActive = provider.stats()
	return replies, maxActive, channel.sentCount()
}
