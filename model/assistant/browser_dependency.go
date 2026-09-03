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
	dep := ResolverDependency{
		Name:    browserDependencyName,
		Purpose: "网页渲染：优先使用系统 Chrome/Chromium，没有时使用轻量 Obscura",
	}
	if status.Available {
		dep.Available = true
		dep.Path = status.Path
		dep.Version = strings.TrimSpace(status.Version)
		return []ResolverDependency{dep}
	}
	dep.Detail = strings.TrimSpace(status.Detail)
	if _, ok := obscuraReleaseAssets[currentPlatformKey()]; ok {
		dep.Installable = true
		dep.Installer = "Diana 下载 Obscura " + obscuraVersion
	} else {
		dep.Detail = strings.TrimSpace(dep.Detail + "。当前平台没有 Obscura 预编译包，请手动安装 Chrome/Chromium")
	}
	return []ResolverDependency{dep}
}

// installBrowserDependency 在系统没有浏览器时下载固定版本的 Obscura。Diana 不再
// 默认通过系统包管理器安装数 GB 的 Chrome；用户已经装过 Chrome 时仍优先复用它。
func installBrowserDependency(ctx context.Context) (ResolverDependencyInstallResult, error) {
	deps := RefreshBrowserDependencies()
	if dep, ok := resolverDependencyByName(deps, browserDependencyName); ok && dep.Available {
		return ResolverDependencyInstallResult{Dependency: dep, Plugins: browserDependencyGroup(deps)}, nil
	}
	path, err := installObscura(ctx)
	if err != nil {
		return ResolverDependencyInstallResult{}, err
	}
	deps = RefreshBrowserDependencies()
	dep, ok := resolverDependencyByName(deps, browserDependencyName)
	if !ok || !dep.Available {
		detail := ""
		if ok && strings.TrimSpace(dep.Detail) != "" {
			detail = "：" + dep.Detail
		}
		return ResolverDependencyInstallResult{}, fmt.Errorf("Obscura 已安装到 %s，但网页渲染仍然不可用%s", path, detail)
	}
	return ResolverDependencyInstallResult{Dependency: dep, Plugins: browserDependencyGroup(deps), Installer: "Diana 下载 Obscura " + obscuraVersion}, nil
}

func currentPlatformKey() string { return runtime.GOOS + "/" + runtime.GOARCH }

func browserDependencyGroup(deps []ResolverDependency) map[string][]ResolverDependency {
	return map[string][]ResolverDependency{SandboxedBrowserPluginID: deps}
}

// RelationRenderDependencies 返回关系图两条可替代渲染路径的状态。字体能用时直接
// 栅格化，字体不能用时浏览器截图兜底；界面会按「至少一条可用」判断整体状态。
func RelationRenderDependencies(browser []ResolverDependency) []ResolverDependency {
	fontDep := ResolverDependency{
		Name:    relationFontDependencyName,
		Purpose: "直接渲染：读取中文字形并在进程内生成 PNG",
	}
	if _, path, err := searchUsableCJKFont(); err == nil {
		fontDep.Available = true
		fontDep.Path = path
		fontDep.Version = "可用"
	} else {
		fontDep.Detail = err.Error()
	}
	result := []ResolverDependency{fontDep}
	if dep, ok := resolverDependencyByName(browser, browserDependencyName); ok {
		dep.Purpose = "浏览器渲染：用真实无头截图生成 PNG"
		result = append(result, dep)
	}
	return result
}
