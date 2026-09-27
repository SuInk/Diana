// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 生图次数是限额的账本，重启后必须接着算，不然重启一次就白送一天的份额。
func TestMediaGenerationUsageSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media-usage.db")
	ctx := context.Background()
	key := assistant.MediaGenerationKey{ProfileID: "qq", Platform: "onebot-v11", Kind: assistant.MediaGenerationImage, Day: "2026-09-27", GroupID: "20001", UserID: "20002"}
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaGeneration(ctx, key, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaGeneration(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	counts, err := reopened.MediaGenerationCounts(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Group != 3 || counts.User != 3 {
		t.Fatalf("重启后 counts = %#v，应当是群 3、人 3", counts)
	}
}

// 群和人各算各的：同一个人在别的群画的算进他自己，不算进这个群；另一台机器人、
// 另一天、另一种媒体都不相干。
func TestMediaGenerationUsageScopes(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "media-scopes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := assistant.MediaGenerationKey{ProfileID: "qq", Platform: "onebot-v11", Kind: assistant.MediaGenerationImage, Day: "2026-09-27", GroupID: "20001", UserID: "20002"}
	otherGroup := base
	otherGroup.GroupID = "20003"
	private := base
	private.GroupID = ""
	otherBot := base
	otherBot.ProfileID = "tg"
	yesterday := base
	yesterday.Day = "2026-09-26"
	video := base
	video.Kind = "video"
	for _, key := range []assistant.MediaGenerationKey{base, otherGroup, private, otherBot, yesterday, video} {
		if err := store.AddMediaGeneration(ctx, key, 1); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := store.MediaGenerationCounts(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Group != 1 || counts.User != 3 {
		t.Fatalf("counts = %#v，应当是本群 1、这个人 3（本群、别的群、私聊）", counts)
	}
	counts, err = store.MediaGenerationCounts(ctx, private)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Group != 0 || counts.User != 3 {
		t.Fatalf("私聊 counts = %#v", counts)
	}
}

// 只清过了保留期的天，当天和近几天的都留着。
func TestMediaGenerationUsagePrunesOldDays(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "media-prune.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	old := assistant.MediaGenerationKey{ProfileID: "qq", Kind: assistant.MediaGenerationImage, Day: "2026-07-01", GroupID: "20001", UserID: "20002"}
	recent := old
	recent.Day = "2026-09-20"
	today := old
	today.Day = "2026-09-27"
	for _, key := range []assistant.MediaGenerationKey{old, recent, today} {
		if err := store.AddMediaGeneration(ctx, key, 1); err != nil {
			t.Fatal(err)
		}
	}
	for key, want := range map[assistant.MediaGenerationKey]int64{old: 0, recent: 1, today: 1} {
		counts, err := store.MediaGenerationCounts(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if counts.Group != want {
			t.Fatalf("%s: group = %d，want %d", key.Day, counts.Group, want)
		}
	}
}

// 清理每天只跑一次：同一天再写不再扫表，换了一天才再清，而且走 day 索引。
func TestMediaGenerationUsagePrunesOncePerDay(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "media-prune-once.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	today := assistant.MediaGenerationKey{ProfileID: "qq", Kind: assistant.MediaGenerationImage, Day: "2026-09-27", GroupID: "20001", UserID: "20002"}
	if err := store.AddMediaGeneration(ctx, today, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO media_generation_usage (profile_id, platform, kind, day, scope, subject_id, count, updated_at) VALUES ('qq', '', 'image', '2026-01-01', 'group', '20001', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaGeneration(ctx, today, 1); err != nil {
		t.Fatal(err)
	}
	ancient := today
	ancient.Day = "2026-01-01"
	if counts, _ := store.MediaGenerationCounts(ctx, ancient); counts.Group != 1 {
		t.Fatalf("同一天第二次写入不该再清理，ancient = %#v", counts)
	}
	tomorrow := today
	tomorrow.Day = "2026-09-28"
	if err := store.AddMediaGeneration(ctx, tomorrow, 1); err != nil {
		t.Fatal(err)
	}
	if counts, _ := store.MediaGenerationCounts(ctx, ancient); counts.Group != 0 {
		t.Fatalf("换了一天应当清掉过期记录，ancient = %#v", counts)
	}
	var plan string
	if err := store.db.QueryRowContext(ctx, `EXPLAIN QUERY PLAN DELETE FROM media_generation_usage WHERE day < '2026-01-01'`).Scan(new(int), new(int), new(int), &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "idx_media_generation_usage_day") {
		t.Fatalf("清理应当走 day 索引：%s", plan)
	}
}
