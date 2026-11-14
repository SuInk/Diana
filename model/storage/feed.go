// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/google/uuid"
)

// 动态的持久化：一张帖子表，一张配图表。
//
// 配图字节直接存在数据库里而不是散落成文件：动态是长期保留的内容，不能像工作目录的
// downloads/ 那样过期清理，放进同一个库里备份、迁移、Docker 挂载都不用多管一份目录。
// 代价是库会变大，所以单张、整条都有硬上限（见 assistant.FeedImageMaxBytes）。
//
// 列表查询只取元数据，字节不进列表；控制台按图片 ID 单独取，并靠内容不可变让浏览器长期缓存。
const feedSchema = `
CREATE TABLE IF NOT EXISTS feed_posts (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK (kind IN ('post', 'diary')),
  title TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  source_session TEXT NOT NULL DEFAULT '',
  source_user_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_feed_posts_profile_time ON feed_posts(profile_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feed_posts_time ON feed_posts(created_at DESC);

CREATE TABLE IF NOT EXISTS feed_images (
  id TEXT PRIMARY KEY,
  post_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  mime TEXT NOT NULL,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  size INTEGER NOT NULL,
  data BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_feed_images_post ON feed_images(post_id, position);

CREATE TABLE IF NOT EXISTS feed_comments (
  id TEXT PRIMARY KEY,
  post_id TEXT NOT NULL,
  parent_id TEXT NOT NULL DEFAULT '',
  reply_to_id TEXT NOT NULL DEFAULT '',
  reply_to_author TEXT NOT NULL DEFAULT '',
  author_kind TEXT NOT NULL CHECK (author_kind IN ('bot', 'admin')),
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_feed_comments_post ON feed_comments(post_id, created_at);

CREATE TABLE IF NOT EXISTS feed_likes (
  post_id TEXT NOT NULL,
  author_kind TEXT NOT NULL CHECK (author_kind IN ('bot', 'admin')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (post_id, author_kind)
);
`

func (s *SQLiteStore) migrateFeed() error {
	if _, err := s.db.Exec(feedSchema); err != nil {
		return fmt.Errorf("create feed schema: %w", err)
	}
	return nil
}

