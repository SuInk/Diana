// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestFeedPostRoundTripWithImagesAndPaging(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	base := time.Unix(1_788_247_000, 0)

	first, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{
		ProfileID: "bot-1", Kind: "diary", Title: "今天", Content: "第一段\n\n第二段",
		Images: []assistant.FeedImageInput{
			{Data: []byte("img-a"), MIME: "image/png", Width: 2, Height: 3},
			{Data: []byte("img-b"), MIME: "image/jpeg"},
		},
		Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-1", Content: "随手一句", Now: base.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-2", Content: "别的机器人", Now: base.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	posts, err := store.ListFeedPosts(ctx, assistant.FeedListQuery{ProfileID: "bot-1", Limit: 10})
	if err != nil || len(posts) != 2 {
		t.Fatalf("bot-1 posts = %#v, err=%v", posts, err)
	}
	if posts[0].Content != "随手一句" || posts[1].ID != first.ID {
		t.Fatalf("expected newest first, got %#v", posts)
	}
	if len(posts[0].Images) != 0 || len(posts[1].Images) != 2 || posts[1].Images[1].Position != 1 {
		t.Fatalf("images = %#v / %#v", posts[0].Images, posts[1].Images)
	}
	if all, _ := store.ListFeedPosts(ctx, assistant.FeedListQuery{Limit: 10}); len(all) != 3 {
		t.Fatalf("empty profile should list every bot, got %d", len(all))
	}
	if diaries, _ := store.ListFeedPosts(ctx, assistant.FeedListQuery{Kind: "diary", Limit: 10}); len(diaries) != 1 || diaries[0].Kind != "diary" {
		t.Fatalf("diaries = %#v", diaries)
	}
	older, err := store.ListFeedPosts(ctx, assistant.FeedListQuery{ProfileID: "bot-1", Before: posts[0].CreatedAt, Limit: 10})
	if err != nil || len(older) != 1 || older[0].ID != first.ID {
		t.Fatalf("older = %#v, err=%v", older, err)
	}

	data, mime, found, err := store.FeedImageData(ctx, first.Images[0].ID)
	if err != nil || !found || string(data) != "img-a" || mime != "image/png" {
		t.Fatalf("image = %q %q %v %v", data, mime, found, err)
	}
}

