// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// speakerHistoryStore 是带发言人名录的历史存储替身：名录按昵称子串、用户名或账号匹配。
type speakerHistoryStore struct {
	capturingHistorySearchStore
	speakers     []MessageSpeaker
	speakerCalls int
}

func (s *speakerHistoryStore) ListMessageSpeakers(_ context.Context, query MessageSpeakerQuery) ([]MessageSpeaker, error) {
	s.speakerCalls++
	name := strings.ToLower(strings.TrimSpace(query.Name))
	handle := strings.TrimPrefix(name, "@")
	var out []MessageSpeaker
	for _, speaker := range s.speakers {
		if name == "" {
			out = append(out, speaker)
			continue
		}
		matched, exact := false, false
		for _, candidate := range speaker.Names {
			if strings.Contains(strings.ToLower(candidate), name) {
				matched = true
				exact = exact || strings.EqualFold(candidate, query.Name)
			}
		}
		if speaker.Username != "" && strings.EqualFold(speaker.Username, handle) {
			matched, exact = true, true
		}
		if speaker.UserID == query.Name {
			matched, exact = true, true
		}
		if matched {
			speaker.ExactMatch = exact
			out = append(out, speaker)
		}
	}
	return out, nil
}

func newSpeakerTestRuntime(store *speakerHistoryStore) *Runtime {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	return runtime
}

func runChatHistorySearch(t *testing.T, runtime *Runtime, input map[string]any) dianaChatHistoryResult {
	t.Helper()
	raw, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "-10042", Platform: PlatformTelegram, UserID: "40000"}).Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// Telegram 里只知道昵称（还是旧昵称）也能按人查聊天记录：昵称经名录解析成账号再过滤，
// 有精确命中时不把「小明的猫」这种子串命中并进来。
func TestChatHistorySearchResolvesSenderName(t *testing.T) {
	store := &speakerHistoryStore{speakers: []MessageSpeaker{
		{UserID: "50001", Names: []string{"明明同学", "小明"}, Username: "ming_cat"},
		{UserID: "50002", Names: []string{"小明的猫"}},
	}}
	runtime := newSpeakerTestRuntime(store)

	result := runChatHistorySearch(t, runtime, map[string]any{"operation": "search", "sender_name": "小明"})
	if got := strings.Join(store.query.SenderUserIDs, ","); got != "50001" {
		t.Fatalf("sender ids = %q, want exact match only", got)
	}
	if len(result.ResolvedSenders) != 1 || result.ResolvedSenders[0].UserID != "50001" || result.Sender != "50001" {
		t.Fatalf("resolved senders not reported: %+v sender=%q", result.ResolvedSenders, result.Sender)
	}

	runChatHistorySearch(t, runtime, map[string]any{"operation": "search", "sender_name": "@ming_cat"})
	if got := strings.Join(store.query.SenderUserIDs, ","); got != "50001" {
		t.Fatalf("username lookup sender ids = %q", got)
	}

	result = runChatHistorySearch(t, runtime, map[string]any{"operation": "search", "sender_name": "明"})
	if got := strings.Join(store.query.SenderUserIDs, ","); got != "50001,50002" || !strings.Contains(result.Message, "多个账号") {
		t.Fatalf("ambiguous name should merge and say so: ids=%q message=%q", got, result.Message)
	}
}

// 模型把昵称误填进 sender_user_id 时按昵称处理；真账号照旧直通。
func TestChatHistorySearchTreatsNicknameInSenderUserID(t *testing.T) {
	store := &speakerHistoryStore{speakers: []MessageSpeaker{{UserID: "50001", Names: []string{"小明"}}}}
	runtime := newSpeakerTestRuntime(store)

	result := runChatHistorySearch(t, runtime, map[string]any{"operation": "search", "sender_user_id": "小明"})
	if got := strings.Join(store.query.SenderUserIDs, ","); got != "50001" || !strings.Contains(result.Message, "不是账号") {
		t.Fatalf("nickname in sender_user_id not resolved: ids=%q message=%q", got, result.Message)
	}

	runChatHistorySearch(t, runtime, map[string]any{"operation": "search", "sender_user_id": "50001"})
	if got := strings.Join(store.query.SenderUserIDs, ","); got != "50001" {
		t.Fatalf("real account changed: %q", got)
	}
	runChatHistorySearch(t, runtime, map[string]any{"operation": "search", "sender_user_id": "77777"})
	if got := strings.Join(store.query.SenderUserIDs, ","); got != "77777" {
		t.Fatalf("unknown account must pass through: %q", got)
	}
}

