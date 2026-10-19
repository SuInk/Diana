# τ² 风格的 Agent 主对话评测（taueval）

`model/agent/taueval` 照 [τ²-bench](https://github.com/sierra-research/tau2-bench) 的方法评测 Diana 的 Agent 主对话：

- **真实起点**：用例的 `messages` 原样取自生产 `debug-traces` 里第一次 `reply` 请求（系统提示词、人设、历史和当前消息）。
- **有状态环境**：工具换成按 `actions` 改写 JSON 状态的模拟工具，名字、描述、参数照录生产定义；`core_tools` 与生产一致常驻，其余工具要先 `tools_load`。
- **模拟用户**：由模型扮演，只看得到 `user.instructions`，信息按需透露，说完输出 `###STOP###`；也可以用 `user.script` 写死追问。
- **按终态评分**：对话结束后用 `expect.state` 对环境终态做子集匹配，`exact` 里的集合还要求条数一致（多建、漏删都会被抓到）；没法模拟状态的工具用 `expect.calls` 核对写操作的关键参数（失败的调用不算）。再核对 `require_tools`/`forbid_tools`、`replies`，最后可选用 `criteria` 交给模型评审。
- **分类汇报**：`category` 分组算 pass^k。目前用的四类：`state`（改对提醒/订阅/配置等）、`no_tool`（BFCL 式反例：闲聊不该调工具）、`persona`（群聊口吻与篇幅）、`research`（回答是否有工具结果撑腰）。
- **pass^k**：每条用例跑 n 次、成功 c 次，pass^k = 平均 C(c,k)/C(n,k)，衡量「连续 k 次都做对」的概率。pass^1 和平均成功率相同，k 越大越看一致性。

## 做一条用例

1. 在 miku 的 `data/debug-traces/<日期>/<会话>/<message_id>/` 里挑一条涉及有状态工具（`subscription`、`tasks`、`reminder`、`bot_markers`、`relationship`……）的消息，拷到本机。
2. 抽骨架：

   ```bash
   go run ./cmd/tau-extract -id my-case -schemas schemas.jsonl <message_dir> > my-case.json
   ```

   工具若是在更早的轮次 `tools_load` 的，本条轨迹里没有它的定义，命令会报 warning；从别的轨迹的 `tools_load` 输出里把 `loaded` 条目逐行存成 `schemas.jsonl` 传给 `-schemas`。这类工具同时记进 `loaded_tools`，评测时视为会话已加载，和生产一致。工具默认按生产录到的输出依次重放（`replay`）。
3. 手写剩下的部分（骨架里 `reference` 是生产那一轮实际发生的调用，可作参考）：
   - `initial_state`：一般直接用 `reference` 里列表类工具的输出。
   - 工具的 `action_field`（如 `operation`）和 `actions`：`read` / `append` / `update` / `remove` / `fixed` / `replay`，语义见 `env.go`。
   - `user.instructions`：目标、知道的信息、何时结束。
   - `expect`：字符串以 `re:` 开头按正则匹配；值写 `null` 表示该键必须不存在。

格式可以参考 [tau/example-reminder-reschedule.json](tau/example-reminder-reschedule.json)。

**真实用例含群聊原文，不进仓库**，放在仓库外的目录里；仓库只放合成示例。

## 运行

```bash
DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=https://sub2api.earlyso.com/v1 DIANA_TEST_LLM_MODEL=gpt-5.6-terra DIANA_TAU_CASES=~/diana-tau-cases DIANA_TAU_TRIALS=8 DIANA_TAU_PARALLEL=2 DIANA_TAU_REPORT=report.json go test -run TestLiveTau -v -timeout 60m ./model/agent/taueval
```

- `DIANA_TAU_TRIALS` 默认 4；`DIANA_TAU_PARALLEL` 默认 2，网关 429 时调小。
- `DIANA_TAU_USER_MODEL`、`DIANA_TAU_JUDGE_MODEL` 分别指定用户模拟器和评审模型，避免被测模型自己当用户、自己判分。
- 要和生产一致就测生产主对话模型：`DIANA_TEST_LLM_PROVIDER=gemini`，地址和 key 用生产 antigravity 供应商的配置。
- 报告里有每次试验的对话、工具调用和终态，失败原因写在 `failures`。

## 已知差异

- 测试直接用 `agent.Runner`，没有经过 `assistant.Runtime` 的接话判定、分段发送和收尾提示注入；比较的是主对话里的工具决策和终态。
- `loaded_tools` 只能从「这一轮直接用了却没加载」推断，生产会话里加载过但这一轮没用到的工具会漏掉。
- 重放的工具输出跟着生产那一轮走：模型换了查询词也拿到同样的结果，生产没调过的工具只返回 `{"ok":true}`，依赖这些工具的用例改用 `criteria`，不要卡死参数。
- 后续追问以「【当前需要回复的消息】」的用户消息追加，上一轮回复作为 assistant 消息，和生产把机器人发言折进历史的格式不同。
- 模型评审会偏宽，`criteria` 要把「不允许什么」写具体；能用 `expect.state` 断言的尽量别交给评审。
