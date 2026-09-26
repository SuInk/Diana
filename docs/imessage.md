# iMessage 接入（BlueBubbles）

Diana 不直接和 Apple 通信，而是通过 [BlueBubbles Server](https://bluebubbles.app) 收发 iMessage。BlueBubbles 跑在一台登录了 Apple ID 的 Mac 上，把 Messages.app 包成 REST 接口，新消息再以 webhook 推给 Diana。

## 需要准备什么

- **一台常开的 Mac**：登录用来当机器人的 Apple ID，Messages.app 能正常收发。Mac 休眠、注销或 Messages 掉线时 Diana 收不到也发不出。
- **BlueBubbles Server**：按[官方安装指南](https://docs.bluebubbles.app/server/)装好，在设置里设一个服务器密码，记下它监听的地址（例如 `http://192.168.1.10:1234`）。Diana 要能访问到这个地址；不在同一个局域网时，用 BlueBubbles 自带的 Cloudflare / ngrok 代理地址或自己的反向代理。
- **Private API（可选）**：不开也能收发文本和附件，发送走 AppleScript。开了（需要按 BlueBubbles 文档关闭 SIP 并安装 helper）之后 Diana 才会用 `selectedMessageGuid` 做引用回复；没开时引用回复自动退回普通发送，不会因此发不出去。

## 在 Diana 里配置

在「机器人」页新建机器人，平台选 **iMessage**：

| 字段 | 说明 |
| --- | --- |
| BlueBubbles 服务器地址 | 例如 `http://192.168.1.10:1234`，必须以 `http://` 或 `https://` 开头 |
| 服务器密码 | BlueBubbles Server 设置里的密码，REST 请求以 `?password=` 携带 |
| Webhook 密钥（可选） | webhook 回调要带的 `?token=`，留空时用服务器密码 |
| 轮询兜底（秒） | 留空只用 webhook；填写时按间隔调用 `POST /api/v1/message/query` 拉新消息，最短 5 秒 |

填完点「测试连接」，Diana 会请求一次 `GET /api/v1/server/info`，显示 BlueBubbles 版本、登录的 iMessage 账号和 Private API 是否可用。

## 配置 webhook

BlueBubbles 的 webhook 不签名、也不带凭据，所以 Diana 要求回调地址里带一个密钥。在 BlueBubbles Server 的 **API & Webhooks** 页添加：

```
http(s)://DIANA_HOST/api/channels/imessage/callback?token=<Webhook 密钥或服务器密码>
```

事件至少勾选 **New Messages**。`DIANA_HOST` 是那台 Mac 能访问到的 Diana 地址，不一定要公网——Mac 和 Diana 在同一局域网时填内网地址即可。同一平台配了多台机器人时，在路径后加配置档 ID：`/api/channels/imessage/callback/<profile-id>?token=...`。

Mac 访问不到 Diana（例如 Diana 在内网、Mac 在别处）时，可以不配 webhook，改开「轮询兜底」。两条路同时开也不会重复回答，消息按 guid 去重。

## 行为说明

- **会话映射**：chat guid 形如 `iMessage;-;+8613800000000` 的是私聊，`iMessage;+;chat123…` 的是群聊，群号就是完整的 chat guid。发送者账号是 handle（手机号或邮箱），进入模型前和 QQ 号一样会被隐私代理换成别名。
- **过滤**：自己发出的消息（`isFromMe`）、tapback 回应和改群名之类的系统项不会触发回复。
- **群聊点名**：iMessage 没有 @机器人 的结构化提及，群里是否接话由群触发词和主动回复判断决定。
- **收**：文本、图片、视频、语音和文件；附件通过 `GET /api/v1/attachment/:guid/download` 下载到本地媒体缓存，下载地址带着密码，不会进入事件记录。
- **发**：文本走 `POST /api/v1/message/text`，图片、视频、音频和文件走 `POST /api/v1/message/attachment`；私聊回到入站时的那个会话（SMS 进来就回 SMS）。
- **不支持**：踢人、禁言等群管理操作（iMessage 本身没有），Markdown 渲染。tapback、标记已读等可以通过平台接口透传 BlueBubbles 的 REST 接口，需要 Private API。

协议依据：[BlueBubbles REST API 文档](https://docs.bluebubbles.app/server/developer-guides/rest-api-and-webhooks)，以及 [bluebubbles-server](https://github.com/BlueBubblesApp/bluebubbles-server) 源码中的 `httpRoutes.ts`、`messageValidator.ts`、`MessageSerializer.ts` 和 `webhookService`。
