# 桌面控制（阶段 1–3）

这一档让主人通过 Diana **查看并受限操作本机桌面窗口**，并支持**持久电脑任务**（暂停、恢复、等待确认、取消、预算；重启后保留）。真正枚举窗口、截图与键鼠的是用户机器上的本地执行器；Diana 只下发有限指令并等回执。

它和[浏览器控制扩展](browser-control.md)、[内置浏览器](browser-builtin.md)都是两回事，不互相替代：

| | 浏览器控制扩展 | 桌面控制（本档） |
| --- | --- | --- |
| 对象 | 用户日常浏览器里的标签页 | 操作系统窗口 |
| 白名单 | 站点主机名 | 应用 Bundle ID / 显示名 |
| 工具 | `browser_ext_*` | `desktop_*` + `desktop_job_*` |
| 默认 | 全关 | 全关 |

**仍不做：** WebUI 实时画面与完整控制台体验（阶段 4）。

## 授权模型

1. **总开关** `enabled`（默认关）。
2. **执行器已连接**。
3. **应用范围**：`allowed_apps` 为空时主人默认可访问全部应用；可再收窄。`denied_apps` 优先。显示名给人看；Bundle ID / `window_id` 做定位。
4. **读写档位**：列窗口与截图只读；点击/输入/按键须 `write_enabled`。
5. **机器人开关** `agent_desktop_control_enabled`。
6. **身份**：仅主人；群成员工具白名单不含这些工具。
7. **macOS 权限**：Screen Recording（截图）；Accessibility（写操作）。失败返回可解释 `permission_denied`。

人工接管开启后，一切指令立即拒绝（`takeover`）。

## 工具

| 工具 | 档位 | 说明 |
| --- | --- | --- |
| `desktop_windows` | 读 | 列出已授权窗口 |
| `desktop_screenshot` | 读 | 截图；可清掉任务的「须重新观察」标记 |
| `desktop_click` / `desktop_type` / `desktop_key` | 写 | 须 `write_enabled` |
| `desktop_job_create` | 任务 | 创建持久任务，返回 `job_id` |
| `desktop_job_status` | 任务 | 查询或列出 |
| `desktop_job_pause` / `desktop_job_resume` | 任务 | 暂停 / 恢复（恢复须先截图再写） |
| `desktop_job_wait_confirm` | 任务 | 进入等待确认 |
| `desktop_job_confirm` | 任务 | 结束「等待确认」 |
| `desktop_job_cancel` | 任务 | 取消并停止后续下发（不撤销已发生操作） |

所有操作工具可带可选 `job_id` 与 `idempotency_key`：同键已完成步骤不会再次下发。

## 持久任务（阶段 3）

- **状态**：`queued` → `running`；可进入 `paused` / `waiting_confirm`；终态 `succeeded` / `failed` / `cancelled`。
- **预算**：`max_steps`、`max_duration_ms`；下发前在锁内预占步数并持久化，保存失败则回滚并拒绝下发。已授权的尝试即占一步，失败回执不退还，成功回执不重复计数；用尽则停止下发。已有下发记录但结果未确认的幂等键不会重复执行，需要先人工核实。
- **重启**：非终态任务接回为 `paused` 且 `needs_reobserve`；恢复后必须先 `desktop_screenshot`，再写操作。
- **不重放**：已 `completed` 的步骤不会再次下发；`idempotency_key` 防止重复提交。
- **取消**：立即拒绝带该 `job_id` 的后续 Dispatch，并取消在飞等待。

任务记录与策略一并落盘（SQLite）。

## macOS 执行器

见 [`native/macos/README.md`](../native/macos/README.md)。单测用假连接与 `MockAdapter`，不依赖显示器或系统权限。

## 协议

帧：`hello` / `welcome` / `command` / `result` / `takeover` / `ping`。  
指令：`windows.list`、`window.screenshot`、`window.click`、`window.type`、`window.key`。  
载荷含 `job_id`、`observation`、`idempotency_key`。

macOS 写入要求目标窗口当前位于前台；helper 在发送前核对窗口和进程，点击坐标必须在窗口内。键盘和鼠标事件发送到目标进程，文本输入期间切换窗口会停止后续输入。
