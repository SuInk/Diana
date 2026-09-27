// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// GroupHistoryAnchors 用内存里的入站记录模拟 SQLite 的实现：带 seq 的群消息，新的在前。
func (s *memoryInboundEventStore) GroupHistoryAnchors(_ context.Context, _ string, groupID string, limit int) ([]HistoryAnchor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type candidate struct {
		anchor HistoryAnchor
		time   int64
	}
	var candidates []candidate
	for _, record := range s.records {
		event := record.item.Event
		seq, ok := parseMessageSeq(event.MessageSeq)
		if event.Kind != EventKindGroup || event.GroupID != groupID || !ok || event.MessageID == "" {
			continue
		}
		candidates = append(candidates, candidate{anchor: HistoryAnchor{MessageID: event.MessageID, Seq: seq}, time: event.Time})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].time > candidates[j].time })
	anchors := make([]HistoryAnchor, 0, limit)
	for _, item := range candidates {
		if len(anchors) == limit {
			break
		}
		anchors = append(anchors, item.anchor)
	}
	return anchors, nil
}

// snowLumaHistoryChannel 按 2026-09-27 线上实测的 SnowLuma v1.14.10 行为模拟群历史：
//   - 不带 message_id：从它最后收到的那条（observed）往回翻，永远看不到更新的；
//   - 带 message_id 往新方向：按 30 个 seq 一段往后拉，一段的末尾超过群里最新 seq 14
//     个以上时这段为空，整个请求就此打住；
//   - 从服务器拉到的消息会记进它自己的库，observed 跟着往前推。
type snowLumaHistoryChannel struct {
	*queueTestChannel
	mu       sync.Mutex
	times    map[int64]int64
	head     int64
	observed int64
	requests []map[string]any
}

func newSnowLumaHistoryChannel(base int64, seqs []int64, observed int64) *snowLumaHistoryChannel {
	channel := &snowLumaHistoryChannel{queueTestChannel: newQueueTestChannel(), times: map[int64]int64{}, observed: observed}
	for _, seq := range seqs {
		channel.times[seq] = base + (seq-100)*10
		channel.head = max(channel.head, seq)
	}
	return channel
}

func (c *snowLumaHistoryChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action != "get_group_msg_history" {
		return c.queueTestChannel.CallAPI(ctx, action, params)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make(map[string]any, len(params))
	for key, value := range params {
		copied[key] = value
	}
	c.requests = append(c.requests, copied)

	count := intFromAny(params["count"])
	if count <= 0 {
		count = 20
	}
	anchor, reverse := c.observed, true
	if raw, ok := params["message_id"]; ok {
		seq, known := parseMessageSeq(fmt.Sprint(raw))
		if _, exists := c.times[seq]; !known || !exists {
			return map[string]any{"messages": []any{}}, nil
		}
		anchor = seq
		if value, ok := params["reverse_order"].(bool); ok {
			reverse = value
		}
	}

	var picked []int64
	if reverse {
		for seq := anchor; seq > 0 && len(picked) < count; seq-- {
			if _, ok := c.times[seq]; ok {
				picked = append(picked, seq)
			}
		}
	} else {
		for start := anchor; len(picked) < count; start += 30 {
			end := start + 29
			if end-c.head > 14 {
				break
			}
			for seq := start; seq <= min(end, c.head) && len(picked) < count; seq++ {
				if _, ok := c.times[seq]; ok {
					picked = append(picked, seq)
				}
			}
		}
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i] < picked[j] })
	messages := make([]any, 0, len(picked))
	for _, seq := range picked {
		messages = append(messages, historyTestMessage(seq, c.times[seq], fmt.Sprintf("群消息 %d", seq)))
		c.observed = max(c.observed, seq)
	}
	return map[string]any{"messages": messages}, nil
}

