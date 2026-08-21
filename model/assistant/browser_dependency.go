// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// 网页渲染插件唯一的外部依赖就是一个 Chrome/Chromium，可它以前只在真正渲染时
// 才去找：插件在控制台上是「已启用」，机器上没装浏览器也照样是「已启用」，直到
// 有人在群里发了个链接，才收到一句「渲染失败」。这里把它做成和 yt-dlp / ffmpeg
// 一样的运行依赖，启用之前就能看出来齐不齐。
var (
	browserDepsMu    sync.RWMutex
	browserDepsCache []ResolverDependency
)

const browserDependencyName = "chrome"

// 插件 ID 对外导出，WebUI 要按插件把依赖分组显示。
const (
	ResolverPluginID         = resolverPluginID
	SandboxedBrowserPluginID = sandboxedBrowserPluginID
)

// BrowserDependencies 返回缓存的浏览器探测结果。
func BrowserDependencies() []ResolverDependency {
	browserDepsMu.RLock()
	if browserDepsCache != nil {
		out := cloneResolverDependencies(browserDepsCache)
		browserDepsMu.RUnlock()
		return out
	}
	browserDepsMu.RUnlock()
	return RefreshBrowserDependencies()
}

// RefreshBrowserDependencies 重新探测浏览器。用户装完浏览器之后不必重启，
// 在设置页点一下刷新就能看到最新状态。
func RefreshBrowserDependencies() []ResolverDependency {
	deps := probeBrowserDependencies()
	browserDepsMu.Lock()
	browserDepsCache = cloneResolverDependencies(deps)
	browserDepsMu.Unlock()
	return deps
}

func probeBrowserDependencies() []ResolverDependency {
	status := agent.ProbeHeadlessBrowser(context.Background(), "")
	dep := ResolverDependency{
		Name:    browserDependencyName,
		Purpose: "网页渲染：在一次性沙盒里执行页面 JS 后读取正文",
	}
	if status.Available {
		dep.Available = true
		dep.Path = status.Path
		dep.Version = strings.TrimSpace(status.Version)
		return []ResolverDependency{dep}
	}
	// 浏览器不走包管理器一键装：体积大、发行版之间差异也大，装错版本比没装更难查。
	// 这里只如实说明卡在哪一步，界面会显示成「需手动安装」加上这句原因。
	dep.Detail = strings.TrimSpace(status.Detail)
	return []ResolverDependency{dep}
}
