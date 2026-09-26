# 平台目录能力与边界

群资料、成员和允许的管理动作统一通过 `platform` 工具完成（OneBot 必走此路，其他平台在启用平台接口插件时也走此路）；非 OneBot 平台未启用平台接口时，改由只读的 `group_directory` 查询可用群资料、成员与身份。本地头像匹配在 `platform` 可用时是单独的只读工具 `match_avatar`，否则是 `group_directory` 的 `match_avatar` 操作；同一时刻只会有一个工具能查群成员。Diana 自身的回复设置统一使用 `bot_config`，原 `diana.onebot_group` 入口已删除。底层 GET 参数统一编码并保留已有查询参数，鉴权参数由通道管理。

| 平台 | 已接入 | 必须保留的边界 |
| --- | --- | --- |
| Telegram | 群资料/人数、单成员、管理员与已知候选、用户/群头像 | Bot API 名单不完整，完整枚举仍需另接 MTProto |
| 飞书 | 群资料、分页人类成员、按群列表核验成员、群头像、授权用户头像 | 成员列表不含机器人；自身机器人用调用者在群接口核验；非 open_id 入站标识不混用于查询；内部群与应用通讯录可见范围受权限限制 |
| 钉钉 | 企业内部群资料与分页、成员在群核验、可读取的用户及群头像、自定义角色展示 | 需要当前企业 userId 与授权；外部联系人 senderId 不当作企业 userId；自定义角色名不授予管理员权限，未核验管理权限时仅机器人主人可配置群策略 |
| QQ 官方 | 保留普通群/频道类型及 guild_id；公开子频道所属频道成员、角色与头像 | 普通 QQ 群不能套用 guild API；私密子频道不能凭父频道名单证明可见性；频道接口权限由平台审核决定 |
| 企业微信 | 本应用获授权的应用群资料、成员、群主、通讯录用户头像与应用头像 | 不代表任意普通群或客户群；应用群头像暂无对应实现；通讯录可见范围和应用权限不足时明确报错 |

所有非 OneBot 平台不使用 QQ 群等级，不用过期消息角色授予群管理权限。
名单结果包含 `member_list_complete`、`member_source`、`member_count_known`、`warnings`；分页到达上限、名单范围不含机器人或数量无法对齐时不声明完整。
私聊请求私有群头像必须明确给出群标识并验证请求者在群内。头像只从平台提供的 HTTP(S) 地址读取，拒绝本地文件路径；向图片处理传递图片字节，不传带凭据的 API URL。

## 群管与规则防御

群管写操作对机器人主人、本群群主和群管理员开放，并且要求机器人本身是该群管理员或群主。非主人的身份每次都实时向平台查询，不信消息里的自称或事件缓存；只能管理当前所在的群，私聊里不放开。层级约束：非主人不能对主人和机器人自己动手，群管理员不能对群主或其他管理员动手，群主可以对管理员动手；群公告、精华、全员禁言这类不针对个人的操作群管理员都能用。安全模式下这些操作照旧全部关闭。能力矩阵以 `model/assistant/platform_governance_tool.go` 的 `platformWriteOperationSupport` 为准，测试会逐平台核对。

| 操作 | OneBot v11（NapCat / LLOneBot） | Telegram | 其他平台 |
| --- | --- | --- | --- |
| 禁言 / 解禁 / 踢人 | `set_group_ban` / `set_group_kick` | `restrictChatMember` / `banChatMember` | 不支持 |
| 群公告：发、查、删 | `_send_group_notice` / `_get_group_notice` / `_del_group_notice` | 不支持（没有群公告） | 不支持 |
| 精华：设、取消 | `set_essence_msg` / `delete_essence_msg` | 映射为置顶 `pinChatMessage` / `unpinChatMessage` | 不支持 |
| 群名片 | `set_group_card` | 不支持（没有群名片） | 不支持 |
| 专属头衔 | `set_group_special_title`（仅群主） | `setChatAdministratorCustomTitle`（仅机器人提拔的管理员） | 不支持 |
| 全员禁言开关 | `set_group_whole_ban` | `setChatPermissions` | 不支持 |
| 撤回成员消息（单条或某人最近 N 条） | `delete_msg` | `deleteMessage`（48 小时内） | 不支持 |

规则防御在群配置的「规则防御」里逐群开启，默认全部关闭：刷屏检测（时间窗内条数、重复内容）、违规词拦截（子串或 `re:` 正则，命中即撤回）、阶梯处罚（第一次警告，之后按阶梯禁言）和退群/踢人审计（私聊通知主人）。主人、群管理员不受约束；机器人不是管理员时只记日志不处罚；重连回填的旧消息不处罚。违规计数只在内存里，重启后从零算。

## 接口依据

- 飞书：官方 oapi-sdk-go 的 im/v1 Chat、ChatMembers 和 contact/v3 User；`https://github.com/larksuite/oapi-sdk-go`。
- 钉钉：官方 im_1.0 的 QueryOpenGroupBaseInfo、QueryInnerGroupMemberList、CheckUserIsGroupMember、QueryUserGroupRoles，及企业用户资料接口；`https://github.com/alibabacloud-go/dingtalk`。
- QQ 官方：botgo 的 GuildMembers、GuildMember、Channel 和 User；`https://github.com/tencent-connect/botgo`。
- 企业微信：自建应用 appchat/get、user/get、agent/get；`https://developer.work.weixin.qq.com/document/path/90247`。

这些适配不会自动申请平台权限、升级群类型或把未知身份猜成管理员。接口存在和当前应用已获授权是两回事，生产部署仍需按平台配置权限。
