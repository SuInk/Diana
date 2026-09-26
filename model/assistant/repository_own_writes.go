// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
	"time"
)

// 机器人自己在 GitHub 上建的 Issue、发的评论，下一轮仓库订阅轮询会原样当成「新动态」
// 报回来。2026-09-26 15:12：机器人在群里建了一个 Issue，正常回复说「建好了」，
// 订阅卡片又报一遍，跟评再感想一遍——同一件事在群里确认了三次。
//
// 卡片照发：那是订阅的事实记录，订阅了就该有。跟评是「机器人对这条动态说句话」，
// 动态本身就是它刚做的事，再说一遍只是复读，这里让跟评跳过。
//
// 判断依据两条，任一成立即可：
//   - 内存里记着这台机器人刚写过这个编号（建、改、评论、审阅都算）；
//   - Issue 正文带 diana-operation 创建标记，且创建时间在窗口内。进程重启后内存
//     没了，靠这条兜住「刚建完就重启」。
//
// 一轮里只要混着别人的动态（别人的 Issue、提交、发版、星标），跟评照常。
const ownRepositoryWriteWindow = 10 * time.Minute

func ownRepositoryWriteKey(repository string, number int) string {
	repository = strings.ToLower(strings.TrimSpace(repository))
	if repository == "" || number <= 0 {
		return ""
	}
	return repository + "#" + strconv.Itoa(number)
}

// noteOwnRepositoryWrite 记下机器人刚写过的 Issue / PR 编号。
func (r *Runtime) noteOwnRepositoryWrite(repository string, numbers ...int) {
	now := time.Now()
	r.ownRepositoryWriteMu.Lock()
	defer r.ownRepositoryWriteMu.Unlock()
	if r.ownRepositoryWrites == nil {
		r.ownRepositoryWrites = map[string]time.Time{}
	}
	for key, at := range r.ownRepositoryWrites {
		if now.Sub(at) > ownRepositoryWriteWindow {
			delete(r.ownRepositoryWrites, key)
		}
	}
	for _, number := range numbers {
		if key := ownRepositoryWriteKey(repository, number); key != "" {
			r.ownRepositoryWrites[key] = now
		}
	}
}

func (r *Runtime) ownRepositoryWriteRecent(repository string, number int, now time.Time) bool {
	key := ownRepositoryWriteKey(repository, number)
	if key == "" {
		return false
	}
	r.ownRepositoryWriteMu.Lock()
	defer r.ownRepositoryWriteMu.Unlock()
	at, ok := r.ownRepositoryWrites[key]
	return ok && now.Sub(at) <= ownRepositoryWriteWindow
}

// noteOwnRepositoryWriteResult 从一次已经落到 GitHub 的写操作结果里取出涉及的编号。
func (r *Runtime) noteOwnRepositoryWriteResult(result repositoryIssueResult) {
	if r == nil || strings.TrimSpace(result.Repository) == "" {
		return
	}
	numbers := append([]int{result.RequestedNumber}, result.RequestedNumbers...)
	if result.Issue != nil {
		numbers = append(numbers, result.Issue.Number)
	}
	for _, item := range result.Items {
		numbers = append(numbers, item.Number)
	}
	r.noteOwnRepositoryWrite(result.Repository, numbers...)
}

// repositoryWatchChangeOnlyOwnRecentWrites 判断这一轮动态是不是全都是机器人自己刚做的事。
func (r *Runtime) repositoryWatchChangeOnlyOwnRecentWrites(repository string, change repositoryWatchChange, now time.Time) bool {
	if len(change.Commits) > 0 || len(change.Releases) > 0 || change.Stars != nil {
		return false
	}
	if len(change.Issues) == 0 && len(change.PullRequests) == 0 {
		return false
	}
	repository = firstNonEmpty(strings.TrimSpace(change.Repository), strings.TrimSpace(repository))
	ownCreated := func(body string, createdAt time.Time) bool {
		return repositoryIssueAnyMarkerPattern.MatchString(body) && !createdAt.IsZero() && now.Sub(createdAt) <= ownRepositoryWriteWindow
	}
	for _, issue := range change.Issues {
		if !r.ownRepositoryWriteRecent(repository, issue.Number, now) && !ownCreated(issue.Body, issue.CreatedAt) {
			return false
		}
	}
	for _, pull := range change.PullRequests {
		if !r.ownRepositoryWriteRecent(repository, pull.Number, now) {
			return false
		}
	}
	return true
}
