// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"log"
	"strings"
	"sync"
)

// 整个进程只需要一个浏览器：网页渲染插件、browser_render 工具、链接解析的渲染兜底
// 用的都是同一个可执行文件。所以「没有就装一个」这件事也该只有一处，而不是每个调用方
// 各写一遍——写三遍的结果一定是有的地方装、有的地方只会报「缺少浏览器」。
//
// 装的动作在上层（assistant 知道包管理器、Chrome for Testing 下载、data 目录在哪），
// 这里只留一个注册点和一道闸：探到没有就调一次，装不上也不再重试。
var (
	browserInstallerMu sync.RWMutex
	browserInstaller   func(context.Context) error
	browserInstallOnce sync.Once
)

// SetBrowserInstaller 注册「没浏览器时怎么装」。上层在启动时注入；没注入时
// EnsureBrowser 什么也不做，测试和库使用者不会被顺手拖去下载一个浏览器。
func SetBrowserInstaller(install func(context.Context) error) {
	browserInstallerMu.Lock()
	defer browserInstallerMu.Unlock()
	browserInstaller = install
}

// EnsureBrowser 在真正要用浏览器之前探一次；没有就装。整个进程只做一次：装不上就别
// 每次渲染都重试，几十 MB 的下载失败重试会把带宽和日志一起打满。
func EnsureBrowser(ctx context.Context) {
	browserInstallerMu.RLock()
	install := browserInstaller
	browserInstallerMu.RUnlock()
	if install == nil {
		return
	}
	browserInstallOnce.Do(func() {
		if status := ProbeHeadlessBrowser(ctx, ""); status.Available {
			return
		}
		if err := install(ctx); err != nil {
			log.Printf("diana browser: 没有可用浏览器，自动安装也没成功：%v", err)
			return
		}
		status := ProbeHeadlessBrowser(ctx, "")
		log.Printf("diana browser: 自动装好了渲染用浏览器 path=%s version=%s", status.Path, strings.TrimSpace(status.Version))
	})
}

// resetBrowserInstallStateForTest 让用例能反复验证这道闸。只在测试里用。
func resetBrowserInstallStateForTest() {
	browserInstallerMu.Lock()
	defer browserInstallerMu.Unlock()
	browserInstaller = nil
	browserInstallOnce = sync.Once{}
}