func (c *snowLumaHistoryChannel) forwardAnchors() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var anchors []int64
	for _, request := range c.requests {
		if reverse, ok := request["reverse_order"].(bool); ok && !reverse {
			if seq, ok := parseMessageSeq(fmt.Sprint(request["message_id"])); ok {
				anchors = append(anchors, seq)
			}
		}
	}
	return anchors
}

func seqRange(from, to int64) []int64 {
	seqs := make([]int64, 0, to-from+1)
	for seq := from; seq <= to; seq++ {
		seqs = append(seqs, seq)
	}
	return seqs
}

// 2026-09-27 线上：miku 宕机 25 分钟，重连后整体回补点了几次都是「新入库 0 条」。
// SnowLuma 不带锚点时只从它最后收到的那条往回翻，断线那段永远在范围外；要拿库里的
// 消息当锚点往新方向翻才补得回来。
func TestHistoryBackfillWalksForwardPastStaleBridgeAnchor(t *testing.T) {
	withGapTestDelays(t, nil, nil)
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-2 * time.Hour).Unix()
	for seq := int64(60); seq <= 100; seq++ {
		event := seqTestEvent(seq, base+(seq-100)*10, "10001")
		if _, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event); err != nil {
			t.Fatal(err)
		}
	}
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base}}
	channel := newSnowLumaHistoryChannel(base, seqRange(60, 110), 100)
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	startTestRuntime(t, runtime)
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed")
	})

	for seq := int64(101); seq <= 110; seq++ {
		if id := fmt.Sprintf("group:123:%d", seq); !store.hasEvent(id) {
			t.Fatalf("forward backfill did not enqueue %s", id)
		}
	}
	// 锚点离已知最新（100）不到 15 时 SnowLuma 会整段返回空，第一次必须退到 85 及以前。
	anchors := channel.forwardAnchors()
	if len(anchors) == 0 || anchors[0] > 100-historyForwardAnchorLag {
		t.Fatalf("forward anchors = %v, first must be <= %d", anchors, 100-historyForwardAnchorLag)
	}
}

// 断线久、漏的多：一轮翻不完，要以新拿到的消息为锚点继续往后翻，直到推不动。
func TestHistoryBackfillForwardKeepsWalkingUntilHead(t *testing.T) {
	withGapTestDelays(t, nil, nil)
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-2 * time.Hour).Unix()
	for seq := int64(60); seq <= 100; seq++ {
		event := seqTestEvent(seq, base+(seq-100)*10, "10001")
		if _, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event); err != nil {
			t.Fatal(err)
		}
	}
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base}}
	channel := newSnowLumaHistoryChannel(base, seqRange(60, 190), 100)
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	startTestRuntime(t, runtime)
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed")
	})

	for seq := int64(101); seq <= 190; seq++ {
		if id := fmt.Sprintf("group:123:%d", seq); !store.hasEvent(id) {
			t.Fatalf("forward backfill stopped before %s (anchors %v)", id, channel.forwardAnchors())
		}
	}
	if rounds := len(channel.forwardAnchors()); rounds > historyForwardMaxRounds {
		t.Fatalf("forward rounds = %d, want <= %d", rounds, historyForwardMaxRounds)
	}
}

// 往回翻页要带 message_id：SnowLuma 不认 message_seq，只带它时第二页就是第一页再来一遍。
func TestHistoryBackfillPagesBackwardWithMessageID(t *testing.T) {
	withGapTestDelays(t, nil, nil)
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-2 * time.Hour).Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base - 10000}}
	channel := newSnowLumaHistoryChannel(base, seqRange(1, 250), 250)
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	startTestRuntime(t, runtime)
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed")
	})

	// 第一页 151~250，之后每页从上一页最旧那条往回，直到凑满扫描上限。
	for _, seq := range []int64{250, 151, 60} {
		if id := fmt.Sprintf("group:123:%d", seq); !store.hasEvent(id) {
			t.Fatalf("backward paging did not reach %s", id)
		}
	}
}
