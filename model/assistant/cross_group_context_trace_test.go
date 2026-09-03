// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func crossGroupTraceRuntime(t *testing.T, debug bool) (*Runtime, *captureAppLogs, *crossGroupSearchCounter) {
	t.Helper()
	logs := &captureAppLogs{}
	store := &crossGroupSearchCounter{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	runtime := NewRuntime(BotConfig{
		CrossGroupMemoryEnabled: boolPointer(true),
		DebugModeEnabled:        debug,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	runtime.SetAppLogWriter(logs)
	return runtime, logs, store
}

func crossGroupTraceEntry(t *testing.T, logs *captureAppLogs) map[string]any {
	t.Helper()
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action == "diana.cross_group_context" {
			return entry.Metadata
		}
	}
	t.Fatalf("没有记录跨群上下文追踪：%#v", logs.entriesSnapshot())
	return nil
}

// 词信号不足是最容易踩的一道闸（「在吗」「好的」都过不去），必须能从日志看出来。
func TestCrossGroupTraceReportsWeakQuerySignal(t *testing.T) {
	runtime, logs, store := crossGroupTraceRuntime(t, true)
	runtime.contextHistory(MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: "m1", Time: 100,
		RawMessage: "在吗",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}},
	})
	metadata := crossGroupTraceEntry(t, logs)
	reason, _ := metadata["skip_reason"].(string)
	if !strings.Contains(reason, "信号不足") {
		t.Fatalf("应当记录词信号不足，实际 skip_reason=%q", reason)
	}
	if store.searches != 0 {
		t.Fatalf("信号不足时不该真的去检索，实际 %d 次", store.searches)
	}
}

// 走到检索之后要能看出每道过滤刷掉多少，否则还是分不清卡在哪。
func TestCrossGroupTraceReportsFunnelCounts(t *testing.T) {
	runtime, logs, _ := crossGroupTraceRuntime(t, true)
	runtime.contextHistory(crossGroupProbeEvent())

	metadata := crossGroupTraceEntry(t, logs)
	if reason, ok := metadata["skip_reason"]; ok {
		t.Fatalf("这次应当走完检索，实际提前退出：%v", reason)
	}
	for _, key := range []string{"terms", "sample_terms", "candidates", "dropped", "authors", "selected", "duration_ms"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("漏斗缺少字段 %q：%#v", key, metadata)
		}
	}
	dropped, ok := metadata["dropped"].(map[string]int)
	if !ok {
		t.Fatalf("dropped 不是计数表：%#v", metadata["dropped"])
	}
	for _, key := range []string{"same_group", "outbound", "platform", "topic", "text"} {
		if _, ok := dropped[key]; !ok {
			t.Errorf("dropped 缺少 %q：%#v", key, dropped)
		}
	}
}

// 关掉调试模式就不该写日志：这条链每轮都会走，常态下不能产生额外开销。
func TestCrossGroupTraceStaysSilentWithoutDebugMode(t *testing.T) {
	runtime, logs, _ := crossGroupTraceRuntime(t, false)
	runtime.contextHistory(crossGroupProbeEvent())
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action == "diana.cross_group_context" {
			t.Fatalf("未开调试模式却写了追踪日志：%#v", entry)
		}
	}
}
