# 桌面控制（只读 · 阶段 1）

这一档让主人通过 Diana **查看本机桌面窗口**：列出获准应用的窗口，并截取窗口画面。真正枚举窗口、截图的是用户机器上的本地执行器；Diana 只下发只读指令并等回执。

它和[浏览器控制扩展](browser-control.md)、[内置浏览器](browser-builtin.md)都是两回事，不互相替代：

| | 浏览器控制扩展 | 桌面控制（本档） |
| --- | --- | --- |
| 对象 | 用户日常浏览器里的标签页 | 操作系统窗口 |
| 白名单 | 站点主机名 | 应用 Bundle ID / 显示名 |
| 阶段 1 工具 | `browser_ext_*`（读写分档） | `desktop_windows`、`desktop_screenshot`（只读） |
| 默认 | 全关 | 全关 |

**本阶段不做：** 点击、键盘输入、持久电脑任务、确认流、WebUI 实时画面。

## 授权模型

要真的能看到一个窗口，下面每一项都必须成立：

1. **总开关**：桌面控制策略里 `enabled` 打开（默认关）。
2. **执行器已连接**：本机 helper 已与控制面握手。
3. **应用范围**：`allowed_apps` 为空时，主人默认可读全部应用窗口；填写后只保留名单内的 Bundle ID 或应用显示名。`denied_apps` 优先于白名单。
4. **只读**：`write_enabled` 在阶段 1 保持关闭；即使打开也不会下发点击/输入。
5. **按机器人开关**：`agent_desktop_control_enabled`。默认关闭，逐台打开。
6. **身份**：这组工具只对主人开放；群成员的工具白名单里没有它们。
7. **系统权限（macOS）**：截图需要 **Screen Recording**。阶段 2 的点击/输入还需要 Accessibility；阶段 1 不申请控键鼠。

未授权应用的窗口在 `desktop_windows` 结果里**不可见**；对不可见窗口的 `desktop_screenshot` 会返回明确的未授权/未知窗口错误。人工接管开启后，一切指令立即拒绝（`takeover`）。权限不足时返回可解释的 `permission_denied` 等错误码。

## 工具

| 工具 | 说明 |
| --- | --- |
| `desktop_windows` | 列出已授权应用的窗口（显示名、标题、`window_id`、`bundle_id`） |
| `desktop_screenshot` | 截取指定窗口；图片经 `ToolResultParts` 回传模型 |

## macOS 执行器

仓库提供参考 helper：[`native/macos/desktopctl-helper.swift`](../native/macos/desktopctl-helper.swift)。编译与权限说明见同目录 `README.md`。

单元测试使用假连接，**不依赖真实显示器或 Screen Recording**。CI 无需图形环境。

## 与浏览器控制的协议对照

帧类型同样是 `hello` / `welcome` / `command` / `result` / `takeover` / `ping`，但载荷是窗口而不是标签页；指令是 `windows.list` / `window.screenshot`。协议里预留了 `job_id` 与 `observation` 字段，供后续持久任务使用，阶段 1 可空。
