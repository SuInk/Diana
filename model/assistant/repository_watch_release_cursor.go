package assistant

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type repositoryReleaseRecord struct {
	ID          int64     `json:"id"`
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// releaseKind 认 GitHub 的 prerelease 标记，不看标签怎么写：作者不勾「Set as a
// pre-release」时，v1.2.0-rc.1 在 API 里就是正式版，反过来 v2.0.0 也可以是预发布。
func releaseKind(item repositoryReleaseRecord) string {
	if item.Prerelease {
		return repositoryWatchReleaseKindPrerelease
	}
	return repositoryWatchReleaseKindStable
}

func releaseCursorAfter(at time.Time, id int64, previousAt time.Time, previousID int64) bool {
	return !at.IsZero() && (at.After(previousAt) || at.Equal(previousAt) && id > previousID)
}

func (p *RepositoryWatchPlugin) fetchReleases(ctx context.Context, repository string, cursor repositoryWatchSnapshot, selection repositoryWatchSelection, settings SettingValues) ([]repositoryWatchRelease, repositoryWatchSnapshot, error) {
	base := repositoryWatchSnapshot{ReleaseTag: strings.TrimSpace(cursor.ReleaseTag), ReleasePublishedAt: cursor.ReleasePublishedAt, ReleaseID: cursor.ReleaseID}
	var payload []repositoryReleaseRecord
	if err := p.getJSON(ctx, "/repos/"+repository+"/releases?per_page=50", settings, &payload); err != nil {
		return nil, base, fmt.Errorf("读取 %s releases: %w", repository, err)
	}
	filtered := payload[:0]
	for _, item := range payload {
		if item.Draft {
			continue
		}
		item.Tag = strings.TrimSpace(item.Tag)
		if item.Tag == "" || item.PublishedAt.IsZero() {
			return nil, base, fmt.Errorf("仓库 %s 返回缺少标签或发布时间的 Release", repository)
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		if base.ReleaseTag == "" {
			base.ReleaseTag = repositoryWatchNoReleaseCursor
		} else if base.ReleaseTag != repositoryWatchNoReleaseCursor {
			logRepositoryOpaqueCursorRetained(repository, "release", base.ReleaseTag, repositoryWatchNoReleaseCursor, "empty_response")
		}
		return nil, base, nil
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return releaseCursorAfter(filtered[i].PublishedAt, filtered[i].ID, filtered[j].PublishedAt, filtered[j].ID)
	})
	if base.ReleaseTag == "" {
		latest := filtered[0]
		return nil, repositoryWatchSnapshot{ReleaseTag: latest.Tag, ReleasePublishedAt: latest.PublishedAt, ReleaseID: latest.ID}, nil
	}
	// Upgrade old tag-only checkpoints from GitHub metadata, never tag spelling.
	if base.ReleaseTag != repositoryWatchNoReleaseCursor && base.ReleasePublishedAt.IsZero() {
		var anchor repositoryReleaseRecord
		for _, item := range filtered {
			if item.Tag == base.ReleaseTag {
				anchor = item
				break
			}
		}
		if anchor.Tag == "" {
			if err := p.getJSON(ctx, "/repos/"+repository+"/releases/tags/"+url.PathEscape(base.ReleaseTag), settings, &anchor); err != nil {
				return nil, base, fmt.Errorf("核对 %s 原 Release 游标 %s: %w", repository, base.ReleaseTag, err)
			}
		}
		if anchor.Tag != base.ReleaseTag || anchor.PublishedAt.IsZero() || anchor.Draft {
			return nil, base, fmt.Errorf("无法核对 %s 原 Release 游标 %s 的发布时间", repository, base.ReleaseTag)
		}
		base.ReleasePublishedAt, base.ReleaseID = anchor.PublishedAt, anchor.ID
	}
	if base.ReleaseID == 0 {
		for _, item := range filtered {
			if item.Tag == base.ReleaseTag && item.PublishedAt.Equal(base.ReleasePublishedAt) {
				base.ReleaseID = item.ID
				break
			}
		}
	}
	next := base
	var result []repositoryWatchRelease
	for _, item := range filtered {
		if base.ReleaseID == 0 && item.PublishedAt.Equal(base.ReleasePublishedAt) {
			continue
		}
		if !releaseCursorAfter(item.PublishedAt, item.ID, base.ReleasePublishedAt, base.ReleaseID) {
			continue
		}
		// 和 PR / Issue 的种类过滤一样：游标跟着所有已发布版本走，种类只决定报不报。
		// 否则关掉预发布的订阅一旦重新打开，会把中间攒下的 rc 版一次性全补推出来。
		if selection.wants(selection.ReleaseKinds, releaseKind(item)) {
			result = append(result, repositoryWatchRelease{Tag: item.Tag, Name: item.Name, Body: truncateRunes(strings.TrimSpace(item.Body), 4000), URL: item.HTMLURL, PublishedAt: item.PublishedAt, Prerelease: item.Prerelease})
		}
		if releaseCursorAfter(item.PublishedAt, item.ID, next.ReleasePublishedAt, next.ReleaseID) {
			next.ReleaseTag, next.ReleasePublishedAt, next.ReleaseID = item.Tag, item.PublishedAt, item.ID
		}
	}
	if next.ReleaseTag == base.ReleaseTag && filtered[0].Tag != base.ReleaseTag {
		logRepositoryOpaqueCursorRetained(repository, "release", base.ReleaseTag, filtered[0].Tag, "older_or_unverifiable_response")
	}
	return result, next, nil
}
