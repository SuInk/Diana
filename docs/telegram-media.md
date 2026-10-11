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

Telegram Bot API 的普通群和超级群消息更新没有删除通知。即使机器人是管理员，也不能通过增加轮询事件类型来监听群成员删除消息。

因此 `chat_history(operation="recalls")` 在 Telegram 群聊中明确说明无法自动识别撤回消息，不会把空结果描述为“最近没有消息被撤回”，并引导查询本地缓存。

**缓存的原文仍然可以读。** Diana 已收到并保存的消息，可以通过 `recent`/`range` 查询相关时段，`search` 按关键词查找，或 `around` 按消息 ID 读取。用户提供大致时间、发送者、内容等线索后，可据此查找缓存；没有线索时先询问，不把普通历史消息猜成撤回消息。缓存保留范围取决于实际存储和清理配置，未收到或未保存的内容无法补回。历史里保留的消息也不代表该消息现在仍存在于 Telegram。

`deleted_business_messages` 只属于已关联 Business 账号的消息删除事件，并不适用于普通群。本次没有引入 Business 账号接入或 MTProto 客户端。OneBot 的撤回事件记录、查询及转发行为不变。

官方事件定义：[Update](https://core.telegram.org/bots/api#update)、[BusinessMessagesDeleted](https://core.telegram.org/bots/api#businessmessagesdeleted)。
