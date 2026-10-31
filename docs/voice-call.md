# 实时语音通话协议

和机器人实时语音通话走一条 WebSocket。WebUI 里的「语音通话」页和以后的手机 App 都按这份协议对接，服务端不区分客户端。

WebUI 的入口在「聊天」页右上角的电话按钮。一通电话在机器人那一侧就是**主人发来的私聊**，和聊天页里打字是同一段私聊：人设、记忆、历史都和主人在这个平台上的私聊是同一份，通话里说的话挂断后在私聊记录里也看得到。回复不会发到 QQ 或 Telegram 上，只在通话里念出来。

代码位置：

| 位置 | 内容 |
| --- | --- |
| `model/voicecall` | 会话逻辑：静音分段、整句转写、逐句合成、打断。和传输层、机器人内部都无关 |
| `model/assistant/web_chat.go` | 聊天页和通话共用的「主人私聊」：造事件、走回复流程、把回复交给页面、读私聊记录 |
| `model/assistant/voice_call.go` | 机器人一侧：语音识别插槽转写、按私聊走回复流程、语音合成插槽合成 |
| `webui/voice_call.go` | 鉴权、升级 WebSocket、收发帧 |
| `webui/web_chat.go` | 聊天页的接口：读私聊记录、发一句话流式收回复 |
| `frontend/src/views/ChatView.vue` | QQ 式聊天页：机器人列表、私聊气泡、电话按钮 |
| `frontend/src/components/VoiceCallScreen.vue` | 全屏通话界面 |

## 前提

- 已登录 WebUI。鉴权靠登录 Cookie（`/api/auth/login` 拿到的会话），和其他 `/api/*` 接口一样；浏览器里只接受同源连接。App 先调登录接口，带着 Cookie 连 WebSocket。
- 登录 WebUI 的人就是主人，所有机器人都能打：填了主人账号就用它，和平台上的私聊是同一段记录；Telegram 主人填的是 @用户名时用主人说过话后记下的数字 ID，还没记下就拿用户名当账号；没填主人时用 `webui-owner` 单独一段记录，同样按主人对待。
- 模型分配里配了「语音识别」和「语音合成」两个插槽。

连之前可以先问一下：

```
GET /api/assistant/call?profile=<机器人 ID>
```

```json
{"bot_id":"default","bot_name":"Diana","platform":"onebot-v11","stt":true,"tts":true}
```

`problem` 非空时打不了电话，内容是给人看的原因（「模型分配里还没配语音识别」这类）。

## 连接

```
GET /api/assistant/call/ws?profile=<机器人 ID>   (WebSocket)
```

不满足前提时不升级，直接回 HTTP 409 和 `{"error": "原因"}`。

帧分两种：

- **文本帧**：JSON 控制消息和事件，见下文。
- **二进制帧**：音频。上行是用户的声音，下行是合成好的回复。

服务端每 20 秒发一次 ping；60 秒没收到任何东西（包括 pong）就断开。

## 上行

### start

连上后第一条必须是 `start`，在此之前发的别的消息都会回 `not_started` 错误。

```json
{"type":"start","format":"pcm_s16le","sample_rate":16000,"segmentation":"server_vad","barge_in":true}
```

| 字段 | 说明 |
| --- | --- |
| `format` | 上行音频格式。`pcm_s16le`（16 位小端单声道裸 PCM，默认）、`webm`、`ogg`、`mp4`、`wav` |
| `sample_rate` | 只对 `pcm_s16le` 有意义，8000–48000，默认 16000。建议客户端自己降到 16000 |
| `segmentation` | 谁来判断「一句说完了」。`server_vad`：服务端听静音自动分句（默认）；`client`：客户端说完一句发 `commit`。只有 `pcm_s16le` 能用 `server_vad`，其他格式一律按 `client` |
| `barge_in` | 播报中用户开口时是否打断，默认 `true`。关掉后播报期间上行的声音直接丢弃，适合没有回声消除的外放设备 |

服务端回 `ready`，然后是 `state: listening`。格式不支持时回一条 `fatal` 的 `error` 并关闭连接。

### 音频（二进制帧）

- `pcm_s16le`：持续上行，每帧 20–100ms 为宜。服务端分句时一直开着麦就行，静音也照常发，服务端靠它判断说完没有。
- 客户端分句（按住说话）：只在用户说话时上行，说完发 `commit`。压缩格式可以分多帧发同一个文件的连续片段，也可以说完一次性发一帧。

一帧最大 1 MiB，一句话的音频最多 8 MiB，超了回 `utterance_too_long` 并丢掉这句。

