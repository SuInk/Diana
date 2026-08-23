// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestStructuredMemoryVersionsSourcesAndForget(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	request := assistant.MemoryWriteRequest{
		SubjectUserID:   "10001",
		SubjectName:     "Alice",
		Session:         "group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: "m1",
		SourceEventTime: time.Unix(100, 0),
		Candidates: []assistant.MemoryCandidate{{
			Action:     assistant.MemoryActionUpsert,
			Key:        "preference.food.spicy",
			Kind:       assistant.MemoryKindPreference,
			Topic:      "饮食偏好",
			Entity:     "辣味食物",
			Content:    "Alice偏好辣味食物",
			Evidence:   "我喜欢吃辣",
			SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.98,
			Importance: 0.72,
			Visibility: assistant.MemoryVisibilityUser,
		}},
	}
	written, err := store.ApplyMemoryCandidates(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].Version != 1 || written[0].SupersedesID != "" {
		t.Fatalf("first write = %#v", written)
	}
	firstID := written[0].ID

	request.SourceMessageID = "m2"
	request.SourceEventTime = time.Unix(200, 0)
	written, err = store.ApplyMemoryCandidates(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].ID != firstID || written[0].Version != 1 {
		t.Fatalf("confirmation created another version: %#v", written)
	}
	var sourceCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_sources WHERE memory_id = ?`, firstID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 2 {
		t.Fatalf("source count = %d, want 2", sourceCount)
	}

	request.SourceMessageID = "m3"
	request.SourceEventTime = time.Unix(300, 0)
	request.Candidates[0].Content = "Alice现在不喜欢辣味食物"
	request.Candidates[0].Evidence = "我现在不吃辣了"
	written, err = store.ApplyMemoryCandidates(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].Version != 2 || written[0].SupersedesID != firstID || written[0].ID == firstID {
		t.Fatalf("conflict version = %#v", written)
	}
	secondID := written[0].ID
	var firstStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM memory_items WHERE id = ?`, firstID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != string(assistant.MemoryStatusSuperseded) {
		t.Fatalf("first status = %q", firstStatus)
	}

	// A crash after applying the memory but before completing its queue job must
	// not create a third version when the LLM phrases the retry differently.
	request.Candidates[0].Content = "Alice已经停止食用辣味食物"
	written, err = store.ApplyMemoryCandidates(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].ID != secondID || written[0].Version != 2 {
		t.Fatalf("same source retry was not idempotent: %#v", written)
	}

	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "10001",
		Session:       "group:999",
		Now:           time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != secondID || items[0].Content != "Alice现在不喜欢辣味食物" {
		t.Fatalf("active global memory = %#v", items)
	}

	request.SourceMessageID = "m4"
	request.Candidates[0] = assistant.MemoryCandidate{
		Action:     assistant.MemoryActionForget,
		Key:        "preference.food.spicy",
		Kind:       assistant.MemoryKindPreference,
		Topic:      "饮食偏好",
		SourceType: assistant.MemorySourceExplicit,
		Confidence: 0.99,
		Importance: 0.9,
		Visibility: assistant.MemoryVisibilityUser,
	}
	written, err = store.ApplyMemoryCandidates(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].Status != assistant.MemoryStatusForgotten {
		t.Fatalf("forget result = %#v", written)
	}
	items, err = store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{SubjectUserID: "10001", Session: "group:123", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("forgotten memory is still active: %#v", items)
	}
}

