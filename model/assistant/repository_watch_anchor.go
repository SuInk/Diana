// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 仓库订阅的锚点:PR/Issue 第一次出现在推送里时,记下那条通知消息的 ID;
// 后续同一个 PR/Issue 的更新或合并推送引用它——QQ 的引用框把「新增 PR」
// 和「PR 合并」串成一条线,不用翻记录找是哪个 PR。
//
// 锚点按投递目标分别记(同一订阅可以推到多个群,消息 ID 各不相同),随
// 订阅本体持久化,轮询间隔和进程重启都不丢。条目 FIFO 封顶,不会无限膨胀。

const repositoryWatchAnchorLimit = 256

type repositoryWatchAnchor struct {
	Key       string `json:"key"`
	MessageID string `json:"message_id"`
}

func repositoryWatchAnchorKey(targetKey, kind string, number int) string {
	return targetKey + "|" + kind + ":" + fmt.Sprint(number)
}

func decodeRepositoryWatchAnchors(raw string) []repositoryWatchAnchor {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var anchors []repositoryWatchAnchor
	if err := json.Unmarshal([]byte(raw), &anchors); err != nil {
		return nil
	}
	return anchors
}

func encodeRepositoryWatchAnchors(anchors []repositoryWatchAnchor) string {
	if len(anchors) == 0 {
		return ""
	}
	raw, err := json.Marshal(anchors)
	if err != nil {
		return ""
	}
	return string(raw)
}

func repositoryWatchAnchorLookup(anchors []repositoryWatchAnchor, key string) string {
	for _, anchor := range anchors {
		if anchor.Key == key {
			return anchor.MessageID
		}
	}
	return ""
}

// appendRepositoryWatchAnchors 只在键不存在时追加:锚点要一直指向最初宣布
// 那条消息,后续更新不覆盖。超出上限时挤掉最老的。
func appendRepositoryWatchAnchors(anchors []repositoryWatchAnchor, added map[string]string) []repositoryWatchAnchor {
	for key, messageID := range added {
		if key == "" || strings.TrimSpace(messageID) == "" {
			continue
		}
		if repositoryWatchAnchorLookup(anchors, key) != "" {
			continue
		}
		anchors = append(anchors, repositoryWatchAnchor{Key: key, MessageID: messageID})
	}
	if len(anchors) > repositoryWatchAnchorLimit {
		anchors = anchors[len(anchors)-repositoryWatchAnchorLimit:]
	}
	return anchors
}

// repositoryWatchAnchorReplyID 给一次推送挑引用目标:本次动态里第一个「已有
// 锚点」的 PR/Issue 更新(opened 是首次宣布,不需要引用自己)。一条消息只能
// 引用一个目标,多个更新时引最靠前的。
func repositoryWatchAnchorReplyID(anchors []repositoryWatchAnchor, targetKey string, change repositoryWatchChange) string {
	for _, pullRequest := range change.PullRequests {
		if strings.EqualFold(strings.TrimSpace(pullRequest.Status), "opened") {
			continue
		}
		if id := repositoryWatchAnchorLookup(anchors, repositoryWatchAnchorKey(targetKey, "pr", pullRequest.Number)); id != "" {
			return id
		}
	}
	for _, issue := range change.Issues {
		if strings.EqualFold(strings.TrimSpace(issue.Status), "opened") {
			continue
		}
		if id := repositoryWatchAnchorLookup(anchors, repositoryWatchAnchorKey(targetKey, "issue", issue.Number)); id != "" {
			return id
		}
	}
	return ""
}

// repositoryWatchAnchorEntries 列出本次推送里所有 PR/Issue 的锚点键——不限
// opened:订阅建立时 PR 可能已经开了,第一次出现是 updated,合并推送也该能
// 引到它。写入端只在键不存在时记录,首次出现的那条消息永远是锚点。
func repositoryWatchAnchorEntries(targetKey string, change repositoryWatchChange, messageID string) map[string]string {
	if strings.TrimSpace(messageID) == "" {
		return nil
	}
	entries := make(map[string]string, len(change.PullRequests)+len(change.Issues))
	for _, pullRequest := range change.PullRequests {
		entries[repositoryWatchAnchorKey(targetKey, "pr", pullRequest.Number)] = messageID
	}
	for _, issue := range change.Issues {
		entries[repositoryWatchAnchorKey(targetKey, "issue", issue.Number)] = messageID
	}
	return entries
}
