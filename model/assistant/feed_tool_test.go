// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
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

// matchingPosts 照 SQLite 的语义筛选：(created_at, id) 新到旧，键集游标只要严格排在
// 它后面的，needs_reply 看最新一条评论是不是主人写的。
func (s *stubFeedStore) matchingPosts(query FeedListQuery) []FeedPost {
	posts := make([]FeedPost, 0, len(s.posts))
	for _, post := range s.posts {
		if query.ID != "" && post.ID != query.ID || query.Kind != "" && post.Kind != query.Kind {
			continue
		}
		if !query.Before.IsZero() && !post.CreatedAt.Before(query.Before) &&
			!(post.CreatedAt.Equal(query.Before) && query.BeforeID != "" && post.ID < query.BeforeID) {
			continue
		}
		if last := len(post.Comments) - 1; query.NeedsReply && (last < 0 || post.Comments[last].AuthorKind != FeedAuthorAdmin) {
			continue
		}
		posts = append(posts, post)
	}
	sort.SliceStable(posts, func(i, j int) bool {
		if !posts[i].CreatedAt.Equal(posts[j].CreatedAt) {
			return posts[i].CreatedAt.After(posts[j].CreatedAt)
		}
		return posts[i].ID > posts[j].ID
	})
	return posts
}

func (s *stubFeedStore) ListFeedPosts(_ context.Context, query FeedListQuery) ([]FeedPost, error) {
	posts := s.matchingPosts(query)
	if query.Limit > 0 && len(posts) > query.Limit {
		posts = posts[:query.Limit]
	}
	for i := range posts {
		posts[i].CommentCount = len(posts[i].Comments)
		if query.LatestComments > 0 && len(posts[i].Comments) > query.LatestComments {
			posts[i].Comments = posts[i].Comments[len(posts[i].Comments)-query.LatestComments:]
		}
	}
	return posts, nil
}

func (s *stubFeedStore) CountFeedPosts(_ context.Context, query FeedListQuery) (int, error) {
	return len(s.matchingPosts(query)), nil
}

func (s *stubFeedStore) matchingComments(query FeedCommentQuery) ([]FeedComment, error) {
	for _, post := range s.posts {
		if post.ID != query.PostID {
			continue
		}
		comments := []FeedComment{}
		for _, comment := range post.Comments {
			if query.After.IsZero() || comment.CreatedAt.After(query.After) || comment.CreatedAt.Equal(query.After) && comment.ID > query.AfterID {
				comments = append(comments, comment)
			}
		}
		return comments, nil
	}
	return nil, ErrFeedPostNotFound
}

func (s *stubFeedStore) ListFeedComments(_ context.Context, query FeedCommentQuery) ([]FeedComment, error) {
	comments, err := s.matchingComments(query)
	if query.Limit > 0 && len(comments) > query.Limit {
		comments = comments[:query.Limit]
	}
	return comments, err
}