### commit

```json
{"type":"commit"}
```

这一句说完了，送去转写。服务端分句模式下发它会把正在进行的那句立即切出来（比如用户点了「说完了」）。

### interrupt

```json
{"type":"interrupt"}
```

停止当前这一轮：正在生成的回复、还没合成的句子、还没发完的音频都作废。客户端应同时停掉本地正在播放的音频。

### text

```json
{"type":"text","text":"今天天气怎么样"}
```

打字说一句，跳过语音识别，其余和说话一样。调试和网络差时用。

### hangup

```json
{"type":"hangup"}
```

挂断。服务端停掉进行中的一轮并关闭连接。直接断开 WebSocket 效果相同。

## 下行

所有事件都是 `{"type": ...}` 的 JSON，和某一轮对话有关的带 `turn`（从 1 递增），和某一句回复有关的再带 `seq`（每轮从 1 递增）。

| type | 字段 | 含义 |
| --- | --- | --- |
| `ready` | `ready{call_id, bot_id, bot_name, format, sample_rate, segmentation, barge_in}` | 通话建立，回显实际生效的参数 |
| `state` | `state` | `listening` 等用户说 / `thinking` 转写和生成回复中 / `speaking` 在下发合成的音频 |
| `speech` | `event` | 服务端分句模式下的检测结果：`start` 用户开口，`end` 一句说完，`discarded` 太短被丢弃（咳嗽、碰麦） |
| `transcript` | `turn, text` | 用户这一轮说的话（转写结果） |
| `reply` | `turn, seq, text` | 一句回复的文字。总在这一句的音频之前到 |
| `audio_start` | `turn, seq, mime` | 这一句的音频开始，后面跟若干二进制帧 |
| （二进制帧） | | 音频数据，属于最近一个 `audio_start` |
| `audio_end` | `turn, seq` | 这一句的音频发完了 |
| `interrupted` | `turn` | 这一轮被打断，客户端应立即停止播放 |
| `turn_end` | `turn, reason` | 这一轮结束，见下表 |
| `error` | `code, text, fatal, turn?, seq?` | 出错。`fatal` 为真时服务端随后会关闭连接 |

`turn_end.reason`：

| reason | 含义 |
| --- | --- |
| `done` | 正常回完 |
| `interrupted` | 被打断（用户开口或发了 `interrupt`） |
| `merged` | 这一句还没进回复流程，用户又接着说了；它的文字会并进下一轮的 `transcript`。客户端可以不显示这一轮 |
| `no_speech` | 没听出字 |
| `no_reply` | 机器人决定不回（比如命中了回复规则） |
| `failed` | 转写或回复失败，前面会有一条 `error` |

`error.code`：`not_started`、`already_started`、`unsupported_format`、`bad_message`、`unknown_type`、`utterance_too_long`、`stt_failed`、`reply_failed`、`tts_failed`。`tts_failed` 只影响那一句的声音，文字已经下发，通话继续。

### 音频格式

下行音频是语音合成插槽配置的格式；配的是浏览器和手机放不了的 `pcm`、`silk`、`amr` 时改用 `mp3`。`mime` 以 `audio_start` 里给的为准（`audio/mpeg`、`audio/wav`、`audio/ogg`……）。

OpenAI 兼容接口和 ElevenLabs 是边合成边下发的，一句会分成多个二进制帧；其他各家合成完整句后一帧发出。无论哪种，`audio_start` 到 `audio_end` 之间的帧拼起来就是一个完整的音频文件。客户端可以边收边播（MSE、原生流式播放器），也可以一句收齐后解码播放（网页测试页就是这样）。

## 时序

```
客户端                                   服务端
  │── start ─────────────────────────────▶│
  │◀──────────────────────── ready, state:listening
  │── PCM PCM PCM … ─────────────────────▶│  speech:start
  │── PCM PCM（静音）… ──────────────────▶│  speech:end
  │◀────────────────── state:thinking, transcript(1)
  │◀────────────────── reply(1.1)
  │◀────────────────── state:speaking, audio_start(1.1), [音频…], audio_end(1.1)
  │◀────────────────── reply(1.2), audio_start(1.2), [音频…]
  │── PCM（用户开口）───────────────────▶│  speech:start
  │◀────────────────── interrupted(1), turn_end(1, interrupted), state:listening
  │   （客户端停止播放）                    │
```

## 行为说明

