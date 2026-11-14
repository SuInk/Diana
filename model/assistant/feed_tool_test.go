// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type stubFeedStore struct {
	created  []FeedPostCreateRequest
	deleted  string
	comments []FeedCommentRequest
	likes    []string
	posts    []FeedPost
}

func (s *stubFeedStore) CreateFeedPost(_ context.Context, request FeedPostCreateRequest) (FeedPost, error) {
	s.created = append(s.created, request)
	return FeedPost{ID: "post-1", ProfileID: request.ProfileID, Kind: request.Kind, Content: request.Content}, nil
}

func (s *stubFeedStore) ListFeedPosts(_ context.Context, query FeedListQuery) ([]FeedPost, error) {
	posts := append([]FeedPost(nil), s.posts...)
	if query.Limit > 0 && len(posts) > query.Limit {
		posts = posts[:query.Limit]
	}
	return posts, nil
}

func (s *stubFeedStore) DeleteFeedPost(_ context.Context, _, id string) (FeedPost, error) {
	if id != "post-1" {
		return FeedPost{}, ErrFeedPostNotFound
	}
	s.deleted = id
	return FeedPost{ID: id}, nil
}

func (s *stubFeedStore) AddFeedComment(_ context.Context, request FeedCommentRequest) (FeedComment, error) {
	s.comments = append(s.comments, request)
	return FeedComment{ID: "c-1", PostID: request.PostID, AuthorKind: request.AuthorKind, Content: request.Content}, nil
}

func (s *stubFeedStore) DeleteFeedComment(context.Context, string, string) (FeedComment, error) {
	return FeedComment{}, ErrFeedCommentNotFound
}

func (s *stubFeedStore) SetFeedLike(_ context.Context, _, postID, kind string, liked bool, _ time.Time) (FeedLikes, error) {
	s.likes = append(s.likes, kind+":"+postID+":"+strconv.FormatBool(liked))
	return FeedLikes{Count: 1, LikedByBot: liked}, nil
}

func (s *stubFeedStore) FeedImageData(context.Context, string) ([]byte, string, bool, error) {
	return nil, "", false, nil
}

func feedTestPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func feedTestTool(t *testing.T, store FeedStore) *dianaFeedTool {
	t.Helper()
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if store != nil {
		runtime.SetFeedStore(store)
	}
	return newDianaFeedTool(runtime, MessageEvent{ProfileID: "bot-1", UserID: "owner", GroupID: "g1"})
}

func TestFeedToolPostsWithWorkspaceImageAndFindsExtension(t *testing.T) {
	store := &stubFeedStore{}
	tool := feedTestTool(t, store)
	outputs := filepath.Join(AgentWorkspaceDir(), "outputs")
	if err := os.MkdirAll(outputs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "sunset.png"), feedTestPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	// 生图工具只给不含扩展名的前缀，模型原样传过来也要能找到。
	output, err := tool.Run(context.Background(), map[string]any{
		"operation": "post", "kind": "日记", "title": "傍晚", "content": "今天看了日落。",
		"images": []any{"outputs/sunset"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaFeedResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || !result.OK {
		t.Fatalf("result = %s, err=%v", output, err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %#v", store.created)
	}
	request := store.created[0]
	if request.Kind != FeedKindDiary || request.ProfileID != "bot-1" || len(request.Images) != 1 {
		t.Fatalf("request = %#v", request)
	}
	if got := request.Images[0]; got.MIME != "image/png" || got.Width != 4 || got.Height != 3 {
		t.Fatalf("image = %#v", got)
	}
}

func TestFeedToolRejectsNonImageFilesAndPublishesNothing(t *testing.T) {
	store := &stubFeedStore{}
	tool := feedTestTool(t, store)
	if err := os.MkdirAll(AgentWorkspaceDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(AgentWorkspaceDir(), "notes.txt"), []byte("secret text"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Run(context.Background(), map[string]any{
		"operation": "post", "content": "hi", "images": []any{"notes.txt"},
	})
	if err == nil || !strings.Contains(err.Error(), "没有发出") {
		t.Fatalf("err = %v", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("nothing must be published when an image is unusable: %#v", store.created)
	}
}

func TestFeedToolValidatesInputAndGates(t *testing.T) {
	store := &stubFeedStore{}
	tool := feedTestTool(t, store)
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"operation": "post", "content": "  "}); err == nil {
		t.Fatal("empty content must fail")
	}
	tooMany := make([]any, FeedMaxImages+1)
	for i := range tooMany {
		tooMany[i] = "x" + itoa(i)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "post", "content": "x", "images": tooMany}); err == nil {
		t.Fatal("too many images must fail")
	}
	output, err := tool.Run(ctx, map[string]any{"operation": "delete", "id": "missing"})
	if err != nil || !strings.Contains(output, "可能已经删过") {
		t.Fatalf("delete missing = %s, err=%v", output, err)
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "delete", "id": "post-1"}); err != nil || store.deleted != "post-1" {
		t.Fatalf("delete err=%v deleted=%q", err, store.deleted)
	}
	noProfile := newDianaFeedTool(tool.runtime, MessageEvent{UserID: "owner"})
	if _, err := noProfile.Run(ctx, map[string]any{"operation": "list"}); err == nil {
		t.Fatal("missing bot identity must be refused")
	}
	noStore := feedTestTool(t, nil)
	if _, err := noStore.Run(ctx, map[string]any{"operation": "list"}); err == nil {
		t.Fatal("missing store must be refused")
	}
}

func TestFeedContentNormalizationKeepsParagraphs(t *testing.T) {
	got := NormalizeFeedContent("  第一段  \r\n\r\n\r\n\r\n第二段\t\n")
	if got != "第一段\n\n第二段" {
		t.Fatalf("got %q", got)
	}
	if NormalizeFeedKind("Diary") != FeedKindDiary || NormalizeFeedKind("whatever") != FeedKindPost {
		t.Fatal("kind normalization")
	}
}

func TestFeedToolIsOwnerOnly(t *testing.T) {
	if RelationshipPolicyFor(UserMemoryProfile{}, "owner", "member").allowedAgentToolNames()[dianaFeedToolName] {
		t.Fatal("feed_post must not be granted to non-owner members")
	}
}

func TestFeedToolCommentsAsBotAndScopesToOwnProfile(t *testing.T) {
	store := &stubFeedStore{}
	tool := feedTestTool(t, store)
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"operation": "comment", "content": "  谢谢\n你  "}); err == nil {
		t.Fatal("comment without post id must fail")
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "comment", "id": "post-1", "content": " "}); err == nil {
		t.Fatal("empty comment must fail")
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "comment", "id": "post-1", "reply_to": "c-0", "content": "谢谢\n你"}); err != nil {
		t.Fatal(err)
	}
	got := store.comments[0]
	if got.AuthorKind != FeedAuthorBot || got.ProfileID != "bot-1" || got.ReplyToID != "c-0" || got.Content != "谢谢 你" {
		t.Fatalf("request = %#v", got)
	}
}

