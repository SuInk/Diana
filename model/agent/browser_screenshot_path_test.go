// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"path/filepath"
	"testing"
)

// 工作目录是所有机器人共用的：不指定 path 的截图按对话分文件，A 机器人这边截的图
// 不会被 B 机器人另一个对话的截图覆盖。
func TestDefaultScreenshotPathIsPerConversation(t *testing.T) {
	a := browserToolBase{session: &browserSession{key: "bot-a\x00group:30001"}}
	b := browserToolBase{session: &browserSession{key: "bot-b\x00group:30001"}}
	pathA, pathB := a.defaultScreenshotPath(), b.defaultScreenshotPath()
	if pathA == pathB {
		t.Fatalf("两台机器人的截图落在同一个文件：%s", pathA)
	}
	for _, path := range []string{pathA, pathB} {
		if filepath.Dir(path) != filepath.Dir(defaultScreenshotPath) || filepath.Ext(path) != ".png" {
			t.Fatalf("截图路径 %s 不在 %s 下", path, filepath.Dir(defaultScreenshotPath))
		}
	}
	if again := (browserToolBase{session: &browserSession{key: "bot-a\x00group:30001"}}).defaultScreenshotPath(); again != pathA {
		t.Fatalf("同一个对话前后两次截图路径不一致：%s / %s", pathA, again)
	}
	if got := (browserToolBase{}).defaultScreenshotPath(); got != defaultScreenshotPath {
		t.Fatalf("没有会话时应当回落到老路径，实际 %s", got)
	}
}
