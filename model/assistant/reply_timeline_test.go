// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"
)

// 时间线把各阶段换成相对收到消息的毫秒数，每个阶段只记第一次，整轮只写一条。
func TestReplyTimelineRecordsFirstOccurrenceOnce(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "12345", UserID: "23456", MessageID: "m1", Time: time.Now().Unix()}
	ctx := runtime.withDebugTraceContext(context.Background(), event)
	received := replyTimelineFromContext(ctx).received

	markReplyStage(ctx, replyStageAgentStarted, received.Add(800*time.Millisecond))
	markReplyStage(ctx, replyStageFirstSent, received.Add(2500*time.Millisecond))
	// 多步 Agent 后面的发送不覆盖第一条。
	markReplyStage(withSubtaskDebugTrace(ctx, "task"), replyStageFirstSent, received.Add(9*time.Second))
	runtime.recordReplyTimeline(ctx, "replied")
	runtime.recordReplyTimeline(ctx, "replied")

	var timelines []map[string]any
	for _, entry := range logs.entriesSnapshot() {
		if entry.Metadata["phase"] == "reply_timeline" {
			timelines = append(timelines, entry.Metadata)
		}
	}
	if len(timelines) != 1 {
		t.Fatalf("reply_timeline 记录 %d 条，应为 1", len(timelines))
	}
	stages, _ := timelines[0]["stages"].(map[string]any)
	if stages["agent_started_ms"] != int64(800) || stages["first_sent_ms"] != int64(2500) {
		t.Fatalf("stages = %#v", stages)
	}
}

// 调试关着时没有轨迹状态，打点和写时间线都是空操作。
func TestReplyTimelineNoopWithoutDebugTrace(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	ctx := context.Background()
	markReplyStage(ctx, replyStageFirstSent, time.Now())
	runtime.recordReplyTimeline(ctx, "replied")
	if entries := logs.entriesSnapshot(); len(entries) != 0 {
		t.Fatalf("没有调试轨迹时不该写日志：%#v", entries)
	}
}
