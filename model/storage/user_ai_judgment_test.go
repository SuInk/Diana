// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestUserAIJudgmentRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// 群里的机器人可能从没和 Diana 说过话，档案要能直接建出来。
	for i := 0; i < 3; i++ {
		if _, err := store.ObserveUserAI(ctx, "bot", "30001", "小汐", 0.9, "百科式总结"); err != nil {
			t.Fatal(err)
		}
	}
	profile, found, err := store.GetUserMemoryExact(ctx, "bot", "30001")
	if err != nil || !found {
		t.Fatalf("GetUserMemoryExact = %v, %v", found, err)
	}
	if profile.AI == nil || !profile.AI.Likely || profile.AI.Observations != 3 || profile.DisplayName != "小汐" {
		t.Fatalf("profile = %+v, ai = %+v", profile, profile.AI)
	}
	refs, err := store.ListLikelyAIUsers(ctx)
	if err != nil || len(refs) != 1 || refs[0] != (assistant.UserAIRef{BotProfileID: "bot", UserID: "30001"}) {
		t.Fatalf("ListLikelyAIUsers = %+v, %v", refs, err)
	}

	judgment, err := store.SetUserAIOverride(ctx, "bot", "30001", assistant.UserAIOverrideHuman)
	if err != nil || judgment.Likely || judgment.Observations != 3 {
		t.Fatalf("SetUserAIOverride = %+v, %v", judgment, err)
	}
	if refs, _ := store.ListLikelyAIUsers(ctx); len(refs) != 0 {
		t.Fatalf("标成真人后不应再列出：%+v", refs)
	}

	// 档案的其余写入不能冲掉 AI 判断。
	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, ProfileID: "bot", UserID: "30001", SenderName: "小汐"}
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{FavorabilityDelta: 1}); err != nil {
		t.Fatal(err)
	}
	profile, _, _ = store.GetUserMemoryExact(ctx, "bot", "30001")
	if profile.AI == nil || profile.AI.Override != assistant.UserAIOverrideHuman {
		t.Fatalf("UpdateUserMemory 之后 AI 判断丢了：%+v", profile.AI)
	}
}
