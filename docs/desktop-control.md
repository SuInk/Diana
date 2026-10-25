# 桌面控制

这一档让主人通过 Diana **查看并受限操作本机桌面窗口**，并支持**持久电脑任务**（暂停、恢复、等待确认、取消、防空转；重启后保留）。真正枚举窗口、截图与键鼠的是用户机器上的本地执行器；Diana 只下发有限指令并等回执。

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
| `desktop_job_create` | 任务 | 创建持久任务，返回 `job_id`；不限总步数和时长 |
| `desktop_job_status` | 任务 | 查询或列出 |
| `desktop_job_pause` / `desktop_job_resume` | 任务 | 暂停 / 恢复（恢复须先截图再写） |
| `desktop_job_wait_confirm` | 任务 | 进入等待确认 |
| 控制台“确认继续” | 人工操作 | 结束「等待确认」，不向模型注册确认工具 |
| `desktop_job_finish` | 任务 | 核实结果后结束为成功或失败 |
| `desktop_job_cancel` | 任务 | 取消并停止后续下发（不撤销已发生操作） |

所有操作工具可带可选 `job_id` 与 `idempotency_key`：同键已完成步骤不会再次下发。

## 持久任务（阶段 3）

- **状态**：`queued` → `running`；可进入 `paused` / `waiting_confirm`；终态 `succeeded` / `failed` / `cancelled`。
- **不限总步数和时长**：任务跑到完成、失败或被取消为止。`steps_used` 只记账：下发前在锁内记一步并持久化，保存失败则回滚并拒绝下发；失败回执不退还，成功回执不重复计数。旧任务里存的 `max_steps`、`max_duration_ms` 不再生效。已有下发记录但结果未确认的幂等键不会重复执行，需要先人工核实。
- **防空转**：命中下面任一条，任务转入 `waiting_confirm` 并写明原因，不判失败；主人在控制台看过现场点「确认继续」后，计数从那一步重新开始：
  - 同一画面下第 3 次执行同一操作（同窗口、同参数，观察版本和幂等键不算），说明前两次都没让画面变化。来回切换的 A→B→A→B 因画面重复也会命中。
  - 同一段文字在最近 10 次写操作里输入了 4 次（输入框光标闪烁让截图哈希每次不同，上一条认不出来）。点击、滚动、按键不套这条：连点「下一页」、往下滚长列表是正常进展。
  - 连续 24 次只截图或读元素、没有任何写操作。
- **重启**：非终态任务接回为 `paused` 且 `needs_reobserve`；恢复后必须先 `desktop_screenshot`，再写操作。
- **不重放**：已 `completed` 的步骤不会再次下发；`idempotency_key` 防止重复提交。
- **取消**：立即拒绝带该 `job_id` 的后续 Dispatch，并取消在飞等待。

任务记录与策略一并落盘（SQLite）。

## 执行器

