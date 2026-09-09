# 协议操作与回复设置

原先 `diana.onebot_group` 同时负责群查询和修改 Diana 自身设置，且“关闭插话”只写旧版开关。机器人已有 `participation` 配置时，旧字段可能不再决定实际行为，造成回复说关闭了但运行时仍沿用原欲望。

现在删除 `diana.onebot_group`，不提供同名别名。内置 `bot-protocol` skill 区分以下路径：

- OneBot 群资料、名单、成员与平台管理通过 `onebot-v11` skill 调用 `diana.onebot_v11`。读取当前群资料和成员时可省略 group_id；需要其他群时必须提供真实 ID。普通成员只有既有只读白名单，写操作和未知扩展仍仅主人可用。
- Telegram 等其他平台使用只读 `diana.group`。名单是否完整、成员权限及平台能力边界继续由适配器返回。没有支持的平台管理操作不会因添加 skill 自动变得可用。
- `diana.group` 的 `match_avatar` 保留本地头像比较；在 OneBot 上只开放这一项，其他查询转协议工具。
- 相关度、闲聊、可回答门槛和主动闲聊冷却通过 `diana.bot_config` 读取或局部修改。`diana.config` 仍仅用于主人的脱敏运行配置查询，`diana.llm_config` 继续负责模型分配。

群聊默认 `scope=group`，需要主人或实时核验的群主、管理员；`scope=bot` 只改消息所属机器人的默认设置，仅主人可用。不能在参数中指定其他机器人或其他群。

只关闭本群闲聊使用 `{"operation":"update","scope":"group","chat_level":"off"}`；关闭两条主动接话分支时同时设置 `relevance_level=off` 和 `chat_level=off`。已进入直接回复流程的请求不受这两个主动分支开关影响。

`relevance_level`、`chat_level`、`answerability_level` 均支持 `off/minimal/low/medium/high/extreme/always`（关/极低/低/中/高/极高/总是），数值门槛为 0.90/0.70/0.50/0.30/0.10。回复条件为可回答分达标，并且相关度达标或闲聊分达标且冷却结束。可回答门槛选关表示不检查质量分；总是跳过对应分数门槛，但不覆盖停止或重复循环。`cooldown_seconds` 为 0–3600，0 只表示关闭闲聊冷却。

旧调用的 `desire_level=off` 同时关闭两条新评分分支；旧 `substance_level` 保留原字段并映射至最近的新可回答门槛档位。新调用应使用三个独立等级字段。

修改欲望不会清空评分门槛和冷却。更新时复制当前实际生效设置，只修改指定字段，保存成功后返回实际 `participation`；失败会明确报错，不以口头承诺代替配置操作。其他群和机器人默认设置保持各自范围。

Skill 只是操作指引。权限、平台动作白名单和配置保存由 Go 后端执行，模型不能通过调用旧工具名、改参数或换工具绕过。
