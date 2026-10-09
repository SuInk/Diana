# 桌面控制（阶段 1–2）

这一档让主人通过 Diana **查看并受限操作本机桌面窗口**：列出获准应用的窗口、截取画面，并在打开写权限后点击、输入与按键。真正枚举窗口、截图与键鼠的是用户机器上的本地执行器；Diana 只下发有限指令并等回执。

它和[浏览器控制扩展](browser-control.md)、[内置浏览器](browser-builtin.md)都是两回事，不互相替代：

| | 浏览器控制扩展 | 桌面控制（本档） |
| --- | --- | --- |
| 对象 | 用户日常浏览器里的标签页 | 操作系统窗口 |
| 白名单 | 站点主机名 | 应用 Bundle ID / 显示名 |
| 工具 | `browser_ext_*`（读写分档） | `desktop_windows` / `desktop_screenshot`；写操作另需 `write_enabled` |
| 默认 | 全关 | 全关 |

**仍不做：** 持久电脑任务、确认流、WebUI 实时画面。

## 授权模型

要真的能看到或操作一个窗口，下面每一项都必须成立：

1. **总开关**：桌面控制策略里 `enabled` 打开（默认关）。
2. **执行器已连接**：本机 helper 已与控制面握手。
3. **应用范围**：`allowed_apps` 为空时，主人默认可访问全部应用窗口；填写后只保留名单内的 Bundle ID 或应用显示名。`denied_apps` 优先于白名单。显示名给人看；内部用 Bundle ID 与 `window_id` 定位。
4. **读写档位**：列窗口与截图是只读；**点击、输入、按键**须 `write_enabled` 打开，否则返回 `write_disabled`。
5. **按机器人开关**：`agent_desktop_control_enabled`。默认关闭，逐台打开。
6. **身份**：这组工具只对主人开放；群成员的工具白名单里没有它们。
7. **系统权限（macOS）**：
   - **Screen Recording**：截图必需。
   - **Accessibility（辅助功能）**：点击、输入、按键必需。未授权时返回可解释的 `permission_denied`，而不是静默失败。

未授权应用的窗口在 `desktop_windows` 结果里**不可见**；对不可见窗口的截图或写操作会返回明确错误。人工接管开启后，**一切指令**（含只读）立即拒绝（`takeover`）。

## 工具

| 工具 | 档位 | 说明 |
| --- | --- | --- |
| `desktop_windows` | 读 | 列出已授权应用的窗口（显示名、标题、`window_id`、`bundle_id`） |
| `desktop_screenshot` | 读 | 截取指定窗口；图片经 `ToolResultParts` 回传模型 |
| `desktop_click` | 写 | 相对窗口左上角坐标点击（`x`/`y`，可选 `button`） |
| `desktop_type` | 写 | 向窗口输入文字（不要填密码/验证码/支付信息） |
| `desktop_key` | 写 | 按键或组合键（如 `Return`、`Tab`、`cmd+c`） |

写操作工具在桥接可用时会登记；能否真正执行由 `write_enabled`、应用白名单与接管状态决定。

## macOS 执行器

仓库提供参考 helper：[`native/macos/desktopctl-helper.swift`](../native/macos/desktopctl-helper.swift)。编译与权限说明见同目录 `README.md`。

单元测试使用假连接与 `MockAdapter`，**不依赖真实显示器、Screen Recording 或 Accessibility**。CI 无需图形环境。

## 与浏览器控制的协议对照

帧类型同样是 `hello` / `welcome` / `command` / `result` / `takeover` / `ping`，但载荷是窗口而不是标签页。指令包括 `windows.list`、`window.screenshot`、`window.click`、`window.type`、`window.key`。协议预留 `job_id` 与 `observation`，供后续持久任务使用。

macOS 写入要求目标窗口当前位于前台；helper 在发送前核对窗口和进程，点击坐标必须在窗口内。键盘和鼠标事件发送到目标进程，文本输入期间切换窗口会停止后续输入。
