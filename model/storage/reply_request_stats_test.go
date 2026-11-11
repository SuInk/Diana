// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 每轮按 message_id 归并：第一次请求的输入和缓存单独算，工具步骤的请求只进合计；
// 续接结论按同一个 message_id 对上；别的群、别的机器人、非主回复的调用、太早的都不算。
func TestGroupReplyRequestStats(t *testing.T) {
	ctx := context.Background()
	store := openInboundTestStore(t, filepath.Join(t.TempDir(), "stats.db"))
	defer func() { _ = store.Close() }()
	base := time.Now().Add(-time.Hour)
	at := 0
	log := func(action string, metadata map[string]any) {
		t.Helper()
		at++
		if err := store.AppendLog(ctx, applog.Entry{Action: action, Target: "t", CreatedAt: base.Add(time.Duration(at) * time.Second), Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
	}
	usage := func(message, group, profile, purpose string, input, cached int) {
		log("llm_usage", map[string]any{"message_id": message, "group_id": group, "profile_id": profile, "purpose": purpose, "model": "swe", "input_tokens": input, "cached_input_tokens": cached})
	}
	decision := func(message string, continued bool, reason string) {
		log("append_context", map[string]any{"message_id": message, "group_id": "g", "profile_id": "bot", "continued": continued, "reason": reason})
	}
	// m1：续接，两步，第一步命中 90%。
	decision("m1", true, "")
	usage("m1", "g", "bot", "reply", 50000, 45000)
	usage("m1", "g", "bot", "reply", 52000, 51000)
	// m2：太长重组，第一步没命中。
	decision("m2", false, "too_long")
	usage("m2", "g", "bot", "reply", 40000, 2000)
	// m3：升级前的轮次，没有续接记录。
	usage("m3", "g", "bot", "reply", 60000, 30000)
	// 不该算进来的：别的群、别的机器人、非主回复。
	usage("x1", "other", "bot", "reply", 99999, 0)
	usage("x2", "g", "another-bot", "reply", 99999, 0)
	usage("m1", "g", "bot", "reply_send_audit", 2000, 0)

	stats, err := store.GroupReplyRequestStats(ctx, "bot", "g", base, 50)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Turns != 3 || stats.Requests != 4 {
		t.Fatalf("turns=%d requests=%d, want 3/4", stats.Turns, stats.Requests)
	}
	if stats.FirstInputMedian != 50000 || stats.FirstInputMax != 60000 {
		t.Fatalf("first input median=%d max=%d", stats.FirstInputMedian, stats.FirstInputMax)
	}
	wantFirst := 100 * float64(45000+2000+30000) / float64(50000+40000+60000)
	wantAll := 100 * float64(45000+51000+2000+30000) / float64(50000+52000+40000+60000)
	if math.Abs(stats.FirstCachedPercent-wantFirst) > 0.01 || math.Abs(stats.AllCachedPercent-wantAll) > 0.01 {
		t.Fatalf("cached first=%.2f all=%.2f, want %.2f/%.2f", stats.FirstCachedPercent, stats.AllCachedPercent, wantFirst, wantAll)
	}
	if stats.Misses != 1 {
		t.Fatalf("misses=%d, want 1", stats.Misses)
	}
	if stats.Continued != 1 || stats.Rebuilt != 1 || stats.Unrecorded != 1 || stats.RebuildReasons["too_long"] != 1 {
		t.Fatalf("continued=%d rebuilt=%d unrecorded=%d reasons=%v", stats.Continued, stats.Rebuilt, stats.Unrecorded, stats.RebuildReasons)
	}

	// 只取最近 limit 轮。
	latest, err := store.GroupReplyRequestStats(ctx, "bot", "g", base, 1)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Turns != 1 || latest.FirstInputMax != 60000 {
		t.Fatalf("limit 1 should keep only the newest turn: %+v", latest)
	}
	// since 之后没有记录就是零值。
	empty, err := store.GroupReplyRequestStats(ctx, "bot", "g", time.Now().Add(time.Minute), 50)
	if err != nil || empty.Turns != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}
