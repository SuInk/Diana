// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"strings"
)

// Agent 的工作目录不做成配置项。它是 Agent 能读写、能执行白名单命令的那个目录，
// 填错一个路径就等于把命令执行的作用域挪到了别处——控制台上让人随手改，风险和
// 收益完全不成比例。位置固定跟着数据库走，和入站媒体缓存同一套约定。
//
// AgentWorkspaceDir 返回 Agent 的工作目录。每次现算：只有一次环境变量读取和一次
// 路径拼接，代价可以忽略，换来测试可以按用例隔离目录。
func AgentWorkspaceDir() string {
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "workspace")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "diana", "workspace")
	}
	return "workspace"
}
