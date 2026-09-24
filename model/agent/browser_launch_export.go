// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"strconv"
	"strings"
)

// 这里把浏览器启动要用的两件事导出给 model/browserbox：可执行文件查找和
// 沙盒参数。内置浏览器和一次性渲染那条路必须共用同一份加固参数——同一个
// Chrome 二进制、同一套 UA，不该因为「是常驻的」就少几条（解析规则是唯一的例外，见
// PersistentBrowserArgs）。
//
// 反过来 agent 不认识 browserbox：内置浏览器只是把 CDP 地址喂给现有的
// browser_* 工具，工具那一侧不需要知道浏览器是谁拉起来的。

// FindBrowserExecutable 按配置、环境变量、常见安装路径的顺序找 Chrome/Chromium。
func FindBrowserExecutable(configured string) (string, error) {
	return findHeadlessBrowserExecutable(configured)
}

// PersistentBrowserArgs 返回常驻浏览器的启动参数。
//
// profileDir 不是临时目录：内置浏览器的意义就在于登录态留得住，所以它落在
// 数据目录下，跟着挂卷走。debugPort 传 0 表示让 Chrome 自己挑端口，调用方
// 从进程输出里读实际地址。
//
// 唯一和一次性渲染不同的是解析规则：常驻浏览器不屏蔽 localhost、*.local 和
// host.docker.internal。一次性渲染群成员也能触发，屏蔽内网是为了不让别人借机器人
// 探主人的内网；常驻浏览器只有主人能驱动，他要机器人打开路由器后台、NAS、本机起的
// 服务，本来就是他自己能打开的地址。
func PersistentBrowserArgs(profileDir, cacheDir, crashDir string, headless bool, debugPort, windowWidth, windowHeight int) []string {
	base := sandboxedChromeBaseArgsForMode(profileDir, cacheDir, crashDir, headless)
	args := make([]string, 0, len(base)+3)
	for _, arg := range base {
		if strings.HasPrefix(arg, "--host-resolver-rules=") {
			continue
		}
		args = append(args, arg)
	}
	// 常驻浏览器要留住登录态，就不能带 --disable-extensions 之外的隐私清理项；
	// 基础参数里没有清 profile 的项，这里只追加模式相关的三条。
	return append(args,
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port="+strconv.Itoa(debugPort),
		"--window-size="+strconv.Itoa(windowWidth)+","+strconv.Itoa(windowHeight),
	)
}

// BrowserLaunchEnvironment 返回启动浏览器时要用的环境变量，屏蔽掉会把缓存和
// 配置写回主目录的那几项。
func BrowserLaunchEnvironment(current []string, root string) []string {
	return sandboxedBrowserEnvironment(current, root)
}
