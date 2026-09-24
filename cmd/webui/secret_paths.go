// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/assistant"
)

// protectRuntimeSecrets 在启动时把凭据的落脚点告诉 Agent 和 secretmask。
//
// 文件：config.yaml 有管理员密码和首启播种的 API Key；SQLite 数据库（连同 WAL）
// 存着全部插件凭据、LLM 密钥和 OAuth 令牌；日志文件里有历史报错。它们都在 Agent
// 工作目录外面，文件工具够不着，但 run_command 的沙箱只限写不限读，白名单里有 cat、
// strings、sqlite3 时一句 `cat ../diana.db` 就能读出来。
//
// 目录：内置浏览器的 profile（各站点的 Cookie 和保存的登录）、编码代理的登录目录。
//
// 原文：进程环境里名字像凭据的变量（TAVILY_API_KEY、DIANA_BILI_SESSDATA……）和管理员
// 密码，登记之后出现在工具结果、报错和外发消息里就只剩掩码。
func protectRuntimeSecrets(appCfg appConfig, dbPath, dataDir string) {
	var files []string
	if appCfg.path != "" {
		files = append(files, appCfg.path)
	}
	if dbPath = strings.TrimSpace(dbPath); dbPath != "" {
		files = append(files, dbPath, dbPath+"-wal", dbPath+"-shm", dbPath+"-journal")
	}
	if logPath := strings.TrimSpace(appCfg.Storage.LogPath); logPath != "" {
		files = append(files, logPath)
		for index := 1; index <= logRotationBackups; index++ {
			files = append(files, logPath+"."+strconv.Itoa(index))
		}
	}
	if cookies := strings.TrimSpace(os.Getenv("DIANA_YTDLP_COOKIES")); cookies != "" {
		files = append(files, cookies)
	}
	if cookies, err := filepath.Abs("ytb_cookies.txt"); err == nil {
		files = append(files, cookies)
	}
	agent.ProtectRuntimeFiles(files...)

	var dirs []string
	if dataDir = strings.TrimSpace(dataDir); dataDir != "" && dataDir != "." {
		// 与 browserbox.Manager 的 profilesDir / legacyProfileDir 一致。
		dirs = append(dirs, filepath.Join(dataDir, "browser-box", "profiles"), filepath.Join(dataDir, "browser-box", "profile"))
	}
	dirs = append(dirs, assistant.CodingAgentCredentialDirs()...)
	agent.ProtectRuntimeDirs(dirs...)

	secretmask.RegisterEnvironment(os.Environ())
	secretmask.Register(appCfg.Admin.Password)
}