func TestChatHistorySearchUnknownSenderNameErrors(t *testing.T) {
	runtime := newSpeakerTestRuntime(&speakerHistoryStore{})
	_, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "-10042"}).Run(context.Background(), map[string]any{"operation": "search", "sender_name": "不存在"})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("unknown name should explain: %v", err)
	}
}

// 消息里点到的人：@、引用作者、正文里写出的昵称（含旧昵称）和 @用户名都算，
// 当前发言者、机器人自己和太短的昵称不算。
func TestMentionedSubjectUserIDs(t *testing.T) {
	store := &speakerHistoryStore{speakers: []MessageSpeaker{
		{UserID: "50001", Names: []string{"明明同学", "小明"}},
		{UserID: "50002", Names: []string{"a"}},
		{UserID: "50003", Names: []string{"路人甲"}, Username: "passerby"},
		{UserID: "40000", Names: []string{"提问者"}},
		{UserID: "90000", Names: []string{"机器人"}},
	}}
	runtime := newSpeakerTestRuntime(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "-10042", UserID: "40000", SelfID: "90000"}

	got := runtime.mentionedSubjectUserIDs(context.Background(), event, "提问者想问小明喜欢什么，a 也说说，机器人你知道吗")
	if strings.Join(got, ",") != "50001" {
		t.Fatalf("mentioned = %v", got)
	}
	event.MentionTargets = []MessageMention{{Username: "passerby", Target: "other"}}
	event.Quoted = &QuotedMessage{UserID: "60000"}
	got = runtime.mentionedSubjectUserIDs(context.Background(), event, "他俩谁说的")
	if strings.Join(got, ",") != "60000,50003" {
		t.Fatalf("quoted author and @username should resolve: %v", got)
	}

	// 名录有缓存，同一会话短时间内不重复查库。
	calls := store.speakerCalls
	runtime.mentionedSubjectUserIDs(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "-10042", UserID: "40000"}, "小明")
	if store.speakerCalls != calls {
		t.Fatalf("speaker directory should be cached: %d -> %d", calls, store.speakerCalls)
	}
}

// 问「小明喜欢什么」：小明名下的偏好正文不带他名字，也要因为被点到而进结果。
func TestRankStructuredMemoriesBoostsMentionedSubject(t *testing.T) {
	now := time.Now()
	items := []StructuredMemoryItem{{
		ID: "pref", SubjectUserID: "50001", SubjectName: "明明同学", Kind: MemoryKindPreference,
		Topic: "饮食", Content: "爱吃凤爪", Importance: 0.7, Confidence: 0.85, Visibility: MemoryVisibilitySession,
		SourceSession: "group:-10042", LastVerifiedAt: now,
	}}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "-10042", UserID: "40000"}
	if ranked := rankStructuredMemories(items, event, "小明平时喜欢啥", now); len(ranked) != 0 {
		t.Fatalf("baseline should miss without mention boost: %+v", ranked)
	}
	query := StructuredMemoryQuery{MentionedSubjectUserIDs: []string{"50001"}}
	markLinkedMemorySubjects(query, items)
	ranked := rankStructuredMemories(items, event, "小明平时喜欢啥", now)
	if len(ranked) != 1 || !strings.Contains(ranked[0].RetrievalReason, "提到的人") {
		t.Fatalf("mentioned subject memory not recalled: %+v", ranked)
	}
}
