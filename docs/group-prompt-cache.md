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

### Yuki 的补充参考

本次另外核对了 [Yuki 源码快照 `99ce5a6`](https://github.com/YuanYeYouTao/Yuki/tree/99ce5a643f1fd6c126bf64af7c8963d1082c3864)：它按会话冻结已经提交的模型输入片段，在 epoch 内只追加；摘要内容或覆盖范围变化，以及来源、权限、删除等变化，会触发重建，并通过 revision/generation 校验拒绝迟到更新。Diana 借鉴其稳定前缀原则，但本轮不另建 epoch 状态机：已有历史锚点在历史选取时更新，适配器最终裁剪也不会回写锚点，单在摘要阶段增加 epoch 无法解决完整请求的一致性。

Yuki 的短期状态还采用极小预算、TTL 和版本校验，但其全局 `short_state` 不按群或用户分区；“不可信资料”标记不等于保密隔离。Diana 的[全局状态视图设计](global-state-view.md)只借鉴状态分层与来源追溯，不照搬这种无范围共享。

## 实现

`group_prompt_sessions` 保存群会话的历史窗口锚点、渲染后的公开历史和工具发现顺序。每个 Runtime 首次使用时加载状态，后续复用内存。持久消息档案仍是编辑、删除和上下文代际的权威来源；每轮会读取候选历史来核对，不把整个消息存储当作永不失效的缓存。

同一条原始消息没有变化时，复用第一次渲染的文本。新消息和晚到的补录追加在已保留事件之后，不重新把补录插到前缀中间。异步图片描述不会回填改写旧行；需要最新图片内容时仍走当前消息/媒体工具。消息原文发生编辑、消息退出可见历史或手动清空上下文时允许失效，避免为缓存保留已撤销内容。

群聊原始历史缓冲按 token 预算估算候选条数，与后台记忆抽取使用的短历史分开。后台摘要与长期记忆任务仍按原来的短历史阈值运行；产生摘要不会再把群聊稳定窗口提前清掉。窗口装满后按原有 75% 低水位重新锚定，保留继续追加的空间。一次图片问题不会临时缩短群聊窗口；最终请求仍受模型 token 预算约束。

较早上下文摘要作为群会话检查点一并持久化，重启后仍能恢复。**最新摘要位于稳定历史后的本轮尾部**，只保留一份；后台更新摘要不再移动前面的历史。组装时重新读取当前摘要源，按 `Runtime.mu → session.mu` 的顺序串行发布检查点，释放 Runtime 锁后才保存，避免较早进入请求的旧快照覆盖较新摘要。恢复的摘要也适用当前预算，保留 Summary 优先级和 `AtomicText`，不由最终预算层从中间截断。明确的空摘要、session thread 或 structured memory 接管时不会重新注入旧检查点。跨群召回不会写进检查点。原始事件仍在消息档案中，检查点只改变活动请求投影。

已加载工具按发现顺序保存，API tools 数组只有 `send`、`tool_search`、`tools_execute`，对谁都一样（见[延迟工具调用协议](deferred-tools.md)）。**恢复的完整工具契约放在 caller 人设之后、稳定历史之前**。它曾拼在 Agent system 后面挡在人设前，后来挪到尾部；2026-10-25 按生产统计又移到历史前：224 对同群相邻请求里契约变了 28 次，其中 25 次第一条 system（工具目录）也同时变了，前缀本来就从那里断，单独变化只有 3 次；放在尾部却要每条消息重算平均 7K 多字。加载新工具后的下一轮会让历史缓存断一次。重建 Runner 时从当前授权注册表重新生成契约，模型可直接通过 `tools_execute` 调用，无需再次 `tools_load`；中途加载仍通过工具结果追加。工具被停用或撤权后不会恢复，也不会复用绑定了旧发言者的工具对象。即使没有历史，契约也采用带系统补充标记的 user 资料块及 System 本地预算优先级，避免被适配器并回 leading system。

主回复的逻辑排列为：

```text
固定 Agent 协议/工具目录 + caller 人设与固定规则
→ 恢复的工具契约
→ 掉出历史窗口的媒体索引
→ 稳定群历史 → 缓存断点
→ 本轮资料（最新摘要、跨群引用、记忆等）
→ 本轮格式/身份/时钟 + skills
→ 当前输入
```

尾部内各块沿用现有组装顺序；关键是它们全部位于稳定历史之后，当前输入保持最后。尾部里的时间一律写绝对时间（消息时间、记忆「记于」日期、便签写成时间、跨群历史时间），不写「距当前约 N 分钟」「3天前」：相对说法每轮都变，会让整块资料在下一轮变成新字符串。单条/自动分条的回复呈现规则也移至尾部，不改变发送层行为。这里不冻结过期摘要，也不靠减少历史、缩短记忆或新增收费保活来提高命中率；摘要本身不保证跨轮显式缓存。

「更早的图片与文件索引」只列掉出本轮历史窗口的媒体，按渲染进提示词的那段窗口过滤（以前用的是内存里的短期上下文，窗口里刚发的图也被列进来，每来一张新图这段就变），因此只在窗口往前滑时才变，放在历史之前。索引里的消息都在身份隐私代理里登记，message_id 和其他标识一样换成别名；以前这些行没登记过，有一部分真实消息 ID 原样发给了模型。

同一天还回放过把单条/自动分条之外的回复呈现规则（约 2.2K 字）挪进人设头部：离线估算可复用前缀只多 1.6 个百分点，又会让格式规则离生成更远，没有采用。契约前移用生产请求经 DeepSeek（`deepseek-flash`）回放 39 条首步请求、新旧排列各 2 次：回复字数中位 43 / 42.5，含真实换行都是 22%，首步 web_search 都是 21 次、`tools_execute` 都是 2 次，没有 emoji 或附和开头。离线按 388 个生产请求估算，可复用前缀从 66.8% 升到 73.8%。

请求预算现在覆盖消息文本、图片/音频、工具 schema、`tool_choice`、工具调用参数、工具结果标识以及供应商 continuation 元数据。assistant 工具调用批次与对应 tool 结果即使没有显式 `ContextGroup` 也会自动成组，裁剪时整组保留或整组删除。若固定工具 schema 加当前输入已经超过窗口，本地直接返回带所需预算的配置错误；压缩旧历史无法解决这种情况。

模型名和目录都无法推断窗口时，默认上下文与单次请求上限为 128,000 token。明确配置的小窗口仍按实际值执行；16K 配置会先为固定工具定义和输出保留空间，再压缩/淘汰旧历史，而不是假定 128K 一定可用。

来源 URL 和图片任务状态不改写工具 schema。搜索不再要求 claims 或终稿事实复核，来源回溯从实际工具结果被动记录；异步图片任务的收尾状态校验保持不变。

`prompt_cache_key` 使用会话身份与调用用途的 SHA-256 摘要，不携带明文群号和用户名称，不随同群发言者或工具加载变化。Responses、Chat Completions、流式及 Registry 转接路径均保留此字段。该字段供支持它的 OpenAI 兼容端点使用，Anthropic/Gemini 不会收到此 OpenAI 专用字段。

同一个摘要还会写进 `X-Session-Affinity`、`X-Session-Id`、`X-Conversation-Id` 三个请求头。`prompt_cache_key` 是 OpenAI 专有的 body 字段，中转网关未必读它；请求头则是中转普遍使用的会话亲和信号。按 sub2api 当前源码，`explicitOpenAIRequestSessionID` 无条件先查请求头（`session-id`、`session_id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`），再回落到 body 的 `prompt_cache_key`，两样都没有时才按模型、系统提示词、工具和首条用户消息推导——而工具定义和首条消息恰恰会变。

三个头只发给会话类请求，图片等无状态端点不带；配置档里已经写过同名头时以配置档为准，不覆盖部署方对自己网关的了解。值与 `prompt_cache_key` 同源，同样不含明文群号和用户名称。OpenCode 核心不会自动稳定缓存键，其生态靠 `opencode-context-cache` 一类插件补同一个洞，做法也是把同一个哈希同时写进键和请求头。

请求头的实测结果是**没有额外收益**，见下文对照：只发请求头、不发 `prompt_cache_key` 时命中 0%。上游源码里这几个头是无条件优先读取的，所以原因不在协议设计，可能是测试端点的部署版本早于该逻辑、边缘回源时丢头，或对照只发了一个头名而该版本认的是列表里的其他名字——三者都没有排除。这几个头保留下来是因为它们成本可忽略、不造成回归，且面向的是「只读请求头、不读 body 字段」这类中转；不要据此宣称它们在当前链路上带来了命中率提升。

## 只追加上下文

主回复不再逐轮重新组装，而是在上一轮**实际发给模型的请求**后面接着发：

- 上一轮 Runner 最后一次发出的请求（含每步临时插入的工具执行状态），接上模型最后的 send 调用和「已发送」结果，作为这一轮的前缀（`agent.Response.Transcript`）。
- 这一轮追加：历史区里记录中还没有的群消息（机器人自己发过的回复跳过），然后是这一轮的尾部和当前消息。历史区里的摘要和媒体索引不再追加——续接时历史不滑动。
- 尾部只发变了的东西，段和条目都整块保留，不按行删：
  - 每一段（以「【标题】」开头）和上文里同一标题最近的那一版比，一样就不发。群约定、说话风格这类参考资料很少变，基本只发一次；对话对象、本轮路由说明这类状态段在切换时（比如上一轮是主动接话，这一轮有人 @ 了机器人）会补上。
  - 长期记忆按条，只发上文里没有的条目；同一发言者的画像不重发，只带一行标题。跨群历史按条，发过的不再发。
  - 可提及成员候选不发：它按最近发言排序、几乎每轮都变，同样的 ID 在历史行里都有。
  - 10-27 生产三个群各 12 轮模拟，每轮增长从约 1.05 万字降到约 5000 字，剩下主要是新记忆条目（约 1550）、新群消息（约 1070）和新跨群条目（约 800）。
  - 只追加对话、尾部一律不发也试过：每轮只增长约 1000 字，但 15 轮回放有 2 轮出错——缺了本轮路由说明，被 @ 之后的下一轮误判成「没人叫我」而沉默；少了按当前消息检索的记忆，也少了一次查证。所以状态段变了要补，记忆和跨群按条补。
- Runner 核对系统提示、契约和工具定义的指纹（`agent.Continuation.Key`），assistant 核对头部 system；任一项变了、续接请求超过这一轮完整组装的两倍、上一轮失败或进程重启，都退回完整组装并重开一份记录。定时查询、事件触发这些后台入口不参与续接。

为什么需要它：经 sub2api 转发的 gpt（ChatGPT Codex 上游）只在「instructions 加工具定义末尾」和「上一次完整请求末尾」两个位置复用缓存。2026-10-27 用一条 4.7 万 token 的生产请求实测：完全相同或纯末尾追加命中 46848，改动 input 里任何一处（第 0 条、80%、95% 位置）都只剩 18304。逐轮重新组装的请求从来不是上一条的纯追加，生产上同一会话连续请求每条都恰好命中 18432，gpt 主回复命中只有三到四成。

同群连续 8 轮实测（不计第一轮）：

| deepseek-flash | 现布局 | 只追加 |
|---|---|---|
| 命中率 | 82.9% | 96.2% |
| 输入 token | 31.5 万 | 40.4 万 |
| 未命中 token | 5.4 万 | 1.5 万 |

gpt-6.1-sol 只在早先尾部按行去重的版本上测过：47.4% → 90.9%，未命中 token 从 16.1 万降到 4.9 万。gpt 只认整段前缀，收益主要来自续接本身。

代价是请求逐轮变长，碰到两倍上限就重开一次，重开那一轮不命中。

## 跨群和权限边界

- 跨群内容继续经过已有检索、成员可见性和脱敏检查，但只进入本轮尾部，绝不写进目标群的持久历史。关闭跨群记忆后，旧检索结果也不会从持久前缀复活。
- 个人画像、私有线程状态、记忆召回、笔记本命中、时钟和当前身份仍按本轮重新取值。共享群前缀不是共享所有参与者的私有记忆。
- 主人加载的工具不会提升普通成员权限；被停用、移除或撤权的工具不会通过恢复名称绕过当前注册表。重新授权后可恢复之前的发现状态。
- 清空上下文在同一个 SQLite 事务中推进 generation 并删除会话快照；旧 generation 的迟到保存被拒绝。内存里的旧工具加载回调也失效。其他群的快照不受影响，消息档案保留用于显式检索。
- 这里的 append-only 指稳定群历史窗口及工具发现顺序，并非把所有轮次的动态私有上下文、工具结果和完整供应商推理状态永久拼进一份无限增长的请求。达到预算、编辑/删除消息、配置或权限变化仍可能打断前缀。

### 工具定义不随非主人发言者变

API 的 tools 数组排在历史前面（DeepSeek 实测在首条 system 之后、其余 system 之前），所以同一个群里非主人发言者的工具定义必须逐字相同，按发言者增减的只能写进尾部：

- `platform` 的群管操作对所有人列在 schema 和描述里。展示与否只决定尾部那段群管说明，门禁仍在 `Run` 里实时核验。
- `github` 按群登记：本群有授权，或配置了按人的授权，就对全群登记；描述对非主人统一，谁有写入授权、限于哪些仓库写在尾部「GitHub 授权」。没授权的人拿到的只是 `Run` 本来就放开的部分（公开仓库可读，#576），写入照样按白名单拒。
- 主人的工具集（主人专属工具、Agent 协议规则）本来就和别人不同，主人和成员交替时前缀照样从工具目录处分叉，这一点没有改。

2026-10-25 生产主回复切到 `deepseek-flash` 后，按同群前后两次主回复请求的分叉位置统计 388 个请求：工具定义变化占全部输入字符的 14.3%，是最大的一类损失，几乎都来自管理员/群主与普通成员交替（`platform` 操作 6 个对 18 个）和有无 GitHub 授权交替。用 miku 上的生产请求直连 DeepSeek 回放：只换 `platform` 的 schema，下一轮只命中 8,064 / 41,623 token；schema 一致时命中 29,696 / 41,296，正好到历史末尾。用新 schema 让普通成员要求禁言他人，4 次都直接说明没有群管权限，没有调用工具。

## 观测与边界

`prompt_cache_divergence` 按平台、profile、SelfID、上下文命名空间、会话、用途和工具指纹分别保存基线；同群换发言者仍可复用。同编号的其他机器人或平台不再被拿来互相比较。它只比较本地请求的规范化前缀，忽略断点后预期变化，不能替代供应商 usage：最终适配器预算裁剪、供应商协议合并、模型/工具定义或权限变化仍可能让 wire 前缀失效。小窗口先裁剪历史是预算行为，不是缓存故障；固定开销超窗仍需明确报错。

缓存命中率应按完整样本组计算 `Σ cached_input_tokens / Σ input_tokens`，不能平均各轮百分比，也不能只挑有命中的轮次。当前 `llm.Usage` 的整数无法区分供应商没有返回缓存字段与显式返回零；未确认原始字段存在的零值只能记为 `unknown_or_zero`，不能据此宣布零命中或总体收益。正缓存值能证明发生了复用，但不自动证明同组其他零值样本统计完整。

### Gemini 隐式缓存不是最长公共前缀

上面的排列默认供应商按最长公共前缀复用：易变块放在缓存断点之后，就不会影响历史。OpenAI 兼容网关和 DeepSeek 确实如此，但 Gemini 不是。[#1140](https://github.com/SuInk/Diana/issues/1140) 的直连实验（`gemini-flash-latest`，响应为 `gemini-3.8-flash`）显示，可命中的位置只有两处：

- `systemInstruction + tools` 之后；
- 上一次请求「总长度 − g」处。g 是原样重发时没被命中的 token 数，18K 请求约 1,749，31K～50K 请求约 2,035～2,040，会随请求大小变化，不能当成常数。

下一次请求只有在与上一次共享的前缀覆盖到第二个位置时，历史才会命中；否则退回静态前缀。换句话说，**上一次请求在历史末尾之后、与本次不同的那段必须小于 g**。尾部比 g 只多 61 token，命中也会从 16K 掉到静态前缀那 4.4K。原生 Gemini 适配层和 OpenAI 兼容网关转发的 gemini 模型上都观察到了同样现象。

两次首次回复之间群里通常有新消息，新消息追加在历史末尾，所以下一次请求从上一次的历史末尾起就开始不同：上一次请求在历史之后的**全部内容**都算作这段尾部，尾部内部怎么排序都没有用。2026-10-20 用生产主回复模型（`gemini-3.8-flash-low`，经 Antigravity 网关）回放同一个群连续 16 条首次回复请求，对照了原排列和「稳定块紧跟历史、发言者规则拆出固定部分」的新排列：两组每次都只命中约 16.4K token 的静态前缀，历史一次都没命中，新排列没有带来提升，因此没有采用。生产上首次回复在历史之后约有 2.4 万字符的本轮资料，其中按发言者检索的记忆约 4K 字符，单这一块就超过 g。要让 Gemini 首次调用命中历史，只能把历史之后的内容整体压到约 1.5K token 以内，这需要删减记忆、对话对象规则等内容，会影响回复效果，目前没有做。同一次回复里工具循环的后续调用本来就能命中约九成。

显式 `cachedContents` 不受尾部长度影响，但生产使用的 Antigravity 网关没有这个路由（请求落到管理界面 HTML，模型列表也只声明 `generateContent` / `countTokens`），只有直连 Gemini API 才能用，目前没有接入。

`prompt_cache_divergence` 仍按最长公共前缀判断，会把这类请求算成正常。因此探针另外估算上一次请求在分叉点之后的 token 数：响应来自 Gemini（原生 provider，或模型名含 `gemini`），而这个数超过 1,500 时，额外记一条 `prompt_cache_gemini_tail_overflow` debug 日志，带上 `previous_tail_tokens`、`cached_input_tokens` 和 `input_tokens`。1,500 是给 g 的波动和本地估算误差留出的余量。估算用的是本地的保守口径，不是 Gemini tokenizer 的计数。

直连实验只在单个账号的 `gemini-flash-latest` 上做过，生产回放只有一个群的 16 条请求；都没有测缓存 TTL，也不保证其他 Gemini 版本行为相同。

## 历史真实 API 结果（不是本轮收益）

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

上述历史缓存测试读取供应商返回的 `cached_input_tokens`。如果新排列完全没有缓存读入，测试明确失败，不把本地前缀相同比对当作缓存命中。缓存 TTL、账号调度和服务端负载仍可能影响后续复现。

### 三类动态尾部的合成对照

新增 `TestLiveStableTailCache` 分别对照摘要更新、回复呈现切换（恢复工具契约变化那一组已随契约移到历史前而撤掉）。它先用真实 Runtime helper 和 Agent Runner 捕获请求，再仅移动目标块构造旧位置对照；两臂使用相同的合成历史并等量追加，实验运行、因素与实验臂各自隔离前缀和路由键。不读取生产聊天，不执行真实工具，不向群发送消息。

先配置上面的测试端点、API key 与模型，再显式启用第二个开关：

```bash
DIANA_LIVE_LLM=1 DIANA_LIVE_STABLE_TAIL_CACHE=1 go test ./model/assistant -run '^TestLiveStableTailCache$' -v -count=1
```

三因素 × 两臂 × 三轮，最多 18 个出网请求，每次输出上限 64 token；不额外预热、不做收费保活，并阻断同一次 Generate 的再次出网和重定向。不使用 OAuth 刷新。测试日志输出逐样本 JSON 和按因素、实验臂、冷暖分组的全样本原始 token 总数；`cached=0` 标为 `unknown_or_zero`，不推断缓存未命中，也不从不完整的 usage 推导命中率。这个实验采集数据而不保证供应商缓存必然生效，不使用 `DIANA_CACHE_REPORT` 写报告。

不调用收费模型的 fixture、统计与请求上限检查：

```bash
go test ./model/assistant -run '^TestStableTailCache' -count=1
```

## 验证记录

### 本轮稳定尾部改动（2026-10-11）

已通过 `go test ./...`、`go test -race ./...`、`TZ=UTC go test -race ./...`、`go test -race ./model/agent ./model/llm -count=1`、摘要/群会话及合成对照的定向竞态测试，以及完整请求、工具恢复与撤权、四种供应商协议排列和小窗口预算的回归测试。`gofmt`、`git diff --check` 与公开仓库审计通过。核心实现的两项独立代码审查完成；发现并修正了旧 live 工具恢复测试仍要求契约位于首条 system 的过时断言。后续新增的生产回放工具另做有效性审查，不把核心实现审查结果当作回放工具也已验收。

当前生产主回复模型 `gemini-3.8-flash-medium` 的真实合成对照已完成（原生 Gemini 接口，18/18 个请求，无重试）。每因素每臂包含 1 个冷请求和 2 个热尝试，下表保留全部样本，不筛除零值：

| 因素 | 旧位置 input 合计 | 新尾部 input 合计 | 旧位置耗时合计 | 新尾部耗时合计 |
| --- | ---: | ---: | ---: | ---: |
| 摘要变化 | 33,942 | 35,105 | 10,314 ms | 18,115 ms |
| 呈现模式变化 | 34,450 | 35,039 | 34,020 ms | 16,130 ms |
| 恢复工具变化 | 33,995 | 36,848 | 27,415 ms | 31,953 ms |

每因素每臂输出合计均为 48 token。18 个样本的标准化 cached 计数均为 0，无法区分原始字段缺失与明确零值，因此全部标记 `unknown_or_zero`，**不能据此计算命中率，也未证明实际缓存收益**。两臂供应商报告的 input 不完全相等；本地等量历史不代表上游实际计费输入相等。只读核查没有发现 fixture 跨臂 slice 污染或模型返回内容回填历史；Gemini 的 input 直接取 `PromptTokenCount`，本地没有加上 `ThoughtsTokenCount`。但消息位置、system/user 归属和实验臂 nonce 本来不同，且未保存完整 wire payload 与原始 usageMetadata，因此仍不能解释差值，也不能断言供应商计数异常。小样本耗时仅作原始记录，不推导延迟提升；上方历史百分比不是本轮收益。

另从当前生产成功的首轮文本请求中严格筛选 3 条，做相同模型的原始位置/尾部位置对照，共 6/6 次请求成功，无工具执行或群消息投递。每条均包含呈现规则与恢复工具契约；没有旧摘要样本，不能声称覆盖了生产摘要回放。

| 样本 | 原始 input / cached / 耗时 | 尾部 input / cached / 耗时 |
| --- | --- | --- |
| 0 | 58,035 / 21,608 / 4,833 ms | 52,109 / unknown / 5,056 ms |
| 1 | 63,350 / unknown / 6,747 ms | 61,761 / unknown / 6,860 ms |
| 2 | 63,360 / 25,233 / 10,141 ms | 62,359 / 40,889 / 11,944 ms |

这些是生产请求的**第一步块位置反事实回放**，不是完整 Runtime 端到端回放；trace 没有保存 Priority、CacheBreakpoint 等完整内部元数据。补强后的回放仅给明确识别并移动的块恢复生产优先级（摘要另设 AtomicText），不重建其余未知状态；发送前要求所有样本两臂完整请求均符合相同配置的本地预算，否则整批拒绝。历史生产缓存可能已预热原始位置，样本也不保证同一稳定前缀；Gemini 适配器不使用 `PromptCacheKey`，回放仅分配不同 key 不能提供该协议的缓存隔离。因此正缓存计数只证明发生过复用，不证明本次改动提升命中率。生成内容与原始 trace 留在私有报告中，不进入仓库。

回放工具有效性审查另发现原 guard 未排除文本兼容工具续轮、且未检查预算裁剪。已补上保守续轮标记拒绝及整批零裁剪预检，再对已有 3 个私有样本离线复验，全部通过，无新增模型调用。相同默认窗口为 128,000、输出上限 1,024、可用输入预算 126,848；三对本地估算分别为 59,285/59,330、70,214/70,259、70,538/70,583，均无需本地裁剪。这只能排除本批样本在当前同配置下的上述混淆因素，不能还原历史实际 wire，也不是供应商精确 token 计数。

离线逐对核对生成行为后，不能把回放 `PASS` 等同于语义质量通过：

- 样本 0 两臂均选择 `web_search`，首步符合查证意图；检索侧重点不同，未执行搜索，无法评价最终答复。
- 样本 1 从 `memory` 改为 `chat_history`，新臂与生产原始响应一致，符合追溯前情的意图；仍不能确定检索是否必要、最终理解是否正确。
- 样本 2 从直接文本/兼容协议改为原生 `agent_finalize`，但新臂未保留旧臂的点名标记，存在回复对象定位弱化的迹象。该场景建议点名而非无条件强制，每臂仅一次，尚不能确定是稳定回归或由排列改动造成。两臂也都存在未经独立查证便附和评价的遵从疑点，不能用原生收尾掩盖语义问题。

这 3 对样本未见新臂明显选错工具或产生越权意图，但不支持“回复质量完全无回归”的结论，也未验证真实发送效果。

同一当前模型的 `TestLiveGroupToolSession` 已通过，共 5 次模型调用：首次 `tools_load → tools_execute → agent_finalize`；重建 Runtime/Runner 后 `tools_execute → agent_finalize`，没有再次加载。执行的是无外部副作用的合成工具，并断言恢复契约位于尾部、当前输入仍最后。

回放入口 `TestLiveGroupPromptTraceReplay` 需要 `DIANA_LIVE_LLM=1` 和 `DIANA_LIVE_PROMPT_TRACE_REPLAY=1`，以及 `DIANA_PROMPT_TRACE_REPLAY_IN/OUT` 两个绝对私有文件路径。IN 为 1–3 条原始 trace envelope 的 JSON 数组，OUT 必须不存在且父目录仅所有者可访问；凭据通过 `DIANA_TEST_LLM_PROVIDER/API_KEY/BASE_URL/MODEL` 环境变量提供。每次最多 6 请求，串行间隔 5 秒，无重试；默认输出 512，Gemini 可显式设置 `DIANA_PROMPT_TRACE_REPLAY_MAX_OUTPUT=1024`。必须在独立进程精确使用 `-run '^TestLiveGroupPromptTraceReplay$' -count=1`，不混跑其他测试、benchmark、fuzz 或 `-cpu`，防止进程内 learned context limit 干扰预检；任一请求失败即终止。可显式设置 `DIANA_PROMPT_TRACE_REPLAY_CONTEXT_WINDOW/MAX_CONTEXT`，不能为了通过测试擅自增大生产回放预算。

离线复验使用 `TestGroupPromptTraceReplayOffline`：禁用上述两个 live 开关，设置 `DIANA_PROMPT_TRACE_REPLAY_OFFLINE=1`、IN、provider、model 及与原批次相同的输出/上下文设置。不需要 key、端点或 OUT，不创建 client 或发送 HTTP。

全局状态视图仅完成设计，没有新增运行时功能。

### 长历史真实对照（2026-10-11 23:16—23:18，UTC+8）

`TestLiveStableTailCacheLongHistory` 使用相同当前模型 `gemini-3.8-flash-medium`，完成 16/16 次请求，无重试。每臂为首次请求、客户端完全重复控制及 6 个更新轮，交替发送顺序、间隔 3 秒。合成历史的实际输入约 54k token；两臂仅摘要位置与等长实验臂标识不同，每轮追加相同历史并更新摘要，其他动态块固定。不执行模型返回的工具调用，不投递消息。

离线检查覆盖 Gemini 适配后的请求内容等量、重复控制 wire 完全相同及所有请求无需本地裁剪。运输层直接检查原始 `usageMetadata.cachedContentTokenCount`，区分字段缺失与显式零，不把缺失记为已确认未命中。

| 阶段 | 旧排列 input / cached | 新尾部 input / cached | 原始缓存字段 |
| --- | ---: | ---: | --- |
| 首次请求 | 54,274 / unknown | 54,274 / unknown | 两臂均缺失 |
| 完全重复控制 | 54,425 / 4,650（8.54%） | 54,368 / 4,650（8.55%） | 两臂均有正值 |
| 6 个更新轮合计 | 326,211 / unknown | 326,880 / 27,888（**8.53%**） | 旧臂 6/6 缺失，新臂 6/6 正值 |

新尾部更新轮的全部样本都有有效原始缓存字段，因此 8.53% 是完整分母的 token 加权缓存读入占比。旧臂字段缺失，不能写成“旧 0% → 新 8.53%”或断言提升 8.53 个百分点。该批结果支持：在本次长历史、持续更新摘要的合成场景中，新排列持续获得供应商报告的缓存读入，旧排列未报告；不是线上整体利用率或所有动态因素的收益证明。

更新轮平均调用耗时旧臂 5.487 秒、新臂 5.562 秒，没有观察到延迟改善。新臂每轮平均缓存约 4,648 token，远小于约 54k 的输入，不能声称整段历史都被复用；即便完全重复控制，供应商 input 也略有变化，具体计数与缓存覆盖边界仍未查明。本轮总调用成功不等于回复质量验收，不追加请求追求更好数字。

### 长历史低命中的后续排查

离线通过实际 Gemini adapter / genai SDK 捕获 HTTP 请求，确认每个实验臂的 baseline 与 exact-repeat 正文逐字节相同（264,587 bytes）；96 条历史逐条完整保留，没有客户端去重或预算裁剪。更新轮旧臂从 `contents[0]` 就变化，新臂保留相同 contents 前缀依次为 96、98、100、102、104、106 条。旧臂摘要是第一个 user content，不在 `systemInstruction` 中。

固定 leading system 共 6,498 字符，本地估算约 5,260 token；tools JSON 本地估算约 733 token；96 条历史共 230,486 字符，本地估算约 77,035 token。这些不是 Gemini tokenizer 计数，不能因为 4,650 与固定 system 量级相近就认定“只缓存 system”。历史约 99.92% 为 ASCII，包含大量重复 archive 模板，适合验证传输完整性，不足以代表真实中文群历史的缓存表现。

客户端没有“只缓存 system”的分支，也未设置 Gemini 显式 `cachedContent`。Google 将 `cachedContentTokenCount` 定义为 prompt 中被缓存部分的 token 数，并未定义为 system/tools 专用计数；字段缺失只表明该响应没有报告缓存 token。显式 CachedContent 支持 contents，不能反过来据此推断隐式缓存的内部 key、序列化顺序或命中保证。参考 [Gemini 缓存文档](https://ai.google.dev/gemini-api/docs/generate-content/caching)、[UsageMetadata](https://ai.google.dev/api/generate-content#UsageMetadata)。

只读核对当前部署的域名反代配置与运行镜像，确认该测试端点走 Antigravity-Manager v4.9.0，调度配置为 `Balance`。随后在网关容器内以 SQLite `mode=ro` / `query_only` 关联实验时段 16 条合成请求，仅输出字段路径、计数和匿名摘要，不导出生产聊天、账号或凭据，得到以下真实链路证据：

- 16 条请求使用同一个账号，排除了本批实验因账号切换造成缓存失效的解释。
- 两臂 baseline 与 exact-repeat 的客户端请求日志投影相同，与离线 wire 重复一致的结果吻合。但记录的上游投影不同：在其保留字段中，除 `requestId` 外，旧臂多出 `contents[96].parts[0].thoughtSignature`，新臂多出 `contents[95].parts[0].thoughtSignature`。因此原“完全重复控制”仅是客户端重复，不能当作上游完全重复。
- 新臂 8 条请求的网关 session ID 相同；旧臂 baseline/repeat 相同，6 个摘要更新轮各使用一个新 session ID。前置摘要变化与网关会话变化同时发生，不能把两臂差异归结为单纯 token 前缀位置。
- 新臂后续每轮只有最后一条 model 消息被补签名；随着新增历史，补签名位置从 95 移到 97、99、101、103、105、107，旧位置不再带签名。相邻上游请求保留的相同 contents 前缀为 95、95、97、99、101、103、105 条，仍然很长，故签名变化本身尚不足以解释约 4,650 token 的具体缓存量。
- 固定 `systemInstruction` 未改变，历史条数未减少。tools 和 generationConfig 被网关固定改写；本批客户端输出上限 1,024 被转发为 12,288，并使用 `includeThoughts: true` / `thinkingBudget: 4096`。此前上限描述是客户端配置，不是网关最终生效上限。

日志边界：v4.9.0 的 `request_logs.upstream_request_body` 在 `simple` 存储模式下是字段白名单投影，不是最终线缆正文。源码 `payload_audit.rs` 的 `simplify_payload_json` 从 envelope 的 `request` 提取内容，省略 `labels`、`project`、`userAgent`、`requestType` 等字段；handler 记录之后，upstream client 还会执行 sanitize / ensure-user / envelope 对齐。因此上述 system/contents 差异有直接记录支持，但不能说“完整上游只有两项差异”，也不能根据该日志断言没有动态 labels。源码还显示 request ID 的随机 trajectory 可生成 labels，其是否参与 Google 缓存仍未知。参考固定版本 [payload_audit.rs](https://github.com/lbjlaq/Antigravity-Manager/blob/6e8b982aee7e3d3d53501825431142eea9a3f9ab/src-tauri/src/proxy/payload_audit.rs#L418-L455)、[upstream/client.rs](https://github.com/lbjlaq/Antigravity-Manager/blob/6e8b982aee7e3d3d53501825431142eea9a3f9ab/src-tauri/src/proxy/upstream/client.rs#L337-L360)。

同版本原生 Gemini 成功响应的解包、流式转换和收集路径直接保留上游 usage，未发现按本地 system/tools 长度估算 `cachedContentTokenCount` 的逻辑；其他协议的统计修正不能套用到本次原生 Gemini 调用。

进一步只读比较同一 155 秒窗口、同账号、同请求模型/映射模型和同协议的其他成功请求，共 12 条。其中两条记录的 input/cached 分别为 **65,461/60,001（91.66%）** 和 **57,553/24,895（43.26%）**，另外 10 条没有数据库 cached 值。这排除了“该账号或该模型链路普遍只能报告约 4,650 cached token”的解释；但这些不是相同请求的配对对照，不能与合成实验直接计算优化收益。这里使用数据库计数而非重新采集的原始响应，不把缺值当零，也不计算这 12 条的总体命中率。高缓存两条的日志投影分别含 10 个和 1 个带签名 part；另有含 8 个签名 part 却未记录 cached 的请求，因此也不能把签名数量当作命中判据。

固定 v4.9.0 源码进一步解释了会话变化：`gemini_content_anchor` 以首条有效 user 文本、systemInstruction 文本和有序工具名计算 anchor；`SessionScope` 的思维库键始终包含此 anchor，随后参与上游 session ID 的派生。显式稳定会话头可以稳定账号亲和，但不能消除首 user 改变造成的思维库键变化。Google 是否按此 session ID 隔离隐式缓存仍未知。参考 [session_manager.rs](https://github.com/lbjlaq/Antigravity-Manager/blob/6e8b982aee7e3d3d53501825431142eea9a3f9ab/src-tauri/src/proxy/session_manager.rs#L276-L346)、[thinking_store.rs](https://github.com/lbjlaq/Antigravity-Manager/blob/6e8b982aee7e3d3d53501825431142eea9a3f9ab/src-tauri/src/proxy/thinking_store.rs#L1192-L1216)。

签名回填优先使用本轮已有有效签名或精确工具签名；只在最后 model/tool-call 轮次缺少这些签名时，才使用 session 缓存签名。本批每次都从无签名的合成客户端历史重建，所以“旧位置消失、新最后 model 获得签名”并不证明网关删除了客户端已有的有效签名。参考 [thinking_store.rs](https://github.com/lbjlaq/Antigravity-Manager/blob/6e8b982aee7e3d3d53501825431142eea9a3f9ab/src-tauri/src/proxy/thinking_store.rs#L1522-L1755)。

Diana 当前已在单次 Agent 原生工具循环中保留并回灌 `functionCall.thoughtSignature`，换供应商、端点、协议或模型时清除外来续接状态。普通文本 part 的签名未进入通用响应结构；群稳定历史则是公共消息投影，并非 Gemini 原生会话回放，因此不是重启之后才丢签名。本实验也没有将实际生成的回复追加到下一轮，单独增加文本签名保存不会直接改变这批 fixture。完整文本 part 续接涉及正文对应关系、来源隔离和持久化，不在缺少因果证据时顺手扩大本轮改动。

这些只读记录确认了网关的实际改写与会话差异，并排除了普遍 4,650 token 上限，但不足以证明合成请求低命中的完整因果。随后按用户要求完成了下面的最小真实续接对照；没有改网关配置或重启。

### 真实签名冻结请求对照（2026-10-12 00:41—00:42，UTC+8）

使用同一 `gemini-3.8-flash-medium`，共 **4/4 次调用成功，无重试**。从此前新尾部合成 baseline 的客户端日志恢复纯合成请求，保留长历史，替换实验标识，并固定会话头；一次引导后，原样追加实际返回的完整 model content，包括真实签名。该回复包含 `agent_finalize` 调用，测试仅构造明确注明“未发送消息、未执行动作”的合成 functionResponse，再加固定 user 问句；不执行任何真实工具。随后冻结整个请求原样发送 3 次，不追加后续生成结果，不任意移植签名，不固定网关 requestId。

此次是私有诊断脚本直接调用原生 Gemini HTTP 接口，不经过 Diana Runtime/SDK，不能当作新实现的端到端验收。凭据仅在内存中使用；请求及响应仅保存在私有临时目录，不进入仓库。客户端输出上限仍为 1,024，网关投影仍改为 12,288 / thinkingBudget 4096。最多 4 次、无重试、遇错误或限流即停的限制已落实。

| 请求 | 原始 input | 原始 cached | 缓存字段 | 耗时 |
| --- | ---: | ---: | --- | ---: |
| 引导 | 54,269 | unknown | 缺失 | 9,393 ms |
| 冻结 1 | 55,109 | 52,816（95.84%） | 有效正值 | 4,111 ms |
| 冻结 2 | 55,109 | unknown | 缺失 | 10,396 ms |
| 冻结 3 | 55,109 | 52,816（95.84%） | 有效正值 | 4,845 ms |

三次冻结请求的客户端正文 SHA-256 完全相同。容器内只读关联全部 4 条记录，确认同账号、同 session、同请求/映射模型；三次冻结请求的上游日志投影中 systemInstruction、tools、contents 和 generationConfig 分别相同，真实签名也相同，保留字段的差异只有 requestId。网关仍对 model parts 做了归一化：签名由客户端 `contents[101].parts[2]` 出现在投影 `contents[101].parts[1]`，冻结请求的客户端 contents 为 103 条，网关投影为 104 条；这项改写在三次之间稳定。simple 投影不包含完整最终线缆体，不能据此排除未记录的上游路由或元数据差异。

**结论与边界：**

- 同一份长合成历史在真实续接条件下可以得到超过 52k token 的缓存读入，因此不能将前批约 4,650 视为这类历史不可突破的上限。
- 这次既加入了真实签名，也加入了真实 model parts、合成工具响应和新 user 问句，还更换了实验标识、会话头及客户端实现；不是只改变签名的单变量 A/B，不能据此断言签名丢失就是此前低命中的根因。
- 内容、账号和会话均保持稳定的三个冻结请求仍有一次缺失缓存字段。95.84% 只属于两条明确报告缓存的请求，**不是整批命中率，也不是本轮代码优化后的线上命中率**；不丢弃缺值样本后算总体收益，不把缺值当物理零。
- Diana 已保留原生工具循环签名，本次并未修改或验证跨群聊轮次的原生续接实现。因此不能把这次诊断的高缓存直接归功于本轮提示词排列代码，也不据此扩展为未经验证的跨轮签名持久化改造。

本次证明了可获得大范围缓存复用的真实请求条件，同时保留了缓存报告不稳定这一未解问题。没有部署、改生产配置、群消息投递或真实工具执行；4 次调用完成后停止，没有追加请求追求连续高命中。

### 历史验证

2026-09-19 完成并通过：

- `go test ./...`：全部 Go 包通过。
- 定向 `go test -race`：群历史、会话恢复、并发工具发现、跨机器人隔离、重置迟到写入、工具 schema 稳定性以及两种 OpenAI 协议的路由键透传。
- 两项显式启用的真实 API 测试：缓存排列对照、重建 Runtime/Runner 后直接使用已加载工具。
- 同两项真实 API 测试在显式 16,384 token 窗口下再次通过。
- 本次 Go 改动的 `gofmt` 检查及 `git diff --check`。

没有更改依赖、前端或版本号，也没有执行部署或发布。
