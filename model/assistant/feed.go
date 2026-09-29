// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 动态：机器人在 WebUI「动态」页里自己发的帖子，可以是随手一句、也可以是一篇日记，
// 可以配图。
//
// 它和自述、笔记本的区别是读者：那两层是写给模型自己下一轮看的，动态是写给人看的，
// 不进任何提示词。所以这里没有注入层、没有 token 预算，只有存储、发布工具和控制台页面。
const (
	dianaFeedToolName = "feed_post"

	FeedKindPost  = "post"
	FeedKindDiary = "diary"

	// FeedTitleMaxRunes 限制标题长度；标题可选，日记常用，随手一句不需要。
	FeedTitleMaxRunes = 40
	// FeedContentMaxRunes 限制正文。日记要写得开，但不是长文：控制台是时间线，
	// 一条动辄上万字会把整页撑开。
	FeedContentMaxRunes = 3000
	// FeedMaxImages 是一条动态最多配几张图，和常见的九宫格一致。
	FeedMaxImages = 9
	// FeedImageMaxBytes 限制单张图。图存在数据库里，放开到几十 MB 会让备份和查询都变慢。
	FeedImageMaxBytes = 10 << 20
	// FeedPostImagesMaxBytes 限制一条动态所有图加起来的大小。
	FeedPostImagesMaxBytes = 32 << 20
	// FeedCommentMaxRunes 限制单条评论，评论是一两句话，不是另一篇日记。
	FeedCommentMaxRunes = 500
	// FeedMaxCommentsPerPost 限制一条动态下的评论总数，防止一个帖子被刷成无限长的串。
	FeedMaxCommentsPerPost = 200

	FeedAuthorBot   = "bot"
	FeedAuthorAdmin = "admin"
)

// ErrFeedPostNotFound 表示要删的动态不存在（或属于别的机器人）。
var ErrFeedPostNotFound = errors.New("feed post not found")

// ErrFeedCommentNotFound 表示要删的评论或回复的目标评论不存在。
var ErrFeedCommentNotFound = errors.New("feed comment not found")

// ErrFeedCommentCapacity 表示这条动态下的评论已满。
var ErrFeedCommentCapacity = errors.New("feed comment capacity reached")

// FeedComment 是动态下的一条评论。串只有一层缩进：ParentID 是这条回复所在串的顶层
// 评论，回复的回复仍挂在同一条顶层评论下；ReplyToID 和 ReplyToAuthor 说明它具体回的
// 是哪条、谁写的。顶层评论这三个字段都为空。
type FeedComment struct {
	ID            string    `json:"id"`
	PostID        string    `json:"post_id"`
	ParentID      string    `json:"parent_id,omitempty"`
	ReplyToID     string    `json:"reply_to_id,omitempty"`
	ReplyToAuthor string    `json:"reply_to_author,omitempty"`
	AuthorKind    string    `json:"author_kind"`
	Content       string    `json:"content"`
	CreatedAt     time.Time `json:"created_at"`
}

// FeedLikes 是一条动态的点赞状态。
type FeedLikes struct {
	Count        int  `json:"like_count"`
	LikedByBot   bool `json:"liked_by_bot"`
	LikedByAdmin bool `json:"liked_by_admin"`
}

// FeedCommentRequest 是一次评论。AuthorKind 是 bot（机器人自己）或 admin（控制台里的主人）。
type FeedCommentRequest struct {
	PostID string
	// ReplyToID 非空表示回复这条评论；存储层会把它归到所在串的顶层评论下。
	ReplyToID  string
	AuthorKind string
	Content    string
	// ProfileID 非空时要求这条动态属于该机器人（机器人工具用）；控制台留空。
	ProfileID string
	Now       time.Time
}