真正截图、读元素、发键鼠的执行器默认是 trycua 开源的 [cua-driver](https://github.com/trycua/cua)（MIT），通过 MCP stdio 接入。Diana 只把它当执行层：观察版本、画面比对、单次确认、取证、应用范围、读写档位、人工接管和持久任务仍全部由 Diana 自己判断，驱动在这些检查通过后才收到指令。

- **按需下载**：完整包不附带驱动。主人第一次打开桌面控制时，Diana 在后台下载锁定版本（当前 `0.38.0`）的官方 Release 到 `<数据目录>/desktop/cua-driver/<版本>/`，先核对压缩包的 SHA-256，只解出 `cua-driver` 一个文件，再核对它的 SHA-256。每次启动前都会重新核对，被替换的文件不会被运行。下载走「系统更新」里设置的 GitHub 下载线路；失败后五分钟内不重试，控制台显示原因。
- **版本锁定**：驱动接口仍是 0.x，Diana 不跟随它自动升级，也关闭了它的更新检查。升级时更新 `model/desktopctl/cua_driver_install.go` 里的版本和两个哈希，并重跑 `TestLiveCUADriver*`。
- **不外发数据**：启动驱动时设置 `DO_NOT_TRACK=1` 等变量，关闭它默认开启的产品遥测。
- **权限归属**：驱动以 `mcp --direct` 运行在 Diana 的子进程里，macOS 的屏幕录制和辅助功能授权算在运行 Diana 的程序名下（终端或 Diana 应用），和原生 helper 一致，不需要另装 CuaDriver.app。
- **后台操作**：驱动优先用辅助功能接口操作窗口，文字输入、点击和普通按键多数不必切到前台，也不会和正在用这台电脑的人抢焦点。带 ⌘/⌃/⌥ 的组合键（如 ⌘S）后台收不到，会短暂把目标窗口切到前台按一次再切回；少数只接受前台输入的应用会拒绝或无效，结果仍需按截图核实。
- **坐标**：截图长边按窗口的点数封顶，截图像素就是窗口内逻辑坐标，`x/y` 直接取自截图。元素 ID 是驱动快照里的行号，每次读取都会刷新。
- **自备驱动**：`DIANA_CUA_DRIVER` 可指定一个绝对路径的 `cua-driver`，跳过下载（不做哈希核对，由部署者自己负责）。
- **回退**：下载失败且完整包带有原生 helper 时退回 helper，见 [`native/macos/README.md`](../native/macos/README.md)。

cua-driver 自带的 `bounded` 权限模式（只放行清单里的工具和应用）在不带 pid 时会拒绝枚举应用和窗口，接入它需要另起一路只取元数据的查询，目前未启用。

单测用假连接、`MockAdapter` 和按 cua-driver 0.38 实际输出构造的假驱动，不依赖显示器或系统权限；`TestLiveCUADriver*` 在设置环境变量时对真实驱动只读运行。

## 协议

帧：`hello` / `welcome` / `command` / `result` / `takeover` / `ping`。  
指令：`windows.list`、`window.screenshot`、`window.click`、`window.type`、`window.key`。  
载荷含 `job_id`、`observation`、`idempotency_key`。

原生 helper 写入要求目标窗口当前位于前台，发送前核对窗口和进程，点击坐标必须在窗口内；文本输入期间切换窗口会停止后续输入。cua-driver 按 pid 和窗口号定位，多数情况下在后台送达。两者都把键盘和鼠标事件发给目标进程。

## 本机连接

执行器的选择顺序：`DIANA_CUA_DRIVER` → 已下载并核对通过的 cua-driver → 开始下载（下载中等待，不中途换执行器）→ 下载失败时退回原生 helper（主程序同目录的 `desktopctl-helper`，源码部署可用 `DIANA_DESKTOP_HELPER` 指定绝对路径）。控制台的「执行器」一行显示当前用的是哪一个。服务定期恢复连接，每次操作前重新枚举窗口并复核应用范围；窗口 ID 被其他应用复用时不会沿用旧授权。文本经标准输入传递。取消、超时、人工接管或修改权限会中断仍在执行的 helper；已经发生的操作不能回滚。同一任务只允许一条在途指令。

HTTP 管理接口位于 `/api/desktop-control`，沿用控制台会话认证。启用和输入权限均默认关闭。系统授权需要用户在 macOS 系统设置中完成。

## 观察、确认和结果核实

本机执行器的写操作使用以下流程：

1. `desktop_screenshot` 或 `desktop_elements` 返回绑定当前窗口、应用和画面的 `observation`，有效期两分钟。元素 ID 仅用于本次元素观察；优先元素点击，坐标用于不提供元素的应用。
2. 模型提交具体操作与观察版本。执行器核对窗口画面，先返回 `confirmation_required`，这时尚未发送输入。控制台展示请求时的窗口截图、目标应用、元素或坐标、输入文字或按键。
3. 主人在控制台确认后，让 Diana 使用相同参数重试。确认只用一次，不能换文字、目标、观察版本或任务。使用任务幂等键时须给这次尝试新的键；原先的等待确认记录不会重放。
4. 执行器再次截图比较。画面变动、过期或连接重建会令旧观察失效。比较允许一处不超过 4×40 像素的变化（输入框里闪烁的光标），打一个字、勾一个复选框或多处同时变化都按画面已变处理；动画仍会使比较失败，需要重新观察。
5. 操作下发后附带新截图，`ok` 仅表示输入已经发送，`needs_verification` 提醒模型核实实际效果。若回看截图失败，保留输入已发送这一事实，返回核实错误，禁止盲目重复提交。

当前通用执行器不能可靠判断一个按钮是否代表发送、支付或删除，因此所有写操作逐条确认。任务的“继续任务”只解除任务等待，不代替具体操作授权。确认与观察只在内存保留；重启后必须重新观察并确认。

滚动的 `x/y` 指向窗口内区域，`delta_y` 正值向下，`delta_x` 正值向右，每轴最多 1000 像素。同一执行器的观察、核对、输入、回看串行执行，不能让其他任务在核对和输入之间插入操作。

任务审计新记录保存输入文本与截图的 SHA-256 摘要，不保存输入全文、原始截图或元素值。截图通过本次工具结果交给模型，待确认窗口截图只在内存中短期保留；这不改变聊天记录或模型提供商自己的留存行为。历史任务记录不会自动迁移或清除。

## 尚未覆盖的边界

本功能还不能视为完整的电脑任务沙箱：尚无文件选择、上传、导出路径的目录级强制约束，也没有通用截图敏感信息识别与遮罩。系统标记的安全输入框会要求人工接管，普通文本框中的验证码、密钥等仍需人工处理。实时画面流、完整的任务证据归档/保留策略和跨应用任务仍待完善。真实写入并保存产物的验收由 `TestLiveCUADriverWriteAndSave` 覆盖（「文本编辑」输入并 ⌘S 保存，读文件逐字核对），需设置 `DIANA_CUA_DRIVER_LIVE` 与 `DIANA_CUA_DRIVER_LIVE_WRITE=1` 在有权限的 Mac 上手动运行；[新增受限电脑操作能力：本地桌面执行器与持久任务管理（#948）](https://github.com/SuInk/Diana/issues/948) 保持开放。

原生实现使用 Apple 的 [Accessibility 元素读取 API](https://developer.apple.com/documentation/applicationservices/1462085-axuielementcopyattributevalue) 和 [Core Graphics 滚动事件 API](https://developer.apple.com/documentation/coregraphics/cgevent/init(scrollwheelevent2source:units:wheelcount:wheel1:wheel2:wheel3:))。
