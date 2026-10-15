# 子代理实现对照与 Diana 方案

2026-10-15，对照官方仓库实际源码和说明。

- **Pi**：官方子代理扩展示例为每个任务启动独立 `pi --mode json -p --no-session` 进程，传入任务及代理提示词。主模型拿到子代理最终输出；中间消息保留在详情中。它是扩展示例，不是所有 Pi 会话必经的调研管线。见 [实现](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/examples/extensions/subagent/index.ts) 与 [说明](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/examples/extensions/subagent/README.md)。
- **Codex CLI**：V2 `spawn_agent` 用独立线程执行任务；`fork_turns="none"` 不继承历史，`"all"` 继承历史，当前源码默认 `all`。因此不能将“开子代理”直接等同于“清空上下文”。见 [参数与分支](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/multi_agents_v2/spawn.rs)、[线程创建](https://github.com/openai/codex/blob/main/codex-rs/core/src/agent/control/spawn.rs)、[官方子代理说明](https://developers.openai.com/codex/subagents)。

- **Claude Code**：普通子代理以专用系统提示词和委派消息启动，不继承主对话；fork 类型另行继承历史。工具可按代理限制，完成后回传结果。依据[官方文档](https://code.claude.com/docs/en/sub-agents)，不是闭源实现审计。

## Diana 的最小改动

已有 `subtask(mode=search)` 为任务单独构造消息，只带 objective、必要 material、起始查询和来源偏好，不带聊天历史或人设；工具仅有 web_search 和 browser_render，返回短结论，已有用量、并发和取消机制。

按 Claude Code 普通子代理方式复用这条路径：搜索 Worker 使用专用系统提示词，替换 Runner 的通用主代理提示词；主代理只保留简短委派说明。补主代理的委派提示、Worker 的中性核验和来源日期要求，并阻止 Worker 中间文字进入主回复流式草稿。单一问题委派一个 Worker，最终返回自然语言摘要，主代理综合来源和不确定性。不新增开关、报告协议或引文匹配校验器。

搜索规则同时移除 10% 阈值及不搜索场景描述：只要信息可能变化、记忆可能出错或存在不确定性，就先搜索核实。

## 后续修复与验证（2026-10-16）

- Gemini 流式提前取消已定位到当前 genai v1.61.0 的 `sendStreamRequest`：设置 HTTPOptions.Timeout 时，函数在返回流迭代器后就 defer cancel，响应体还未消费完。由 Gemini 适配器在读流 goroutine 内持有完整请求期限，避免 SDK 再创建短生命周期的 context。延迟分块回归测试修复前失败、修复后通过，主动取消测试仍通过。
- 搜索 Worker 明确区分“本次未确认”与“没有官方公告/媒体均未报道”；旧动态不能反证后来事件，转载应追踪原始报道。
- 子任务的内部搜索预算原为 180 秒，但外层普通工具默认 60 秒会提前取消。Runner 允许有界多步工具声明自己的预算，subtask 使用 180 秒；父请求截止时间和收尾预留继续优先。
- 同款 gemini-3.8-flash-low、生产搜索与浏览器配置的真实流式回放已完成搜索、读取正文并确认去世消息。带错误历史的主代理也完成委派并明确纠正原结论；默认父级超时配置下再次通过。最终使用原句“张雪峰死了”（无额外查证要求）回放也自行委派并纠错，子任务耗时约 69 秒，超过旧的 60 秒工具期限。
- 回放使用本地记录通道，不向真实群聊发送、不修改线上配置。它覆盖指定问题及主代理到 Worker 的调用链，不等于完整群聊历史、回复润色和消息投递的端到端评测，也不证明一般事实准确率。
- 定向 race 测试覆盖 Gemini 分块读流、子代理提示词和输出隔离、工具期限与父请求期限。前端 247 项单元测试通过，演示提示词目录已同步。

两套全量 `go test -race ./... -timeout 30m`（默认时区与 `TZ=UTC`）均通过，assistant 包约 891 秒；随后补充的工具期限改动及最新子代理/Gemini 测试也在两个时区的定向 race 检查中通过。改动未部署到 miku。

用户后续要求取消总时长上限：subtask 现在以零总预算运行，移除 analyze 的 90 秒和 search 的 180 秒整体计时；不再套用普通工具总超时。单次模型/网页/搜索超时、步数预算及父级取消继续生效，调用方明确提供的 deadline 也不会被绕过。
