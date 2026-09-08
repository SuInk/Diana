# Telegram 成员与头像适配

Diana 的 Telegram 通道使用 HTTP Bot API。群聊 Agent 注册 `diana.group`；
OneBot 保留 `diana.onebot_group`，其他未实现相应能力的平台返回明确的不支持错误。

## 已接入能力

- 群资料和实时人数：`getChat`、`getChatMemberCount`。
- 单个成员身份与权限：`getChatMember`。群主映射为 owner，管理员为 admin；restricted 仅在 is_member=true 时视为仍在群内，left/kicked 不通过校验。
- 成员候选：`getChatAdministrators(return_bots=true)` 与消息、进退群更新、当前群历史中已经观察到的账号。
- 用户头像：`getUserProfilePhotos`，选择最大尺寸后通过 `getFile` 下载。
- 群头像：复用 `getChat` 与服务端群头像下载能力。
- 图片编辑的 sender_avatar、bot_avatar、group_avatar、member_avatar，以及已知成员候选的本地头像图片匹配。

头像以图片字节交给后续处理。带 Bot Token 的下载地址留在通道内部，不作为模型输入或公开图片链接；下载错误不回显 Token，不跟随文件下载重定向。
私聊请求 TG 私有群头像时，除了明确提供负数群 ID，还需核验请求者确实是该群成员。

## 名单与身份边界

Bot API 没有完整成员枚举方法。成员候选结果固定标记 `member_list_complete=false`，
并提供 `member_source`、`member_count_known` 和警告信息。实际群人数与候选数分别返回，
不能把管理员或已知账号候选称为全部成员，也不能宣称已提及所有群友。

`membership_verified=true` 表示本次通过 Telegram 返回的信息核验过；历史中看到过的账号
可能已离群。按账号确认成员关系使用 `member` 操作，不能用昵称、管理员旧缓存或候选列表推断权限。
跨群记忆校验和 TG 群配置权限检查使用实时成员信息；TG 不存在 QQ 群等级，相关等级门槛不应用于 TG。
关系榜单、头像匹配只覆盖可用且可核验的候选，会明确标记为部分结果。

候选缓存按机器人通道隔离，每群最多 500 个账号、最多缓存 256 个群。
订阅 `chat_member` 和 `my_chat_member`，离群时移除候选、机器人退群时清理该群缓存。
匿名管理员的 SenderChat 不作为普通用户账号缓存。更换凭据或 API 服务地址会取消旧轮询，清理缓存、用户名、自身 ID 和更新偏移。

`getChatMember` 查询其他用户时，只有机器人具备群管理员身份才能保证可用；权限不足时报告无法核验，不声称对方不在群内。
完整成员枚举需额外接入 MTProto 的 `channels.getParticipants`，并提供该协议所需配置和权限；本次没有添加 MTProto 登录或枚举。

## 官方接口

- https://core.telegram.org/bots/api#getchatmember
- https://core.telegram.org/bots/api#getchatadministrators
- https://core.telegram.org/bots/api#getuserprofilephotos
- https://core.telegram.org/method/channels.getParticipants
