// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// memFeedStore 是只够自动回复用的内存存储：一条动态，评论按追加顺序。
type memFeedStore struct {
	stubFeedStore
	mu   sync.Mutex
	post FeedPost
}

func (s *memFeedStore) ListFeedPosts(_ context.Context, query FeedListQuery) ([]FeedPost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if query.ID != "" && query.ID != s.post.ID {
		return nil, nil
	}
	post := s.post
	post.Comments = append([]FeedComment(nil), s.post.Comments...)
	return []FeedPost{post}, nil
}

func (s *memFeedStore) AddFeedComment(_ context.Context, request FeedCommentRequest) (FeedComment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	comment := FeedComment{
		ID: "c-" + itoa(len(s.post.Comments)+1), PostID: request.PostID, ReplyToID: request.ReplyToID,
		AuthorKind: request.AuthorKind, Content: request.Content,
	}
	s.post.Comments = append(s.post.Comments, comment)
	return comment, nil
}

func (s *memFeedStore) commentsSnapshot() []FeedComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]FeedComment(nil), s.post.Comments...)
}

type feedReplyProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	text     string
}

func (p *feedReplyProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return &llm.GenerateResponse{Text: p.text}, nil
}

func feedReplyRuntime(t *testing.T, enabled bool, provider LLMProvider, store FeedStore) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{ID: "bot-1", FeedAutoReplyEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil,
		func() (LLMProvider, error) { return provider, nil })
	runtime.SetFeedStore(store)
	return runtime
}

func waitForFeedComments(t *testing.T, store *memFeedStore, want int) []FeedComment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if comments := store.commentsSnapshot(); len(comments) >= want {
			return comments
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到 %d 条评论：%#v", want, store.commentsSnapshot())
	return nil
}

func TestFeedAutoReplyIsOffByDefaultAndDoesNothing(t *testing.T) {
	store := &memFeedStore{post: FeedPost{ID: "p1", ProfileID: "bot-1", Content: "今天下雨", Comments: []FeedComment{{ID: "c-1", AuthorKind: FeedAuthorAdmin, Content: "好看"}}}}
	provider := &feedReplyProvider{text: "谢谢"}
	runtime := feedReplyRuntime(t, false, provider, store)
	if runtime.QueueFeedReply("p1") {
		t.Fatal("开关没开时不应排队")
	}
	if len(provider.requests) != 0 || len(store.commentsSnapshot()) != 1 {
		t.Fatalf("关着时不能调用模型也不能写评论：%d %#v", len(provider.requests), store.commentsSnapshot())
	}
}

func TestFeedAutoReplyRepliesToOwnerCommentWithoutTools(t *testing.T) {
	store := &memFeedStore{post: FeedPost{ID: "p1", ProfileID: "bot-1", Kind: FeedKindDiary, Title: "雨天", Content: "今天下雨，整理了梗。",
		Comments: []FeedComment{{ID: "c-1", PostID: "p1", AuthorKind: FeedAuthorAdmin, Content: "好看，哪里的窗？"}}}}
	provider := &feedReplyProvider{text: "  机房那扇窗。\n下雨前最好看。 "}
	runtime := feedReplyRuntime(t, true, provider, store)

	if !runtime.QueueFeedReply("p1") {
		t.Fatal("开着时应排队")
	}
	comments := waitForFeedComments(t, store, 2)
	reply := comments[1]
	if reply.AuthorKind != FeedAuthorBot || reply.ReplyToID != "c-1" || strings.Contains(reply.Content, "\n") {
		t.Fatalf("reply = %#v", reply)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 1 {
		t.Fatalf("应只调一次模型：%d", len(provider.requests))
	}
	request := provider.requests[0]
	if len(request.Tools) != 0 {
		t.Fatalf("自动回复不能带工具：%#v", request.Tools)
	}
	joined := ""
	for _, message := range request.Messages {
		joined += message.Content + "\n"
	}
	for _, want := range []string{"今天下雨，整理了梗。", "好看，哪里的窗？", "日记"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("提示词缺少 %q：%s", want, joined)
		}
	}
}

func TestFeedAutoReplySkipsWhenAlreadyAnswered(t *testing.T) {
	store := &memFeedStore{post: FeedPost{ID: "p1", ProfileID: "bot-1", Content: "x", Comments: []FeedComment{
		{ID: "c-1", AuthorKind: FeedAuthorAdmin, Content: "好看"},
		{ID: "c-2", AuthorKind: FeedAuthorBot, Content: "谢谢"},
	}}}
	provider := &feedReplyProvider{text: "多余"}
	runtime := feedReplyRuntime(t, true, provider, store)
	runtime.QueueFeedReply("p1")
	time.Sleep(300 * time.Millisecond)
	if len(provider.requests) != 0 || len(store.commentsSnapshot()) != 2 {
		t.Fatalf("最后一条是机器人自己的，不该再回：%d %#v", len(provider.requests), store.commentsSnapshot())
	}
}

func TestFeedReplySlotEnforcesDailyLimitAndCooldown(t *testing.T) {
	runtime := feedReplyRuntime(t, true, &feedReplyProvider{}, &memFeedStore{})
	runtime.feedReply.running = map[string]bool{}
	runtime.feedReply.dirty = map[string]bool{}
	runtime.feedReply.lastAt = map[string]time.Time{}
	runtime.feedReply.day = map[string]feedReplyDay{}
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.Local)

	if wait, err := runtime.takeFeedReplySlot("bot-1", "p1", now); err != nil || wait != 0 {
		t.Fatalf("first wait=%v err=%v", wait, err)
	}
	if wait, err := runtime.takeFeedReplySlot("bot-1", "p1", now.Add(2*time.Second)); err != nil || wait != feedReplyCooldown-2*time.Second {
		t.Fatalf("同一条动态要冷却：wait=%v err=%v", wait, err)
	}
	if wait, _ := runtime.takeFeedReplySlot("bot-1", "p2", now); wait != 0 {
		t.Fatalf("另一条动态不受冷却影响：%v", wait)
	}
	for i := 0; i < feedReplyDailyLimit; i++ {
		_, _ = runtime.takeFeedReplySlot("bot-1", "p-"+itoa(i), now)
	}
	if _, err := runtime.takeFeedReplySlot("bot-1", "px", now); err == nil {
		t.Fatal("超过每日上限必须被拒")
	}
	if _, err := runtime.takeFeedReplySlot("bot-1", "px", now.Add(24*time.Hour)); err != nil {
		t.Fatalf("隔天应重新计数：%v", err)
	}
}

func TestFeedToolLikesAsBot(t *testing.T) {
	store := &stubFeedStore{}
	tool := feedTestTool(t, store)
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"operation": "like"}); err == nil {
		t.Fatal("like without id must fail")
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "like", "id": "p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "like", "id": "p1", "liked": false}); err != nil {
		t.Fatal(err)
	}
	if len(store.likes) != 2 || store.likes[0] != "bot:p1:true" || store.likes[1] != "bot:p1:false" {
		t.Fatalf("likes = %#v", store.likes)
	}
}
