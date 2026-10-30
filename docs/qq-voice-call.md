# QQ 语音通话

主人用 QQ 给机器人打语音电话，机器人自动接起来实时对话。通话用的是和 WebUI 电话按钮同一套引擎（[实时语音通话协议](voice-call.md)）：人设、记忆、私聊记录都和主人的 QQ 私聊是同一份，回复只念出来，不发 QQ 文字。

第一版只接主人的电话，也不会主动拨号。

## 为什么要这样绕

QQ 的语音是闭源的音视频 SDK，协议不开放，没法自己写一个客户端去打电话。能做的只有让真正的 QQ 客户端去接，再把声音从中间掏出来。SnowLuma 容器里跑的正好是真的 Linux QQ（3.2.x，带 `avsdk` 语音组件），它的音频走 PulseAudio，所以在容器里建两条虚拟声卡就能接上：

```
对方说话 → QQ 扬声器 → qq_speaker（虚拟）→ qqcall-agent 录音 → Diana 转写、回复、合成
机器人说话 ← QQ 麦克风 ← bot_mic（虚拟）← qqcall-agent 播放 ← Diana
```

`qqcall-agent`（`cmd/qqcall-agent`）在容器里管窗口和声卡，Diana 的「QQ 语音通话」插件（`model/assistant/qq_voice_call_plugin.go`）连着它决定接不接、对话。两者之间的协议在 `model/qqcall`。

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

在仓库根目录构建：

```bash
docker build -f packaging/snowluma-voice/Dockerfile -t snowluma-voice .
```

它在 SnowLuma 镜像上加了 PulseAudio、`libpulse-mainloop-glib0`、xdotool、ImageMagick、tesseract（含简体中文）和 `qqcall-agent`。`libpulse-mainloop-glib0` 是 QQ 语音插件的依赖，原镜像没有：缺了它点「语音通话」不报错，只是过一会儿显示「已取消」，对方根本没响铃。

容器启动参数和原来一样（`--cap-add SYS_PTRACE --security-opt seccomp=unconfined`、原来的数据卷），再加：

- 环境变量 `QQCALL_TOKEN`：随便一串足够长的随机字符，和 Diana 插件设置里的令牌一致。不设时 agent 不启动，镜像的其余部分照常工作。
- 端口 6190：只给 Diana 访问，例如 `-p 127.0.0.1:6190:6190`；同一个 compose 里就不用映射。

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

## agent 的环境变量

窗口里的按钮位置写死在坐标上（相对通话窗口左上角，窗口 360×640）。QQ 改版挪了按钮时改这些变量，不用重新编译：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `QQCALL_TOKEN` | 无 | 必填，不填不启动 |
| `QQCALL_LISTEN` | `:6190` | 监听地址 |
| `QQCALL_WINDOW_TITLE` | `语音通话` | 通话窗口标题 |
| `QQCALL_ANSWER_POS` | `112,508` | 来电时的「接听」 |
| `QQCALL_REJECT_POS` | `244,508` | 来电时的「拒绝」 |
| `QQCALL_HANGUP_POS` | `287,508` | 通话中的「挂断」 |
| `QQCALL_NAME_AREA` | `20,255,320,75` | 昵称和「邀请你语音通话」那块，`x,y,宽,高` |
| `QQCALL_LOG_DIR` | `/app/data/logs` | SnowLuma 日志目录，读对方 QQ 号用 |

## 已知限制

- **没有回声消除**：虚拟声卡不经过系统的回声消除。对方外放时，机器人自己的声音可能经对方手机回传过来，被当成对方开口把自己打断，所以「允许打断」默认关：机器人说话时对方的声音直接丢掉。对方戴耳机时可以打开。
- **不能重启 QQ**：重启 QQ 进程会掉登录，要重新扫码。agent 从不碰 QQ 进程；Linux QQ 也不认 `--remote-debugging-port`，没法走 DevTools 读界面。
- **靠界面和日志**：昵称靠 OCR，接通靠「QQ 开始采集麦克风」，对方 QQ 号靠 SnowLuma 的一行警告日志。QQ 或 SnowLuma 升级改了其中任何一样都会失效；失效时插件只会不接或立刻挂断，不会接错人。
- **一个容器只有一通**：QQ 同一时间只能有一个通话窗口。