func (s *stubFeedStore) CountFeedComments(_ context.Context, query FeedCommentQuery) (int, error) {
	comments, err := s.matchingComments(query)
	return len(comments), err
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

type feedListTestResult struct {
	Message    string     `json:"message"`
	Items      []FeedPost `json:"items"`
	Total      int        `json:"total"`
	HasMore    bool       `json:"has_more"`
	Remaining  int        `json:"remaining_count"`
	NextCursor string     `json:"next_cursor"`
	PageNote   string     `json:"page_note"`
}

func runFeedList(t *testing.T, ctx context.Context, tool *dianaFeedTool, input map[string]any) feedListTestResult {
	t.Helper()
	out, err := tool.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var result feedListTestResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return result
}

// feedTestPosts 造 count 条动态，post-0 最新；index%10==9 的最新一条评论是主人写的。
func feedTestPosts(count int) []FeedPost {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	posts := make([]FeedPost, 0, count)
	for index := 0; index < count; index++ {
		post := FeedPost{ID: fmt.Sprintf("post-%03d", index), Content: "第 " + strconv.Itoa(index) + " 条", CreatedAt: base.Add(-time.Duration(index) * time.Minute)}
		if index%10 == 9 {
			post.Comments = []FeedComment{{ID: "c-" + strconv.Itoa(index), PostID: post.ID, AuthorKind: FeedAuthorAdmin, Content: "看看", CreatedAt: post.CreatedAt}}
		}
		posts = append(posts, post)
	}
	return posts
}

// list 不设条数上限：总数如实报，按 (created_at, id) 键集游标翻页，加大 limit 后装不下
// 单次上限的整条留到下一页，顺着 next_cursor 不重不漏读完全部。
func TestFeedToolListPagesAllPosts(t *testing.T) {
	const total = 150
	store := &stubFeedStore{posts: feedTestPosts(total)}
	tool := feedTestTool(t, store)
	ctx := context.Background()
	first := runFeedList(t, ctx, tool, map[string]any{"operation": "list"})
	if first.Total != total || len(first.Items) != defaultFeedListLimit || !first.HasMore || first.NextCursor == "" || first.Remaining != total-defaultFeedListLimit {
		t.Fatalf("first page = %+v", first)
	}
	if !strings.Contains(first.Message, strconv.Itoa(total)) || !strings.Contains(first.Message, "operation=comments") {
		t.Fatalf("message should report real total and how to read comments: %q", first.Message)
	}
	seen := map[string]bool{}
	for _, post := range first.Items {
		seen[post.ID] = true
	}
	for page, cursor := 0, first.NextCursor; ; page++ {
		rest := runFeedList(t, ctx, tool, map[string]any{"operation": "list", "limit": 1000, "cursor": cursor})
		if rest.Total != total || len(rest.Items) == 0 {
			t.Fatalf("page %d = %+v", page, rest)
		}
		for _, post := range rest.Items {
			if seen[post.ID] {
				t.Fatalf("post %s returned twice", post.ID)
			}
			seen[post.ID] = true
		}
		if !rest.HasMore {
			break
		}
		cursor = rest.NextCursor
		if page > total {
			t.Fatal("分页没有前进")
		}
	}
	if len(seen) != total {
		t.Fatalf("读完全部页共 %d 条，want %d", len(seen), total)
	}
	waiting := runFeedList(t, ctx, tool, map[string]any{"operation": "list", "needs_reply": true, "limit": 1000})
	if waiting.Total != total/10 || len(waiting.Items) != total/10 || waiting.Items[0].ID != "post-009" {
		t.Fatalf("needs_reply = total %d items %d", waiting.Total, len(waiting.Items))
	}
}

// 按单次上限切页：上限很小时每页只装下几条，page_note 写明是单次上限装不下，游标续读拿全。
func TestFeedToolListSplitsAtOutputBudget(t *testing.T) {
	const total = 12
	posts := feedTestPosts(total)
	for i := range posts {
		posts[i].Content = strings.Repeat("长", 400)
	}
	tool := feedTestTool(t, &stubFeedStore{posts: posts})
	ctx := agent.WithToolOutputBudget(context.Background(), 2500)
	seen := 0
	cursor := ""
	for page := 0; ; page++ {
		input := map[string]any{"operation": "list", "limit": 100}
		if cursor != "" {
			input["cursor"] = cursor
		}
		result := runFeedList(t, ctx, tool, input)
		if len(result.Items) == 0 || len(result.Items) >= total {
			t.Fatalf("page %d should be cut by budget: %d items", page, len(result.Items))
		}
		if result.Items[0].ID != fmt.Sprintf("post-%03d", seen) {
			t.Fatalf("page %d starts at %s, want post-%03d", page, result.Items[0].ID, seen)
		}
		seen += len(result.Items)
		if !result.HasMore {
			break
		}
		if !strings.Contains(result.PageNote, "单次结果上限") || result.Remaining != total-seen {
			t.Fatalf("page %d note=%q remaining=%d", page, result.PageNote, result.Remaining)
		}
		cursor = result.NextCursor
	}
	if seen != total {
		t.Fatalf("seen %d, want %d", seen, total)
	}
}

// needs_reply 翻页时前一页的动态回复完离开筛选结果，下一页照样从没看过的那条接着给：
// offset 翻页会在这里跳过一批。
func TestFeedToolNeedsReplyPagingSurvivesReplies(t *testing.T) {
	store := &stubFeedStore{posts: feedTestPosts(100)}
	tool := feedTestTool(t, store)
	ctx := context.Background()
	first := runFeedList(t, ctx, tool, map[string]any{"operation": "list", "needs_reply": true, "limit": 3})
	if first.Total != 10 || len(first.Items) != 3 || !first.HasMore {
		t.Fatalf("first page = %+v", first)
	}
	// 回复掉第一页的三条：它们的最新评论变成机器人写的。
	for _, done := range first.Items {
		for i := range store.posts {
			if store.posts[i].ID == done.ID {
				store.posts[i].Comments = append(store.posts[i].Comments, FeedComment{ID: "r-" + done.ID, PostID: done.ID, AuthorKind: FeedAuthorBot, Content: "好", CreatedAt: done.CreatedAt.Add(time.Second)})
			}
		}
	}
	second := runFeedList(t, ctx, tool, map[string]any{"operation": "list", "needs_reply": true, "limit": 3, "cursor": first.NextCursor})
	if len(second.Items) != 3 || second.Items[0].ID != "post-039" || second.Remaining != 4 {
		t.Fatalf("second page = %+v", second)
	}
}

// list 只带最新几条评论和评论总数；comments 按时间正序分页读全部评论，单次上限装不下时
// 用 cursor 续读，翻页期间新来的评论排在末尾，不打乱已读的部分。
func TestFeedToolCommentsPageSeparately(t *testing.T) {
	posts := feedTestPosts(1)
	base := posts[0].CreatedAt
	for i := 0; i < 40; i++ {
		author := FeedAuthorBot
		if i%2 == 0 {
			author = FeedAuthorAdmin
		}
		posts[0].Comments = append(posts[0].Comments, FeedComment{
			ID: fmt.Sprintf("c-%02d", i), PostID: posts[0].ID, AuthorKind: author,
			Content: strings.Repeat("评", 300), CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	store := &stubFeedStore{posts: posts}
	tool := feedTestTool(t, store)
	ctx := agent.WithToolOutputBudget(context.Background(), 4000)
	list := runFeedList(t, ctx, tool, map[string]any{"operation": "list"})
	if len(list.Items) != 1 || list.Items[0].CommentCount != 40 || len(list.Items[0].Comments) != feedListLatestComments || list.Items[0].Comments[feedListLatestComments-1].ID != "c-39" {
		t.Fatalf("list item = %+v", list.Items)
	}
	type commentsResult struct {
		Total      int           `json:"total"`
		Items      []FeedComment `json:"items"`
		HasMore    bool          `json:"has_more"`
		NextCursor string        `json:"next_cursor"`
		PageNote   string        `json:"page_note"`
	}
	var got []string
	cursor := ""
	for page := 0; ; page++ {
		input := map[string]any{"operation": "comments", "id": posts[0].ID, "limit": 1000}
		if cursor != "" {
			input["cursor"] = cursor
		}
		out, err := tool.Run(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		var result commentsResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if result.Total < 40 || len(result.Items) == 0 || (result.HasMore && !strings.Contains(result.PageNote, "单次结果上限")) {
			t.Fatalf("page %d = %+v", page, result)
		}
		for _, comment := range result.Items {
			got = append(got, comment.ID)
		}
		if page == 0 {
			// 翻页期间来了一条新评论：排在末尾，后面的页照样给到。
			store.posts[0].Comments = append(store.posts[0].Comments, FeedComment{ID: "c-40", PostID: posts[0].ID, AuthorKind: FeedAuthorAdmin, Content: "新的", CreatedAt: base.Add(41 * time.Minute)})
		}
		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
		if page > 50 {
			t.Fatal("分页没有前进")
		}
	}
	if len(got) != 41 || got[0] != "c-00" || got[40] != "c-40" {
		t.Fatalf("comments = %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("comments out of order or repeated: %v", got)
		}
	}
	if _, err := tool.Run(ctx, map[string]any{"operation": "comments", "id": "nope"}); err == nil {
		t.Fatal("comments on a missing post must fail")
	}
}
