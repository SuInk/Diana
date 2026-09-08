package assistant

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"time"
)

type repositoryCommitRecord struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (p *RepositoryWatchPlugin) forwardCommitRange(ctx context.Context, repository, base, head string, limit int, settings SettingValues) ([]repositoryCommitRecord, int, bool, error) {
	type comparison struct {
		Status  string                   `json:"status"`
		Total   int                      `json:"total_commits"`
		Commits []repositoryCommitRecord `json:"commits"`
	}
	path := "/repos/" + repository + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?per_page=100"
	var first comparison
	if err := p.getJSON(ctx, path, settings, &first); err != nil {
		return nil, 0, false, fmt.Errorf("核对 %s commit 游标祖先关系: %w", repository, err)
	}
	if first.Status != "ahead" {
		logRepositoryOpaqueCursorRetained(repository, "commit", base, head, first.Status)
		return nil, 0, false, nil
	}
	if len(first.Commits) == 0 {
		return nil, 0, false, fmt.Errorf("仓库 %s 的 compare 响应缺少新增提交", repository)
	}
	records := first.Commits
	// Compare returns oldest first. Read the final page(s) of the immutable
	// SHA range so the summary contains the newest verified additions.
	if first.Total > len(records) {
		records = nil
		for page := (first.Total + 99) / 100; page > 0 && len(records) < min(max(limit, 1), 100); page-- {
			var part comparison
			if page == 1 {
				part = first
			} else {
				if err := p.getJSON(ctx, fmt.Sprintf("%s&page=%d", path, page), settings, &part); err != nil {
					return nil, 0, false, err
				}
			}
			if part.Status != "ahead" || part.Total != first.Total || len(part.Commits) == 0 {
				return nil, 0, false, fmt.Errorf("仓库 %s 的 compare 分页不一致", repository)
			}
			records = append(part.Commits, records...)
		}
	}
	for _, item := range records {
		if item.SHA == "" || item.SHA == base {
			return nil, 0, false, fmt.Errorf("仓库 %s 的 compare 提交范围无效", repository)
		}
	}
	slices.Reverse(records)
	return records, max(first.Total, len(records)), true, nil
}