// FeedImage 是动态的一张配图。字节不随列表返回，控制台按 ID 单独取。
type FeedImage struct {
	ID       string `json:"id"`
	MIME     string `json:"mime"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Bytes    int    `json:"bytes"`
	Position int    `json:"position"`
}

// FeedPost 是一条动态。它按机器人档案存，不分群：动态是机器人自己的时间线。
type FeedPost struct {
	ID        string      `json:"id"`
	ProfileID string      `json:"profile_id,omitempty"`
	Kind      string      `json:"kind"`
	Title     string      `json:"title,omitempty"`
	Content   string      `json:"content"`
	Images    []FeedImage `json:"images"`
	// Comments 按时间正序，列表接口一并返回。
	Comments []FeedComment `json:"comments"`
	// LikeCount 是赞数；LikedByBot、LikedByAdmin 说明机器人和主人各自点没点。
	LikeCount    int  `json:"like_count"`
	LikedByBot   bool `json:"liked_by_bot"`
	LikedByAdmin bool `json:"liked_by_admin"`
	// SourceSession 记下是在哪次对话里发的，主人要能回溯「这条是谁让它发的」。
	SourceSession string    `json:"source_session,omitempty"`
	SourceUserID  string    `json:"source_user_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// FeedImageInput 是待存的一张图。
type FeedImageInput struct {
	Data   []byte
	MIME   string
	Width  int
	Height int
}

// FeedPostCreateRequest 是一次发布。
type FeedPostCreateRequest struct {
	ProfileID     string
	Kind          string
	Title         string
	Content       string
	Images        []FeedImageInput
	SourceSession string
	SourceUserID  string
	Now           time.Time
}

// FeedListQuery 是列表查询。Before 非零时只取更早的，用来往下翻页。
type FeedListQuery struct {
	// ID 非空时只取这一条。
	ID string
	// ProfileID 为空表示所有机器人。
	ProfileID string
	Kind      string
	Before    time.Time
	Limit     int
}

// FeedStore 是动态的持久化。
type FeedStore interface {
	CreateFeedPost(context.Context, FeedPostCreateRequest) (FeedPost, error)
	ListFeedPosts(context.Context, FeedListQuery) ([]FeedPost, error)
	// DeleteFeedPost 连同配图一起删掉。profileID 为空时不限机器人（控制台在
	// 「全部机器人」视图里删）。找不到返回 ErrFeedPostNotFound。
	DeleteFeedPost(ctx context.Context, profileID, id string) (FeedPost, error)
	// AddFeedComment 给动态加一条评论或回复。动态不存在返回 ErrFeedPostNotFound，
	// 回复的目标不在这条动态下返回 ErrFeedCommentNotFound，评论已满返回 ErrFeedCommentCapacity。
	AddFeedComment(context.Context, FeedCommentRequest) (FeedComment, error)
	// DeleteFeedComment 删除一条评论，连同挂在它下面的回复。
	DeleteFeedComment(ctx context.Context, postID, commentID string) (FeedComment, error)
	// SetFeedLike 点赞或取消点赞，幂等：重复点赞不会多算一次。profileID 非空时要求动态
	// 属于该机器人。返回这条动态点赞后的状态。
	SetFeedLike(ctx context.Context, profileID, postID, authorKind string, liked bool, now time.Time) (FeedLikes, error)
	// FeedImageData 取一张配图的字节。
	FeedImageData(ctx context.Context, id string) (data []byte, mime string, found bool, err error)
}

// SetFeedStore 注入动态存储。
func (r *Runtime) SetFeedStore(store FeedStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.feed = store
}

func (r *Runtime) feedStore() FeedStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.feed
}

// NormalizeFeedKind 只认 post 和 diary，其余按 post 处理。
func NormalizeFeedKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case FeedKindDiary, "日记":
		return FeedKindDiary
	default:
		return FeedKindPost
	}
}

// NormalizeFeedTitle 压成单行并截断。
func NormalizeFeedTitle(raw string) string {
	return truncateRunesPlain(strings.Join(strings.Fields(raw), " "), FeedTitleMaxRunes)
}

// NormalizeFeedContent 保留换行（日记要分段），去掉首尾空白和每行尾部空格，
// 三个以上连续空行收成一个。超长时截断。
func NormalizeFeedContent(raw string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return truncateRunesPlain(strings.TrimSpace(strings.Join(out, "\n")), FeedContentMaxRunes)
}

// NormalizeFeedComment 压成单行并截断：评论是一两句话。
func NormalizeFeedComment(raw string) string {
	return truncateRunesPlain(strings.Join(strings.Fields(raw), " "), FeedCommentMaxRunes)
}

// NormalizeFeedAuthor 只认 bot 和 admin。
func NormalizeFeedAuthor(raw string) string {
	if strings.TrimSpace(raw) == FeedAuthorBot {
		return FeedAuthorBot
	}
	return FeedAuthorAdmin
}
