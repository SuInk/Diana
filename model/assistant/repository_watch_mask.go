// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// 仓库订阅推送的打码模式。推送发进群里，私有仓库的 owner/repo、提交者和 Issue 作者的
// 账号名、带路径的链接都会原样出现——群里有人指出这等于开盒。
const (
	// repositoryWatchMaskAuto：私有仓库打码，公开仓库照常显示。还不知道是否私有时按私有算。
	repositoryWatchMaskAuto = ""
	// repositoryWatchMaskAlways：公开仓库也打码，给不想让群里知道具体仓库的人用。
	repositoryWatchMaskAlways = "always"
	// repositoryWatchMaskNever：私有仓库也照常显示，推送只发给自己人时用。
	repositoryWatchMaskNever = "never"

	repositoryWatchMaskedLabel = "私有仓库"
	// 仓库可见性一天刷新一次，只在有动态要推的那一轮顺手查，空轮询不多花请求。
	repositoryWatchVisibilityTTL       = 24 * time.Hour
	repositoryWatchDisplayNameMaxRunes = 40
)

func normalizeRepositoryWatchMask(value string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(value)); mode {
	case repositoryWatchMaskAuto, "auto":
		return repositoryWatchMaskAuto, nil
	case repositoryWatchMaskAlways, repositoryWatchMaskNever:
		return mode, nil
	default:
		return "", fmt.Errorf("打码模式只能是 auto、always 或 never")
	}
}

func normalizeRepositoryWatchDisplayName(value string) (string, error) {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > repositoryWatchDisplayNameMaxRunes {
		return "", fmt.Errorf("仓库显示名不能超过 %d 个字", repositoryWatchDisplayNameMaxRunes)
	}
	return value, nil
}

// repositoryWatchMasked 判断这条订阅的推送要不要打码。
func repositoryWatchMasked(item Reminder) bool {
	switch item.RepositoryMask {
	case repositoryWatchMaskAlways:
		return true
	case repositoryWatchMaskNever:
		return false
	default:
		return item.RepositoryPrivate == nil || *item.RepositoryPrivate
	}
}

// RepositoryWatchMasked 给控制台显示「推送已打码」用。
func RepositoryWatchMasked(item Reminder) bool {
	return reminderIsRepositoryWatch(item) && repositoryWatchMasked(item)
}

// repositoryWatchLabel 是推送和告警里对这个仓库的称呼：设了显示名用显示名，
// 否则打码时写「私有仓库」，不打码时写 owner/repo。
func repositoryWatchLabel(item Reminder) string {
	if name := strings.TrimSpace(item.RepositoryDisplayName); name != "" {
		return name
	}
	if repositoryWatchMasked(item) {
		return repositoryWatchMaskedLabel
	}
	return strings.TrimSpace(item.Repository)
}

// maskRepositoryWatchChange 去掉一轮动态里会暴露仓库或人的字段：作者账号、Star 的人、
// 所有链接（链接路径里就是 owner/repo）。模板会把整行为空的行删掉，所以作者行和链接行
// 直接消失，不留「作者：」空壳。标题、编号、分支、时间照常保留。
func maskRepositoryWatchChange(change repositoryWatchChange) repositoryWatchChange {
	change.Commits = append([]repositoryWatchCommit(nil), change.Commits...)
	for index := range change.Commits {
		change.Commits[index].Author = ""
		change.Commits[index].URL = ""
	}
	change.PullRequests = append([]repositoryWatchPullRequest(nil), change.PullRequests...)
	for index := range change.PullRequests {
		change.PullRequests[index].Author = ""
		change.PullRequests[index].URL = ""
	}
	change.Issues = append([]repositoryWatchIssue(nil), change.Issues...)
	for index := range change.Issues {
		change.Issues[index].Author = ""
		change.Issues[index].URL = ""
	}
	change.Releases = append([]repositoryWatchRelease(nil), change.Releases...)
	for index := range change.Releases {
		change.Releases[index].URL = ""
	}
	if change.Stars != nil {
		stars := *change.Stars
		stars.URL = ""
		stars.AddedUsers = append([]repositoryWatchStargazer(nil), stars.AddedUsers...)
		for index := range stars.AddedUsers {
			stars.AddedUsers[index].Login = ""
		}
		change.Stars = &stars
	}
	return change
}

// maskRepositoryWatchText 把一段发往会话的文字里的仓库路径换成称呼，给失败告警这类
// 把错误原文带出去的地方兜底。github.com 链接整条换掉，裸的 owner/repo 不分大小写替换。
func maskRepositoryWatchText(text, repository, label string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" || text == "" {
		return text
	}
	quoted := regexp.QuoteMeta(repository)
	text = regexp.MustCompile(`(?i)https?://(?:www\.)?github\.com/`+quoted+`\S*`).ReplaceAllString(text, label)
	return regexp.MustCompile(`(?i)`+quoted).ReplaceAllString(text, label)
}

// repositoryWatchMaskReferenceNote 加在跟评参考资料最前面。推送已经打了码，但模型能从
// 正文、diff 里看到人名，接话时顺嘴说出来就白打了。
func repositoryWatchMaskReferenceNote(label string) string {
	return fmt.Sprintf("隐私要求：这个仓库在群里称为「%s」。回复里不要写出仓库的 owner/repo 路径、GitHub 链接和任何人的 GitHub 账号名，提到人时用「有人」「作者」这类说法。", label)
}

// resolveRepositoryWatchVisibility 在有动态要推的那一轮确认仓库是否私有，结果记在订阅上。
// 本轮已经顺带读到（跟评拉仓库简介时）就直接用；否则只在不知道或超过一天没查时补查一次。
// 查不到时沿用旧结论，一直不知道就按私有打码。
func (r *Runtime) resolveRepositoryWatchVisibility(ctx context.Context, item Reminder, plugin *RepositoryWatchPlugin, settings SettingValues, observed *bool) Reminder {
	if item.RepositoryMask != repositoryWatchMaskAuto {
		return item
	}
	private := observed
	stale := item.RepositoryPrivate == nil || time.Since(item.RepositoryVisibilityCheckedAt) > repositoryWatchVisibilityTTL
	if private == nil && stale && plugin != nil {
		if value, err := plugin.fetchRepositoryPrivate(ctx, item.Repository, settings); err == nil {
			private = &value
		} else {
			log.Printf("diana repository watch %s: visibility check failed: %v", strings.TrimSpace(item.Repository), err)
		}
	}
	if private == nil {
		return item
	}
	updated, err := r.mutateRepositoryWatch(item.OwnerID, item.ID, func(stored *Reminder) error {
		value := *private
		stored.RepositoryPrivate = &value
		stored.RepositoryVisibilityCheckedAt = time.Now()
		return nil
	})
	if err != nil {
		log.Printf("diana repository watch %s: save visibility failed: %v", strings.TrimSpace(item.Repository), err)
		value := *private
		item.RepositoryPrivate = &value
		return item
	}
	item.RepositoryPrivate = updated.RepositoryPrivate
	item.RepositoryVisibilityCheckedAt = updated.RepositoryVisibilityCheckedAt
	return item
}

// fetchRepositoryPrivate 读仓库是否私有。
func (p *RepositoryWatchPlugin) fetchRepositoryPrivate(ctx context.Context, repository string, settings SettingValues) (bool, error) {
	_, private, err := p.fetchRepositoryInfo(ctx, repository, settings)
	return private, err
}
