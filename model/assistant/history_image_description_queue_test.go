// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// queueVisionProvider 按调用顺序执行 behave，并记录最大并发数。
type queueVisionProvider struct {
	mu        sync.Mutex
	calls     int
	running   int
	maxActive int
	behave    func(ctx context.Context, call int) error
}

func (p *queueVisionProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.running++
	if p.running > p.maxActive {
		p.maxActive = p.running
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.running--
		p.mu.Unlock()
	}()
	if p.behave != nil {
		if err := p.behave(ctx, call); err != nil {
			return nil, err
		}
	}
	return &llm.GenerateResponse{Text: fmt.Sprintf("第 %d 次识图的描述", call)}, nil
}

func (p *queueVisionProvider) snapshot() (calls, maxActive int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.maxActive
}

// newQueueTestRuntime 的 backoff 传负数表示失败后立即可重试。
func newQueueTestRuntime(t *testing.T, provider *queueVisionProvider, timeout, backoff time.Duration) (*Runtime, *recallImageTestStore) {
	t.Helper()
	store := newRecallImageTestStore()
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(store)
	runtime.historyImageDescTimeout, runtime.historyImageDescBackoff = timeout, backoff
	return runtime, store
}

// multiImageEvent 模拟一条带封面和若干视频关键帧的消息；各帧内容哈希不同。
func multiImageEvent(t *testing.T, messageID string, frames int) (MessageEvent, []string) {
	t.Helper()
	imagePath, _ := writeRecallImageFixture(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: messageID, Time: time.Now().Unix()}
	var hashes []string
	for index := 0; index < frames; index++ {
		hash := fmt.Sprintf("%064x", index+1)
		data := map[string]string{"cached_file": imagePath, imageContentSHA256Key: hash}
		if index > 0 {
			data["source_type"] = "video_frame"
		}
		event.Segments = append(event.Segments, MessageSegment{Type: "image", Data: data})
		hashes = append(hashes, hash)
	}
	return event, hashes
}

func describedCount(store *recallImageTestStore, hashes []string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for _, hash := range hashes {
		if store.descriptions[hash].Description != "" {
			count++
		}
	}
	return count
}

// 线上那次的主因：排队时间被算进了识图超时。前台忙得比超时还久，图也不该超时。
func TestHistoryImageDescriptionTimeoutExcludesQueueWait(t *testing.T) {
	provider := &queueVisionProvider{}
	runtime, store := newQueueTestRuntime(t, provider, 200*time.Millisecond, time.Hour)
	event, hashes := multiImageEvent(t, "waits-long", 1)

	runtime.incActive(1)
	runtime.enqueueHistoryImageDescriptions(event)
	time.Sleep(600 * time.Millisecond)
	if calls, _ := provider.snapshot(); calls != 0 {
		t.Fatalf("background description ran while a reply was active: calls=%d", calls)
	}
	runtime.incActive(-1)
	waitForCondition(t, 2*time.Second, func() bool { return describedCount(store, hashes) == 1 })
}

