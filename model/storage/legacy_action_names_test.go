// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

// 运行时的日志动作名改过两轮：assistant.* -> chatbot.* -> diana.*。
//
// 升级不会重写历史日志行，所以老库里存的还是老名字。查询侧漏掉哪个名字，那段时间的
// 记录就会整段查不出来——界面上看起来像「日志没了」，而不是像一个 Bug，因此格外容易
// 一直没人发现。这个测试盯着这件事：写进老名字，仍然要能读出来。
func TestInboundEventDebugTraceReadsLegacyActionNames(t *testing.T) {
	for _, action := range []string{"diana.debug_trace", "chatbot.debug_trace"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "legacy-trace.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()

			event := assistant.MessageEvent{
				Kind:      assistant.EventKindGroup,
				GroupID:   "group-1",
				UserID:    "user-1",
				MessageID: "message-1",
				Time:      time.Now().Unix(),
			}
			eventID, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", event)
			if err != nil || !inserted {
				t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
			}
			if err := store.AppendLog(ctx, applog.Entry{
				Kind: applog.KindDebug, Action: action, Target: event.MessageID,
				Message:  "模型请求完成",
				Metadata: map[string]any{"kind": "group", "group_id": "group-1", "user_id": "user-1", "phase": "model_request"},
			}); err != nil {
				t.Fatal(err)
			}

			_, steps, found, err := store.InboundEventDebugTrace(ctx, eventID)
			if err != nil {
				t.Fatal(err)
			}
			if !found || len(steps) != 1 || steps[0].Message != "模型请求完成" {
				t.Fatalf("按 %q 存的调试轨迹读不出来：found=%v steps=%#v", action, found, steps)
			}
		})
	}
}

// 用量统计同样按动作名过滤，少认一个名字会让那段时间的 token 统计凭空归零。
func TestInboundEventTokenUsageReadsLegacyActionNames(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "legacy-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	for _, action := range []string{"diana.llm_usage", "chatbot.llm_usage", "assistant.llm_usage"} {
		if err := store.AppendLog(ctx, applog.Entry{
			Action:    action,
			Target:    "message-" + action,
			CreatedAt: now.Add(-time.Minute),
			Metadata:  map[string]any{"input_tokens": 10, "output_tokens": 5},
		}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := store.DashboardStatsForDay(ctx, now, "")
	if err != nil {
		t.Fatal(err)
	}
	// 三个名字各记一条，一条都不能漏。
	if stats.LLMCalls != 3 || stats.LLMInputTokens != 30 || stats.LLMOutputTokens != 15 {
		t.Fatalf("totals = calls:%d input:%d output:%d, want 3/30/15", stats.LLMCalls, stats.LLMInputTokens, stats.LLMOutputTokens)
	}
}
