// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"regexp"
	"runtime"
	"sync"
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
	// fallbackChromeUAVersion 只在探测不到本机 Chrome 版本时用（没装 Chrome、
	// Windows 上 --version 不输出）。正常情况下 HTTP 抓取的 UA 跟本机 Chrome 的
	// 实际大版本走，见 BrowserUserAgent。
	fallbackChromeUAVersion = "155.0.0.0"

	// BrowserAcceptLanguage 是两条路共用的语言偏好。浏览器那条不设的话会跟着
	// 宿主机 locale 走，容器里通常是 C/POSIX，取回来的就是英文页面。
	//
	// 这是给 Chrome --accept-lang 的列表，只能写语言代码，不能带 q 值：Chrome 会
	// 原样塞进 navigator.languages，页面拿 "zh;q=0.9" 去构造 Intl.Locale 当场抛错。
	// Steam 社区这类前端因此整页只剩一句「Invalid language tag」，搜索和读网页都
	// 拿不到正文。q 值由 Chrome 自己补，发出去的请求头就是 HTTP 抓取那条路写死的
	// "zh-CN,zh;q=0.9,en;q=0.8"。
	BrowserAcceptLanguage = "zh-CN,zh,en"
)

// BrowserUserAgent 是本机对外声明的浏览器身份。
//
// 平台段跟着 runtime.GOOS 走，不是写死一个。写死的话，Linux 上跑的浏览器会一边
// 用 UA 说自己是 macOS，一边通过 UA Client Hints 报 sec-ch-ua-platform: Linux
// ——`--user-agent` 不会连带改客户端提示。这种自相矛盾比默认的 HeadlessChrome
// 更容易被认出来。
//
// 架构段同理跟着 runtime.GOARCH 走：sec-ch-ua-arch 由真实运行时决定，改不掉。
// ARM64 机器声称 x86_64 会和 Client Hints 报告的 ARM 打架。例外是 darwin 和
// windows：Chrome 在 Apple Silicon Mac 上仍报 Intel Mac OS X 10_15_7，在
// Windows ARM64 上仍报 Win64; x64（冻结的兼容 UA），照抄真实浏览器反而更一致。
//
// 浏览器自己不再用 --user-agent 覆盖：那个参数只改 UA 字符串，Client Hints
// （sec-ch-ua）照样报真实版本。写死 138 而本机是 155 时，Cloudflare 看到两边版本
// 对不上，托管验证就一直卡在「请稍候…」。只用有头之后默认 UA 本来就没有
// HeadlessChrome，覆盖它只会添乱。这里只给纯 HTTP 抓取用，版本号跟本机 Chrome 对齐，
// 两条路对外仍是同一个访客。
func BrowserUserAgent() string {
	browserUserAgentOnce.Do(func() {
		browserUserAgent = chromeUserAgent(runtime.GOOS, runtime.GOARCH, detectChromeUAVersion())
	})
	return browserUserAgent
}

var (
	browserUserAgentOnce sync.Once
	browserUserAgent     string
	chromeMajorPattern   = regexp.MustCompile(`\b(\d{2,4})\.\d+\.\d+\.\d+\b`)
)

// detectChromeUAVersion 读本机 Chrome 的大版本，按真实 Chrome 的精简 UA 写成
// "<大版本>.0.0.0"。
func detectChromeUAVersion() string {
	status := ProbeHeadlessBrowser(context.Background(), "")
	return chromeUAVersionFrom(status.Version)
}

func chromeUAVersionFrom(versionOutput string) string {
	if match := chromeMajorPattern.FindStringSubmatch(versionOutput); match != nil {
		return match[1] + ".0.0.0"
	}
	return fallbackChromeUAVersion
}

func chromeUserAgent(goos, goarch, version string) string {
	platform := linuxPlatform(goarch)
	switch goos {
	case "darwin":
		platform = "Macintosh; Intel Mac OS X 10_15_7"
	case "windows":
		platform = "Windows NT 10.0; Win64; x64"
	}
	return fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36",
		platform, version,
	)
}

// linuxPlatform 按架构给 Linux UA 平台段。ARM64 用 aarch64，与真实 Chrome on
// ARM Linux 的 UA 及 sec-ch-ua-arch: ARM 保持一致。
func linuxPlatform(goarch string) string {
	if goarch == "arm64" {
		return "X11; Linux aarch64"
	}
	return "X11; Linux x86_64"
}
