# Telegram 媒体与撤回说明

## 图片与表情包

单张图片和表情包不再统一调用 `sendPhoto`：

- 本地 GIF 按文件内容识别，通过 `sendAnimation` 发送，保留动画；缓存文件没有扩展名或扩展名不正确也可以识别。
- 本地静态 WebP 在大小不超过 512 KiB、一边为 512 像素且另一边不超过 512 像素时使用 `sendSticker`；其他 WebP（包括动画 WebP）通过 `sendDocument` 发送，保留原始文件。
- 本地静态图片超过照片的 10 MiB、宽高之和 10000 或长宽比 20 的限制时，改为文件发送。上传仍受现有 50 MiB 限制约束。
- 远程 URL 根据路径扩展名识别 GIF 和 WebP，查询参数不影响判断。GIF 使用动画接口，WebP 使用贴纸接口，远程文件须满足 Telegram 对应接口的格式、尺寸与访问限制。没有格式信息的 URL 和不透明的图片 `file_id` 仍使用照片接口。
- 单张图片发送保留话题和回复目标；回复目标已删除时仍允许发送。普通照片、视频和音频的发送接口保持不变。多图相册仍仅支持照片，不支持将动画或贴纸混入相册。

这里修复的是出站格式选择，不会把收到的动画贴纸缩略图还原成原始 TGS/WebM 动画，也不会绕过 Telegram 的上传限制。

接口约束：[sendPhoto](https://core.telegram.org/bots/api#sendphoto)、[sendAnimation](https://core.telegram.org/bots/api#sendanimation)、[sendSticker](https://core.telegram.org/bots/api#sendsticker)。

## 群消息撤回

Telegram Bot API 的普通群和超级群消息更新没有删除通知。即使机器人是管理员，也不能通过增加轮询事件类型来实时监听群成员删除消息。

用户调用 `chat_history(operation="recalls")` 时，Diana 会改为做一次有限的主动探测：

1. 先检查触发本轮查询的当前消息，确认机器人档案、群和接口仍然可用。这个安全校验失败时不会继续，避免把连接、权限或路由错误批量误判成删除。
2. 从本群、本机器人档案最近 24 小时的本地缓存中选取带有效 `message_id` 和正文或媒体内容的成员消息。默认最多 20 条，`limit` 可提高到 50 条；机器人自己的消息、已知不可访问记录、空服务消息和窗口外消息不参与。首次按最新优先检查，后续查询会轮转到尚未检查或最久未检查的候选；30 秒内刚检查过的消息不会重复请求。
3. 对每条候选调用 `setMessageReaction`，传入空的 `reaction=[]`。成功表示查询时消息仍可访问；只有 Telegram 明确返回 `400 message to react not found` 才记录为“查询时不可访问”。其他 400、权限错误、旧 Bot API 不支持、网络错误和服务端错误都保持“状态未知”。遇到 429 限流会立即停止剩余探测，并在工具结果中保留 `retry_after`。

同一机器人账号的探测会跨群串行，并对连续 Bot API 请求限速；发生限流、连接、权限或服务端错误后会进入账号级冷却，期间不重复探测。一个 Bot token 配置到多个 Diana 档案时，历史原文仍按档案严格隔离，API 请求则按机器人账号共用串行与冷却状态。

探测确认的原文来自本地缓存。默认的“原文转发”模式会沿正常 Telegram 发送链路把完整文本和可用媒体发回当前会话；若配置为只发送模型摘要，则只在摘要中引用缓存内容。记录时间是 Diana **发现消息已不可访问的时间**，不是实际删除时间；Bot API 也不会说明是发送者、管理员、自动删除计时器还是其他原因造成。零命中只表示这次有限候选中没有确认当前不可访问，不能据此断言群里没有发生撤回。

`reaction=[]` 不会发送新消息或新增表情，但会清除这个机器人自己已经挂在目标消息上的表情回应。Diana 当前没有主动设置 Telegram reaction 的功能，因此通常没有可见变化；若同一 Bot token 被其他程序用于贴表情，探测可能清掉那个表情。

**缓存的原文仍然可以单独查询。** Diana 已收到并保存的消息，可以通过 `recent`/`range` 查询相关时段，`search` 按关键词查找，或 `around` 按消息 ID 读取。机器人离线、隐私模式下未收到、已清理、24 小时窗口外或超过单次条数上限的消息无法由本次探测确认。

`deleted_business_messages` 只属于已关联 Business 账号的消息删除事件，并不适用于普通群。本次不需要 Business 账号或 MTProto 客户端。OneBot 的实时撤回通知、记录、查询和转发行为不变。

接口与实现依据：[Update](https://core.telegram.org/bots/api#update)、[setMessageReaction](https://core.telegram.org/bots/api#setmessagereaction)、[Telegram Bot API 服务端的消息检查流程](https://github.com/tdlib/telegram-bot-api/blob/master/telegram-bot-api/Client.cpp)。
