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

// 网页渲染需要 Chrome/Chromium，中文截图还需要字体。过去浏览器只在真正渲染时
// 才去找：插件在控制台上是「已启用」，机器上没装浏览器也照样是「已启用」，直到
// 有人在群里发了个链接，才收到一句「渲染失败」。这里把它做成和 yt-dlp / ffmpeg
// 完全一样的运行依赖：启用之前就能看出来齐不齐，缺了也能一键装。
var (
	browserDepsMu    sync.RWMutex
	browserDepsCache []ResolverDependency
)

const browserDependencyName = "browser-renderer"

const relationFontDependencyName = "cjk-font"

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
	status := agent.ProbeHeadlessBrowserRendering(context.Background(), "")
	return append(browserDependenciesFromStatus(status, runtime.GOOS, lookResolverCommand), cjkFontDependency())
}

func browserDependenciesFromStatus(status agent.HeadlessBrowserStatus, goos string, lookPath func(string) (string, error)) []ResolverDependency {
	dep := ResolverDependency{
		Name:    browserDependencyName,
		Purpose: "网页渲染：使用系统 Chromium / Google Chrome",
	}
	if status.Available {
		dep.Available = true
		dep.Path = status.Path
		dep.Version = strings.TrimSpace(status.Version)
		return []ResolverDependency{dep}
	}
	dep.Detail = strings.TrimSpace(status.Detail)
	if plan, err := resolverDependencyInstallPlan(browserDependencyName, goos, lookPath); err == nil {
		dep.Installable = true
		dep.Installer = plan.installer
	} else {
		dep.Detail = strings.TrimSpace(dep.Detail + "。没有可用的系统包管理器，请手动安装 Chromium / Google Chrome")
	}
	return []ResolverDependency{dep}
}

// installBrowserDependency 通过系统包管理器安装 Chromium / Chrome，随后验证真实截图。
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
		return ResolverDependencyInstallResult{}, fmt.Errorf("%s 已执行 Chromium / Chrome 安装，但网页渲染仍然不可用%s", plan.installer, detail)
	}
	return ResolverDependencyInstallResult{Dependency: dep, Plugins: browserDependencyGroup(deps), Installer: plan.installer}, nil
}

func browserDependencyGroup(deps []ResolverDependency) map[string][]ResolverDependency {
	return map[string][]ResolverDependency{SandboxedBrowserPluginID: deps, GroupRelationsPluginID: RelationRenderDependencies(deps)}
}

// RelationRenderDependencies 展示中文字体与备用浏览器。复杂文字走浏览器排版，
// 缺少的字体在首次出图时按需下载。
func RelationRenderDependencies(browser []ResolverDependency) []ResolverDependency {
	fontDep := cjkFontDependency()
	result := []ResolverDependency{fontDep}
	if dep, ok := resolverDependencyByName(browser, browserDependencyName); ok {
		dep.Purpose = "浏览器渲染：用真实无头截图生成 PNG"
		result = append(result, dep)
	}
	return result
}
