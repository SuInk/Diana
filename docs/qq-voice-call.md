# QQ 语音通话

主人用 QQ 给机器人打语音电话，机器人自动接起来实时对话。通话用的是和 WebUI 电话按钮同一套引擎（[实时语音通话协议](voice-call.md)）：人设、记忆、私聊记录都和主人的 QQ 私聊是同一份，回复只念出来，不发 QQ 文字。

第一版只接主人的电话，也不会主动拨号。

## 为什么要这样绕

QQ 的语音是闭源的音视频 SDK，协议不开放，没法自己写一个客户端去打电话。能做的只有让真正的 QQ 客户端去接，再把声音从中间掏出来。SnowLuma 容器里跑的正好是真的 Linux QQ（3.2.x，带 `avsdk` 语音组件），它的音频走 PulseAudio，所以在容器里建两条虚拟声卡就能接上：

```
对方说话 → QQ 扬声器 → qq_speaker（虚拟）→ qqcall-agent 录音 → Diana 转写、回复、合成
机器人说话 ← QQ 麦克风 ← bot_mic（虚拟）← qqcall-agent 播放 ← Diana
```

`qqcall-agent` 在容器里管窗口和声卡，和镜像一起在单独的仓库 [SuInk/snowluma-voice](https://github.com/SuInk/snowluma-voice) 维护，Diana 的「QQ 语音通话」插件（`model/assistant/qq_voice_call_plugin.go`）连着它决定接不接、对话。两者之间的协议在 `model/qqcall`。

## 一通电话的流程

1. 有人打来，容器里弹出标题为「语音通话」的窗口。agent 截下昵称那一块做 OCR：有「邀请你语音通话」字样才算来电（自己拨出去的不管），把昵称报给 Diana。
2. **第一道核对**：昵称要和主人在机器人 QQ 上的好友备注或昵称完全一致（不计大小写和空格）。不一致就不接，让它响到超时。
3. 核对通过，等「响铃多久后接听」设定的秒数，让 agent 点接听。
4. 接通后 QQ 开始从虚拟麦克风采集，agent 据此判断接通，并从 SnowLuma 日志里读对方的 QQ 号：接通那一刻 QQ 会推一条 `msgType=529/11` 的私聊，SnowLuma 解不出内容，只记一行日志，`from` 就是对方。
5. **第二道核对**：这个 QQ 号必须是主人。不是主人，或者 6 秒内读不到，立刻挂断。
6. 开始对话。对方的声音按 20ms 一帧送进通话引擎，静音分句、转写、回复、逐句合成，合成好的一句转成 PCM 送回 agent 播给 QQ。
7. 任何一方挂断，通话窗口关闭，会话结束。超过「单次通话最长」自动挂断。

响铃时 QQ 不推任何带 QQ 号的东西，所以只能「先按昵称接、接通后按 QQ 号验」。被冒用昵称的人最多会被接通一两秒，然后挂断，机器人不会对他说话。

## 部署

### 1. 换用带语音组件的 SnowLuma 镜像

```bash
docker pull ghcr.io/suink/snowluma-voice:latest
```

它在原版 SnowLuma 上加了 PulseAudio、QQ 语音插件缺的 `libpulse-mainloop-glib0`、xdotool、tesseract 和 `qqcall-agent`，标签和上游 SnowLuma 版本一致。容器参数照旧，再加环境变量 `QQCALL_TOKEN`（和下面插件里的令牌一致）和端口 6190（只给 Diana 访问）。详细参数、按钮坐标等 agent 环境变量见 [snowluma-voice 的 README](https://github.com/SuInk/snowluma-voice#readme)。

**换镜像要重建容器，QQ 会重新登录**；数据卷保留时一般能快速登录，不行就扫码。

### 2. 打开插件

插件页「QQ 语音通话」默认关闭。打开后填：

| 设置 | 说明 |
| --- | --- |
| qqcall-agent 地址 | `ws://127.0.0.1:6190/ws`；同一个 compose 里写 `ws://snowluma:6190/ws` |
| qqcall-agent 令牌 | 和 `QQCALL_TOKEN` 一致 |
| 接电话的机器人 | 容器里登录的那个 QQ 对应的机器人 ID；留空用启用了插件的第一台 OneBot 机器人 |
| 响铃多久后接听 | 默认 2 秒 |
| 单次通话最长 | 默认 30 分钟 |
| 允许打断 | 默认关，见下 |

机器人要填好主人账号，模型分配里要配好「语音识别」和「语音合成」，和 WebUI 打电话的前提一样。

## 已知限制

- **没有回声消除**：虚拟声卡不经过系统的回声消除。对方外放时，机器人自己的声音可能经对方手机回传过来，被当成对方开口把自己打断，所以「允许打断」默认关：机器人说话时对方的声音直接丢掉。对方戴耳机时可以打开。
- **不能重启 QQ**：重启 QQ 进程会掉登录，要重新扫码。agent 从不碰 QQ 进程；Linux QQ 也不认 `--remote-debugging-port`，没法走 DevTools 读界面。
- **靠界面和日志**：昵称靠 OCR，接通靠「QQ 开始采集麦克风」，对方 QQ 号靠 SnowLuma 的一行警告日志。QQ 或 SnowLuma 升级改了其中任何一样都会失效；失效时插件只会不接或立刻挂断，不会接错人。
- **一个容器只有一通**：QQ 同一时间只能有一个通话窗口。
