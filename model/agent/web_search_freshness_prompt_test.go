// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"strings"
	"testing"
)

// 线上 09-15：搜索引擎返回了苹果条款页 2024 年的旧缓存，实际页面前一天刚修订；
// 机器人还按训练知识断言「没有 iOS 27」。
func TestWebSearchDescriptionWarnsAboutStaleCacheAndNewerFacts(t *testing.T) {
	description := (&WebSearchTool{}).Description()
	for _, want := range []string{"旧缓存", "修订或发布日期", "不要断言它不存在", "查不到就说没查到"} {
		if !strings.Contains(description, want) {
			t.Fatalf("web search description missing %q", want)
		}
	}
}
