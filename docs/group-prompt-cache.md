# 群聊持久上下文与前缀缓存

群聊现在按平台、机器人配置、机器人账号、上下文命名空间和群号保存稳定历史窗口与已发现工具。普通新消息追加到窗口尾部，重启后从 SQLite 恢复；跨群检索、个人状态和动态摘要不会插入这个稳定前缀。

## 调研与选择

核对日期：2026-09-19。参考的是项目自身文档、源码和官方 API 文档。

- [Manus：Context Engineering for AI Agents](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus)：保持历史与观察只追加、序列化确定；动态增删工具会破坏缓存，并让历史工具调用失去定义。本项目保留按需加载以控制初始工具体积，加载后则在同群后续轮次保留。
- [Aider：Prompt caching](https://aider.chat/docs/usage/caching.html)：将系统提示词、只读文件、仓库映射等组织成可缓存材料。这里对应稳定系统头部与已渲染群历史；没有增加收费的定时保活请求。
- [Sub2API 会话调度源码](https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/service/openai_gateway_scheduling.go)：显式会话信号先查请求头（`session-id`、`session_id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`），再回落到请求体的 `prompt_cache_key`，无显式信号时才按内容推导。工具和首条消息变化可能影响内容推导。本项目补充稳定、匿名的路由键，并按同一摘要写入请求头。
- [OpenAI：Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)：缓存需要匹配实际渲染前缀，工具定义和设置也参与匹配；路由键不能代替稳定内容，更不能保证缓存永久驻留。
- [OpenCode V2：Compaction](https://opencode.ai/v2/docs/compaction)：在完整请求接近窗口时生成可校验检查点，保留近期原文；固定系统提示和工具定义本身超限时，压缩历史无济于事。
- [OpenClaw：Compaction 与 session pruning](https://docs.openclaw.ai/concepts/compaction)：摘要与近期原文组合使用，工具调用和结果必须成对保留；旧工具结果可以单独裁剪，但原始会话档案继续保存。
- [Pi：Compaction](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md) 与 [Aider：History](https://github.com/Aider-AI/aider/blob/main/aider/history.py)：均以窗口余量触发摘要/检查点，并从完整轮次边界保留最近尾部，而不是无限增长单次请求。

## 实现

`group_prompt_sessions` 保存群会话的历史窗口锚点、渲染后的公开历史和工具发现顺序。每个 Runtime 首次使用时加载状态，后续复用内存。持久消息档案仍是编辑、删除和上下文代际的权威来源；每轮会读取候选历史来核对，不把整个消息存储当作永不失效的缓存。

同一条原始消息没有变化时，复用第一次渲染的文本。新消息和晚到的补录追加在已保留事件之后，不重新把补录插到前缀中间。异步图片描述不会回填改写旧行；需要最新图片内容时仍走当前消息/媒体工具。消息原文发生编辑、消息退出可见历史或手动清空上下文时允许失效，避免为缓存保留已撤销内容。

群聊原始历史缓冲按 token 预算估算候选条数，与后台记忆抽取使用的短历史分开。后台摘要与长期记忆任务仍按原来的短历史阈值运行；产生摘要不会再把群聊稳定窗口提前清掉。窗口装满后按原有 70% 低水位重新锚定，保留继续追加的空间。一次图片问题不会临时缩短群聊窗口；最终请求仍受模型 token 预算约束。

较早上下文摘要作为群会话检查点一并持久化，重启后仍能恢复。检查点位于稳定群历史之前，只有后台摘要真正更新时才替换；常规消息仍只追加近期原文。跨群召回始终留在本轮临时尾部，不会写进检查点。原始事件继续保存在消息档案中，检查点只改变活动请求投影。

已加载工具按发现顺序保存，API tools 数组继续保持 `agent_finalize`、`tools_load`、`tools_execute` 和核心工具的稳定定义。加载得到的完整工具契约写进稳定系统提示；重建 Runner 时从当前授权注册表重新生成契约，模型可直接通过 `tools_execute` 调用，无需再次 `tools_load`。工具被停用或撤权后不会恢复，也不会复用绑定了旧发言者的工具对象。新增工具只会让稳定提示在首次加载时重建一次。

请求预算现在覆盖消息文本、图片/音频、工具 schema、`tool_choice`、工具调用参数、工具结果标识以及供应商 continuation 元数据。assistant 工具调用批次与对应 tool 结果即使没有显式 `ContextGroup` 也会自动成组，裁剪时整组保留或整组删除。若固定工具 schema 加当前输入已经超过窗口，本地直接返回带所需预算的配置错误；压缩旧历史无法解决这种情况。

模型名和目录都无法推断窗口时，默认上下文与单次请求上限为 128,000 token。明确配置的小窗口仍按实际值执行；16K 配置会先为固定工具定义和输出保留空间，再压缩/淘汰旧历史，而不是假定 128K 一定可用。

证据账本的 claim ID、来源 URL 和图片任务状态不再逐步改写工具 schema。异步任务状态继续由收尾校验处理；claim 结算只记录不拦截，证据绑不上时按 insufficient 留痕，不再改写或替换最终回复。

`prompt_cache_key` 使用会话身份与调用用途的 SHA-256 摘要，不携带明文群号和用户名称，不随同群发言者或工具加载变化。Responses、Chat Completions、流式及 Registry 转接路径均保留此字段。该字段供支持它的 OpenAI 兼容端点使用，Anthropic/Gemini 不会收到此 OpenAI 专用字段。

同一个摘要还会写进 `X-Session-Affinity`、`X-Session-Id`、`X-Conversation-Id` 三个请求头。`prompt_cache_key` 是 OpenAI 专有的 body 字段，中转网关未必读它；请求头则是中转普遍使用的会话亲和信号。按 sub2api 当前源码，`explicitOpenAIRequestSessionID` 无条件先查请求头（`session-id`、`session_id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`），再回落到 body 的 `prompt_cache_key`，两样都没有时才按模型、系统提示词、工具和首条用户消息推导——而工具定义和首条消息恰恰会变。

三个头只发给会话类请求，图片等无状态端点不带；配置档里已经写过同名头时以配置档为准，不覆盖部署方对自己网关的了解。值与 `prompt_cache_key` 同源，同样不含明文群号和用户名称。OpenCode 核心不会自动稳定缓存键，其生态靠 `opencode-context-cache` 一类插件补同一个洞，做法也是把同一个哈希同时写进键和请求头。

请求头的实测结果是**没有额外收益**，见下文对照：只发请求头、不发 `prompt_cache_key` 时命中 0%。上游源码里这几个头是无条件优先读取的，所以原因不在协议设计，可能是测试端点的部署版本早于该逻辑、边缘回源时丢头，或对照只发了一个头名而该版本认的是列表里的其他名字——三者都没有排除。这几个头保留下来是因为它们成本可忽略、不造成回归，且面向的是「只读请求头、不读 body 字段」这类中转；不要据此宣称它们在当前链路上带来了命中率提升。

## 跨群和权限边界

- 跨群内容继续经过已有检索、成员可见性和脱敏检查，但只进入本轮尾部，绝不写进目标群的持久历史。关闭跨群记忆后，旧检索结果也不会从持久前缀复活。
- 个人画像、私有线程状态、记忆召回、笔记本命中、时钟和当前身份仍按本轮重新取值。共享群前缀不是共享所有参与者的私有记忆。
- 主人加载的工具不会提升普通成员权限；被停用、移除或撤权的工具不会通过恢复名称绕过当前注册表。重新授权后可恢复之前的发现状态。
- 清空上下文在同一个 SQLite 事务中推进 generation 并删除会话快照；旧 generation 的迟到保存被拒绝。内存里的旧工具加载回调也失效。其他群的快照不受影响，消息档案保留用于显式检索。
- 这里的 append-only 指稳定群历史窗口及工具发现顺序，并非把所有轮次的动态私有上下文、工具结果和完整供应商推理状态永久拼进一份无限增长的请求。达到预算、编辑/删除消息、配置或权限变化仍可能打断前缀。

## 真实 API 结果

使用本地配置中的 `gpt-5.6-sol`，通过配置的 Sub2API Responses 端点，实际执行 21 个模型请求。全部输入为合成群聊与无副作用测试工具，没有向真实群发送消息。保留了[逐请求 token 和耗时记录](testing/group-prompt-cache-api-2026-09-19.json)。

缓存对照使用 120 条合成历史与一条每轮变化的跨群引用。两种排列各执行 4 次，比较后三次热请求；系统前缀包含每组独立标识，避免前一组的预热污染后一组。固定历史长度是为了单独检验引用位置，不是完整线上流量回放。

| 路由键 | 排列 | 热请求总输入 token | 热请求缓存 token | 加权命中率 |
| --- | --- | ---: | ---: | ---: |
| 未发送 | 引用插在历史前 | 31,239 | 0 | 0% |
| 未发送 | 引用放在历史后 | 31,233 | 0 | 0% |
| 稳定键 | 引用插在历史前 | 31,239 | 0 | 0% |
| 稳定键 | 引用放在历史后 | 31,233 | 30,720 | 98.36% |

另一组 2026-09-21 的对照单独检验请求头，同样 3 臂各 4 轮：不发任何信号 0%（31,089 token 命中 0）、只发 `X-Session-Affinity` 不发 `prompt_cache_key` 同样 0%（31,089 命中 0）、发 `prompt_cache_key` 并由适配层补上三个头 96.33%（31,092 命中 29,952）。结论是 body 字段在该端点有效、请求头无额外收益，且三个头不造成回归。复现见 `TestLiveSessionAffinityHeader`。

带稳定路由键的新排列中，首次请求缓存为 0，之后三次每次命中 10,240 / 10,411 token。旧排列和新排列的热请求完成耗时中位数分别为 3,318 ms 与 3,316 ms；这组小样本没有证明端到端延迟显著下降，也不能推断生产全流量命中率。

另用真实模型执行“加载测试工具 → `tools_execute` 调用 → 收尾”，再创建新的 Runtime 和 Runner 从存储恢复。首轮 3 次模型请求，第二轮 2 次；第二轮首个请求的稳定提示已包含恢复后的完整契约，模型直接调用 `tools_execute`，没有再次调用 `tools_load`。SQLite 关闭/重开、跨群/跨机器人隔离、并发加载与清空后的迟到写入另由自动化测试覆盖。

预算修正后又把真实适配层的 `context_window_tokens` 和 `max_context_tokens` 强制设为 16,384 重跑同一组测试：8 个缓存对照请求和 5 个工具请求全部成功，稳定尾部的后三次请求仍各命中 10,240 / 10,411 token（98.36%）；工具链重启后仍直接调用已加载工具。这个结果验证 10K 级提示在 16K 配置下不会因新增的工具预算核算而被误裁或被上游拒绝；更大的固定 schema 超限由自动化边界测试覆盖。

## 复现

普通测试不访问收费模型。真实 API 测试需要显式设置环境变量，使用自己的可用端点与模型；不要将密钥写入测试文件或提交日志。

```sh
export DIANA_LIVE_LLM=1
export DIANA_TEST_LLM_API_KEY='你的 API key'
export DIANA_TEST_LLM_BASE_URL='你的 OpenAI 兼容 API 地址'
export DIANA_TEST_LLM_MODEL='你的模型'
export DIANA_TEST_LLM_API_STYLE=responses
export DIANA_CACHE_REPORT=/tmp/diana-cache-results.json
go test ./model/assistant -run '^TestLiveGroup(PromptCache|ToolSession)$' -v -count=1
```

缓存测试读取供应商返回的 `cached_input_tokens`。如果新排列完全没有缓存读入，测试明确失败，不把本地前缀相同比对当作缓存命中。缓存 TTL、账号调度和服务端负载仍可能影响后续复现。

## 验证记录

2026-09-19 完成并通过：

- `go test ./...`：全部 Go 包通过。
- 定向 `go test -race`：群历史、会话恢复、并发工具发现、跨机器人隔离、重置迟到写入、工具 schema 稳定性以及两种 OpenAI 协议的路由键透传。
- 两项显式启用的真实 API 测试：缓存排列对照、重建 Runtime/Runner 后直接使用已加载工具。
- 同两项真实 API 测试在显式 16,384 token 窗口下再次通过。
- 本次 Go 改动的 `gofmt` 检查及 `git diff --check`。

没有更改依赖、前端或版本号，也没有执行部署或发布。
