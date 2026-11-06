// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"os"

	"github.com/SuInk/diana/model/runtimeconfig"
)

// runtimeConfigFields 是运行配置页列出的 config.yaml 基础设施项。
//
// 可改的边界：基础设施项 WebUI 管理员都能改，重启后生效。改错会让控制台自己
// 连不上（监听地址、端口、前端目录）或换掉数据位置（数据库、媒体、更新目录）
// 的项标 Risky，前端保存前单独确认，且不会替人搬数据。初始管理员只在首启用，
// 之后改它没有意义，保持只读。
// 机器人和 WebUI 管理员同权：主人在私聊里能改的就是这里能改的全部。工具要求先
// 复述、主人同意后才写，改的是文件、重启才生效，改坏了也能再改回来。
//
// 下载缓存和历史媒体的保留策略在各自的设置页保存后以数据库为准，这里只读，
// 免得出现两个入口改同一件事却只有一个生效。
func runtimeConfigFields() []runtimeconfig.Field {
	fields := baseRuntimeConfigFields()
	for i := range fields {
		fields[i].BotEditable = fields[i].Editable
	}
	return fields
}

func baseRuntimeConfigFields() []runtimeconfig.Field {
	days := func(key, label, def, hint string) runtimeconfig.Field {
		return runtimeconfig.Field{Key: key, Label: label, Kind: runtimeconfig.KindInt, Default: def, Hint: hint,
			Editable: true, Min: runtimeconfig.Int64(-1), Max: runtimeconfig.Int64(36500)}
	}
	return []runtimeconfig.Field{
		{Key: "server.host", Label: "监听地址", Kind: runtimeconfig.KindString, Default: "", Hint: "留空监听所有地址；填 127.0.0.1 后只有本机能打开控制台。", Editable: true, Risky: true},
		{Key: "server.port", Label: "端口", Kind: runtimeconfig.KindPort, Default: "18080", Hint: "重启后控制台换到新端口；Docker 部署还要同步改端口映射，OneBot 反连地址也要跟着改。", Editable: true, Risky: true},
		{Key: "server.trusted_proxies", Label: "信任的反向代理", Kind: runtimeconfig.KindList, Hint: "只有这些地址转发来的 X-Forwarded-For 才会被采信，每行一个 IP 或网段。", Editable: true},
		{Key: "server.frontend_dist", Label: "前端目录", Kind: runtimeconfig.KindString, Hint: "留空使用内嵌前端。", Editable: true, Risky: true},
		{Key: "storage.db_path", Label: "数据库路径", Kind: runtimeconfig.KindString, Default: "data/diana.db", Hint: "不会搬旧数据：新路径没有库时重启后是一份空数据，要先把旧库文件复制过去。", Editable: true, Risky: true},
		days("storage.debug_log_retention_days", "调试记录保留天数", "0", "0 用内置默认值，-1 永久保留。"),
		days("storage.log_retention_days", "操作日志保留天数", "0", "0 用内置默认值，-1 永久保留。"),
		{Key: "storage.download_cache_retention_days", Label: "下载缓存保留天数", Kind: runtimeconfig.KindInt, Default: "0", Hint: "在「下载缓存」页保存过之后以页面为准。"},
		{Key: "storage.download_cache_max_mb", Label: "下载缓存容量上限（MB）", Kind: runtimeconfig.KindInt, Default: "0", Hint: "在「下载缓存」页保存过之后以页面为准。"},
		{Key: "storage.history_media_retention_days", Label: "历史媒体保留天数", Kind: runtimeconfig.KindInt, Default: "0", Hint: "在「媒体与文件」页保存过之后以页面为准。"},
		{Key: "storage.history_media_max_mb", Label: "历史媒体容量上限（MB）", Kind: runtimeconfig.KindInt, Default: "0", Hint: "在「媒体与文件」页保存过之后以页面为准。"},
		{Key: "storage.log_path", Label: "日志文件", Kind: runtimeconfig.KindString, Hint: "留空只写标准输出；Docker 部署默认写到数据目录。", Editable: true},
		{Key: "storage.media_dir", Label: "媒体目录", Kind: runtimeconfig.KindString, Hint: "留空放在数据库同级目录。换目录不会搬旧文件，要自己复制过去。", Editable: true, Risky: true},
		{Key: "storage.media_max_mb", Label: "单个媒体大小上限（MB）", Kind: runtimeconfig.KindInt, Default: "0", Hint: "0 用内置默认值。", Editable: true, Min: runtimeconfig.Int64(0), Max: runtimeconfig.Int64(1 << 20)},
		{Key: "storage.media_cache_mb", Label: "媒体内存缓存（MB）", Kind: runtimeconfig.KindInt, Default: "0", Hint: "0 用内置默认值。", Editable: true, Min: runtimeconfig.Int64(0), Max: runtimeconfig.Int64(1 << 20)},
		{Key: "storage.local_media_base_url", Label: "本地媒体对外地址", Kind: runtimeconfig.KindString, Hint: "留空按反连握手地址自动推断。", Editable: true},
		{Key: "admin.username", Label: "初始管理员账号", Kind: runtimeconfig.KindString, Hint: "只在第一次启动、还没有管理员时使用；之后在「访问安全」里改。"},
		{Key: "admin.password", Label: "初始管理员密码", Kind: runtimeconfig.KindString, Secret: true, Hint: "只在第一次启动时使用。"},
		{Key: "update.root", Label: "源码目录", Kind: runtimeconfig.KindString, Hint: "源码部署拉取更新的仓库位置。", Editable: true, Risky: true},
		{Key: "update.apply_enabled", Label: "允许源码更新", Kind: runtimeconfig.KindBool, Default: "true", Hint: "关掉后控制台不能拉取源码更新。", Editable: true},
		{Key: "update.release_enabled", Label: "允许 Release 更新", Kind: runtimeconfig.KindBool, Default: "true", Hint: "关掉后控制台不能下载安装新版本。", Editable: true},
		{Key: "update.work_dir", Label: "更新工作目录", Kind: runtimeconfig.KindString, Hint: "留空跟着数据目录走。", Editable: true, Risky: true},
		{Key: "update.group_test_enabled", Label: "群测试通道", Kind: runtimeconfig.KindBool, Default: "false", Editable: true},
		{Key: "llm_runtime.output_token_max", Label: "默认最大输出 token", Kind: runtimeconfig.KindInt, Default: "0", Hint: "Anthropic 协议没填最大输出时代发的值，0 用 32000。", Editable: true, Min: runtimeconfig.Int64(0), Max: runtimeconfig.Int64(1 << 20)},
	}
}

