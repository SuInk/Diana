// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/SuInk/diana/internal/browseruser"
	"github.com/SuInk/diana/internal/termux"
)

// BrowserSandboxMode 决定 Diana 拉起的、会打开外部网页的 Chromium（网页渲染、浏览器
// 搜索、内置浏览器）要不要关进程沙箱。只渲染本地 HTML 的出图另有取舍：简单截图一直
// 不带沙箱，带脚本的出图在 root 下放行，其余跟这里的档位走。
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

// browserLaunchAccount 是 root 运行时拉起 Chromium 用的普通用户（见 internal/browseruser）。
// Chromium 不许 root 带沙箱启动，换成 diana 用户启动，沙箱就照开。
func browserLaunchAccount() (browseruser.Account, bool) {
	if !browserRunningAsRoot() || termux.Detected() {
		return browseruser.Account{}, false
	}
	return browseruser.Lookup()
}

// browserAccountFor 返回启动 executable 该用的账号：root 运行、建得出 diana 用户、
// 而且它够得着这个可执行文件。不需要降权或降不了时返回 false。
func browserAccountFor(executable string) (browseruser.Account, bool) {
	account, ok := browserLaunchAccount()
	if !ok || !account.CanReach(executable) {
		return browseruser.Account{}, false
	}
	return account, true
}

// BrowserRootAccount 是 root 运行时降权用的账号，不看可执行文件；给 model/browserbox
// 算登录态目录用。
func BrowserRootAccount() (browseruser.Account, bool) { return browserLaunchAccount() }

// BrowserLaunchAccount 给 model/browserbox：常驻浏览器同样按这个账号启动。
func BrowserLaunchAccount(executable string) (browseruser.Account, bool) {
	return browserAccountFor(executable)
}

// applyBrowserAccount 在 args 带沙箱时把 cmd 换成 diana 用户启动，并把浏览器要写的
// 目录交给它。args 本来就不带沙箱（比如简单截图）时照当前身份启动：那条路要读 root
// 家目录里的字体，降权反而读不到。
func applyBrowserAccount(cmd *exec.Cmd, executable string, args []string, dirs ...string) error {
	if slices.Contains(args, "--no-sandbox") {
		return nil
	}
	account, ok := browserAccountFor(executable)
	if !ok {
		return nil
	}
	if err := account.Own(dirs...); err != nil {
		return err
	}
	account.Apply(cmd)
	return nil
}

// browserRootWithoutAccount 表示以 root 运行、却没法换成 diana 用户启动默认找到的
// 浏览器：这时 Chromium 仍然是 root，起不来沙箱。返回原因，空串表示不是这种情况。
func browserRootWithoutAccount() string {
	if !browserRunningAsRoot() || termux.Detected() {
		return ""
	}
	account, ok := browserLaunchAccount()
	if !ok {
		if reason := browseruser.Reason(); reason != "" {
			return "以 root 运行，没能准备 " + browseruser.Name + " 用户来降权启动浏览器：" + reason
		}
		return "以 root 运行，Chromium 不允许 root 带沙箱启动"
	}
	executable, err := findHeadlessBrowserExecutable("")
	if err != nil || account.CanReach(executable) {
		return ""
	}
	return fmt.Sprintf("以 root 运行，浏览器 %s 在 %s 用户进不去的目录里，没法降权启动；到依赖页重新安装浏览器会装到 %s", executable, browseruser.Name, browseruser.Home)
}

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
	case mode == BrowserSandboxAuto && browserRootWithoutAccount() != "":
		status.Active, status.Reason = false, browserRootWithoutAccount()+"，已自动关闭"
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

// BrowserSandboxFallback 在浏览器启动失败后调用：处于「自动」档、且失败输出是沙箱起不来
// 的特征时，记下降级并返回 true，调用方去掉沙箱重试一次（每次启动最多一次）。
// 已经降级过也返回 true：并发的几次启动可能同时带着沙箱失败，后到的那次同样该重试。
// 其余情况返回 false，原样报错。
func BrowserSandboxFallback(diagnostics string) bool {
	if !browserSandboxFailure(diagnostics) {
		return false
	}
	browserSandbox.Lock()
	defer browserSandbox.Unlock()
	if browserSandbox.mode != BrowserSandboxAuto || termux.Detected() || browserRootWithoutAccount() != "" {
		return false
	}
	if !browserSandbox.fellBack {
		browserSandbox.fellBack = true
		browserSandbox.reason = compactBrowserError(diagnostics)
		log.Printf("WARN 浏览器沙箱起不来，按「自动」档去掉沙箱重试：%s", browserSandbox.reason)
	}
	return true
}

// WithBrowserSandboxHint 给「仅沙箱」档下因沙箱起不来的启动失败补一句怎么办，
// 不然模型和主人只看到 Chromium 的原始报错，不知道能换档。
func WithBrowserSandboxHint(err error) error {
	if err == nil || !browserSandboxFailure(err.Error()) {
		return err
	}
	browserSandbox.RLock()
	mode := browserSandbox.mode
	browserSandbox.RUnlock()
	if mode != BrowserSandboxOnly {
		return err
	}
	hint := "浏览器沙箱起不来"
	if reason := browserRootWithoutAccount(); reason != "" {
		hint = reason
	}
	return fmt.Errorf("%w（%s：当前是「仅沙箱」档。Ubuntu 23.10+ 等用 AppArmor 限制了用户命名空间的系统需由管理员放开；放不开可以在浏览器页「更多设置 → 浏览器沙箱」改成「自动」）", err, hint)
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
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
