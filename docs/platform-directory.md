# 平台目录能力与边界

OneBot 按 `onebot-v11` 协议 skill 使用 `diana.onebot_v11` 查询群资料、成员和执行允许的管理动作；其他平台通过只读 `diana.group` 查询可用群资料、成员与身份。`diana.group` 在 OneBot 下仅保留本地头像匹配。Diana 自身的回复设置统一使用 `diana.bot_config`，原 `diana.onebot_group` 入口已删除。底层 GET 参数统一编码并保留已有查询参数，鉴权参数由通道管理。

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

## 接口依据

- 飞书：官方 oapi-sdk-go 的 im/v1 Chat、ChatMembers 和 contact/v3 User；`https://github.com/larksuite/oapi-sdk-go`。
- 钉钉：官方 im_1.0 的 QueryOpenGroupBaseInfo、QueryInnerGroupMemberList、CheckUserIsGroupMember、QueryUserGroupRoles，及企业用户资料接口；`https://github.com/alibabacloud-go/dingtalk`。
- QQ 官方：botgo 的 GuildMembers、GuildMember、Channel 和 User；`https://github.com/tencent-connect/botgo`。
- 企业微信：自建应用 appchat/get、user/get、agent/get；`https://developer.work.weixin.qq.com/document/path/90247`。

这些适配不会自动申请平台权限、升级群类型或把未知身份猜成管理员。接口存在和当前应用已获授权是两回事，生产部署仍需按平台配置权限。
