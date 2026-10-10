# 协议操作与回复设置

原先 `diana.onebot_group` 同时负责群查询和修改 Diana 自身设置，且“关闭插话”只写旧版开关。机器人已有 `participation` 配置时，旧字段可能不再决定实际行为，造成回复说关闭了但运行时仍沿用原欲望。

现在删除 `diana.onebot_group`，不提供同名别名。内置 `bot-protocol` skill 区分以下路径：

- 群资料、名单、成员与平台管理统一通过 `platform`：`group_info`、`member_list`、`member_info` 读取，`mute`、`unmute`、`kick` 仅主人可用。普通成员只有只读路径，群管理员不等于机器人主人。
- 非 OneBot 平台未启用平台接口插件时，改用只读 `group_directory` 的 `info`、`members`、`member`。名单是否完整、成员权限及平台能力边界继续由适配器返回。没有支持的平台管理操作不会因添加 skill 自动变得可用。
- 本地头像比较在 `platform` 可用时是只读工具 `match_avatar`，否则是 `group_directory` 的 `match_avatar` 操作。同一时刻只有一个工具能查群成员，避免模型在两个入口之间摇摆。
- 接话和入群欢迎设置、脱敏配置诊断统一通过 `bot_config`。旧 `config` 工具已删除，不提供别名；`llm_config` 继续负责模型分配。

群聊默认 `scope=group`，需要主人或实时核验的群主、管理员；`scope=bot` 只改消息所属机器人的默认设置，仅主人可用。不能在参数中指定其他机器人或其他群。

开启本群内置欢迎使用 `{"operation":"update","welcome_enabled":true}`，关闭时传 `false`。欢迎词、模式、模板池和 LLM 冷却分别使用 `welcome_message`、`welcome_mode`、`welcome_templates`、`welcome_llm_cooldown_seconds`；模板最多 50 条、每条最多 200 字，冷却为 0–86400 秒，0 使用机器人或系统默认值。只更新指定字段，保存成功后返回实际生效的 `welcome` 和 `participation`。重复开启修改同一份配置，不创建任务。明确要求新增入群事件任务时才使用 `event_trigger`。

主人可用 `{"operation":"get","section":"all","scope":"bot"}` 查询完整脱敏诊断，也可选择 `bot`、`runtime`、`llm`、`skills`、`paths`。诊断部分只读且仅主人可用，范围可省略或传 `bot`；默认 `section=settings` 查询或修改实际生效的接话和欢迎设置。群管理员无法借诊断入口读取完整机器人配置、凭据状态或主机路径。

只关闭本群闲聊使用 `{"operation":"update","scope":"group","chat_level":"off"}`；关闭两条主动接话分支时同时设置 `relevance_level=off` 和 `chat_level=off`。已进入直接回复流程的请求不受这两个主动分支开关影响。

`relevance_level` 是回应提问开关，只支持 `on/off`；`chat_level` 支持 `off/minimal/low/medium/high/always`（关/极低/低/中/高/总是），四个评分档位的门槛为 0.90/0.70/0.50/0.30。回复条件为回应提问已开启且当前消息在跟机器人说话，或闲聊分达标且冷却结束。独立可回答门槛已移除，不再提交 `answerability_level` 或 `substance_level`，也不声称修改了该门槛。

闲聊分支按模型评分、档位门槛和冷却放行。评分 JSON 解析失败时自动重试一次，仍解析失败才按未放行处理。总是档位不覆盖停止或重复循环；`cooldown_seconds` 为 0–3600，0 只表示关闭闲聊冷却。旧 `extreme` 闲聊档位按 `high` 读取，旧相关度档位除 `off` 外都按 `on` 读取。

旧调用的 `desire_level=off` 同时关闭两条接话分支。历史数值门槛仍保留在配置中，但不控制当前接话判定。修改欲望不会清空其他设置和冷却。更新时复制当前实际生效设置，只修改指定字段，保存成功后返回实际 `participation`；失败会明确报错，不以口头承诺代替配置操作。其他群和机器人默认设置保持各自范围。

## 屏蔽某个人

「以后别理他」不是调低插话欲望，也不是平台禁言，走 `reply_block`：`block` 加入屏蔽名单，`unblock` 移出，`list` 查看。被屏蔽的人此后连主动回复判断都不会进（那一次评分模型调用同样省掉），直到有人解除，没有时限——和 30 分钟的自动响应限制是两回事。

群聊默认 `scope=group`，需要主人或实时核验的群主、管理员；`scope=bot` 对本机所有群和私聊生效，仅主人可用。返回里的 `blocked_users` 是本层自己加的，`inherited_blocked_users` 是从机器人级并下来的：本群同样不回他们，但只有主人能用 `scope=bot` 解除。

`user_id` 只接受账号 ID，取自 @ 的结构化信息、被引用消息的发送者或成员查询，不按昵称猜。屏蔽主人和机器人自己一律拒绝；重复屏蔽不报错，只回报当前状态。保存成功才报告生效。

群里第一次屏蔽人时会为本群新建一份门禁，等级门槛和回复时段按机器人级原样抄一份——否则「少回一个人」会顺手把这个群的门槛全部清零。屏蔽本身不调用任何平台禁言接口，也不撤回消息。被屏蔽的人的消息仍然进群聊上下文，事件页记为 `ignored_user_blocked`，理由写明「该用户已被屏蔽」。

Skill 只是操作指引。权限、平台动作白名单和配置保存由 Go 后端执行，模型不能通过调用旧工具名、改参数或换工具绕过。
