// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"runtime"
)

// 无头浏览器和纯 HTTP 抓取共用的浏览器身份。
//
// 抽出来之前两条路各说各话：链接解析那条（resolver_fetch.go）伪装成真实的桌面
// Chrome，浏览器那条什么都不设，于是 UA 里明晃晃写着 HeadlessChrome，
// navigator.webdriver 还是 true。同一个站点走两条路会看到两个身份，行为自然不一致
// ——HTTP 拿得到内容，浏览器渲染却撞登录墙。
//
// 这不是要跟风控对抗：Cloudflare 那一类挑战不是改个 UA 能过的，也不打算过。
// 要的只是「同一个机器人对外只有一个身份」，并且这个身份是台正常的桌面浏览器。
// 链接预览用真实浏览器请求头是 IM 机器人（Slack/Discord unfurl 等）的通行做法。
const (
	// chromeUAVersion 是 UA 里报的 Chrome 大版本。跟实际跑的 Chromium 版本不必
	// 严格一致，但别落后太多——太旧的版本号本身就是可疑信号。
	chromeUAVersion = "138.0.0.0"

	// BrowserAcceptLanguage 是两条路共用的语言偏好。浏览器那条不设的话会跟着
	// 宿主机 locale 走，容器里通常是 C/POSIX，取回来的就是英文页面。
	BrowserAcceptLanguage = "zh-CN,zh;q=0.9,en;q=0.8"
)

// BrowserUserAgent 是本机对外声明的浏览器身份。
//
// 平台段跟着 runtime.GOOS 走，不是写死一个。写死的话，Linux 上跑的浏览器会一边
// 用 UA 说自己是 macOS，一边通过 UA Client Hints 报 sec-ch-ua-platform: Linux
// ——`--user-agent` 不会连带改客户端提示。这种自相矛盾比默认的 HeadlessChrome
// 更容易被认出来。
var BrowserUserAgent = chromeUserAgent(runtime.GOOS)

func chromeUserAgent(goos string) string {
	platform := "X11; Linux x86_64"
	switch goos {
	case "darwin":
		platform = "Macintosh; Intel Mac OS X 10_15_7"
	case "windows":
		platform = "Windows NT 10.0; Win64; x64"
	}
	return fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36",
		platform, chromeUAVersion,
	)
}
