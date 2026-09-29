// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

// 动态页的接口。
//
// 发帖只有机器人自己能做（对话里的 feed_post 工具）：这一页的意义就是「它自己写了什么」，
// 人代笔发出来就不是它的动态了。控制台只有看和删——主人要能翻它的时间线，也要能把不该
// 留的抹掉。
func (h *BotHandler) registerFeedRoutes(router gin.IRouter, base string) {
	router.GET(base+"/feed", h.listFeed)
	router.GET(base+"/feed/images/:id", h.feedImage)
	router.DELETE(base+"/feed/:id", h.deleteFeedPost)
	router.POST(base+"/feed/:id/like", h.likeFeedPost)
	router.POST(base+"/feed/:id/comments", h.addFeedComment)
	router.DELETE(base+"/feed/:id/comments/:comment", h.deleteFeedComment)
}

const (
	defaultFeedPageSize = 20
	maximumFeedPageSize = 50
)

type feedListResponse struct {
	Posts []assistant.FeedPost `json:"posts"`
	// NextBefore 非空表示后面还有更早的，原样作为下一页的 before 传回来。
	NextBefore string `json:"next_before,omitempty"`
}

func (h *BotHandler) listFeed(c *gin.Context) {
	limit := defaultFeedPageSize
	if parsed, err := strconv.Atoi(c.Query("limit")); err == nil && parsed > 0 {
		limit = min(parsed, maximumFeedPageSize)
	}
	query := assistant.FeedListQuery{ProfileID: botProfileScope(c), Limit: limit + 1}
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		query.Kind = assistant.NormalizeFeedKind(kind)
	}
	if before := strings.TrimSpace(c.Query("before")); before != "" {
		parsed, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			h.writeError(c, http.StatusBadRequest, "feed_list", err, "", nil)
			return
		}
		query.Before = parsed
	}
	posts, err := h.sqlite.ListFeedPosts(c.Request.Context(), query)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "feed_list", err, "", nil)
		return
	}
	response := feedListResponse{Posts: posts}
	if len(posts) > limit {
		// 多取的一条只用来判断还有没有下一页，不返回。
		response.Posts = posts[:limit]
		response.NextBefore = posts[limit-1].CreatedAt.Format(time.RFC3339Nano)
	}
	if response.Posts == nil {
		response.Posts = []assistant.FeedPost{}
	}
	c.JSON(http.StatusOK, response)
}

func (h *BotHandler) feedImage(c *gin.Context) {
	data, mime, found, err := h.sqlite.FeedImageData(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "feed_image", err, c.Param("id"), nil)
		return
	}
	if !found {
		c.Status(http.StatusNotFound)
		return
	}
	// 图片 ID 对应的内容永远不变（删帖只是让这个 ID 失效），可以放心长期缓存。
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, mime, data)
}

func (h *BotHandler) deleteFeedPost(c *gin.Context) {
	post, err := h.sqlite.DeleteFeedPost(c.Request.Context(), botProfileScope(c), c.Param("id"))
	if errors.Is(err, assistant.ErrFeedPostNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "这条动态不存在，可能已经被删除"})
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "feed_delete", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "feed_delete", "动态已删除", post.ID, map[string]any{
		"kind": post.Kind, "title": post.Title, "images": len(post.Images),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type feedCommentPayload struct {
	Content string `json:"content"`
	ReplyTo string `json:"reply_to"`
}

// addFeedComment 是控制台里的主人评论或回复。机器人自己的评论走对话里的 feed_post 工具。
func (h *BotHandler) addFeedComment(c *gin.Context) {
	var payload feedCommentPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "feed_comment", err, "", nil)
		return
	}
	if assistant.NormalizeFeedComment(payload.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "评论内容不能为空"})
		return
	}
	comment, err := h.sqlite.AddFeedComment(c.Request.Context(), assistant.FeedCommentRequest{
		PostID: c.Param("id"), ReplyToID: payload.ReplyTo, AuthorKind: assistant.FeedAuthorAdmin, Content: payload.Content,
	})
	switch {
	case errors.Is(err, assistant.ErrFeedPostNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "这条动态不存在，可能已经被删除"})
		return
	case errors.Is(err, assistant.ErrFeedCommentNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "要回复的评论不存在，可能已经被删除"})
		return
	case errors.Is(err, assistant.ErrFeedCommentCapacity):
		c.JSON(http.StatusConflict, gin.H{"error": "这条动态下的评论已经到上限了"})
		return
	case err != nil:
		h.writeError(c, http.StatusInternalServerError, "feed_comment", err, c.Param("id"), nil)
		return
	}
	// 开了自动回复就在后台排一条回复；前端据 reply_pending 去轮询新评论。
	queued := false
	if queuer, ok := h.runtime.(feedReplyQueuer); ok {
		queued = queuer.QueueFeedReply(c.Param("id"))
	}
	c.JSON(http.StatusOK, feedCommentResponse{Comment: comment, ReplyPending: queued})
}

// feedReplyQueuer 是运行时可选的能力：BotRuntime 接口不为它扩大，测试里的替身不用跟着改。
type feedReplyQueuer interface {
	QueueFeedReply(postID string) bool
}

type feedCommentResponse struct {
	Comment      assistant.FeedComment `json:"comment"`
	ReplyPending bool                  `json:"reply_pending"`
}

func (h *BotHandler) deleteFeedComment(c *gin.Context) {
	comment, err := h.sqlite.DeleteFeedComment(c.Request.Context(), c.Param("id"), c.Param("comment"))
	if errors.Is(err, assistant.ErrFeedCommentNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "这条评论不存在，可能已经被删除"})
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "feed_comment_delete", err, c.Param("comment"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "feed_comment_delete", "动态评论已删除", comment.ID, map[string]any{"post_id": comment.PostID})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type feedLikePayload struct {
	Liked bool `json:"liked"`
}

// likeFeedPost 是控制台里的主人点赞或取消点赞。
func (h *BotHandler) likeFeedPost(c *gin.Context) {
	var payload feedLikePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "feed_like", err, "", nil)
		return
	}
	likes, err := h.sqlite.SetFeedLike(c.Request.Context(), "", c.Param("id"), assistant.FeedAuthorAdmin, payload.Liked, time.Now())
	if errors.Is(err, assistant.ErrFeedPostNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "这条动态不存在，可能已经被删除"})
		return
	}
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "feed_like", err, c.Param("id"), nil)
		return
	}
	c.JSON(http.StatusOK, likes)
}
