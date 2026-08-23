// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// 网页渲染插件唯一的外部依赖就是一个 Chrome/Chromium，可它以前只在真正渲染时
// 才去找：插件在控制台上是「已启用」，机器上没装浏览器也照样是「已启用」，直到
// 有人在群里发了个链接，才收到一句「渲染失败」。这里把它做成和 yt-dlp / ffmpeg
// 完全一样的运行依赖：启用之前就能看出来齐不齐，缺了也能一键装。
var (
	browserDepsMu    sync.RWMutex
	browserDepsCache []ResolverDependency
)

const browserDependencyName = "chrome"

// 插件 ID 对外导出，WebUI 要按插件把依赖分组显示。
const (
	ResolverPluginID         = resolverPluginID
	SandboxedBrowserPluginID = sandboxedBrowserPluginID
	RSSWatchPluginID         = rssWatchPluginID
)

// RSSWatchBrowserDependencies 把同一个浏览器依赖挂到订阅插件下：抖音订阅要靠
// 无头浏览器打开主页截接口，缺浏览器时应该在订阅插件的卡片上就能看出来。
func RSSWatchBrowserDependencies(deps []ResolverDependency) []ResolverDependency {
	out := cloneResolverDependencies(deps)
	for index := range out {
		out[index].Purpose = "抖音订阅：用一次性无头浏览器打开主页并截取官方接口响应"
	}
	return out
}

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
	dep.Detail = strings.TrimSpace(status.Detail)
	// 和 yt-dlp / ffmpeg 一样能一键装。浏览器体积大，但装的过程同样是「一条包管理器
	// 命令」，没理由让用户自己去查这个发行版对应的包名叫什么。
	if plan, err := resolverDependencyInstallPlan(browserDependencyName, runtime.GOOS, lookResolverCommand); err == nil {
		dep.Installable = true
		dep.Installer = plan.installer
	} else {
		// 装不了的时候才需要教怎么装；能一键装时再写这句只会和按钮打架。
		dep.Detail = strings.TrimSpace(dep.Detail + "。这台机器上没有可用的包管理器，需要自己装一个（Linux 上 chromium 或 google-chrome，macOS 上 Google Chrome）")
	}
	return []ResolverDependency{dep}
}

// installBrowserDependency 装浏览器，并用真正的探测确认装完能用。
//
// 装完还要复核，是因为「包管理器说成功了」不等于能用：Ubuntu 的 chromium-browser
// 是个转发到 snap 的过渡包，容器里没有 snapd 就会装上一个跑不起来的壳子。复核这一步
// 会把它照实说出来，而不是让用户在群里发链接时才撞见。
func installBrowserDependency(ctx context.Context) (ResolverDependencyInstallResult, error) {
	deps := RefreshBrowserDependencies()
	if dep, ok := resolverDependencyByName(deps, browserDependencyName); ok && dep.Available {
		return ResolverDependencyInstallResult{Dependency: dep, Plugins: browserDependencyGroup(deps)}, nil
	}
	plan, err := resolverDependencyInstallPlan(browserDependencyName, runtime.GOOS, lookResolverCommand)
	if err != nil {
		return ResolverDependencyInstallResult{}, err
	}
	if err := runDependencyInstallPlan(ctx, plan, browserDependencyName); err != nil {
		return ResolverDependencyInstallResult{}, err
	}
	deps = RefreshBrowserDependencies()
	dep, ok := resolverDependencyByName(deps, browserDependencyName)
	if !ok || !dep.Available {
		detail := ""
		if ok && strings.TrimSpace(dep.Detail) != "" {
			detail = "：" + dep.Detail
		}
		return ResolverDependencyInstallResult{}, fmt.Errorf("%s 已执行，但浏览器仍然不可用%s", plan.installer, detail)
	}
	return ResolverDependencyInstallResult{Dependency: dep, Plugins: browserDependencyGroup(deps), Installer: plan.installer}, nil
}

func browserDependencyGroup(deps []ResolverDependency) map[string][]ResolverDependency {
	return map[string][]ResolverDependency{SandboxedBrowserPluginID: deps}
}
