# 工具目录合并方案（评估稿）

用户问工具是不是太多、太像了。本文先盘点生产上的工具目录和调用量，再逐簇读代码划分职责，最后用生产首步请求做新旧目录回放。结论：**合并能让主人那份目录从 102 行降到 58 行（约 1.14 万字降到 0.81 万字），首步选对率没有变化（94/132 对 92/132，在噪声内）**。所以合并的收益在目录体积和同簇互认错上，选对率不会因此变高。有两处合并会把请求引到 `capabilities` 上，不能照这一版（v1）落地。在 v1 的基础上，[通用化方案（v2）](#通用化方案v2)改成按「做什么」分工具，看图、发送这些操作的对象来源统一成一个媒体引用参数，再在注册表层用通用的工具族外观实现，主人目录可以降到 56 行。v2 回放的选对数和生产一样（都是 94/132）。之后的 v3 把 `history_media` 和 `reply_block` 拆回来单独保留，回放结果和 v2 没有差别；定稿按 v3，主人目录 58 行。本稿只给方案和数据，不改代码。

## 先纠正一个前提

原先的设想是：回复前先过一次「功能路由」（`routeReplyIntent`），路由按紧凑目录挑出工具子集交给 Agent。所以工具一像，最先出错的是路由。

代码和生产数据都不是这样：

- 路由只在 `!directAgentDecision` 时才跑，也就是没开完整 Agent 的时候（`model/assistant/runtime.go` 的 `routeReplyIntent` 调用处）。即使跑了，结果也只记日志（「Planner output is advisory only」），不会裁掉工具。
- 生产近 30 天 `app_logs` 里 `intent_recognition` 和 `visual_intent` 都是 **0 行**。生产开着完整 Agent，路由一次都没跑过。
- 真正挑工具的是主回复 Agent 本身。系统提示词里有一份按需目录（每个工具一行 `- 名字: 描述`），Agent 读这份目录，再用 `tools_execute` 或 `tool_search` 调用，见 [延迟工具调用协议](deferred-tools.md)。`001-agent_started.json` 里的 `metadata.available_tools`，就是 `registry.Catalog(180)` 算出来的这份目录。

所以本次评估的对象是 **Agent 第一步的工具选择**。「路由选对率」在生产上没有可比的东西：路由请求没有落盘，只能从首步请求近似重建。重建版大约要 200 万 token，这次没有跑，等决定后再说。

## 盘点

数据来源：

- 调用量：miku 生产库 `app_logs`，`action='agent_tool'`，时间 `created_at >= '2026-10-04'`，只取 `phase=tool_completed` 一行（每次调用写 started 和 completed 两行）。`tools_load` 已归一化成 `tool_search`。合计 11,010 次调用。
- 目录：`debug-traces` 只保留最近 3 天（10-31～11-02），共 2,193 次 Agent 运行。

目录大小：

| 发言者 | 运行次数 | 目录行数 |
|---|---|---|
| 群友 | 1,845 | 39–47 |
| 主人（含管理员，`toolScope()` 把管理员当主人） | 348 | 94–103 |

「对普通群友不进目录」现在已经成立：群友只拿 `relationship.go` 里的白名单。**要瘦的是主人那份目录。**

首步分布（2,193 次运行）：

- 75% 不调工具。
- 调工具的里面，第一个真实工具最常见的是 `web_search` 155 次、`subtask` 101 次、`capabilities` 43 次。

顺带发现一个问题（未查根因）：主人目录里有 4 个工具各出现两次：`github`、`cross_session_message`、`subscription`、`runtime_model`。一次在稳定前缀，一次在发言者尾部。前三个属于 `speakerVariantTools`。`runtime_model` 是在 `Retain` 之后才注册的。

### 各簇调用量（30 天）

「群友目录 / 主人目录」两列是该工具出现在目录里的运行占比。

| 簇 | 工具 | 30 天调用 | 群友目录 | 主人目录 |
|---|---|---|---|---|
| 看图 | `history_media` | 387 | 100% | 100% |
| | `remote_image`（含 `view_avatar` 动作） | 56 | 100% | 100% |
| | `image_source` | 13 | 100% | 100% |
| | `view_image` | 5 | — | 100% |
| | `ai_image_detect` | 2 | 100% | 100% |
| | `match_avatar` | 0 | 99% | 80% |
| | `telegram_images` | 0 | 100% | 100% |
| 出图/渲染 | `image` | 120 | 100% | 100% |
| | `render_media` | 25 | 94% | 85% |
| | `render` | 16 | 100% | 100% |
| | `webpage_screenshot` | 14 | 100% | 100% |
| | `image_fix` | 4 | 100% | 100% |
| | `mcp_media` | 0 | 100% | 100% |
| | `plugin_media` | 0 | 40% | 23% |
| 文件（除两项外仅主人） | `send_file` | 39 | 94% | 85% |
| | `send_attachment` | 17 | — | 100% |
| | `list_files` | 16 | — | 100% |
| | `save_to_workspace` | 10 | 36% | 100% |
| | `read_file` | 8 | — | 100% |
| | `manage_files` | 7 | — | 100% |
| | `grep` | 6 | — | 100% |
| | `write_file` | 5 | — | 100% |
| | `find_files` | 5 | — | 100% |
| | `extract_archive` | 2 | — | 100% |
| | `edit_file` | 0 | — | 100% |
| 浏览器 | `browser_render`（公网沙箱，常驻推荐） | 2,253 | 100% | 100% |
| | 其余 13 个 `browser_*` | 合计 96 | — | 100% |
| 自省 | `capabilities` | 355 | 100% | 100% |
| | `runtime_model` | 115 | 100% | 100% |
| | `platform` | 71 | 100% | 100% |
| | `bot_config` | 55 | 100% | 100% |
| | `version` | 51 | 100% | 100% |
| | `llm_config` | 38 | — | 100% |
| | `llm_usage` | 14 | — | 100% |
| | `host_stats` | 12 | — | 100% |
| | `list_capabilities` | 6 | — | 100% |
| | `console_config` / `runtime_config` | 0 / 0 | — | 7% / 19% |
| 安装管理 | `install_package` | 2 | — | 100% |
| | `plugin_manage` | 1 | — | 100% |
| | `mcp_install` / `mcp_uninstall` / `mcp_set_enabled` / `install_skill` / `uninstall_skill` / `extension_access` | 全 0 | — | 100% |
| 身份 | `identity_check` | 9 | 100% | 100% |
| | `bot_markers` | 8 | — | 100% |
| | `identity_links` / `cross_platform` / `bot_admins` | 全 0 | — | 100% |
| 任务 | `subtask` | 289 | 100% | 100% |
| | `subscription` | 56 | 100% | 100% |
| | `thread_state` | 50 | 100% | 100% |
| | `reminder` | 26 | 100% | 100% |
| | `tasks` | 21 | 100% | 100% |
| | `event_trigger` | 8 | 100% | 100% |
| | `coding` | 7 | — | 45% |
| | `background_task` | 0 | — | 100% |
| luckin MCP | 8 个 `mcp__luckin__*` | 1 | — | 100% |

## 逐簇结论

判断标准：

- **合并成带 `action` 的工具**：只在同簇工具共用同一类输入，并且模型分不清的时候合并。仓库已有先例：`subscription` 的 kind × operation，`manage_files` 按权限裁 enum，`remote_image` 和 `plugin_media` 用 switch 分派。
- **改写第一句**：只是描述写得像，按 [工具描述写法](tool-descriptions.md) 用动宾句写出分工。
- **收进按需**：调用量接近 0，且只有主人能用。目录里只留一行，或者按 MCP 服务折成一行。

### 看图

- `image_source` + `ai_image_detect` + `match_avatar` → **`image_lookup`**，`action=source|ai_detect|avatar`。三者都只吃「消息 ID + 第几张图」，问的都是「这张图的来历」。30 天合计 15 次，现在要占 3 行目录。
- `history_media`（看画面）和 `remote_image`（网上图片、头像）的职责清楚，**不动**。`remote_image` 的 `view_avatar` 和 `match_avatar` 名字像，但一个是看某人的头像，一个是认图里是谁的头像，合进 `image_lookup` 后就不会混了。
- `telegram_images` 在所有平台都注册，`Run` 里才拒掉非 Telegram（`telegram_images_tool.go:40`）。**在非 Telegram 会话里不该进目录**，这是纯收益：QQ 群每轮少一行，也不会误调。
- `view_image` 只看工作目录里的图，归到文件簇。

### 出图/渲染

- `render` + `render_media` → **`render`**，加 `output=image|video|gif`。两者共用净化和沙箱，`render_media` 是 `render` 的超集，描述也几乎重复。
- `webpage_screenshot`（公网、无登录态）和 `browser_screenshot`（已登录的标签页）**改写第一句**，写清谁该用哪个。回放里有一条从 `browser_render` 改对成了 `webpage_screenshot`。
- `image`、`image_fix`、`mcp_media`、`plugin_media`、`video` 各有专门的入口，**不动**。

### 文件（主人）

- `send_file` + `send_attachment` → **`send_file`**，`content` 或 `path` 二选一，二进制仅主人。两者现在只差在「文本还是工作目录里任意文件」，模型常要在两个名字之间猜。
- `list_files`、`find_files`、`grep`、`read_file`、`view_image` → **`files`**，只读，`action=list|find|grep|read|view`。
- `write_file`、`edit_file`、`manage_files`、`extract_archive` → **`file_edit`**，写操作，enum 按 `FileWriteEnabled` 裁剪，沿用 `manage_files.actions()` 的做法。
- `save_to_workspace` 群友也能用，路径受限，**单独保留**。
- 回放里 A 臂有 `find_files` 和 `list_files` 互认错。合并后这类错误消失了，但 `action` 选错（`find` 和 `list`）的情况还在。

### 浏览器（主人）

- 13 个交互工具（`open`、`text`、`click`、`type`、`press_key`、`select`、`scroll`、`tabs`、`navigate`、`wait`、`eval`、`screenshot`、`handoff`）→ **`browser`**，带 `action`。30 天合计 96 次，却占 13 行目录，是体积收益最大的一簇。群友只能用个人浏览器那几个动作，按发言者裁 enum。
- `browser_render`（2,253 次，公网沙箱只读）**保持独立**，和 `browser` 在第一句里写清分工。
- 代码里还有 `browser_ext_*`（扩展驱动用户自己的浏览器）和 14 个 `desktop_*`，生产没有配。之后可以按同一个 `browser` 的 backend 处理，这次没有评估。

### 自省

- `capabilities` + `list_capabilities` 可以合（`action=search|list`，`list` 仅主人）。但 **`capabilities` 的第一句必须保持原文**：回放里我把它改成了「查 Diana 自己能做什么」，结果它从别的工具手里抢走了 5 次（见下文）。
- `version` + `runtime_model` + `host_stats` → `runtime_info`：**不推荐**。回放里「你什么版本」从 `version` 被抢到 `capabilities`（2 次）。`runtime_model` 按族算没有掉，但工具名本身就是最强的路由信号，`version` 和 `runtime_model` 调用量都不小（51 和 115），名字应该保留。
- `llm_config` 和 `bot_config` 是改配置，不是自省，**改写第一句**：「切换和启停模型」对「读取和修改群/机器人设置」。`console_config`、`runtime_config` 只在私聊或控制台出现，**不动**。
- `platform` 是群资料和群管操作，不属于自省，**不动**。

### 安装管理（主人，30 天合计 3 次）

- `mcp_install`、`mcp_uninstall`、`mcp_set_enabled`、`install_skill`、`uninstall_skill`、`extension_access` → **`extensions`**，确认码流程不变。6 个工具 30 天都是 0 次调用，却占 6 行目录。
- `plugin_manage`（机器人级插件开关）和 `install_package`（npm）**不动**。
- MCP 工具在目录里**按服务折成一行**，例如 `- mcp__luckin__*: …8 个工具，先 tool_search`。完整定义仍可以按名字 `tool_search` 取到。luckin 一个服务就占 8 行，30 天只用过 1 次。

### 身份（主人，30 天合计 17 次）

- `identity_check`（大家都能用，有自省预算）、`identity_links`、`bot_markers`、`bot_admins`、`cross_platform` 的对象各不相同：分别是账号核验、跨平台关联、机器人名单、管理员名单、跨机器人会话。**不合并**。
- 名单类的三个（`identity_links`、`bot_markers`、`bot_admins`）调用接近 0，适合一并收进按需，目录只留一行。

### 任务

- `reminder`、`event_trigger`、`subscription`、`tasks` → **`tasks`**，带 `kind=reminder|event|schedule|rss|github`，`operation=list` 不填 kind 时列出全部。这是把 `subscription` 已有的做法再推一步。回放里这一簇 6 条前后都是 5 对，没有变差。额度规则按条数计、和 kind 无关，可以原样保留，见 `diana-schedule-quota-rules`。
- `subtask`（同步隔离 Worker）、`background_task`（主人，多轮后台任务）、`coding`（外部编码 CLI）、`thread_state`（多轮状态）的职责不同，**不动**。只把 `background_task` 第一句里和 `subtask` 的分工写清楚。

## 回放

### 方法

- **样本**：从最近 3 天 2,193 次运行中，按「生产第一个真实工具 × 是否主人」分层抽样，得到 132 条第一步请求：
  - 主人 78 条，群友 54 条。
  - 首个工具在上述簇里的 52 条，其他工具 52 条，不调工具 28 条。
  - 请求取 `00x-reply.json` 里 `metadata.request` 的原样内容。
- **两臂**：
  - **A 臂**：生产原样。
  - **B 臂**：按合并稿改写三处。一是系统提示词里的目录行，合并组只留一行新描述；二是已加载契约（`tools_execute` 可直接调的那段 JSON），换成合并后的 schema；三是规则和其他描述里点到旧工具名的地方，改成新名。合并稿包含上面的 10 组合并、2 处改写，以及 `telegram_images` 按平台剔除、luckin 折行。
- **模型**：gpt-6.1-sol，经 sub2api 的 `/v1/chat/completions`。每臂每条跑 1 次，`max_tokens` 600。实际输入 1,580 万 token（事先估约 1,500 万）。
- **判分**：取模型第一步的动作，`tools_execute` 的 `name`、`tool_search` 的第一个 name，或者直接回复。和生产里实际执行的第一个真实工具比，跳过 `tool_search`。比较时两边都映射到合并后的「族」，A 臂另外记一个精确名字的结果。
  - 注意：生产第一步用的模型是 terra 或 swe-2，回放用的是 sol。所以「选对」的意思是和生产实际走的路一致，不是人工标注的正确答案。

### 结果

| 分组 | 条数 | A 精确 | A 按族 | B 按族 | A/B 一致 |
|---|---|---|---|---|---|
| 全部 | 132 | 90 | **94** | **92** | 115 |
| 簇内工具 | 52 | 34 | 38 | 37 | 45 |
| 其他工具 | 52 | 31 | 31 | 30 | 42 |
| 不该调工具 | 28 | 25 | 25 | 25 | 28 |
| 主人 | 78 | 49 | 52 | 50 | 65 |
| 群友 | 54 | 41 | 42 | 42 | 50 |

按簇（按族计对的条数）：

| 簇 | 条数 | A | B |
|---|---|---|---|
| `capabilities` | 10 | 7 | 8 |
| `runtime_info`（`version`、`runtime_model`、`host_stats`） | 17 | 12 | 10 |
| `files` | 5 | 3 | 2 |
| `browser` | 5 | 3 | 3 |
| `tasks` | 6 | 5 | 5 |
| `render` | 3 | 3 | 3 |
| `llm_config`（只改写） | 5 | 5 | 5 |
| `webpage_screenshot`（只改写） | 1 | 0 | 1 |

### 读法

- **总体持平。** 94 对 92，相差 2 条，在噪声之内（此前工具描述回放实测，同一组定义跑两次相差 ±5/120）。不该调工具的 28 条两边都是 25 条对，合并没有带来多余的调用。
- **A 臂的同簇互认错是真的**：A 精确 90 条，按族 94 条，差出来的 4 条都是在同簇里选错了兄弟工具（`find_files`/`list_files`、`capabilities`/`runtime_model`）。合并在名字层面消掉了这类错误，但它会变成 `action` 选错：B 臂合并工具的 `action` 对了 16/23，其中群友 9/9，主人 7/14。
- **B 臂的回归集中在 `capabilities`。** B 错选的去向里，`capabilities` 从 3 次涨到 8 次，抢走了「什么版本」2 次、`host_access`、`list_files` 和 `web_search` 各 1 次。原因有两个：一是我把它的第一句改得更泛了；二是 `version` 不再是独立名字，「版本」这个词就没有专门的名字来接。和 `github` 那次一样，第一句写得越宽，抢来的请求越多。
- **新名带括号的写法会被照抄。** 我在规则文字里把旧名改写成「`runtime_info（action=model）`」，B 臂有 11 次模型把整串括号写进了 `tools_execute` 的 `name`。判分时把它们算作选对，但上线就会走协议修复。落地时规则文字要写成「`runtime_info` 的 model」，或者让 `tools_execute` 认这种写法。
- **体积**：主人目录从 102 行降到 58 行，约 1.14 万字降到 0.81 万字，减 29%。群友目录从 40 行降到 33 行，约 0.57 万字降到 0.50 万字，减 11%。目录在缓存前缀里，所以省下的主要是首轮未命中和缓存失效时的 token。

## 通用化方案（v2）

上面那一版是「同簇几个工具合成一个」，合完仍按来源和场景分工具，比如看历史图、看网图、看头像、看工作目录图片各有入口。v2 改成按「做什么」分工具，把「对象从哪来」统一成一个参数。工具数量因此变少，以后新加一种来源也不用再加工具。

### 统一的媒体引用

现在看图和发送工具的入参，本质上都是同几种来源，只是每个工具各写各的字段：

- `history_media`、`image_source`、`ai_image_detect` 用 `message_id`。
- `telegram_images` 用 `message_ids`。
- `remote_image` 用 `url` 和 `avatar_source`。
- `view_image`、`send_attachment` 用 `path`。
- `mcp_media` 用 `media_id=mcpm_…`。
- `plugin_media` 用 `media_id`。

把它们统一成一个引用字符串：

| 写法 | 指向 | 现在由谁读 |
|---|---|---|
| `msg:<消息ID>[#第几个]` | 历史消息里的图片、视频或文件 | `history_media`、`telegram_images` |
| `url:<直链>` | 公网图片或视频 | `remote_image` view、`plugin_media` view_video |
| `avatar:bot\|sender\|group\|member:<ID>` | 头像 | `remote_image` view_avatar |
| `path:<相对路径>` | 工作目录文件 | `view_image`、`send_attachment` |
| `media:<ID>` | `plugin_media` resolve 或 MCP 结果暂存的媒体 | `plugin_media` send、`mcp_media` |

解析放在一处（`resolveMediaRef`），内部调用现有的加载函数：`avatarIdentityImageURLs`、`fetchImageAsDataURL`、`agent.LookupMCPMedia` 等。各来源原有的权限和额度检查原样保留，例如群友只能读本群附件、每轮最多 8 张远程图。

### 通用工具

| 通用工具 | 吸收 | 说明 |
|---|---|---|
| `view` | `view_image`、`remote_image` 的 view/view_avatar | 把任意引用的画面附给模型看。v3 起不再吸收 `history_media`，原因见 v3 回放。 |
| `send` | `send_file`、`send_attachment`、`mcp_media`、`telegram_images`、`remote_image` 的 send/send_album | 发任意引用。多张图自动组成相册，Telegram 复用 file_id 是平台内部的优化。写好的文本用 `content`+`filename` 打包成文件。 |
| `image_lookup` | `image_source`、`ai_image_detect`、`match_avatar` | 查一张图的出处、是否 AI 生成、是谁的头像。 |
| `render` | `render`、`render_media` | 加 `output=image\|video\|gif`。 |
| `files` | `list_files`、`find_files`、`grep`、`read_file` | 只读工作目录。 |
| `file_edit` | `write_file`、`edit_file`、`manage_files`、`extract_archive`、`save_to_workspace` | 写工作目录。`save` 接受媒体引用，群友只能 `save` 本群附件。 |
| `browser` | 13 个 `browser_*` 交互工具 | 带登录态的浏览器。`browser_render` 仍单独保留。 |
| `extensions` | MCP 和 Skill 的安装、卸载、启停，`extension_access` | 确认码流程不变。 |
| `tasks` | `reminder`、`event_trigger`、`subscription`、`tasks` | `kind` × `operation`，额度规则不变。 |
| `roster` | `bot_admins`、`bot_markers`、`identity_links` | 维护只有主人能改的人员名单：管理员、机器人标记、跨平台关联。`reply_block` 群管也能用，v3 起单独保留。 |
| `capabilities` | （暂不吸收） | 原想并入 `list_capabilities`，第一句沿用原文、句尾补一句 `action=list` 仅主人。v2 回放里这一行仍然多抢了请求，所以先保持生产原样，主人目录因此多 1 行（56 行）。 |

v1 回放说明工具名本身就是很强的选择信号，所以下面这些**保留原名**：

- `version`、`runtime_model`、`host_stats`
- `llm_config`、`bot_config`
- `web_search`、`browser_render`
- `chat_history`、`memory`、`notebook`、`relationship`

MCP 工具在目录里**按服务折成一行**（`mcp__<服务>__*`），这是通用机制，对所有 MCP 服务都生效。

### 实现：注册表层的工具族

不重写现有工具，在 `agent.ToolRegistry` 上加一层通用的「工具族」外观（`ToolFamily`）：

- **声明**：一个工具族只声明三样东西：对外的名字、第一句描述、`action` 到成员工具的映射。成员工具的实现、`Run`、单元测试都不动。
- **schema**：由成员的 schema 合并生成，外加一个 `action` enum。enum 只列出本次注册表里实际存在的成员，所以 `Retain`、`DenyTools` 和插件开关对成员的裁剪会自动反映到 enum 上。群友看到的 `file_edit` 只有 `save`，`browser` 只有个人浏览器那几个动作。
- **执行**：按 `action` 转给成员工具。`IntrospectionTool` 也按 action 委托给成员。日志、最近调用记忆、额度统计里同时记族名和成员名，所以本文的调用量统计以后照样能做。
- **目录**：`Catalog()` 和系统提示词里的按需目录，只出现族名。

### 体积

用生产首步请求改写后统计：

| 目录 | 生产 | v1 | v2 |
|---|---|---|---|
| 主人，行数 | 102 | 58 | **56**（`capabilities` 不合并时） |
| 主人，字数 | 1.14 万 | 0.81 万 | **0.77 万**（减 32%） |
| 群友，行数 | 40 | 33 | 33 |

### v2 回放

方法和 v1 相同，A 臂直接复用 v1 的结果，只重跑 B 臂，输入 770 万 token。规则文字里提到旧工具名的地方，这次改写成「`files` 的 list」，不再用括号。

| 分组 | 条数 | 生产（A 按族） | v1 | v2 |
|---|---|---|---|---|
| 全部 | 132 | 94 | 92 | **94** |
| 簇内工具（按 v2 的分族） | 40 | 31 | — | 31 |
| 其他工具 | 64 | 38 | — | 38 |
| 不该调工具 | 28 | 25 | 25 | 25 |
| 主人 | 78 | 52 | 50 | 51 |
| 群友 | 54 | 42 | 42 | 43 |

按工具族：

| 族 | 条数 | A | B |
|---|---|---|---|
| `view` | 4 | 4 | 4 |
| `capabilities` | 10 | 7 | 8 |
| `files` | 5 | 3 | 2 |
| `file_edit` | 1 | 1 | 0 |
| `browser` | 5 | 3 | 3 |
| `tasks` | 6 | 5 | 5 |
| `render` | 3 | 3 | 3 |

- **总体和生产持平**，比 v1 多对 2 条。`view` 吸收了高频的 `history_media`，在这批样本里没有掉分：4 条都对，不该调工具的 28 条也没有因此多调。不过样本里 `history_media` 只有 4 条，覆盖还不够。
- **`capabilities` 仍然多抢了一些**：错选到它的次数，A 是 3，B 是 5（v1 是 8）。「你什么版本」2 条在 v2 里 `version` 已经保留原名，还是被 `capabilities` 抢走了。所以起因不在 `version` 的名字，而在 `capabilities` 这一行本身：v2 在句尾加了一句 `action=list`，或者并入 `list_capabilities` 改变了它的含义。`list_capabilities` 先不要并进来，保持原样。（v3 把 `capabilities` 恢复成生产原文后，仍有 1 条「什么版本」被它抢走；生产 A 臂自己也有 3 次错选到它。可见这部分有不少是噪声和模型本身的倾向，不全是合并造成的。）
- **旧名改写的格式还是会被照抄进工具名**：v1 写成「`runtime_info（action=model）`」，抄了 11 次；v2 写成「`browser` 的 open」，抄了 4 次（`browser 的 open`、`files 的 list`、`files 的 find`、`file_edit 的 save`）。动作本身选对了，只是名字不合法。落地时有两种做法：一是规则文字里只写族名，不带动作；二是让 `tools_execute` 认这种写法，把「名字 + 空格/的/括号 + 动作」拆成 `name` 和 `action`。第二种对所有工具族都有效，也更通用。
- **低频族的样本太少**：`files` 和 `file_edit` 合计 6 条，`find_files` 被选成了 `file_edit` 的 save。这一族的分流靠这批样本判断不了，要用合成探针补。

### v3：`history_media` 和 `reply_block` 单独保留

v2 是按「对象从哪来」合并的，但有两个工具硬塞进去并不合适：

- **`history_media`**：`view_image`、`remote_image` 都是把画面附给模型看。它不一样，默认是把问题交给视觉模型、只拿回文字答案；它还能按页读 PDF 和文件，并有「摘要够用时别调」这类只对历史消息成立的规则。它在 30 天里被调用 387 次，是群友高频用的工具，在常驻推荐里有完整描述；合并却只省下 1 行目录。
- **`reply_block`**：群管也能用，权限和另外三份只有主人能改的名单不一样。

v3 把这两个拆回来单独保留，`roster` 只管主人的三份名单，`view` 的描述改成「把画面直接附给你看，聊天记录里的图用 `history_media`」，`capabilities` 恢复生产原文。回放分两部分：

| 样本 | 条数 | 生产 | v2 | v3 |
|---|---|---|---|---|
| 受影响的 v2 样本（不该调工具 28、`capabilities` 10，以及看图、`version`、`bot_config` 相关） | 46 | 38 | 37 | 37 |
| 其中不该调工具 | 28 | 25 | 25 | 24 |
| 补抽：生产首个工具是 `history_media` | 8 | 3 | 5 | 5 |
| 补抽：生产首个工具是 `remote_image` | 4 | 2 | 2 | 2 |

- **v2 和 v3 在选工具上完全一样。** 补抽的 12 条里，v2 选 `view` 的地方，v3 恰好都选了 `history_media`，没有一条去向不同。也就是说，是否把 `history_media` 并进 `view`，靠选对率分不出好坏。两版都比生产多对 2 条：生产在这两条上先派了 `subtask` 或去查 `capabilities`，新目录下直接去看图了。
- 所以这一项按语义和风险来定：**保留 `history_media` 和 `reply_block`**。代价只是 2 行目录，主人目录 58 行，换来的是参数和规则不混在一起，高频工具的描述也不用改。
- 这一轮输入约 550 万 token。

## 推荐顺序

每步单独开 PR，各自回放：

1. **两样不改契约的**：非 Telegram 会话不进 `telegram_images`；MCP 工具按服务折成一行目录。同时**去掉主人目录里的重复行**：`github`、`cross_session_message`、`subscription`、`runtime_model` 各出现两次，要先查清前缀和尾部为什么都放了它们。
2. **通用机制本身**：`ToolFamily` 外观和 `resolveMediaRef`。先只接只有主人用的工具族：`browser`、`extensions`、`files`、`file_edit`、`roster`。这批低频，行数收益最大，可以验证外观层的权限裁剪和日志。
3. **`view` 和 `send`**：群友可见。`view` 只吸收「把画面附给模型看」的两个工具，`history_media` 保持原样（见 v3）。`send` 统一了好几种来源，还要用合成探针把每种引用都测一遍。
4. **`image_lookup`、`render`、`tasks`**：v1 回放里这三组前后持平。`tasks` 涉及额度、提示词规则和数据迁移，放在最后。
5. **不做**：把 `version`、`runtime_model`、`host_stats` 合成 `runtime_info`；改写 `capabilities` 的第一句。

通用机制里同时让 `tools_execute` 认「族名 + 动作」的写法（见 v2 回放），`capabilities` 那一行保持生产原文，暂不并入 `list_capabilities`。

每一步都要用本文的方法回放：新旧定义重写同一批生产首步请求，除了看总比例，还要逐条看分歧样本。被 `capabilities` 这类宽描述工具抢走的样本，按 [tool-descriptions.md](tool-descriptions.md) 的做法重复 10 次，并逐项换回旧定义做消融。

## 这次没做的

- **近似路由回放**：从首步请求重建 `current_text`、`recent_messages` 和 `available_tools`，比较路由选中集合对生产实际工具的召回，约 200 万 token。生产不跑路由，这一项只在打算重新启用路由时才有意义。
- **合成探针**：每个合并工具造 1～2 句典型请求（「这图出自哪部番」「明早 8 点提醒我」），专门测同簇分流，约 220 万 token。生产 3 天里簇内低频工具的样本太少（`image_source`、`ai_image_detect`、`match_avatar` 一条都没抽到），这一项能补上这个空白。
- 回放脚本和抽样结果在本机临时目录，没有进仓库：样本含真实聊天内容，脚本也依赖生产 trace 的布局。复现的方法以本文「方法」一节为准。