// runtimeConfigEnv 是程序会读的环境变量。测试专用的 DIANA_TEST_* / DIANA_LIVE_* 不列。
func runtimeConfigEnv() []runtimeconfig.EnvVar {
	return []runtimeconfig.EnvVar{
		{Name: "DIANA_DEPLOYMENT", Label: "部署方式"},
		{Name: logStdoutEnv, Label: "日志同时写标准输出"},
		{Name: "TZ", Label: "时区"},
		{Name: "APP_DB_PATH", Label: "数据库路径（旧）"},
		{Name: "DIANA_DOCKER_IMAGE", Label: "Docker 镜像"},
		{Name: "DIANA_DOCKER_UPDATE_TOKEN", Label: "Docker 更新令牌", Secret: true},
		{Name: "DIANA_GITHUB_TOKEN", Label: "GitHub Token", Secret: true},
		{Name: "GITHUB_TOKEN", Label: "GitHub Token（通用）", Secret: true},
		{Name: "DIANA_TRUSTED_LOGIN_APPS_FILE", Label: "受信应用文件（旧）"},
		{Name: "DIANA_DESKTOP_HELPER", Label: "桌面控制助手"},
		{Name: "DIANA_CUA_DRIVER", Label: "CUA 驱动"},
		{Name: "DIANA_BROWSER_DIR", Label: "浏览器数据目录"},
		{Name: "DIANA_BROWSER_EXTENSION_DIR", Label: "浏览器扩展目录"},
		{Name: "DIANA_WEB_SEARCH_CONFIG_FILE", Label: "网页搜索配置文件"},
		{Name: "TAVILY_API_KEY", Label: "Tavily API Key", Secret: true},
		{Name: "DIANA_YTDLP_COOKIES", Label: "yt-dlp Cookie 文件"},
		{Name: "DIANA_YTDLP_COOKIES_FROM_BROWSER", Label: "yt-dlp 读取浏览器 Cookie"},
		{Name: "DIANA_RESOLVER_PROXY", Label: "解析代理"},
		{Name: "RESOLVER_PROXY", Label: "解析代理（旧）"},
		{Name: "DIANA_XHS_CK", Label: "小红书 Cookie", Secret: true},
		{Name: "XHS_CK", Label: "小红书 Cookie（旧）", Secret: true},
	}
}

// newRuntimeConfigManager 用启动时的配置原文构造运行配置管理器。启动时没有配置
// 文件的，第一次保存落到数据目录的 config.yaml，下次启动按约定能找到。
func newRuntimeConfigManager(cfg appConfig, host, port string) (*runtimeconfig.Manager, error) {
	return runtimeconfig.New(runtimeconfig.Options{
		Path:        cfg.path,
		CreatePath:  dataDirConfigPath,
		StartupData: cfg.raw,
		Args:        os.Args[1:],
		Fields:      runtimeConfigFields(),
		Env:         runtimeConfigEnv(),
		ListenHost:  host,
		Port:        port,
		Container:   dockerDeployment(),
	})
}