func TestDeleteFeedPostRemovesImagesAndRespectsProfile(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	post, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{
		ProfileID: "bot-1", Content: "x",
		Images: []assistant.FeedImageInput{{Data: []byte("a"), MIME: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteFeedPost(ctx, "bot-2", post.ID); !errors.Is(err, assistant.ErrFeedPostNotFound) {
		t.Fatalf("other bot must not delete, err=%v", err)
	}
	if _, err := store.DeleteFeedPost(ctx, "bot-1", post.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, found, _ := store.FeedImageData(ctx, post.Images[0].ID); found {
		t.Fatal("images must be deleted with the post")
	}
	if _, err := store.DeleteFeedPost(ctx, "", post.ID); !errors.Is(err, assistant.ErrFeedPostNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
}

func TestCreateFeedPostRejectsEmptyContentAndTooManyImages(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	if _, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-1", Content: "  \n "}); err == nil {
		t.Fatal("empty content must be rejected")
	}
	images := make([]assistant.FeedImageInput, assistant.FeedMaxImages+1)
	for i := range images {
		images[i] = assistant.FeedImageInput{Data: []byte("a"), MIME: "image/png"}
	}
	if _, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-1", Content: "x", Images: images}); err == nil {
		t.Fatal("too many images must be rejected")
	}
	if posts, _ := store.ListFeedPosts(ctx, assistant.FeedListQuery{Limit: 5}); len(posts) != 0 {
		t.Fatalf("rejected posts must not persist: %#v", posts)
	}
}

func TestFeedCommentsThreadFlattenAndDelete(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	base := time.Unix(1_788_247_000, 0)
	post, err := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-1", Content: "x", Now: base})
	if err != nil {
		t.Fatal(err)
	}
	top, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, AuthorKind: "admin", Content: "好看", Now: base.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, ReplyToID: top.ID, AuthorKind: "bot", ProfileID: "bot-1", Content: "谢谢", Now: base.Add(2 * time.Second)})
	if err != nil || reply.ParentID != top.ID || reply.ReplyToAuthor != "admin" {
		t.Fatalf("reply = %#v, err=%v", reply, err)
	}
	// 回复的回复仍挂在顶层评论下，但记着具体回的是谁。
	nested, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, ReplyToID: reply.ID, AuthorKind: "admin", Content: "不客气", Now: base.Add(3 * time.Second)})
	if err != nil || nested.ParentID != top.ID || nested.ReplyToID != reply.ID || nested.ReplyToAuthor != "bot" {
		t.Fatalf("nested = %#v, err=%v", nested, err)
	}
	posts, _ := store.ListFeedPosts(ctx, assistant.FeedListQuery{Limit: 5})
	if len(posts[0].Comments) != 3 || posts[0].Comments[0].ID != top.ID {
		t.Fatalf("comments = %#v", posts[0].Comments)
	}

	if _, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, ProfileID: "bot-2", Content: "越权"}); !errors.Is(err, assistant.ErrFeedPostNotFound) {
		t.Fatalf("other bot must not comment on this post, err=%v", err)
	}
	if _, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, ReplyToID: "missing", Content: "x"}); !errors.Is(err, assistant.ErrFeedCommentNotFound) {
		t.Fatalf("missing reply target err=%v", err)
	}

	if _, err := store.DeleteFeedComment(ctx, post.ID, top.ID); err != nil {
		t.Fatal(err)
	}
	posts, _ = store.ListFeedPosts(ctx, assistant.FeedListQuery{Limit: 5})
	if len(posts[0].Comments) != 0 {
		t.Fatalf("deleting a top-level comment must remove its thread: %#v", posts[0].Comments)
	}
	other, _ := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, Content: "又一条"})
	if _, err := store.DeleteFeedPost(ctx, "", post.ID); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM feed_comments WHERE id = ?`, other.ID).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("deleting a post must remove its comments, orphans=%d err=%v", orphans, err)
	}
}

func TestFeedCommentCapacity(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	post, _ := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-1", Content: "x"})
	for i := 0; i < assistant.FeedMaxCommentsPerPost; i++ {
		if _, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, Content: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddFeedComment(ctx, assistant.FeedCommentRequest{PostID: post.ID, Content: "one more"}); !errors.Is(err, assistant.ErrFeedCommentCapacity) {
		t.Fatalf("err=%v", err)
	}
}

func TestFeedLikesAreIdempotentPerAuthor(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	post, _ := store.CreateFeedPost(ctx, assistant.FeedPostCreateRequest{ProfileID: "bot-1", Content: "x"})

	likes, err := store.SetFeedLike(ctx, "", post.ID, "admin", true, time.Time{})
	if err != nil || likes.Count != 1 || !likes.LikedByAdmin || likes.LikedByBot {
		t.Fatalf("likes = %#v err=%v", likes, err)
	}
	if likes, _ = store.SetFeedLike(ctx, "", post.ID, "admin", true, time.Time{}); likes.Count != 1 {
		t.Fatalf("重复点赞不能多算：%#v", likes)
	}
	if likes, _ = store.SetFeedLike(ctx, "bot-1", post.ID, "bot", true, time.Time{}); likes.Count != 2 || !likes.LikedByBot {
		t.Fatalf("likes = %#v", likes)
	}
	posts, _ := store.ListFeedPosts(ctx, assistant.FeedListQuery{Limit: 5})
	if posts[0].LikeCount != 2 || !posts[0].LikedByAdmin || !posts[0].LikedByBot {
		t.Fatalf("列表应带点赞状态：%#v", posts[0])
	}
	if likes, _ = store.SetFeedLike(ctx, "", post.ID, "admin", false, time.Time{}); likes.Count != 1 || likes.LikedByAdmin {
		t.Fatalf("取消点赞：%#v", likes)
	}
	if _, err := store.SetFeedLike(ctx, "bot-2", post.ID, "bot", true, time.Time{}); !errors.Is(err, assistant.ErrFeedPostNotFound) {
		t.Fatalf("别的机器人不能给这条点赞：%v", err)
	}
	if _, err := store.DeleteFeedPost(ctx, "", post.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM feed_likes WHERE post_id = ?`, post.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("删帖要带走点赞：%d %v", left, err)
	}
	if got, _ := store.ListFeedPosts(ctx, assistant.FeedListQuery{ID: "nope", Limit: 1}); len(got) != 0 {
		t.Fatalf("ID 过滤：%#v", got)
	}
}