- **一次一轮**：回复流程一次只跑一轮。被打断的那一轮收完尾，下一轮才进回复流程；转写不受影响，可以提前做。
- **打断即作废**：`interrupted` 之后，这一轮不会再有任何下行，包括已经合成好还没发的音频。
- **接着说**：服务端分句时，人停顿 0.7 秒就算一句说完。如果这一句还在转写、还没进回复流程时用户又开口了，它不会被丢掉，而是并进下一句一起回（`turn_end: merged`）。已经进了回复流程的那一轮照常被打断，那句话留在私聊记录里，模型下一轮能看到。
- **逐句合成**：回复流程每发出一条消息就交给通话；通话按句末标点切句，一句合成完就下发，不等整段回复写完。太短的句子（不到 6 个字）并到下一句，太长的（80 字以上）在逗号处再切。Markdown 记号和链接地址不念，`reply` 里的文字保留原样。
- **通话语境**：通话的每一轮都会给模型加一段提示，要求像打电话一样口语化地说、一次两三句、不用 Markdown 和表情；`tts` 工具和常驻语音模式在通话里不生效（回复本来就会被念出来）。
- **私聊准入不管通话**：机器人设置里的「私聊准入」管的是平台上谁能私聊机器人，默认仅主人。显式关闭时连主人也拦。通话只有登录了 WebUI 的人打得进来，不受它限制。
- **句号保留**：聊天回复平时会去掉句号（读起来不像公文），通话里保留，切句和合成停顿都靠它。
- **回复的出口**：回复在发送的最后一步交给通话，不发到 QQ 或 Telegram；输入状态、Telegram 草稿流、合并转发卡片在通话里都跳过。机器人的平台连接不需要在线。
- **打断后的记录**：被打断时，回复的文字可能已经整段写进私聊记录，但用户只听到了一部分。

## 网页通话界面

「聊天」页右上角的电话按钮，打给正在聊的那台机器人。点了直接全屏呼叫，界面照手机通话做：头像、名字、通话时长，中间是最近两轮的实时字幕，底下是静音、对讲（按住说话）、打字和挂断。

- 默认自动检测说完（服务端分句），直接说话就行。机器人说话或思考时字幕下面有「轻点打断」，开口也会打断。
- 「对讲」通话中随时切换，开了以后出现「按住说话」大按钮：只在按住时上行，松开发 `commit`。外放没有回声消除时用它，机器人的声音不会被当成开口。网页总以 `server_vad`、`barge_in: true` 开始，这两项都在本地控制上行实现，不用重连。
- 「静音」时照常上行静音帧，服务端靠它判断说完没有。
- 手机浏览器只在 HTTPS 下允许用麦克风；本机调试用 `localhost` 可以。播放用的 AudioContext 和麦克风授权在点电话按钮时就发起，手机浏览器只认用户手势里的。
- 采集用 Web Audio 拿 48kHz 麦克风输入，降到 16kHz 的 `pcm_s16le` 上行；下行一句收齐后用 `decodeAudioData` 解码，按顺序排着播放。
- 完整的对话记录在机器人的私聊记录里，通话界面只显示字幕。
- 挂断后聊天页会刷新私聊记录，通话里说的话和回的话都在里面。

## 聊天页

侧栏「日常 → 聊天」，照 QQ 做：左边是机器人列表（头像、名字、最后一条消息），右边是和选中那台的私聊；手机上先看列表，点进去聊，返回键回列表。这里聊的是机器人本人（人设、记忆），不是「管理对话」里那个替你干活的管理助手。

接口在 `/api/assistant/chat`，和通话一样要登录 WebUI，主人身份的认法和通话一样（见「前提」），所有机器人都能聊：

```
GET  /api/assistant/chat?profile=<机器人 ID>
→ {"info": {"bot_id", "bot_name", "platform", "problem?"}, "messages": [{"id", "role": "user|assistant", "text", "images?", "at"}]}

POST /api/assistant/chat   {"profile": "<机器人 ID>", "text": "在干嘛"}
→ 按行流式返回（application/x-ndjson）：
  {"type":"message","message":{...}}   每发出一条回复推一条
  {"type":"heartbeat"}                 每 15 秒一次
  {"type":"done","replies":1}          结束；出错时是 {"type":"done","error":"原因"}
```

- 记录是主人和这台机器人的私聊，平台上（比如 QQ 私聊）聊过的、通话里说的都在里面，最多返回最近 200 条。
- 回复不发到平台上，输入状态、草稿流、合并转发和 `tts` 工具都跳过；私聊准入同样不拦。回复里的图片只保留 http(s) 地址，本地文件和 base64 图片在页面上不显示。
- 和通话不同，打字聊天不加口语化提示，也照常去掉句号，和平台上的私聊一样。
