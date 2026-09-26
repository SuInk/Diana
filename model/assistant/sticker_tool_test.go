// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type stickerHistoryStore struct {
	events map[string][]MessageEvent
}

type stickerAssetTestStore struct {
	stickerHistoryStore
	assets []StickerAsset
	query  StickerHistoryQuery
	mu     sync.Mutex
	sent   []string
	tagged map[string][]string
}

func (s *stickerAssetTestStore) RecordStickerSent(_ context.Context, session, hash string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, session+"/"+hash)
	return nil
}

func (s *stickerAssetTestStore) SaveStickerTags(_ context.Context, record StickerTagRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tagged == nil {
		s.tagged = map[string][]string{}
	}
	s.tagged[record.ContentSHA256] = record.Tags
	return nil
}

func (s *stickerAssetTestStore) taggedSnapshot(hash string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tags, ok := s.tagged[hash]
	return tags, ok
}

func (s *stickerAssetTestStore) ListStickerAssets(_ context.Context, query StickerHistoryQuery) ([]StickerAsset, error) {
	s.query = query
	return append([]StickerAsset(nil), s.assets...), nil
}

func (s *stickerHistoryStore) AppendMessageEvent(_ context.Context, session string, event MessageEvent) error {
	s.events[session] = append(s.events[session], event)
	return nil
}

