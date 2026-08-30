// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stickerHistoryStore struct {
	events map[string][]MessageEvent
}

type stickerAssetTestStore struct {
	stickerHistoryStore
	assets []StickerAsset
	query  StickerHistoryQuery
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
	if !ok || !state.Enabled || !state.Manifest.BuiltIn || state.Manifest.Version != "0.2.0" {
		t.Fatalf("sticker plugin state=%#v ok=%v", state, ok)
	}
	if len(state.Manifest.Settings) != 5 {
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
	if len(result.Candidates) != 2 || !strings.Contains(result.Message, "按当前语义") {
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
	rankStickerCandidates(candidates, "她今天很难过，安慰一下")
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