func TestStructuredMemoryVisibilityAndExpiry(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	base := assistant.MemoryWriteRequest{
		SubjectUserID:   "user-a",
		SubjectName:     "Alice",
		Session:         "group:one",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "one",
		SourceMessageID: "global",
		SourceEventTime: now,
		Candidates: []assistant.MemoryCandidate{{
			Action:     assistant.MemoryActionUpsert,
			Key:        "profile.pet.cat",
			Kind:       assistant.MemoryKindFact,
			Topic:      "宠物",
			Content:    "Alice养了一只猫",
			SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.98,
			Importance: 0.8,
			Visibility: assistant.MemoryVisibilityUser,
		}},
	}
	if _, err := store.ApplyMemoryCandidates(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.SourceMessageID = "sensitive"
	base.Candidates[0] = assistant.MemoryCandidate{
		Action:        assistant.MemoryActionUpsert,
		Key:           "health.current.medication",
		Kind:          assistant.MemoryKindFact,
		Topic:         "健康",
		Content:       "Alice正在服用某种药物",
		SourceType:    assistant.MemorySourceExplicit,
		Confidence:    0.98,
		Importance:    0.9,
		Visibility:    assistant.MemoryVisibilityUser,
		Sensitive:     true,
		RetentionDays: 1,
	}
	if _, err := store.ApplyMemoryCandidates(ctx, base); err != nil {
		t.Fatal(err)
	}

	otherSession, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "user-a",
		Session:       "group:two",
		Now:           now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherSession) != 1 || otherSession[0].Key != "profile.pet.cat" {
		t.Fatalf("cross-session memories = %#v", otherSession)
	}
	isolatedSession, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "user-a", Session: "group:two", Now: now, CurrentSessionOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(isolatedSession) != 0 {
		t.Fatalf("isolated session memories = %#v", isolatedSession)
	}
	sameSession, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "user-a",
		Session:       "group:one",
		Now:           now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sameSession) != 2 {
		t.Fatalf("same-session memories = %#v", sameSession)
	}
	expired, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "user-a",
		Session:       "group:one",
		Now:           now.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].Key != "profile.pet.cat" {
		t.Fatalf("expired memories = %#v", expired)
	}
}