func (s *stickerHistoryStore) ListRecentMessageEvents(_ context.Context, session string, limit int) ([]MessageEvent, error) {
	events := append([]MessageEvent(nil), s.events[session]...)
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (s *stickerHistoryStore) ListRecentStickerEvents(_ context.Context, query StickerHistoryQuery) ([]MessageEvent, error) {
	if !query.ShareGroups && !query.SharePrivate {
		return s.ListRecentMessageEvents(context.Background(), query.Session, query.Limit)
	}
	var events []MessageEvent
	for session, sessionEvents := range s.events {
		for _, event := range sessionEvents {
			if session == query.Session || (query.ShareGroups && event.Kind == EventKindGroup) || (query.SharePrivate && event.Kind == EventKindPrivate) {
				events = append(events, event)
			}
		}
	}
	return events, nil
}

func TestDefaultPluginManagerIncludesStickerSender(t *testing.T) {
	state, ok := NewDefaultPluginManager().Get(stickerPluginID)
	if !ok || !state.Enabled || !state.Manifest.BuiltIn || state.Manifest.Version != "0.2.2" {
		t.Fatalf("sticker plugin state=%#v ok=%v", state, ok)
	}
	if len(state.Manifest.Settings) != 8 {
		t.Fatalf("settings=%#v", state.Manifest.Settings)
	}
}

func TestStickerSegmentLabelUsesPlatformMetadata(t *testing.T) {
	if label, ok := StickerSegmentLabel(MessageSegment{Type: "image", Data: map[string]string{"sub_type": "1"}}); !ok || label != "动画表情" {
		t.Fatalf("subtype sticker label=%q ok=%v", label, ok)
	}
	if label, ok := StickerSegmentLabel(MessageSegment{Type: "image", Data: map[string]string{"summary": "[投降]"}}); !ok || label != "投降" {
		t.Fatalf("named sticker label=%q ok=%v", label, ok)
	}
	if _, ok := StickerSegmentLabel(MessageSegment{Type: "image", Data: map[string]string{"sub_type": "0", "summary": "[图片]"}}); ok {
		t.Fatal("ordinary image was classified as a sticker")
	}
}

func TestStickerToolUsesDurableAssetStoreAndBoundCandidate(t *testing.T) {
	body := []byte("durable-sticker")
	path := filepath.Join(t.TempDir(), "durable.gif")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := imageBytesSHA256(body)
	event := MessageEvent{
		Kind: EventKindPrivate, ContextNamespace: "bot-a", ProfileID: "profile-a",
		UserID: "user", MessageID: "request",
	}
	store := &stickerAssetTestStore{stickerHistoryStore: stickerHistoryStore{events: map[string][]MessageEvent{}}, assets: []StickerAsset{{
		Session: sessionKey(event), ContextNamespace: "bot-a", ProfileID: "profile-a",
		Kind: EventKindPrivate, UserID: "user", MessageID: "source", EventTime: 1,
		Summary: "投降", Path: path, ContentSHA256: hash,
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(runtime, event, SettingValues{
		stickerSettingHistoryLimit: 100, stickerSettingSearchResults: 3,
		stickerSettingCrossGroup: true, stickerSettingCrossPrivate: true,
	})

	output, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "认输"})
	if err != nil {
		t.Fatal(err)
	}
	var search stickerToolResult
	if err := json.Unmarshal([]byte(output), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Candidates) != 1 || !store.query.ShareGroups || !store.query.SharePrivate || store.query.Session != sessionKey(event) {
		t.Fatalf("search=%#v query=%#v", search, store.query)
	}
	if search.Candidates[0].MessageID != "" {
		t.Fatalf("source message id leaked: %#v", search.Candidates[0])
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "send", "sticker_id": search.Candidates[0].ID}); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || len(sent[0].ImageURLs) != 1 || sent[0].ImageURLs[0] != path {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestStickerToolKeepsCandidatesForSemanticSelectionWithoutLiteralMatch(t *testing.T) {
	dir := t.TempDir()
	comfortPath := filepath.Join(dir, "comfort.gif")
	confusedPath := filepath.Join(dir, "confused.gif")
	for _, path := range []string{comfortPath, confusedPath} {
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "request"}
	store := &stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {
		{Kind: EventKindGroup, GroupID: "group-1", MessageID: "comfort", Time: 1, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[抱抱]", "cached_file": comfortPath}}}},
		{Kind: EventKindGroup, GroupID: "group-1", MessageID: "confused", Time: 2, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[问号]", "cached_file": confusedPath}}}},
	}}}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(runtime, event, SettingValues{stickerSettingSearchResults: 8})

	output, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "她今天很难过，安慰一下"})
	if err != nil {
		t.Fatal(err)
	}
	var result stickerToolResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 || result.Candidates[0].Matched || !strings.Contains(result.Message, "随机候选") {
		t.Fatalf("semantic candidates=%#v", result)
	}

	output, err = tool.Run(context.Background(), map[string]any{"operation": "send", "query": "她今天很难过，安慰一下"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"action":"not_sent"`) || !strings.Contains(output, "sticker_id") {
		t.Fatalf("semantic direct send should require search selection: %s", output)
	}
}

func TestRankStickerCandidatesPrefersSemanticMatchOverRecency(t *testing.T) {
	candidates := []stickerCandidate{
		{ID: "recent", Summary: "动画表情", EventTime: 20},
		{ID: "semantic", Summary: "抱抱", EventTime: 10, SemanticScore: 80},
	}
	rankStickerCandidates(candidates, "她今天很难过，安慰一下", 100)
	if candidates[0].ID != "semantic" || candidates[0].Score != 80 {
		t.Fatalf("semantic ranking=%#v", candidates)
	}
}

func TestStickerToolAddsAndCachesVisionDescriptionForSearchCandidate(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "request"}
	store := newRecallImageTestStore()
	store.timeline = []MessageEvent{{
		Kind: EventKindGroup, GroupID: "group-1", MessageID: "sticker", Time: 1,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"summary": "[动画表情]", "cached_file": imagePath, imageContentSHA256Key: hash,
		}}},
	}}
	provider := &recallImageVisionProvider{}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(store)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	tool := newDianaStickerTool(runtime, event, SettingValues{stickerSettingSearchResults: 3})

	search := func() stickerToolResult {
		output, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "看看系统情况"})
		if err != nil {
			t.Fatal(err)
		}
		var result stickerToolResult
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := search()
	if len(first.Candidates) != 1 || !strings.Contains(first.Candidates[0].Description, "命中率为 63%") {
		t.Fatalf("vision candidate=%#v", first)
	}
	if provider.callCount() != 1 || store.saves != 1 {
		t.Fatalf("vision calls=%d saves=%d", provider.callCount(), store.saves)
	}
	second := search()
	if len(second.Candidates) != 1 || second.Candidates[0].Description == "" || provider.callCount() != 1 {
		t.Fatalf("cached candidate=%#v calls=%d", second, provider.callCount())
	}
	// 给表情包写简介的那次视觉调用要记在触发搜索的这条消息名下；测试替身不报用量，
	// 也要留下这次调用。命中缓存的第二次搜索不调模型，不能多记。
	usage := usageEntriesFor(logs, "request")
	if len(usage) != 1 || usage[0]["purpose"] != "sticker_description" || usage[0]["usage_missing"] != true {
		t.Fatalf("sticker description usage = %#v", usage)
	}
}

// 事件记录里要能看到机器人发的是哪张表情包，而不只是「发了 1 张图片」。
func TestStickerToolSendRecordsWhichStickerWasSent(t *testing.T) {
	dir := t.TempDir()
	stickerPath := filepath.Join(dir, "sticker.gif")
	body := []byte("sticker-image")
	if err := os.WriteFile(stickerPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "request"}
	store := &stickerHistoryStore{events: map[string][]MessageEvent{
		sessionKey(event): {{Kind: EventKindGroup, GroupID: "group-1", MessageID: "sticker", Time: 2, Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"summary": "[无语]", "cached_file": stickerPath, imageContentSHA256Key: imageBytesSHA256(body),
		}}}}},
	}}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(runtime, event, SettingValues{stickerSettingHistoryLimit: 1000, stickerSettingSearchResults: 8, stickerSettingIncludeGeneric: true})

	ctx := withOutboundTurn(context.Background(), "turn-1")
	output, err := tool.Run(ctx, map[string]any{"operation": "search", "query": "无语"})
	if err != nil {
		t.Fatal(err)
	}
	var search stickerToolResult
	if err := json.Unmarshal([]byte(output), &search); err != nil || len(search.Candidates) != 1 {
		t.Fatalf("search=%s err=%v", output, err)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "send", "sticker_id": search.Candidates[0].ID}); err != nil {
		t.Fatal(err)
	}
	delivery := outboundTurnFromContext(ctx).delivery()
	if delivery.Images != 1 || len(delivery.Media) != 1 {
		t.Fatalf("delivery = %#v", delivery)
	}
	if media := delivery.Media[0]; media.Kind != "image" || media.Source != stickerPath || media.Label != "表情包：无语" {
		t.Fatalf("media = %#v", media)
	}
}

// 内联图片（生图常见的 data URI）不往事件表里塞，只标出来；超出上限的只计数。
func TestOutboundTurnRecordsImageSourcesWithinLimit(t *testing.T) {
	turn := &outboundTurn{id: "t"}
	turn.recordSentMessage(OutgoingMessage{
		ImageURLs:   []string{"data:image/png;base64,AAAA", "/tmp/a.png"},
		ImageLabels: []string{"生成的图"},
		Segments:    []MessageSegment{{Type: "image", Data: map[string]string{"file": "https://example.com/b.png", "summary": "[b]"}}},
	})
	media := turn.delivery().Media
	if len(media) != 3 || !media[0].Inline || media[0].Source != "" || media[0].Label != "生成的图" {
		t.Fatalf("media = %#v", media)
	}
	if media[1].Source != "/tmp/a.png" || media[2].Source != "https://example.com/b.png" || media[2].Label != "[b]" {
		t.Fatalf("media = %#v", media)
	}
	many := make([]string, maxOutboundMediaPerTurn+5)
	for index := range many {
		many[index] = "/tmp/x.png"
	}
	turn.recordSentMessage(OutgoingMessage{ImageURLs: many})
	if got := turn.delivery(); len(got.Media) != maxOutboundMediaPerTurn || got.Images != 3+len(many) {
		t.Fatalf("media = %d images = %d", len(got.Media), got.Images)
	}
}

// 主回复的 ctx 带着 reply 用途；检索的 embedding 不能照抄，否则 embedding 模型会被
// 当成主对话模型。只有调用方显式指定时才换成自己的用途。
func TestSemanticSearchPurposeDoesNotInheritReply(t *testing.T) {
	ctx := withLLMUsagePurpose(context.Background(), "reply")
	if got := semanticSearchPurpose(ctx); got != "semantic_search" {
		t.Fatalf("purpose = %q", got)
	}
	if got := semanticSearchPurpose(withSemanticSearchPurpose(ctx, "sticker_search")); got != "sticker_search" {
		t.Fatalf("purpose = %q", got)
	}
}

func TestStickerToolSearchesThenSendsOnlyCurrentConversationSticker(t *testing.T) {
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "current.gif")
	otherPath := filepath.Join(dir, "other.gif")
	privatePath := filepath.Join(dir, "private.gif")
	normalPath := filepath.Join(dir, "normal.png")
	contents := map[string][]byte{
		currentPath: []byte("current-image"), otherPath: []byte("other-image"),
		privatePath: []byte("private-image"), normalPath: []byte("normal-image"),
	}
	for path, body := range contents {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	currentHash := imageBytesSHA256(contents[currentPath])
	otherHash := imageBytesSHA256(contents[otherPath])
	privateHash := imageBytesSHA256(contents[privatePath])
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "request"}
	store := &stickerHistoryStore{events: map[string][]MessageEvent{
		sessionKey(event): {
			{Kind: EventKindGroup, GroupID: "group-1", MessageID: "normal", Time: 1, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[图片]", "cached_file": normalPath}}}},
			{Kind: EventKindGroup, GroupID: "group-1", MessageID: "sticker", Time: 2, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[无语]", "cached_file": currentPath, imageContentSHA256Key: currentHash}}}},
		},
		"group:group-2": {
			{Kind: EventKindGroup, GroupID: "group-2", MessageID: "private", Time: 3, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[无语]", "cached_file": otherPath, imageContentSHA256Key: otherHash}}}},
		},
		"private:other-user": {
			{Kind: EventKindPrivate, UserID: "other-user", MessageID: "private-chat", Time: 4, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[无语]", "cached_file": privatePath, imageContentSHA256Key: privateHash}}}},
		},
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(runtime, event, SettingValues{stickerSettingHistoryLimit: 1000, stickerSettingSearchResults: 8, stickerSettingIncludeGeneric: true})

	output, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "无语"})
	if err != nil {
		t.Fatal(err)
	}
	var search stickerToolResult
	if err := json.Unmarshal([]byte(output), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Candidates) != 1 || search.Candidates[0].Name != "无语" || search.Candidates[0].MessageID == "private" {
		t.Fatalf("search=%#v", search)
	}
	if len(channel.sentSnapshot()) != 0 {
		t.Fatal("search unexpectedly sent a sticker")
	}

	output, err = tool.Run(context.Background(), map[string]any{"operation": "send", "sticker_id": search.Candidates[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	var sent stickerToolResult
	if err := json.Unmarshal([]byte(output), &sent); err != nil {
		t.Fatal(err)
	}
	messages := channel.sentSnapshot()
	if !sent.OK || sent.Action != "sent" || len(messages) != 1 || len(messages[0].ImageURLs) != 1 || messages[0].ImageURLs[0] != currentPath {
		t.Fatalf("sent=%#v messages=%#v", sent, messages)
	}

	output, err = tool.Run(context.Background(), map[string]any{"operation": "send", "sticker_id": otherHash[:24]})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"action":"not_sent"`) || len(channel.sentSnapshot()) != 1 {
		t.Fatalf("foreign sticker was not rejected: %s", output)
	}

	crossGroupTool := newDianaStickerTool(runtime, event, SettingValues{
		stickerSettingHistoryLimit: 1000, stickerSettingSearchResults: 8,
		stickerSettingIncludeGeneric: true, stickerSettingCrossGroup: true,
	})
	output, err = crossGroupTool.Run(context.Background(), map[string]any{"operation": "search", "query": "无语"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Candidates) != 2 || search.Candidates[0].Scope == search.Candidates[1].Scope {
		t.Fatalf("cross-group search=%#v", search)
	}

	crossPrivateTool := newDianaStickerTool(runtime, event, SettingValues{
		stickerSettingHistoryLimit: 1000, stickerSettingSearchResults: 8,
		stickerSettingIncludeGeneric: true, stickerSettingCrossPrivate: true,
	})
	output, err = crossPrivateTool.Run(context.Background(), map[string]any{"operation": "search", "query": "无语"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Candidates) != 2 || search.Candidates[0].Scope == search.Candidates[1].Scope {
		t.Fatalf("cross-private search=%#v", search)
	}
}

func TestStickerToolCanExcludeGenericAnimatedCandidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generic.gif")
	if err := os.WriteFile(path, []byte("gif"), 0o600); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "request"}
	store := &stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {
		{Kind: EventKindPrivate, UserID: "user", MessageID: "generic", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[动画表情]", "cached_file": path}}}},
	}}}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(runtime, event, SettingValues{stickerSettingIncludeGeneric: false})
	output, err := tool.Run(context.Background(), map[string]any{"operation": "search"})
	if err != nil {
		t.Fatal(err)
	}
	var result stickerToolResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || len(result.Candidates) != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// 关键词检索：多个词任一命中就算，名称和标签命中比简介命中分高；一个都不沾的不算命中。
func TestRankStickerCandidatesMatchesAnyKeywordAndPrefersTags(t *testing.T) {
	candidates := []stickerCandidate{
		{ID: "recent", Summary: "动画表情", Description: "一只猫坐着", EventTime: 30},
		{ID: "tagged", Summary: "动画表情", Tags: []string{"安慰", "抱抱"}, EventTime: 10},
		{ID: "described", Summary: "动画表情", Description: "小熊伸手摸头", EventTime: 20},
	}
	rankStickerCandidates(candidates, "安慰 摸头 心疼", 100)
	if candidates[0].ID != "tagged" || candidates[1].ID != "described" || candidates[2].Score != 0 {
		t.Fatalf("ranking = %#v", candidates)
	}
	if candidates[0].Score <= candidates[1].Score || candidates[1].Score <= 0 {
		t.Fatalf("scores = %v %v", candidates[0].Score, candidates[1].Score)
	}
}

// 机器人刚在本会话发过的，同样命中也排到后面；过了窗口就恢复。
func TestRankStickerCandidatesPushesRecentlySentDown(t *testing.T) {
	now := int64(100000)
	fresh := func() []stickerCandidate {
		return []stickerCandidate{
			{ID: "just-sent", Summary: "无语", EventTime: 20, LastSentAt: now - 60},
			{ID: "other", Summary: "无语", EventTime: 10},
		}
	}
	candidates := fresh()
	rankStickerCandidates(candidates, "无语", now)
	if candidates[0].ID != "other" || candidates[1].Score <= 0 {
		t.Fatalf("recent ranking = %#v", candidates)
	}
	candidates = fresh()
	rankStickerCandidates(candidates, "无语", now+int64(stickerRecentSendWindow/time.Second)+60)
	if candidates[0].ID != "just-sent" {
		t.Fatalf("after window ranking = %#v", candidates)
	}
}

// 命中的排在前面；不够时用没命中的随机补位，刚发过的最后才补。
func TestSelectStickerCandidatesFillsRandomlyAndSkipsRecentlySent(t *testing.T) {
	now := int64(100000)
	candidates := []stickerCandidate{
		{ID: "hit", Score: 10},
		{ID: "recent", LastSentAt: now - 60},
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	first := func(int) int { return 0 }
	picked, matched := selectStickerCandidates(candidates, 3, now, first)
	if matched != 1 || len(picked) != 3 || picked[0].ID != "hit" || picked[1].ID != "a" || picked[2].ID != "b" {
		t.Fatalf("picked = %#v matched=%d", picked, matched)
	}
	picked, _ = selectStickerCandidates(candidates, 5, now, first)
	if len(picked) != 5 || picked[4].ID != "recent" {
		t.Fatalf("picked = %#v", picked)
	}
}

// 命中太多时在前几名里加权抽，不是永远同一批；分数最高的仍然排在返回列表前面。
func TestSelectStickerCandidatesSamplesAmongTopMatches(t *testing.T) {
	var candidates []stickerCandidate
	for index := 0; index < 10; index++ {
		candidates = append(candidates, stickerCandidate{ID: string(rune('a' + index)), Score: float64(100 - index)})
	}
	last := func(n int) int { return n - 1 }
	picked, matched := selectStickerCandidates(candidates, 2, 0, last)
	if matched != 2 || len(picked) != 2 {
		t.Fatalf("picked = %#v", picked)
	}
	for _, candidate := range picked {
		if candidate.Score < 97 {
			t.Fatalf("picked outside top pool: %#v", picked)
		}
	}
	if picked[0].Score < picked[1].Score {
		t.Fatalf("picked not ordered: %#v", picked)
	}
}

func TestParseStickerAnnotation(t *testing.T) {
	gist, tags := parseStickerAnnotation("猫猫翻白眼，表示对离谱发言很无语。 标签：无语、翻白眼、离谱，猫猫。")
	if gist != "猫猫翻白眼，表示对离谱发言很无语。" || strings.Join(tags, "|") != "无语|翻白眼|离谱|猫猫" {
		t.Fatalf("gist=%q tags=%q", gist, tags)
	}
	gist, tags = parseStickerAnnotation("只有简介没有标签")
	if gist != "只有简介没有标签" || tags != nil {
		t.Fatalf("gist=%q tags=%q", gist, tags)
	}
}

// 资产库带出的标签交给 Agent；发送后记下这次发送；已有通用描述但没标注过的，后台补标签。
func TestStickerToolUsesAssetTagsRecordsSendAndTagsInBackground(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) (string, string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return path, imageBytesSHA256(body)
	}
	taggedPath, taggedHash := write("tagged.gif", []byte("tagged"))
	plainPath, plainHash := writeRecallImageFixture(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "request"}
	store := &stickerAssetTestStore{stickerHistoryStore: stickerHistoryStore{events: map[string][]MessageEvent{}}, assets: []StickerAsset{
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "m1", EventTime: 1,
			Summary: "动画表情", Path: taggedPath, ContentSHA256: taggedHash, Tagged: true, Gist: "摸摸头安慰", Tags: []string{"安慰", "摸头"}},
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "m2", EventTime: 2,
			Summary: "动画表情", Path: plainPath, ContentSHA256: plainHash, Description: "一张系统面板截图"},
	}}
	provider := &recallImageVisionProvider{}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(runtime, event, SettingValues{stickerSettingSearchResults: 8})

	output, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "安慰 心疼"})
	if err != nil {
		t.Fatal(err)
	}
	var search stickerToolResult
	if err := json.Unmarshal([]byte(output), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Candidates) != 2 || !search.Candidates[0].Matched || search.Candidates[0].Description != "摸摸头安慰" ||
		strings.Join(search.Candidates[0].Tags, "|") != "安慰|摸头" || search.Candidates[1].Matched {
		t.Fatalf("search = %s", output)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "send", "sticker_id": search.Candidates[0].ID}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	sent := append([]string(nil), store.sent...)
	store.mu.Unlock()
	if len(sent) != 1 || sent[0] != sessionKey(event)+"/"+taggedHash {
		t.Fatalf("sent = %#v", sent)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := store.taggedSnapshot(plainHash); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("untagged candidate was not tagged in background")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := store.taggedSnapshot(taggedHash); ok {
		t.Fatal("already tagged candidate was tagged again")
	}
}