func TestFeedToolOnlyAcceptsOwnImages(t *testing.T) {
	store := &stubFeedStore{}
	tool := feedTestTool(t, store)
	ctx := context.Background()
	downloads := filepath.Join(AgentWorkspaceDir(), "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloads, "theirs.png"), feedTestPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"downloads/theirs.png", "https://example.com/a.png", "../outside.png"} {
		if _, err := tool.Run(ctx, map[string]any{"operation": "post", "content": "hi", "images": []any{source}}); err == nil {
			t.Fatalf("source %q must be rejected", source)
		}
	}
	if len(store.created) != 0 {
		t.Fatalf("nothing may be published: %#v", store.created)
	}
}

// list 不设条数上限：存储一次只给 limit 条也要取全，总数如实报，按整条分页；
// needs_reply 在全部动态里筛，不会因为只取了最近一截漏掉更早的待回动态。
func TestFeedToolListPagesAllPosts(t *testing.T) {
	store := &stubFeedStore{}
	const total = 150
	for index := 0; index < total; index++ {
		post := FeedPost{ID: "post-" + strconv.Itoa(index), Content: "第 " + strconv.Itoa(index) + " 条"}
		if index%10 == 9 {
			post.Comments = []FeedComment{{ID: "c-" + strconv.Itoa(index), AuthorKind: FeedAuthorAdmin, Content: "看看"}}
		}
		store.posts = append(store.posts, post)
	}
	tool := feedTestTool(t, store)
	type listResult struct {
		Message    string     `json:"message"`
		Items      []FeedPost `json:"items"`
		Total      int        `json:"total"`
		HasMore    bool       `json:"has_more"`
		NextOffset int        `json:"next_offset"`
	}
	run := func(input map[string]any) listResult {
		t.Helper()
		out, err := tool.Run(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		var result listResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("decode %s: %v", out, err)
		}
		return result
	}
	first := run(map[string]any{"operation": "list"})
	if first.Total != total || len(first.Items) != defaultFeedListLimit || !first.HasMore || first.NextOffset != defaultFeedListLimit {
		t.Fatalf("first page = total %d items %d has_more %v next %d", first.Total, len(first.Items), first.HasMore, first.NextOffset)
	}
	if !strings.Contains(first.Message, strconv.Itoa(total)) {
		t.Fatalf("message should report real total: %q", first.Message)
	}
	// 加大 limit 后装不下单次上限的整条留到下一页，按 next_offset 读完全部。
	seen := len(first.Items)
	for page, offset := 0, first.NextOffset; ; page++ {
		rest := run(map[string]any{"operation": "list", "limit": 1000, "offset": offset})
		if rest.Total != total || len(rest.Items) == 0 || rest.Items[0].ID != "post-"+strconv.Itoa(offset) {
			t.Fatalf("offset %d page = total %d items %d", offset, rest.Total, len(rest.Items))
		}
		seen += len(rest.Items)
		if !rest.HasMore {
			break
		}
		offset = rest.NextOffset
		if page > total {
			t.Fatal("分页没有前进")
		}
	}
	if seen != total {
		t.Fatalf("读完全部页共 %d 条，want %d", seen, total)
	}
	waiting := run(map[string]any{"operation": "list", "needs_reply": true, "limit": 1000})
	if waiting.Total != total/10 || len(waiting.Items) == 0 || waiting.Items[0].ID != "post-9" {
		t.Fatalf("needs_reply = total %d items %d", waiting.Total, len(waiting.Items))
	}
}
