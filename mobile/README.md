# Diana 手机客户端

安卓和 iOS 上的「和机器人聊天 + 语音通话」App，Flutter 一套代码出两端。对接的是 WebUI 聊天页同一套接口（协议见 [docs/voice-call.md](../docs/voice-call.md)），服务端不区分客户端，不用改。

- **登录**：填服务器地址和 WebUI 管理员账号密码，调 `/api/auth/login` 拿会话 Cookie。之后的请求和通话 WebSocket 都带这个 Cookie。登录的人就是主人。
- **消息**：机器人列表（`/api/assistant/config`），点进去是和这台机器人的私聊，照 QQ 做：头像、左右气泡、五分钟一条时间分隔、「对方正在输入…」。发消息走 `POST /api/assistant/chat` 的 NDJSON 流，回复一条条出来。
- **通话**：聊天页右上角电话按钮。上行 16kHz `pcm_s16le`、`server_vad`，下行一句收齐后按顺序播放。支持打断（开口或点「轻点打断」）、静音、对讲（按住说话）、打字和挂断。

## 目录

| 位置 | 内容 |
| --- | --- |
| `lib/api/` | HTTP / WebSocket 客户端、数据结构、自签证书指纹信任 |
| `lib/session.dart` | 登录状态：地址、用户名放普通存储，Cookie 放系统钥匙串，密码不存 |
| `lib/call/` | 通话协议和音频：字幕折叠、逐句收音频（`call_turns.dart`），采集、播放、打断（`call_controller.dart`） |
| `lib/pages/` | 登录、机器人列表、私聊、通话四个页面 |
| `test/` | 纯逻辑单测 |
| `integration_test/` | 对着真实服务端在模拟器上跑的端到端测试 |

## 平台细节

- **回声消除**：交给系统。iOS 录音走 VoiceProcessingIO（`record` 的 `echoCancel`），安卓用 `VOICE_COMMUNICATION` 音源加通话模式、外放。外放仍有回声时用「对讲」。
- **明文 HTTP 和局域网**：家里的服务器多半是 `http://192.168.x.x`，所以安卓开了 `usesCleartextTraffic`，iOS 开了 `NSAllowsArbitraryLoads` 并声明了本地网络用途。
- **自签证书**：HTTPS 握手因证书不受信失败时，弹窗显示证书 SHA-256 指纹，用户核对后信任，之后只认这一张（图片加载也一样）。换证书会再问。
- **后台**：iOS 声明了 `audio` 后台模式，通话中切到后台不断。安卓还没做前台服务，切后台久了可能被系统停掉麦克风。

## 开发

需要 Flutter 3.47 以上（Dart 3.13+，见 `pubspec.yaml`）。安卓构建要 JDK 17–21，更新的 JDK 会让 Gradle 报 `Unsupported class file major version`，用 `flutter config --jdk-dir` 指过去。

```bash
cd mobile
flutter pub get
flutter run            # 选模拟器或真机
flutter analyze
flutter test           # 单测
```

模拟器连本机的 Diana：iOS 模拟器填 `http://127.0.0.1:<端口>`，安卓模拟器填 `http://10.0.2.2:<端口>`。真机填电脑的局域网地址，Diana 要监听 `0.0.0.0`。

端到端测试要一台在跑的 Diana（配好语音识别和语音合成），账号密码从命令行传，不写进仓库。每次跑之前先给 App 授权麦克风（`flutter test` 结束会卸载 App，授权跟着清掉；iOS 可以在装 App 之前授权），不然系统权限弹窗会卡住通话那一步：

```bash
xcrun simctl privacy booted grant microphone com.suink.diana.mobile   # iOS
adb shell pm grant com.suink.diana.mobile android.permission.RECORD_AUDIO   # 安卓
flutter test integration_test -d <设备> \
  --dart-define=DIANA_SERVER=http://127.0.0.1:18311 \
  --dart-define=DIANA_USER=<账号> --dart-define=DIANA_PASSWORD=<密码>
```
