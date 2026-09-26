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
// 看的是这一条「最近一次动静」是不是机器人自己的写入，任一成立即可：
//   - 内存里记着这台机器人刚写过这个编号（建、改、评论、审阅都算），而这条的
//     最后更新时间没有晚于那次写入；
//   - Issue 正文带 diana-operation 创建标记、创建时间在窗口内，而且建完之后就
//     没再动过。进程重启后内存没了，靠这条兜住「刚建完就重启」。
//
// 机器人写完之后别人又评论了，最后更新时间就会晚于那次写入，跟评照常——那是新的
// 动静。一轮里只要混着别人的动态（别人的 Issue、提交、发版、星标），跟评也照常。
const (
	ownRepositoryWriteWindow = 10 * time.Minute
	// ownRepositoryWriteSlack 容忍本机时钟和 GitHub 时间戳之间的偏差：写入请求
	// 返回之后才登记，GitHub 记下的更新时间本来就不会更晚，留一点余量足够。
	ownRepositoryWriteSlack = 30 * time.Second
)

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

// ownRepositoryWriteIsLatest 判断这个编号最近一次动静（updatedAt）是不是机器人
// 刚才那次写入。
func (r *Runtime) ownRepositoryWriteIsLatest(repository string, number int, updatedAt, now time.Time) bool {
	key := ownRepositoryWriteKey(repository, number)
	if key == "" {
		return false
	}
	r.ownRepositoryWriteMu.Lock()
	at, ok := r.ownRepositoryWrites[key]
	r.ownRepositoryWriteMu.Unlock()
	if !ok || now.Sub(at) > ownRepositoryWriteWindow {
		return false
	}
	return updatedAt.IsZero() || !updatedAt.After(at.Add(ownRepositoryWriteSlack))
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
	ownCreated := func(body string, createdAt, updatedAt time.Time) bool {
		if !repositoryIssueAnyMarkerPattern.MatchString(body) || createdAt.IsZero() || now.Sub(createdAt) > ownRepositoryWriteWindow {
			return false
		}
		return updatedAt.IsZero() || !updatedAt.After(createdAt.Add(ownRepositoryWriteSlack))
	}
	for _, issue := range change.Issues {
		if !r.ownRepositoryWriteIsLatest(repository, issue.Number, issue.UpdatedAt, now) && !ownCreated(issue.Body, issue.CreatedAt, issue.UpdatedAt) {
			return false
		}
	}
	for _, pull := range change.PullRequests {
		if !r.ownRepositoryWriteIsLatest(repository, pull.Number, pull.UpdatedAt, now) {
			return false
		}
	}
	return true
}