type stickerPruneTestStore struct {
	stickerHistoryStore
	mu       sync.Mutex
	sessions []string
	capacity int
}

func (s *stickerPruneTestStore) PruneStickerAssets(_ context.Context, session string, capacity int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, session)
	s.capacity = capacity
	return 0, nil
}

// 收到带表情包的消息落库后按插件上限修剪这个会话的表情包库；纯文字消息不触发。
func TestPersistMessageEventPrunesStickerLibrary(t *testing.T) {
	store := &stickerPruneTestStore{stickerHistoryStore: stickerHistoryStore{events: map[string][]MessageEvent{}}}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	text := MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "t", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "hi"}}}}
	runtime.persistMessageEvent(text)
	sticker := MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "s", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[无语]"}}}}
	runtime.persistMessageEvent(sticker)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sessions) != 1 || store.sessions[0] != sessionKey(sticker) || store.capacity != 1000 {
		t.Fatalf("prune calls = %#v capacity=%d", store.sessions, store.capacity)
	}
}

func stickerLimitTestTool(t *testing.T, runtime *Runtime, event MessageEvent, settings SettingValues) (*dianaStickerTool, func() string) {
	t.Helper()
	tool := newDianaStickerTool(runtime, event, settings)
	sendOne := func() string {
		output, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "无语"})
		if err != nil {
			t.Fatal(err)
		}
		var search stickerToolResult
		if err := json.Unmarshal([]byte(output), &search); err != nil {
			t.Fatal(err)
		}
		if search.Action == "limited" {
			return output
		}
		if len(search.Candidates) == 0 {
			t.Fatalf("search = %s", output)
		}
		output, err = tool.Run(context.Background(), map[string]any{"operation": "send", "sticker_id": search.Candidates[0].ID})
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	return tool, sendOne
}