// CreateFeedPost 发布一条动态。帖子和配图在同一个事务里写入，不会留下没有图的半条帖子。
func (s *SQLiteStore) CreateFeedPost(ctx context.Context, request assistant.FeedPostCreateRequest) (assistant.FeedPost, error) {
	defer s.observeStorage(ctx, "CreateFeedPost", "write")()
	request.Content = assistant.NormalizeFeedContent(request.Content)
	if request.Content == "" {
		return assistant.FeedPost{}, fmt.Errorf("feed post content is empty")
	}
	if len(request.Images) > assistant.FeedMaxImages {
		return assistant.FeedPost{}, fmt.Errorf("feed post has more than %d images", assistant.FeedMaxImages)
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	post := assistant.FeedPost{
		ID:            uuid.NewString(),
		ProfileID:     strings.TrimSpace(request.ProfileID),
		Kind:          assistant.NormalizeFeedKind(request.Kind),
		Title:         assistant.NormalizeFeedTitle(request.Title),
		Content:       request.Content,
		Images:        make([]assistant.FeedImage, 0, len(request.Images)),
		Comments:      []assistant.FeedComment{},
		SourceSession: strings.TrimSpace(request.SourceSession),
		SourceUserID:  strings.TrimSpace(request.SourceUserID),
		CreatedAt:     request.Now,
	}
	tx, err := s.beginWriteTx(ctx, "CreateFeedPost")
	if err != nil {
		return assistant.FeedPost{}, err
	}
	defer observeTransaction("CreateFeedPost")()
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO feed_posts (id, profile_id, kind, title, content, source_session, source_user_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, post.ID, post.ProfileID, post.Kind, post.Title, post.Content, post.SourceSession, post.SourceUserID,
		request.Now.UnixNano()); err != nil {
		return assistant.FeedPost{}, err
	}
	for index, input := range request.Images {
		if len(input.Data) == 0 {
			return assistant.FeedPost{}, fmt.Errorf("feed image %d is empty", index+1)
		}
		image := assistant.FeedImage{
			ID: uuid.NewString(), MIME: input.MIME, Width: input.Width, Height: input.Height,
			Bytes: len(input.Data), Position: index,
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO feed_images (id, post_id, position, mime, width, height, size, data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, image.ID, post.ID, image.Position, image.MIME, image.Width, image.Height, image.Bytes, input.Data); err != nil {
			return assistant.FeedPost{}, err
		}
		post.Images = append(post.Images, image)
	}
	if err := tx.Commit(); err != nil {
		return assistant.FeedPost{}, err
	}
	return post, nil
}

// feedPostFilter 是列表和计数共用的筛选条件。
func feedPostFilter(query assistant.FeedListQuery) (string, []any) {
	var (
		where []string
		args  []any
	)
	if id := strings.TrimSpace(query.ID); id != "" {
		where = append(where, "id = ?")
		args = append(args, id)
	}
	if profile := strings.TrimSpace(query.ProfileID); profile != "" {
		where = append(where, "profile_id = ?")
		args = append(args, profile)
	}
	if query.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, assistant.NormalizeFeedKind(query.Kind))
	}
	if !query.Before.IsZero() {
		if beforeID := strings.TrimSpace(query.BeforeID); beforeID != "" {
			where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
			args = append(args, query.Before.UnixNano(), query.Before.UnixNano(), beforeID)
		} else {
			where = append(where, "created_at < ?")
			args = append(args, query.Before.UnixNano())
		}
	}
	if query.NeedsReply {
		// 最新一条评论（和 attachFeedComments 同一个排序）是主人写的。
		where = append(where, `(SELECT c.author_kind FROM feed_comments c WHERE c.post_id = feed_posts.id
ORDER BY c.created_at DESC, c.id DESC LIMIT 1) = ?`)
		args = append(args, assistant.FeedAuthorAdmin)
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ListFeedPosts 按时间倒序列出动态（新的在前）。
func (s *SQLiteStore) ListFeedPosts(ctx context.Context, query assistant.FeedListQuery) ([]assistant.FeedPost, error) {
	defer s.observeStorage(ctx, "ListFeedPosts", "read")()
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	where, args := feedPostFilter(query)
	statement := `SELECT id, profile_id, kind, title, content, source_session, source_user_id, created_at FROM feed_posts` +
		where + " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.eventReader().QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	posts := make([]assistant.FeedPost, 0, min(limit, 64))
	for rows.Next() {
		var post assistant.FeedPost
		var createdNS int64
		if err := rows.Scan(&post.ID, &post.ProfileID, &post.Kind, &post.Title, &post.Content,
			&post.SourceSession, &post.SourceUserID, &createdNS); err != nil {
			_ = rows.Close()
			return nil, err
		}
		post.CreatedAt = time.Unix(0, createdNS)
		post.Images = []assistant.FeedImage{}
		post.Comments = []assistant.FeedComment{}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if err := s.attachFeedImages(ctx, posts); err != nil {
		return nil, err
	}
	if err := s.attachFeedComments(ctx, posts, query.LatestComments); err != nil {
		return nil, err
	}
	if err := s.attachFeedLikes(ctx, posts); err != nil {
		return nil, err
	}
	return posts, nil
}

// CountFeedPosts 数符合条件（含游标）的动态条数，不看 Limit。
func (s *SQLiteStore) CountFeedPosts(ctx context.Context, query assistant.FeedListQuery) (int, error) {
	defer s.observeStorage(ctx, "CountFeedPosts", "read")()
	where, args := feedPostFilter(query)
	var count int
	err := s.eventReader().QueryRowContext(ctx, `SELECT COUNT(1) FROM feed_posts`+where, args...).Scan(&count)
	return count, err
}

// attachFeedImages 一次查出这一页所有帖子的配图元数据，避免每条帖子各查一次。
func (s *SQLiteStore) attachFeedImages(ctx context.Context, posts []assistant.FeedPost) error {
	if len(posts) == 0 {
		return nil
	}
	index := make(map[string]int, len(posts))
	placeholders := make([]string, len(posts))
	args := make([]any, len(posts))
	for i, post := range posts {
		index[post.ID] = i
		placeholders[i] = "?"
		args[i] = post.ID
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT id, post_id, position, mime, width, height, size FROM feed_images
WHERE post_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY post_id, position
`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var image assistant.FeedImage
		var postID string
		if err := rows.Scan(&image.ID, &postID, &image.Position, &image.MIME, &image.Width, &image.Height, &image.Bytes); err != nil {
			return err
		}
		if i, ok := index[postID]; ok {
			posts[i].Images = append(posts[i].Images, image)
		}
	}
	return rows.Err()
}

// DeleteFeedPost 连同配图一起删除。
func (s *SQLiteStore) DeleteFeedPost(ctx context.Context, profileID, id string) (assistant.FeedPost, error) {
	defer s.observeStorage(ctx, "DeleteFeedPost", "write")()
	id = strings.TrimSpace(id)
	profileID = strings.TrimSpace(profileID)
	tx, err := s.beginWriteTx(ctx, "DeleteFeedPost")
	if err != nil {
		return assistant.FeedPost{}, err
	}
	defer observeTransaction("DeleteFeedPost")()
	defer func() { _ = tx.Rollback() }()
	var post assistant.FeedPost
	var createdNS int64
	err = tx.QueryRowContext(ctx, `
SELECT id, profile_id, kind, title, content, source_session, source_user_id, created_at FROM feed_posts
WHERE id = ? AND (? = '' OR profile_id = ?)
`, id, profileID, profileID).Scan(&post.ID, &post.ProfileID, &post.Kind, &post.Title, &post.Content,
		&post.SourceSession, &post.SourceUserID, &createdNS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return assistant.FeedPost{}, assistant.ErrFeedPostNotFound
	case err != nil:
		return assistant.FeedPost{}, err
	}
	post.CreatedAt = time.Unix(0, createdNS)
	post.Images = []assistant.FeedImage{}
	post.Comments = []assistant.FeedComment{}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_images WHERE post_id = ?`, id); err != nil {
		return assistant.FeedPost{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_comments WHERE post_id = ?`, id); err != nil {
		return assistant.FeedPost{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_likes WHERE post_id = ?`, id); err != nil {
		return assistant.FeedPost{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_posts WHERE id = ?`, id); err != nil {
		return assistant.FeedPost{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.FeedPost{}, err
	}
	return post, nil
}

// FeedImageData 取一张配图的字节和类型。
func (s *SQLiteStore) FeedImageData(ctx context.Context, id string) ([]byte, string, bool, error) {
	defer s.observeStorage(ctx, "FeedImageData", "read")()
	var data []byte
	var mime string
	err := s.eventReader().QueryRowContext(ctx, `SELECT data, mime FROM feed_images WHERE id = ?`, strings.TrimSpace(id)).Scan(&data, &mime)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", false, nil
	case err != nil:
		return nil, "", false, err
	}
	return data, mime, true, nil
}

const feedCommentColumns = `id, post_id, parent_id, reply_to_id, reply_to_author, author_kind, content, created_at`

func scanFeedComment(row interface{ Scan(dest ...any) error }) (assistant.FeedComment, error) {
	var comment assistant.FeedComment
	var createdNS int64
	if err := row.Scan(&comment.ID, &comment.PostID, &comment.ParentID, &comment.ReplyToID,
		&comment.ReplyToAuthor, &comment.AuthorKind, &comment.Content, &createdNS); err != nil {
		return assistant.FeedComment{}, err
	}
	comment.CreatedAt = time.Unix(0, createdNS)
	return comment, nil
}

// attachFeedComments 一次查出这一页所有帖子的评论，按时间正序挂上去。latest 大于 0
// 时每条只挂最新 latest 条；CommentCount 总是这条动态下的评论总数。
func (s *SQLiteStore) attachFeedComments(ctx context.Context, posts []assistant.FeedPost, latest int) error {
	if len(posts) == 0 {
		return nil
	}
	index := make(map[string]int, len(posts))
	args := make([]any, len(posts))
	for i, post := range posts {
		index[post.ID] = i
		args[i] = post.ID
	}
	in := placeholders(len(posts))
	statement := `SELECT ` + feedCommentColumns + ` FROM feed_comments WHERE post_id IN (` + in + `) ORDER BY created_at, id`
	if latest > 0 {
		statement = `SELECT ` + feedCommentColumns + ` FROM (
  SELECT ` + feedCommentColumns + `, ROW_NUMBER() OVER (PARTITION BY post_id ORDER BY created_at DESC, id DESC) AS newest
  FROM feed_comments WHERE post_id IN (` + in + `)
) WHERE newest <= ? ORDER BY created_at, id`
		args = append(args, latest)
	}
	rows, err := s.eventReader().QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		comment, err := scanFeedComment(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if i, ok := index[comment.PostID]; ok {
			posts[i].Comments = append(posts[i].Comments, comment)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if latest <= 0 {
		for i := range posts {
			posts[i].CommentCount = len(posts[i].Comments)
		}
		return nil
	}
	counts, err := s.eventReader().QueryContext(ctx, `SELECT post_id, COUNT(1) FROM feed_comments WHERE post_id IN (`+in+`) GROUP BY post_id`, args[:len(posts)]...)
	if err != nil {
		return err
	}
	defer func() { _ = counts.Close() }()
	for counts.Next() {
		var postID string
		var count int
		if err := counts.Scan(&postID, &count); err != nil {
			return err
		}
		if i, ok := index[postID]; ok {
			posts[i].CommentCount = count
		}
	}
	return counts.Err()
}

// feedCommentFilter 是评论分页和计数共用的条件；动态不存在（或不属于该机器人）时
// 返回 ErrFeedPostNotFound。
func (s *SQLiteStore) feedCommentFilter(ctx context.Context, query assistant.FeedCommentQuery) (string, []any, error) {
	postID, profileID := strings.TrimSpace(query.PostID), strings.TrimSpace(query.ProfileID)
	var exists int
	if err := s.eventReader().QueryRowContext(ctx, `SELECT COUNT(1) FROM feed_posts WHERE id = ? AND (? = '' OR profile_id = ?)`,
		postID, profileID, profileID).Scan(&exists); err != nil {
		return "", nil, err
	}
	if exists == 0 {
		return "", nil, assistant.ErrFeedPostNotFound
	}
	where, args := " WHERE post_id = ?", []any{postID}
	if !query.After.IsZero() {
		where += " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, query.After.UnixNano(), query.After.UnixNano(), strings.TrimSpace(query.AfterID))
	}
	return where, args, nil
}

// ListFeedComments 按时间正序分页读一条动态下的评论。
func (s *SQLiteStore) ListFeedComments(ctx context.Context, query assistant.FeedCommentQuery) ([]assistant.FeedComment, error) {
	defer s.observeStorage(ctx, "ListFeedComments", "read")()
	where, args, err := s.feedCommentFilter(ctx, query)
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = -1 // SQLite 的 LIMIT -1 是不限条数。
	}
	rows, err := s.eventReader().QueryContext(ctx, `SELECT `+feedCommentColumns+` FROM feed_comments`+where+` ORDER BY created_at, id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	comments := []assistant.FeedComment{}
	for rows.Next() {
		comment, err := scanFeedComment(rows)
		if err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

// CountFeedComments 数游标之后（没有游标时是全部）的评论条数。
func (s *SQLiteStore) CountFeedComments(ctx context.Context, query assistant.FeedCommentQuery) (int, error) {
	defer s.observeStorage(ctx, "CountFeedComments", "read")()
	where, args, err := s.feedCommentFilter(ctx, query)
	if err != nil {
		return 0, err
	}
	var count int
	err = s.eventReader().QueryRowContext(ctx, `SELECT COUNT(1) FROM feed_comments`+where, args...).Scan(&count)
	return count, err
}

// AddFeedComment 加一条评论或回复。存在性检查、容量检查和插入在同一个事务里。
func (s *SQLiteStore) AddFeedComment(ctx context.Context, request assistant.FeedCommentRequest) (assistant.FeedComment, error) {
	defer s.observeStorage(ctx, "AddFeedComment", "write")()
	request.Content = assistant.NormalizeFeedComment(request.Content)
	if request.Content == "" {
		return assistant.FeedComment{}, fmt.Errorf("feed comment content is empty")
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	postID := strings.TrimSpace(request.PostID)
	profileID := strings.TrimSpace(request.ProfileID)
	tx, err := s.beginWriteTx(ctx, "AddFeedComment")
	if err != nil {
		return assistant.FeedComment{}, err
	}
	defer observeTransaction("AddFeedComment")()
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM feed_posts WHERE id = ? AND (? = '' OR profile_id = ?)`,
		postID, profileID, profileID).Scan(&exists); err != nil {
		return assistant.FeedComment{}, err
	}
	if exists == 0 {
		return assistant.FeedComment{}, assistant.ErrFeedPostNotFound
	}
	comment := assistant.FeedComment{
		ID: uuid.NewString(), PostID: postID, AuthorKind: assistant.NormalizeFeedAuthor(request.AuthorKind),
		Content: request.Content, CreatedAt: request.Now,
	}
	if target := strings.TrimSpace(request.ReplyToID); target != "" {
		targetComment, err := scanFeedComment(tx.QueryRowContext(ctx,
			`SELECT `+feedCommentColumns+` FROM feed_comments WHERE id = ? AND post_id = ?`, target, postID))
		if errors.Is(err, sql.ErrNoRows) {
			return assistant.FeedComment{}, assistant.ErrFeedCommentNotFound
		}
		if err != nil {
			return assistant.FeedComment{}, err
		}
		// 串只有一层：回复的回复挂到同一条顶层评论下。
		comment.ParentID = targetComment.ParentID
		if comment.ParentID == "" {
			comment.ParentID = targetComment.ID
		}
		comment.ReplyToID = targetComment.ID
		comment.ReplyToAuthor = targetComment.AuthorKind
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM feed_comments WHERE post_id = ?`, postID).Scan(&count); err != nil {
		return assistant.FeedComment{}, err
	}
	if count >= assistant.FeedMaxCommentsPerPost {
		return assistant.FeedComment{}, assistant.ErrFeedCommentCapacity
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feed_comments (`+feedCommentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		comment.ID, comment.PostID, comment.ParentID, comment.ReplyToID, comment.ReplyToAuthor,
		comment.AuthorKind, comment.Content, request.Now.UnixNano()); err != nil {
		return assistant.FeedComment{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.FeedComment{}, err
	}
	return comment, nil
}

// DeleteFeedComment 删除一条评论；顶层评论连同它下面的整串回复一起删。
func (s *SQLiteStore) DeleteFeedComment(ctx context.Context, postID, commentID string) (assistant.FeedComment, error) {
	defer s.observeStorage(ctx, "DeleteFeedComment", "write")()
	postID, commentID = strings.TrimSpace(postID), strings.TrimSpace(commentID)
	tx, err := s.beginWriteTx(ctx, "DeleteFeedComment")
	if err != nil {
		return assistant.FeedComment{}, err
	}
	defer observeTransaction("DeleteFeedComment")()
	defer func() { _ = tx.Rollback() }()
	comment, err := scanFeedComment(tx.QueryRowContext(ctx,
		`SELECT `+feedCommentColumns+` FROM feed_comments WHERE id = ? AND post_id = ?`, commentID, postID))
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.FeedComment{}, assistant.ErrFeedCommentNotFound
	}
	if err != nil {
		return assistant.FeedComment{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feed_comments WHERE post_id = ? AND (id = ? OR parent_id = ?)`,
		postID, commentID, commentID); err != nil {
		return assistant.FeedComment{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.FeedComment{}, err
	}
	return comment, nil
}

func (s *SQLiteStore) attachFeedLikes(ctx context.Context, posts []assistant.FeedPost) error {
	if len(posts) == 0 {
		return nil
	}
	index := make(map[string]int, len(posts))
	placeholders := make([]string, len(posts))
	args := make([]any, len(posts))
	for i, post := range posts {
		index[post.ID] = i
		placeholders[i] = "?"
		args[i] = post.ID
	}
	rows, err := s.eventReader().QueryContext(ctx, `SELECT post_id, author_kind FROM feed_likes
WHERE post_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var postID, kind string
		if err := rows.Scan(&postID, &kind); err != nil {
			return err
		}
		i, ok := index[postID]
		if !ok {
			continue
		}
		posts[i].LikeCount++
		if kind == assistant.FeedAuthorBot {
			posts[i].LikedByBot = true
		} else {
			posts[i].LikedByAdmin = true
		}
	}
	return rows.Err()
}

// SetFeedLike 点赞或取消点赞。主键是（动态，点赞者），重复点赞只是一次空操作。
func (s *SQLiteStore) SetFeedLike(ctx context.Context, profileID, postID, authorKind string, liked bool, now time.Time) (assistant.FeedLikes, error) {
	defer s.observeStorage(ctx, "SetFeedLike", "write")()
	postID = strings.TrimSpace(postID)
	profileID = strings.TrimSpace(profileID)
	authorKind = assistant.NormalizeFeedAuthor(authorKind)
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := s.beginWriteTx(ctx, "SetFeedLike")
	if err != nil {
		return assistant.FeedLikes{}, err
	}
	defer observeTransaction("SetFeedLike")()
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM feed_posts WHERE id = ? AND (? = '' OR profile_id = ?)`,
		postID, profileID, profileID).Scan(&exists); err != nil {
		return assistant.FeedLikes{}, err
	}
	if exists == 0 {
		return assistant.FeedLikes{}, assistant.ErrFeedPostNotFound
	}
	if liked {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO feed_likes (post_id, author_kind, created_at) VALUES (?, ?, ?)`,
			postID, authorKind, now.UnixNano())
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM feed_likes WHERE post_id = ? AND author_kind = ?`, postID, authorKind)
	}
	if err != nil {
		return assistant.FeedLikes{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT author_kind FROM feed_likes WHERE post_id = ?`, postID)
	if err != nil {
		return assistant.FeedLikes{}, err
	}
	var likes assistant.FeedLikes
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			_ = rows.Close()
			return assistant.FeedLikes{}, err
		}
		likes.Count++
		if kind == assistant.FeedAuthorBot {
			likes.LikedByBot = true
		} else {
			likes.LikedByAdmin = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return assistant.FeedLikes{}, err
	}
	_ = rows.Close()
	if err := tx.Commit(); err != nil {
		return assistant.FeedLikes{}, err
	}
	return likes, nil
}
