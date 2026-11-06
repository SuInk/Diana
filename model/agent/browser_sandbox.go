// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/SuInk/diana/internal/termux"
)

// BrowserSandboxMode 决定 Diana 拉起的 Chromium（网页渲染、浏览器搜索、内置浏览器、
// 带脚本的 HTML 出图）要不要关进程沙箱；只渲染本地 HTML 的简单截图另有取舍，不归它管。
// WebUI 浏览器页可改，立即生效。
//
// 关掉沙箱后，网页里的渲染器漏洞利用不再被沙箱挡住，页面隔离只剩内网屏蔽和临时
// profile；而网页渲染的页面是群成员发来的链接。所以默认只用沙箱，起不来就报错，由
// 主人显式选择「自动」或「关闭」。
type BrowserSandboxMode string

const (
	// BrowserSandboxOnly 只用沙箱：沙箱起不来就启动失败，不降级。默认值。
	BrowserSandboxOnly BrowserSandboxMode = "sandbox"
	// BrowserSandboxAuto 能开就开：已知开不了（root 运行）直接关；其余情况先带沙箱
	// 启动，因沙箱失败退出时去掉沙箱重试一次，之后这个进程里都不再带沙箱。
	BrowserSandboxAuto BrowserSandboxMode = "auto"
	// BrowserSandboxOff 始终关闭沙箱。
	BrowserSandboxOff BrowserSandboxMode = "off"
)

var browserSandbox = struct {
	sync.RWMutex
	mode BrowserSandboxMode
	// fellBack 记录「自动」档已经因为沙箱起不来退过一次：沙箱起不来是宿主属性，
	// 下一次也起不来，没必要每次先失败一遍。换档时清掉。
	fellBack bool
	reason   string
}{mode: BrowserSandboxOnly}

// browserRunningAsRoot 单独抽出来，测试才能在 root 跑的 CI 容器里固定结果。
var browserRunningAsRoot = func() bool { return os.Geteuid() == 0 }

// ParseBrowserSandboxMode 校验档位；空值按默认「仅沙箱」。
func ParseBrowserSandboxMode(value string) (BrowserSandboxMode, error) {
	switch mode := BrowserSandboxMode(strings.TrimSpace(value)); mode {
	case "":
		return BrowserSandboxOnly, nil
	case BrowserSandboxOnly, BrowserSandboxAuto, BrowserSandboxOff:
		return mode, nil
	}
	return "", fmt.Errorf("浏览器沙箱档位必须是 sandbox（仅沙箱）、auto（自动）或 off（关闭）")
}

// SetBrowserSandboxMode 切换档位，对之后启动的浏览器生效。已经在跑的内置浏览器要重启才换。
func SetBrowserSandboxMode(mode BrowserSandboxMode) {
	if mode == "" {
		mode = BrowserSandboxOnly
	}
	browserSandbox.Lock()
	defer browserSandbox.Unlock()
	if browserSandbox.mode != mode {
		browserSandbox.fellBack, browserSandbox.reason = false, ""
	}
	browserSandbox.mode = mode
}

// BrowserSandboxStatus 是浏览器页要显示的沙箱状态。
type BrowserSandboxStatus struct {
	Mode BrowserSandboxMode `json:"mode"`
	// Active 表示接下来启动的浏览器会带沙箱。
	Active bool `json:"active"`
	// Reason 说明没带沙箱的原因；带沙箱时为空。
	Reason string `json:"reason,omitempty"`
}

// CurrentBrowserSandbox 返回当前档位和实际是否带沙箱。
func CurrentBrowserSandbox() BrowserSandboxStatus {
	browserSandbox.RLock()
	mode, fellBack, reason := browserSandbox.mode, browserSandbox.fellBack, browserSandbox.reason
	browserSandbox.RUnlock()
	status := BrowserSandboxStatus{Mode: mode, Active: true}
	switch {
	case termux.Detected():
		status.Active, status.Reason = false, "Termux 里安卓不给普通 App 开用户命名空间，进程沙箱建不起来，始终关闭"
	case mode == BrowserSandboxOff:
		status.Active, status.Reason = false, "已手动关闭"
	case mode == BrowserSandboxAuto && browserRunningAsRoot():
		status.Active, status.Reason = false, "以 root 运行，Chromium 不允许 root 带沙箱启动，已自动关闭"
	case mode == BrowserSandboxAuto && fellBack:
		status.Active, status.Reason = false, "带沙箱启动失败，已自动关闭："+reason
	}
	return status
}

// browserNoSandbox 决定底座参数要不要带 --no-sandbox。
//
// Termux 不受档位约束：安卓上根本没有可用的沙箱，带着沙箱一个浏览器都起不来，而页面
// 隔离另有 Termux 自己的 App 沙箱。
func browserNoSandbox() bool {
	return !CurrentBrowserSandbox().Active
}

// BrowserSandboxFallback 在浏览器启动失败后调用：处于「自动」档、这次带了沙箱、且失败
// 输出是沙箱起不来的特征时，记下降级并返回 true，调用方去掉沙箱重试一次。其余情况
// 返回 false，原样报错。
func BrowserSandboxFallback(diagnostics string) bool {
	if !browserSandboxFailure(diagnostics) {
		return false
	}
	browserSandbox.Lock()
	defer browserSandbox.Unlock()
	if browserSandbox.mode != BrowserSandboxAuto || browserSandbox.fellBack || termux.Detected() || browserRunningAsRoot() {
		return false
	}
	browserSandbox.fellBack = true
	browserSandbox.reason = compactBrowserError(diagnostics)
	log.Printf("WARN 浏览器沙箱起不来，按「自动」档去掉沙箱重试：%s", browserSandbox.reason)
	return true
}

// browserSandboxFailure 认 Chromium 因沙箱起不来而退出的几种输出：root 被拒、
// setuid/namespace 沙箱不可用、seccomp 拦了 clone/unshare。
func browserSandboxFailure(diagnostics string) bool {
	text := strings.ToLower(diagnostics)
	for _, marker := range []string{
		"without --no-sandbox",
		"no usable sandbox",
		"setuid sandbox",
		"namespace sandbox",
		"zygote_host",
		"operation not permitted",
		"failed to move to new namespace",
		"clone",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