// 发送频率上限：一轮默认只发一张；同一会话一小时内到了上限，新的一轮连搜索都直接返回，
// 不再读库、不调识图；别的会话不受影响。
func TestStickerToolEnforcesTurnAndHourlySendLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sticker.gif")
	body := []byte("limit-sticker")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	segment := func() []MessageSegment {
		return []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[无语]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}}
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u", MessageID: "request"}
	other := MessageEvent{Kind: EventKindGroup, GroupID: "g2", UserID: "u", MessageID: "request"}
	store := &stickerHistoryStore{events: map[string][]MessageEvent{
		sessionKey(event): {{Kind: EventKindGroup, GroupID: "g1", MessageID: "s", Time: 1, Segments: segment()}},
		sessionKey(other): {{Kind: EventKindGroup, GroupID: "g2", MessageID: "s", Time: 1, Segments: segment()}},
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	settings := SettingValues{stickerSettingHourlyLimit: 2}

	_, sendOne := stickerLimitTestTool(t, runtime, event, settings)
	if output := sendOne(); !strings.Contains(output, `"action":"sent"`) {
		t.Fatalf("first send = %s", output)
	}
	if output := sendOne(); !strings.Contains(output, `"action":"limited"`) || !strings.Contains(output, "单轮上限") {
		t.Fatalf("second send in same turn = %s", output)
	}
	_, nextTurn := stickerLimitTestTool(t, runtime, event, settings)
	if output := nextTurn(); !strings.Contains(output, `"action":"sent"`) {
		t.Fatalf("next turn send = %s", output)
	}
	_, thirdTurn := stickerLimitTestTool(t, runtime, event, settings)
	if output := thirdTurn(); !strings.Contains(output, `"action":"limited"`) || !strings.Contains(output, "最近一小时已发 2 张") {
		t.Fatalf("hourly limit = %s", output)
	}
	_, otherTurn := stickerLimitTestTool(t, runtime, other, settings)
	if output := otherTurn(); !strings.Contains(output, `"action":"sent"`) {
		t.Fatalf("other conversation = %s", output)
	}
	if sent := channel.sentSnapshot(); len(sent) != 3 {
		t.Fatalf("sent = %d", len(sent))
	}

	// 填 0 不限每小时张数。
	_, unlimited := stickerLimitTestTool(t, runtime, event, SettingValues{stickerSettingHourlyLimit: 0})
	if output := unlimited(); !strings.Contains(output, `"action":"sent"`) {
		t.Fatalf("unlimited = %s", output)
	}
}

// 占了名额但没发出去要退回，过了一小时的旧记录不再占名额。
func TestStickerSendLimiterReleasesAndExpires(t *testing.T) {
	var limiter stickerSendLimiter
	now := time.Unix(100000, 0)
	release, ok := limiter.reserve("s", now, 1)
	if !ok {
		t.Fatal("first reserve refused")
	}
	if _, ok := limiter.reserve("s", now, 1); ok {
		t.Fatal("reserve over limit accepted")
	}
	if full, wait := limiter.full("s", now.Add(10*time.Minute), 1); !full || wait != 50*time.Minute {
		t.Fatalf("full=%v wait=%v", full, wait)
	}
	release()
	if _, ok := limiter.reserve("s", now, 1); !ok {
		t.Fatal("released slot not reusable")
	}
	if full, _ := limiter.full("s", now.Add(stickerSendRateWindow), 1); full {
		t.Fatal("expired send still counted")
	}
}
