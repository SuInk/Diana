# DS 多人群聊回放

通过已经启动的本地 Diana、真实 SQLite、入站队列、OneBot WebSocket 适配器和真实 DeepSeek 模型执行合成对话。模型的实际回答和工具结果进入后续历史；不是固定 HTTP 报文重复测试。消息处理、工具轨迹、模型用量和笔记都能在同一个 WebUI 查看。

默认集合 `docs/testing/context-simplification.zh.json` 包含 43 条发言、4 位发言者、2 个群和 4 个场景：

- 开发安排与生活话题交叉、换人追问、安静旁观、历史检索、机器人主人与群主身份区别。
- notebook 写入、跨群共享读取、修改原条目、再次读取；capabilities 查询；后台提取偏好后用 memory 查询。
- 两群近同时提问、同群交叉短消息。取消、接管、静默和失败按实际结果记录。
- 引用旧条件与最新纠正交叉出现、不同人引用同一消息、明确要求多条完整说明，以及点名 Skill 后读取正文再执行。

工具选择由真实模型决定。`expect_tools` 是预期覆盖面，分析报告会列出缺项；不会伪造工具调用或为了通过测试反复采样。记忆为空时不算非空记忆读写验证通过。集合中的 `quality` 是人工复核参考，分析脚本不把关键词出现算成语义通过；还需核对回答是否遗漏限制、同条更新是否误清其他字段、建议是否冒充既定条件，以及实际工具输出和投递记录。

## 在 WSL Debian 中运行

使用独立的 Diana 数据目录与空白数据库启动测试实例，再在「提供商」保存一个可用的、直连 `https://api.deepseek.com` 的配置。不要把生产数据库、QQ 接入连接或生产流量用于基线对照。凭据文件使用测试实例的管理员登录信息，格式为两行 `username: ...`、`password: ...`。脚本不会改变管理员凭据，也不会从其他项目自动读取密钥。

```sh
cd /path/to/Diana
go test -race ./scripts/cache-replay/onebot
go build -o .tmp-dev/cache-replay-onebot ./scripts/cache-replay/onebot
python3 scripts/cache-replay/run.py \
  --base http://127.0.0.1:18081 \
  --credentials .tmp-dev/admin-credentials.txt \
  --source-profile deepseek-cache-audit-responses \
  --simulator .tmp-dev/cache-replay-onebot \
  --database data/diana.db \
  --out .tmp-dev/cache-conversations-$(date +%Y%m%d-%H%M%S)
```

仅选择场景时可加 `--case cross_group`；后续场景依赖前面的安排与笔记，应完整执行后再评估内容正确性。`--max-turns 2 --drain-seconds 0` 可做少量事件冒烟检查，报告会标记覆盖不完整；安静旁观也可能触发后台模型调用。完整运行会等待本次机器人自己的后台任务最多 90 秒，剩余 pending/running 任务照实保留，其他机器人任务不会计入。

默认 `--agent-mode safe`。需要 notebook 写入和 Skill 完整执行时，在隔离实例中使用 `--agent-mode standard`；模式也会写进 manifest。最后一个场景需要在该实例的工作区建立 `.agents/skills/context-checker/SKILL.md`：

```markdown
---
name: context-checker
description: 核验群聊中的条件纠正与引用
keywords: 功率, 引用核验
---

先用 chat_history 查询追风方案的功率变更，按最新明确纠正回答，并保留原问题其他条件。结尾附核验码：青屿-742。
```

对照两个版本时分别编译、使用相同模型和配置、各自空白数据库，顺序执行本集合；用 `--source-revision <提交SHA>` 指明实际运行的二进制版本，不能把当前 checkout 的 SHA 误当成另一实例的版本。`--fixtures` 可显式选择其他集合。引用关系使用同群较早消息的 fixture `id`，测试消息不会带真实 QQ 消息 ID。

脚本通过已鉴权的配置 API 复制模型凭据到本地采样提供商，并创建独立的回放机器人；chat、intent、reply_assist 和 background 显式绑定同一个 DS 配置。不会接管已有机器人。完成后停用自己创建的机器人和模拟端，Diana 后台继续运行，记录保留。不能同时运行两套回放。

模拟端仅监听 `127.0.0.1:19090`，只支持两个虚构群。OneBot 出站只记录并返回本地 ACK，不连接 QQ。未实现的 OneBot 操作明确失败。DS 记录器固定转发到官方 HTTPS 接口，不改变模型请求和响应字节，包括原生工具续接及 SSE；不会记录 Authorization。默认每轮最多 240 个真实 HTTP 请求。运行会消耗该 DS 配置的 API 用量。

## 数据口径

输出目录必须是新目录，防止覆盖上次数据：

- `manifest.json`：源码基线、模型、机器人 ID、是否完整、后台任务和持久化计数。
- `events.json`：每条输入与 Runtime 实际事件结果，包含回复、ACK 和耗时。
- `traces.json`：按内部事件 ID 查询的实际模型和工具轨迹。
- `onebot.jsonl`：模拟平台收到和发送的内容。
- `wire.jsonl`：逐 HTTP 请求、状态、原始 usage、独立的 `cache_field_present` 和传输完成状态。

离线分析不调用模型，可反复执行：

```sh
python3 scripts/cache-replay/analyze.py .tmp-dev/cache-conversations-YYYYMMDD-HHMMSS
```

生成 `analysis.json`（分用途与场景聚合、预期工具缺项、首步提示变化）、`requests.csv`（逐请求）、`joined.json`（原始报文与 Runtime 对应关系）和 `answers.json`（每条提问及实际回答）。匹配使用输入、输出、缓存 token 三元组与时间；报告同时保留未匹配和歧义计数，不能把未匹配记录当作适配器用量一致。

文本 Responses 报文还可以离线估算补充尾部尺寸：

```sh
go run ./scripts/cache-replay/context-size .tmp-dev/cache-conversations-YYYYMMDD-HHMMSS/joined.json
```

该工具只选回复首步，按本集合的消息标记定位“最近历史之后、当前用户消息之前”的补充，包含自动记忆、规则尾部和 Skill 目录，排除上一条回复的持久工具记录。固定 instructions、原生工具定义、历史正文、当前用户原文不计入尾部；instructions 的估算另报 `head_median`。使用 Diana 自身估算器，没有对实际调用施加上限，不能当作供应商 usage 或通用多模态测量。遇到不支持的布局会报错，不能静默把缺失边界算作 0。

`manifest.json` 的 `complete` 只表示所选场景的全部输入已处理完毕，成功、失败、取消及工具缺项仍须分别查看。后台计数按本次 profile 的 session/scope 前缀查询，笔记使用该 profile 的共享笔记范围；不能拿整个数据库的前后差值当作该测试的状态。私有输出含合成对话全文与模型请求，默认放在忽略目录，不把 SQLite 数据库、管理员凭据或 API Key 提交到仓库。

记录器对 gzip 响应先解码副本再读 usage，转发给 Diana 的原始字节保持不变。缓存字段缺失不能算 0；明确报告 0 才算未命中。聚合用 `Σcached / Σinput`，分别看冷启动、连续成员首步、主人权限切换、工具续接、并发群和后台任务。HTTP 400 的兼容降级重试、失败和取消应单列。

这些是接近真实运行链路的合成测试。没有生产流量、真实 QQ 投递、图片/音视频、断网故障注入或整机重启，不足以代替这些场景的验收。缓存没有固定的 95% 合格线，输入长度、动态尾部、工具集合和供应商缓存策略都会影响比例。参见 [DeepSeek 缓存文档](https://api-docs.deepseek.com/guides/kv_cache/)。
