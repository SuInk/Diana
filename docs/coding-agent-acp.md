# ACP 编码后端（设计稿）

> 状态：P1 已实现（会话进程、日志事件、三种审批模式、取消和超时、配置页）。P2、P3 未做。原先的待定问题见文末「已定事项」。

编码代理现在只认两种 CLI：Claude Code（stream-json）和 Codex（`exec --json`），其余只能走「自定义命令」，按日志尾巴汇报，没有结构化进度、续跑和聊天审批。[Agent Client Protocol](https://agentclientprotocol.com)（ACP）是编辑器和编码代理之间的 JSON-RPC 协议，注册表里已有 60 多个代理：Gemini CLI、GitHub Copilot、Qwen Code、opencode、goose、Kimi、Cursor 等，Claude Code 和 Codex 也有官方适配器。接一次 ACP，这些代理就都能用。

## 目标与边界

要做到：

- 新增后端 `acp`：填一条启动命令就能接任意 ACP 代理，拿到结构化进度、最终结果、聊天审批，支持的代理能续跑。
- **任务照样扛得住 Diana 重启**。这是编码代理现有设计里最值钱的一条，不能为了 ACP 丢掉。
- 审批、取消、超时、汇报、受理消息、工作区锁、运行日志全部沿用现有机制。

不做：

- 不替换现有的 `claude` / `codex` 原生后端。它们已经稳定、不依赖 Node 适配器；Codex 的 `app-server` 是另一套协议，也不在这里。
- 不让代理读写 Diana 的文件系统或终端（ACP 的 `fs/*`、`terminal/*` 客户端能力一律不声明），代理用自己的工具在工作区里干活，和现在一样。
- 不对非主人开放。

## 为什么不能直接接

ACP 的传输是 stdio：客户端把代理拉成子进程，一直占着它的 stdin/stdout。管道一断这一轮就没了，规范里没有「重新挂到一个正在跑的回合上」。调研过的实现都是这样：

| 实现 | 客户端退出后正在跑的回合 |
|---|---|
| Zed | 连接销毁时 `child.kill()`，回合结束；重开后用 `session/load` 恢复对话 |
| OpenClaw | 网关启动时把上次留下的适配器进程全部杀掉，任务 5 分钟后记为 lost，靠 `session/load` 续 |
| acpx（OpenClaw 拆出的 CLI） | 起一个 detached 的队列进程占住连接，事件写日志，客户端经 unix socket 重新接上。**但这种模式下审批只能用静态策略** |

Diana 要的是 acpx 的形状加上聊天审批：扛重启、又能在群里问主人。所以不直接依赖 acpx，自己做那个中间进程。

## 总体结构

```
Diana ──setsid 拉起──▶ diana __coding-acp <spec.json>   ← Diana 视角的「CLI」
                           │  stdio JSON-RPC（ACP v1）
                           ▼
                        ACP 代理（gemini --acp / opencode acp / claude-agent-acp …）
```

新增内部子命令 `__coding-acp`（和审批 hook 的 `__coding-approve` 同一个套路：回调 Diana 自己的可执行文件，不生成脚本）。它是 ACP 客户端，也是这个任务在 Diana 眼里的进程：

- Diana 按现有流程把它当编码 CLI 启动：`setsid` 脱离进程组、stdout 写 `.jobs/<任务号>.log`、记 PID。
- 接回、判活、超时、取消（对进程组发 SIGTERM，5 秒后 SIGKILL）、按日志收尾、汇报，**Diana 侧一行不用改**。
- 代理是会话进程的子进程，在同一个进程组里，取消和超时会一并收掉。会话进程自己崩了，代理的 stdin 随之关闭、跟着退出。

启动参数写进 `.jobs/<任务号>.acp.json`，不走 argv：指令可能很长，argv 有长度上限；而且放进文件后，这份参数本身就留在任务目录里可查。

## 会话进程的一次运行

1. 读 `spec.json`：代理命令和参数、环境变量、工作区目录、指令、要续的会话 ID、审批策略文件路径（沿用 hook 用的那份）。
2. 拉起代理，发 `initialize`：`protocolVersion: 1`，`clientCapabilities` 里 `fs` 和 `terminal` 都不声明。记下代理回的能力（`loadSession`、`sessionCapabilities.resume`）和 `authMethods`。
3. 代理要求认证（`session/new` 回 `-32000`）时，只按名字挑不需要人在场的方式试 `authenticate`（方法 ID 含 key、env、token，排除 oauth、login、browser），成功后重建会话；都不行就失败，错误里列出代理给的登录方式。不在无人值守的会话里走交互登录。
4. 建会话：新任务发 `session/new`（`cwd` 为工作区，`mcpServers: []`）；续跑优先 `session/resume`（不回放历史），不支持再用 `session/load`，回放期间的 `session/update` 丢弃，不写进日志。
5. 发 `session/prompt`，内容是指令文本。期间把 `session/update` 规整后逐行写进日志（见下节），`session/request_permission` 按审批映射处理。
6. `session/prompt` 返回 `stopReason` 后写结果行，关代理 stdin，等它退出（2 秒后强杀），会话进程退出。

退出码：`end_turn` 为 0；`refusal`、`max_tokens`、`max_turn_requests` 也为 0，但结果行标 `is_error`，汇报按失败说明原因；协议错误、代理崩溃为 1；被取消为 130。

**取消与超时**：会话进程收到 SIGTERM 后发 `session/cancel`；有正在等主人确认的审批，按规范回 `cancelled`；最多等 3 秒拿到 `stopReason: cancelled`，写结果行后退出。3 秒内收不了尾，Diana 那边 5 秒后的 SIGKILL 会连代理一起收掉。

## 日志格式

会话进程写的是规整过的事件，不是 ACP 原文。原文里有大量流式分片（一个字一个 `agent_message_chunk`），直接写会把日志撑大、也让解析跟着协议细节走。每行一个 JSON，`type` 以 `acp.` 开头，`parseCodingLog` 加对应分支：

| 事件 | 字段 | 来源 | 进度里显示 |
|---|---|---|---|
| `acp.session` | `session_id`、`agent`、`can_load`、`can_resume` | `initialize` + `session/new` | 会话已建立 |
| `acp.tool` | `id`、`title`、`kind`、`status`、`detail` | `tool_call` / `tool_call_update`（只在状态变化时写） | `执行命令：…` / `编辑：路径` |
| `acp.message` | `text` | 连续的 `agent_message_chunk` 攒成一段，遇到工具调用或回合结束时落一行 | 前 200 字 |
| `acp.plan` | `entries` | `plan` | 计划：n 项，完成 m 项 |
| `acp.permission` | `tool`、`detail`、`decision` | 审批映射 | 等待确认 / 已放行 / 已拒绝 |
| `acp.result` | `stop_reason`、`result`、`is_error` | `session/prompt` 返回 | 不显示，作为最终结果 |
| `acp.error` | `message` | 协议错误、认证失败 | 错误原文 |

`agent_thought_chunk` 不落日志：那是模型的思考过程，既不是进度也不是结论。

**最终结果取哪段**：最后一次工具调用之后的那些 `acp.message` 拼起来；整轮没调过工具就取全部 `acp.message`。代理的结论总是在收工前最后说，前面边干边说的话属于进度。

**调试原文**：`spec` 里开了调试时，另写 `.jobs/<任务号>.acp.ndjson` 存 ACP 原文，上限 20 MB，超了就停写。默认关闭。

## 审批映射

代理什么时候发 `session/request_permission` 由代理自己的权限模式决定，Diana 只能回答它问的。映射规则：

- 按现有的审批模式判断要不要问主人。从 `toolCall` 取 `kind`（`execute`/`edit`/`delete`/`move`/`read`/`fetch`…）、`title`、`rawInput` 拼出操作说明，喂给现有的 `codingHookNeedsApproval`：
  - 「不确认」：直接选 `allow_once`。
  - 「危险操作」：`execute` 类的命令命中危险清单才问，其余选 `allow_once`。
  - 「所有写操作」：`edit`/`delete`/`move`/`execute` 都问。
- 要问时复用 hook 的 `requestCodingApproval`：写审批信箱，Diana 在聊天里发放行码、常驻放行码、拒绝码，等主人回。等待期间 Diana 重启也不丢，这点和 hook 一样。
- 主人回放行码 → 选 `allow_once`；回常驻放行码 → 记进 Diana 自己的常驻放行清单，再选 `allow_once`；回拒绝码或超时 → 选 `reject_once`。
- **不把 `allow_always` 转给代理。** 「以后都同意」只记在 Diana 这一处，`approvals clear` 才收得回来；代理那边记一份，Diana 就既看不见也清不掉。
- 代理给的选项里没有对应种类时，按「允许类取第一个 allow、拒绝类取第一个 reject」兜底；两类都没有就回 `cancelled`。

能审到多少取决于代理把哪些操作交出来问。建会话后，如果代理报了可选模式，会话进程切到「每次都问」的那档（例如 Claude 适配器的 `default`）。各代理的实际覆盖面要在接入时逐个实测，写进下面的预置表，不做笼统承诺。

## 续跑

`acp.session` 记下会话 ID 以及能否 load/resume，存进任务记录。`followup` 在 `can_load || can_resume` 时可用：起一个新的会话进程，`spec` 里带上原会话 ID，其余沿用原任务的代理配置和工作区（与现有「配置变了不能续」的规则一致，指纹多算进代理命令和参数）。

## 配置

后端下拉新增「ACP 代理」，每份代理配置多两项：

- **ACP 代理**：预置列表加「自定义命令」。预置取自 [ACP 注册表](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json)，首批只放实测过的几家。2026-09-27 注册表里的启动方式：

  | 代理 | 分发 | 启动 |
  |---|---|---|
  | Gemini CLI | npm `@google/gemini-cli` | `gemini --acp` |
  | GitHub Copilot | npm `@github/copilot` | `copilot --acp` |
  | Qwen Code | npm `@qwen-code/qwen-code` | `qwen --acp --experimental-skills` |
  | opencode | 二进制 | `opencode acp` |
  | goose | 二进制 | `goose acp` |
  | Kimi CLI | 二进制 | `kimi acp` |
  | Claude（适配器） | npm `@agentclientprotocol/claude-agent-acp` | 包自带入口 |
  | Codex（适配器） | npm `@agentclientprotocol/codex-acp` | 包自带入口 |

- **环境变量**：若干 `名=值` 或 `名=$服务器变量`，给 `GEMINI_API_KEY` 这类代理自己的凭据用。密钥照现有做法存成插件凭据，读取接口不返回明文。

**安装走现有的「安装 CLI」**，版本钉死在 `coding_agent_setup.go`，随发版核对，装进数据目录下的受管目录。不在运行时用 `npx 包名@latest`：每次启动都去拉一份没审过的新版本，等于把供应链交给别人。自定义命令仍由部署者自己管。

## 测试

- **假代理**：Go 测试里用 helper-process 的办法（测试二进制带特定环境变量启动时，扮演一个 ACP 代理），按脚本回 `initialize`、流式发 `session/update`、发 `request_permission`、回 `stopReason`。不引入 Node。
- **用例**：正常完成并汇报；结果取最后一次工具调用之后那段；流式分片攒段；审批三种回法加超时；会话进程收到 SIGTERM 时走 `session/cancel`；代理崩溃；认证失败；Diana 中途「重启」（新 Runtime 按 PID 接回，同一任务只汇报一次）；续跑优先 resume，其次 load，且回放内容不进日志；原文调试文件的大小上限。
- **真实代理冒烟**：仿照现有的 `DIANA_RUN_REAL_CODEX_SMOKE`，加 `DIANA_RUN_REAL_ACP_SMOKE=<代理>`，默认不跑。

## 分期

1. **P1 会话进程和日志**（已实现）：`__coding-acp`、日志事件、`parseCodingLog` 分支、取消和超时、配置页里的「ACP 代理」后端。审批映射实现起来很薄，三种模式在 P1 一并做了，常驻放行由 Diana 侧照旧处理。
2. **P2 续跑和权限模式**：`session/resume` / `session/load` 续跑；建会话后切到代理「每次都问」的权限模式。
3. **P3 预置和安装**：实测过的代理进预置表，受管安装，WebUI 的检测环境和测试连接支持 ACP。

每期单独一个 PR，按现有惯例先审再合。

## 已定事项

1. **首批接 Gemini CLI 和 opencode**：一个是 npm 包、一个是二进制，两种分发方式都覆盖到，而且都原生支持 ACP，不经适配器。
2. **Claude 和 Codex 不提供 ACP 版本**：原生后端不动，需要时用 ACP 后端加自定义命令自己接适配器。
3. **npx 类代理装进受管目录、钉死版本**（P3），不在运行时现拉 `npx 包名@latest`。
4. **审批覆盖面在配置页标清楚**：三种审批模式都允许选，配置页和文档写明「只能审到代理主动来询问的操作」。
