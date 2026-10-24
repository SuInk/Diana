// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type repositoryWatchComment struct {
	Body      string    `json:"body"`
	URL       string    `json:"url,omitempty"`
	Author    string    `json:"author,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Edited    bool      `json:"edited,omitempty"`
	Review    bool      `json:"review,omitempty"`
}

// 评论失败时不推进本轮游标，以免一次临时 API 错误永久吞掉正文。
func (p *RepositoryWatchPlugin) populateRepositoryComments(ctx context.Context, change *repositoryWatchChange, cursor repositoryWatchSnapshot, selection repositoryWatchSelection, settings SettingValues) error {
	since := func(raw string) time.Time {
		if at := repositoryWatchPullCursorTime(raw); !at.IsZero() {
			return at
		}
		return cursor.CheckedAt
	}
	for i := range change.Issues {
		item := &change.Issues[i]
		comments, truncated, err := p.fetchRepositoryComments(ctx, change.Repository, item.Number, false, since(cursor.IssueCursor), selection.BlockedUsers, settings)
		if err != nil {
			return err
		}
		item.Comments, item.CommentsTruncated = comments, truncated
	}
	for i := range change.PullRequests {
		item := &change.PullRequests[i]
		comments, truncated, err := p.fetchRepositoryComments(ctx, change.Repository, item.Number, true, since(cursor.PullRequestCursor), selection.BlockedUsers, settings)
		if err != nil {
			return err
		}
		item.Comments, item.CommentsTruncated = comments, truncated
	}
	return nil
}

func (p *RepositoryWatchPlugin) fetchRepositoryComments(ctx context.Context, repository string, number int, pull bool, since time.Time, blocked []string, settings SettingValues) ([]repositoryWatchComment, bool, error) {
	// 没有上次水位时只建基线，不能把历史讨论重新播报。
	if since.IsZero() {
		return nil, false, nil
	}
	paths := []string{fmt.Sprintf("/repos/%s/issues/%d/comments", repository, number)}
	if pull {
		paths = append(paths, fmt.Sprintf("/repos/%s/pulls/%d/comments", repository, number))
	}
	var comments []repositoryWatchComment
	truncated := false
	for source, path := range paths {
		for page := 1; page <= 5; page++ {
			query := url.Values{"since": {since.UTC().Format(time.RFC3339)}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
			var payload []struct {
				Body      string    `json:"body"`
				URL       string    `json:"html_url"`
				CreatedAt time.Time `json:"created_at"`
				UpdatedAt time.Time `json:"updated_at"`
				User      struct {
					Login string `json:"login"`
				} `json:"user"`
			}
			if err := p.getJSON(ctx, path+"?"+query.Encode(), settings, &payload); err != nil {
				return nil, false, fmt.Errorf("读取 %s #%d 评论: %w", repository, number, err)
			}
			for _, item := range payload {
				if !item.UpdatedAt.After(since) || repositoryWatchUserBlocked(blocked, item.User.Login) {
					continue
				}
				comments = append(comments, repositoryWatchComment{Body: truncateRunes(strings.TrimSpace(item.Body), 1500), URL: strings.TrimSpace(item.URL), Author: strings.TrimSpace(item.User.Login), UpdatedAt: item.UpdatedAt, Edited: item.UpdatedAt.After(item.CreatedAt), Review: source == 1})
			}
			if len(payload) < 100 {
				break
			}
			if page == 5 {
				truncated = true
			}
		}
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].UpdatedAt.Before(comments[j].UpdatedAt) })
	limit := max(1, settings.Int(repositoryWatchSettingLimit, repositoryWatchDefaultLimit))
	if len(comments) > limit {
		comments, truncated = comments[len(comments)-limit:], true
	}
	return comments, truncated, nil
}

func renderRepositoryWatchComments(comments []repositoryWatchComment, truncated bool) string {
	var lines []string
	for _, item := range comments {
		label := "新增评论"
		if item.Edited {
			label = "编辑评论"
		}
		if item.Review {
			label += "（代码审查）"
		}
		if item.Author != "" {
			label += " · " + item.Author
		}
		lines = append(lines, label+" · "+formatRepositoryWatchTime(item.UpdatedAt), firstNonEmpty(item.Body, "（空评论）"))
		if item.URL != "" {
			lines = append(lines, item.URL)
		}
	}
	if truncated {
		lines = append(lines, "还有评论未列出，请查看原文。")
	}
	return strings.Join(lines, "\n")
}

func maskRepositoryWatchComments(comments []repositoryWatchComment) []repositoryWatchComment {
	comments = append([]repositoryWatchComment(nil), comments...)
	for i := range comments {
		comments[i].Author, comments[i].URL = "", ""
	}
	return comments
}