func TestStructuredMemoryCrossGroupOnlyReturnsSafeNamespaceMemories(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "cross-group-memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	writeSummary := func(session, groupID, key, content string, sensitive bool) {
		t.Helper()
		_, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			Session: session, EventKind: assistant.EventKindGroup, GroupID: groupID,
			SourceMessageID: key, SourceEventTime: now,
			Candidates: []assistant.MemoryCandidate{{
				Action: assistant.MemoryActionUpsert, Key: key, Kind: assistant.MemoryKindSummary,
				Topic: "群聊主题", Content: content, SourceType: assistant.MemorySourceSummary,
				Confidence: 0.96, Importance: 0.8, Visibility: assistant.MemoryVisibilitySession,
				Sensitive: sensitive, RetentionDays: 365,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	writeSummary("bot-a:group:one", "one", "summary.2026-08-01.public", "公开项目决定", false)
	writeSummary("bot-a:group:one", "one", "summary.2026-08-01.private", "群内敏感信息", true)
	writeSummary("bot-b:group:other", "other", "summary.2026-08-01.foreign", "另一个机器人内容", false)

	currentOnly, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		Session: "bot-a:group:two", SubjectUserID: "alice", Now: now, CurrentSessionOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(currentOnly) != 0 {
		t.Fatalf("current-only memories = %#v", currentOnly)
	}
	crossGroup, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		Session: "bot-a:group:two", SubjectUserID: "alice", Now: now,
		CrossGroup: true, GroupSessionPrefix: "bot-a:group:",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(crossGroup) != 1 || crossGroup[0].Key != "summary.2026.08.01.public" {
		t.Fatalf("cross-group memories = %#v", crossGroup)
	}
}

func TestStructuredMemoryQueryPrioritizesLexicalMatchesBeforeCandidateLimit(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory-candidates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	_, err = store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
		Session: "group:one", EventKind: assistant.EventKindGroup, GroupID: "one", SourceMessageID: "cat", SourceEventTime: now.AddDate(-2, 0, 0),
		Candidates: []assistant.MemoryCandidate{{
			Action: assistant.MemoryActionUpsert, Key: "pet.cat.food", Kind: assistant.MemoryKindFact,
			Topic: "猫粮", Entity: "小白", Content: "小白喜欢鸡肉猫粮", SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.95, Importance: 0.55, Visibility: assistant.MemoryVisibilitySession,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 30; index++ {
		_, err = store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			Session: "group:one", EventKind: assistant.EventKindGroup, GroupID: "one", SourceMessageID: fmt.Sprintf("noise-%d", index), SourceEventTime: now,
			Candidates: []assistant.MemoryCandidate{{
				Action: assistant.MemoryActionUpsert, Key: fmt.Sprintf("noise.%d", index), Kind: assistant.MemoryKindFact,
				Topic: "无关资料", Content: fmt.Sprintf("第%d条高重要度无关信息", index), SourceType: assistant.MemorySourceExplicit,
				Confidence: 0.99, Importance: 0.99, Visibility: assistant.MemoryVisibilitySession,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		Session: "group:one", Text: "小白的猫粮", SearchTerms: []string{"小白", "猫粮"}, Now: now, MaxCandidates: 5, CurrentSessionOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 || items[0].Key != "pet.cat.food" {
		t.Fatalf("lexical candidate pool = %#v", items)
	}
}

func TestMemoryJobQueueIsDurableAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	payload := assistant.MemoryJobPayload{
		Kind:    assistant.MemoryJobEvent,
		Session: "group:123",
		Event: assistant.MessageEvent{
			Kind:      assistant.EventKindGroup,
			GroupID:   "123",
			UserID:    "10001",
			MessageID: "m1",
			Time:      100,
			Segments:  []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "我喜欢吃辣"}}},
		},
	}
	id, inserted, err := store.EnqueueMemoryJob(ctx, payload)
	if err != nil || !inserted || id == "" {
		t.Fatalf("first enqueue id=%q inserted=%v err=%v", id, inserted, err)
	}
	duplicateID, inserted, err := store.EnqueueMemoryJob(ctx, payload)
	if err != nil || inserted || duplicateID != id {
		t.Fatalf("duplicate enqueue id=%q inserted=%v err=%v", duplicateID, inserted, err)
	}

	job, ok, err := store.ClaimNextMemoryJob(ctx, "worker-old", time.Now().Add(time.Minute))
	if err != nil || !ok || job.ID != id || job.Attempts != 1 {
		t.Fatalf("first claim job=%#v ok=%v err=%v", job, ok, err)
	}
	if err := store.ReleaseMemoryJobLeases(ctx, ""); err != nil {
		t.Fatal(err)
	}
	job, ok, err = store.ClaimNextMemoryJob(ctx, "worker-new", time.Now().Add(time.Minute))
	if err != nil || !ok || job.Attempts != 2 {
		t.Fatalf("recovered claim job=%#v ok=%v err=%v", job, ok, err)
	}
	lastError := strings.Repeat("temporary-error-", 80) + "tail-marker"
	if err := store.RetryMemoryJob(ctx, id, "worker-new", time.Now().Add(time.Hour), lastError); err != nil {
		t.Fatal(err)
	}
	var storedError string
	if err := store.db.QueryRowContext(ctx, `SELECT last_error FROM memory_jobs WHERE id = ?`, id).Scan(&storedError); err != nil {
		t.Fatal(err)
	}
	if storedError != lastError {
		t.Fatalf("stored error length = %d, want %d", len(storedError), len(lastError))
	}
	if _, ok, err := store.ClaimNextMemoryJob(ctx, "worker-new", time.Now().Add(time.Minute)); err != nil || ok {
		t.Fatalf("future retry was claimable ok=%v err=%v", ok, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE memory_jobs SET available_at = ? WHERE id = ?`, time.Now().Add(-time.Second).UnixNano(), id); err != nil {
		t.Fatal(err)
	}
	job, ok, err = store.ClaimNextMemoryJob(ctx, "worker-new", time.Now().Add(time.Minute))
	if err != nil || !ok || job.Attempts != 3 {
		t.Fatalf("retry claim job=%#v ok=%v err=%v", job, ok, err)
	}
	if err := store.CompleteMemoryJob(ctx, id, "worker-new"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNextMemoryJob(ctx, "worker-new", time.Now().Add(time.Minute)); err != nil || ok {
		t.Fatalf("completed job was claimable ok=%v err=%v", ok, err)
	}
}

func TestStructuredMemoryLexicalScoreWeighsLongTermsOverShortNoise(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory-weight.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	write := func(key, topic, content string, importance float64) {
		t.Helper()
		_, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			Session: "group:one", EventKind: assistant.EventKindGroup, GroupID: "one", SourceMessageID: key, SourceEventTime: now,
			Candidates: []assistant.MemoryCandidate{{
				Action: assistant.MemoryActionUpsert, Key: key, Kind: assistant.MemoryKindPreference,
				Topic: topic, Content: content, SourceType: assistant.MemorySourceExplicit,
				Confidence: 0.95, Importance: importance, Visibility: assistant.MemoryVisibilitySession,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 目标记忆只命中一个三字词；噪声记忆重要度更高、只撞上一个双字词。
	// 命中一律计 1 分的旧打分下两者同分，importance 决出噪声在前。
	write("preference.food.spicy", "饮食偏好", "用户爱吃麻辣烫", 0.5)
	write("noise.chat", "闲聊记录", "今天喜欢出门散步", 0.99)

	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		Session: "group:one", SearchTerms: []string{"麻辣烫", "喜欢"}, Now: now, MaxCandidates: 2, CurrentSessionOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Key != "preference.food.spicy" {
		t.Fatalf("long-term match must outrank short noise: %#v", items)
	}
}
