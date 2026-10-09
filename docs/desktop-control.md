# 桌面控制

这一档让主人通过 Diana **查看并受限操作本机桌面窗口**，并支持**持久电脑任务**（暂停、恢复、等待确认、取消、预算；重启后保留）。真正枚举窗口、截图与键鼠的是用户机器上的本地执行器；Diana 只下发有限指令并等回执。

它和[浏览器控制扩展](browser-control.md)、[内置浏览器](browser-builtin.md)都是两回事，不互相替代：

| | 浏览器控制扩展 | 桌面控制（本档） |
| --- | --- | --- |
| 对象 | 用户日常浏览器里的标签页 | 操作系统窗口 |
| 白名单 | 站点主机名 | 应用 Bundle ID / 显示名 |
| 工具 | `browser_ext_*` | `desktop_*` + `desktop_job_*` |
| 默认 | 全关 | 全关 |

WebUI 的“设置 → 桌面控制”提供系统权限状态、具体操作预览与确认、人工接管和任务管理；当前没有实时画面流。

## 授权模型

1. **总开关** `enabled`（默认关）。
2. **执行器已连接**。
3. **应用范围**：`allowed_apps` 为空时主人默认可访问全部应用；可再收窄。`denied_apps` 优先。显示名给人看；Bundle ID / `window_id` 做定位。
4. **读写档位**：列窗口与截图只读；点击/输入/按键须 `write_enabled`。
5. **机器人开关** `agent_desktop_control_enabled`。
6. **身份**：仅主人；群成员工具白名单不含这些工具。
7. **macOS 权限**：Screen Recording（截图）；Accessibility（写操作）。失败返回可解释 `permission_denied`。

人工接管开启后，一切指令拒绝（`takeover`）。接管状态独立落盘，重连、重启、修改应用权限不会自动解除；只能在控制台明确结束接管。

## 工具

| 工具 | 档位 | 说明 |
| --- | --- | --- |
| `desktop_windows` | 读 | 列出已授权窗口 |
| `desktop_screenshot` | 读 | 截图；可清掉任务的「须重新观察」标记 |
| `desktop_elements` | 读 | 读取可访问元素、标签和观察版本，需要辅助功能权限；安全输入控件不返回 |
| `desktop_click` / `desktop_type` / `desktop_key` / `desktop_scroll` | 写 | 须 `write_enabled`、有效 `observation` 和控制台单次确认 |
| `desktop_job_create` | 任务 | 创建持久任务，返回 `job_id` |
| `desktop_job_status` | 任务 | 查询或列出 |
| `desktop_job_pause` / `desktop_job_resume` | 任务 | 暂停 / 恢复（恢复须先截图再写） |
| `desktop_job_wait_confirm` | 任务 | 进入等待确认 |
| 控制台“确认继续” | 人工操作 | 结束「等待确认」，不向模型注册确认工具 |
| `desktop_job_finish` | 任务 | 核实结果后结束为成功或失败 |
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

## 本机连接

macOS 完整包包含 helper，Diana 自动从主程序同目录发现它。源码部署可用 `DIANA_DESKTOP_HELPER` 指定绝对路径。服务定期恢复连接，每次操作前重新枚举窗口并复核应用范围；窗口 ID 被其他应用复用时不会沿用旧授权。文本经标准输入传递。取消、超时、人工接管或修改权限会中断仍在执行的 helper；已经发生的操作不能回滚。同一任务只允许一条在途指令。

HTTP 管理接口位于 `/api/desktop-control`，沿用控制台会话认证。启用和输入权限均默认关闭。系统授权需要用户在 macOS 系统设置中完成。

## 观察、确认和结果核实

本机执行器的写操作使用以下流程：

1. `desktop_screenshot` 或 `desktop_elements` 返回绑定当前窗口、应用和画面的 `observation`，有效期两分钟。元素 ID 仅用于本次元素观察；优先元素点击，坐标用于不提供元素的应用。
2. 模型提交具体操作与观察版本。执行器核对窗口画面，先返回 `confirmation_required`，这时尚未发送输入。控制台展示请求时的窗口截图、目标应用、元素或坐标、输入文字或按键。
3. 主人在控制台确认后，让 Diana 使用相同参数重试。确认只用一次，不能换文字、目标、观察版本或任务。使用任务幂等键时须给这次尝试新的键；原先的等待确认记录不会重放。
4. 执行器再次截图比较。画面变动、过期或连接重建会令旧观察失效。动画、闪烁光标也可能使画面比较失败，需要重新观察。
5. 操作下发后附带新截图，`ok` 仅表示输入已经发送，`needs_verification` 提醒模型核实实际效果。若回看截图失败，保留输入已发送这一事实，返回核实错误，禁止盲目重复提交。

当前通用执行器不能可靠判断一个按钮是否代表发送、支付或删除，因此所有写操作逐条确认。任务的“继续任务”只解除任务等待，不代替具体操作授权。确认与观察只在内存保留；重启后必须重新观察并确认。

滚动的 `x/y` 指向窗口内区域，`delta_y` 正值向下，`delta_x` 正值向右，每轴最多 1000 像素。同一执行器的观察、核对、输入、回看串行执行，不能让其他任务在核对和输入之间插入操作。

任务审计新记录保存输入文本与截图的 SHA-256 摘要，不保存输入全文、原始截图或元素值。截图通过本次工具结果交给模型，待确认窗口截图只在内存中短期保留；这不改变聊天记录或模型提供商自己的留存行为。历史任务记录不会自动迁移或清除。

## 尚未覆盖的边界

本功能还不能视为完整的电脑任务沙箱：尚无文件选择、上传、导出路径的目录级强制约束，也没有通用截图敏感信息识别与遮罩。系统标记的安全输入框会要求人工接管，普通文本框中的验证码、密钥等仍需人工处理。实时画面流、完整的任务证据归档/保留策略、跨应用任务和真实保存产物的端到端验收仍待完善；[新增受限电脑操作能力：本地桌面执行器与持久任务管理（#948）](https://github.com/SuInk/Diana/issues/948) 保持开放。

原生实现使用 Apple 的 [Accessibility 元素读取 API](https://developer.apple.com/documentation/applicationservices/1462085-axuielementcopyattributevalue) 和 [Core Graphics 滚动事件 API](https://developer.apple.com/documentation/coregraphics/cgevent/init(scrollwheelevent2source:units:wheelcount:wheel1:wheel2:wheel3:))。
