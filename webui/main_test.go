// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain 把 Agent 工作区指到临时目录。
//
// 工作区跟着 APP_DB_PATH 走，没设时落在用户缓存目录（macOS 上是
// ~/Library/Caches/diana/workspace）——那是本机真实 Diana 实例在用的目录。这个包里
// 有用例会拉起运行时、构建 Agent 工具表：前者在启动时把那里的编码任务当遗留任务
// 接管，后者往那里写 .extension-paths.json。不在这里兜住，跑一遍测试就会改动开发机
// 上实例的状态，反过来实例留下的东西也会让用例的结果随机器而变。
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "diana-webui-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("APP_DB_PATH", filepath.Join(root, "app.db"))
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