// 一条消息的全部图（含每一帧）都要描述，而且一张一张来，不因为排在后面就超时。
func TestHistoryImageDescriptionDescribesEveryFrameSerially(t *testing.T) {
	provider := &queueVisionProvider{behave: func(ctx context.Context, _ int) error {
		select {
		case <-time.After(40 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	runtime, store := newQueueTestRuntime(t, provider, 150*time.Millisecond, time.Hour)
	event, hashes := multiImageEvent(t, "video-with-frames", 12)

	runtime.enqueueHistoryImageDescriptions(event)
	waitForCondition(t, 5*time.Second, func() bool { return describedCount(store, hashes) == len(hashes) })
	calls, maxActive := provider.snapshot()
	if calls != len(hashes) || maxActive != 1 {
		t.Fatalf("calls=%d maxActive=%d, want %d serial calls", calls, maxActive, len(hashes))
	}
}

// 新消息打断正在跑的后台识图：放回队首重来，不算失败，也不进重试退避。
func TestHistoryImageDescriptionPreemptionIsNotFailure(t *testing.T) {
	started := make(chan struct{}, 4)
	provider := &queueVisionProvider{behave: func(ctx context.Context, call int) error {
		started <- struct{}{}
		if call == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	runtime, store := newQueueTestRuntime(t, provider, 5*time.Second, time.Hour)
	event, hashes := multiImageEvent(t, "preempted", 1)

	runtime.enqueueHistoryImageDescriptions(event)
	<-started
	runtime.beginHistoryImageDescriptionForeground()
	time.Sleep(100 * time.Millisecond)
	if describedCount(store, hashes) != 0 {
		t.Fatal("description finished while foreground was active")
	}
	runtime.endHistoryImageDescriptionForeground()
	waitForCondition(t, 2*time.Second, func() bool { return describedCount(store, hashes) == 1 })
	runtime.historyImageDescMu.Lock()
	failed := len(runtime.historyImageDescFailed)
	runtime.historyImageDescMu.Unlock()
	if failed != 0 {
		t.Fatalf("preemption was recorded as failure: %d", failed)
	}
}

// 连续失败 3 次后自动路径放弃；真被读取（explicit）时还会再试一次。
func TestHistoryImageDescriptionGivesUpAfterMaxFailures(t *testing.T) {
	provider := &queueVisionProvider{behave: func(context.Context, int) error { return errors.New("vision unavailable") }}
	runtime, _ := newQueueTestRuntime(t, provider, time.Second, -1)
	event, hashes := multiImageEvent(t, "always-fails", 1)

	failures := func() int {
		runtime.historyImageDescMu.Lock()
		defer runtime.historyImageDescMu.Unlock()
		return runtime.historyImageDescFailed[hashes[0]].count
	}
	for attempt := 1; attempt <= historyImageDescriptionMaxFailures+2; attempt++ {
		runtime.enqueueHistoryImageDescriptions(event)
		want := min(attempt, historyImageDescriptionMaxFailures)
		waitForCondition(t, 2*time.Second, func() bool { return failures() == want })
		time.Sleep(20 * time.Millisecond)
	}
	if calls, _ := provider.snapshot(); calls != historyImageDescriptionMaxFailures {
		t.Fatalf("automatic retries = %d, want %d", calls, historyImageDescriptionMaxFailures)
	}
	runtime.enqueueHistoryImageDescriptionsNow(event)
	waitForCondition(t, 2*time.Second, func() bool {
		calls, _ := provider.snapshot()
		return calls == historyImageDescriptionMaxFailures+1
	})
}

// 前台等的图加急：bot 正忙、前台还在处理，也照样开工，而且不会被新消息打断。
func TestAwaitHistoryImageDescriptionsRunsWhileForegroundBusy(t *testing.T) {
	release := make(chan struct{})
	provider := &queueVisionProvider{behave: func(ctx context.Context, _ int) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	runtime, store := newQueueTestRuntime(t, provider, 5*time.Second, time.Hour)
	event, hashes := multiImageEvent(t, "user-is-asking", 1)

	runtime.incActive(1)
	defer runtime.incActive(-1)
	runtime.beginHistoryImageDescriptionForeground()
	defer runtime.endHistoryImageDescriptionForeground()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		runtime.awaitHistoryImageDescriptions(ctx, event)
	}()
	waitForCondition(t, 2*time.Second, func() bool { calls, _ := provider.snapshot(); return calls == 1 })
	// 另一条消息进来，不能打断正在做的加急任务。
	runtime.beginHistoryImageDescriptionForeground()
	runtime.endHistoryImageDescriptionForeground()
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("await did not return after the urgent description finished")
	}
	if describedCount(store, hashes) != 1 {
		t.Fatal("urgent description was not saved")
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("urgent job was restarted: calls=%d", calls)
	}
}

// 前台要等的图正在后台排队：提到队首，不排在别的消息后面。
func TestAwaitHistoryImageDescriptionsJumpsTheQueue(t *testing.T) {
	var order []string
	var orderMu sync.Mutex
	provider := &queueVisionProvider{}
	runtime, store := newQueueTestRuntime(t, provider, 5*time.Second, time.Hour)
	backlog, backlogHashes := multiImageEvent(t, "backlog", 6)
	asked, askedHashes := multiImageEvent(t, "asked", 1)
	askedHashes[0] = fmt.Sprintf("%064x", 999)
	asked.Segments[0].Data[imageContentSHA256Key] = askedHashes[0]
	provider.behave = func(context.Context, int) error {
		store.mu.Lock()
		described := len(store.descriptions)
		store.mu.Unlock()
		orderMu.Lock()
		order = append(order, fmt.Sprint(described))
		orderMu.Unlock()
		return nil
	}

	runtime.incActive(1)
	runtime.enqueueHistoryImageDescriptions(backlog)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runtime.awaitHistoryImageDescriptions(ctx, asked)
	if describedCount(store, askedHashes) != 1 || describedCount(store, backlogHashes) != 0 {
		t.Fatalf("asked image did not jump the busy backlog: asked=%d backlog=%d", describedCount(store, askedHashes), describedCount(store, backlogHashes))
	}
	runtime.incActive(-1)
	waitForCondition(t, 3*time.Second, func() bool { return describedCount(store, backlogHashes) == len(backlogHashes) })
}
