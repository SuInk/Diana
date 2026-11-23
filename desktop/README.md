# Diana 桌面客户端

macOS、Windows、Linux 上和机器人私聊的桌面 App，Electron + Vue。对接的是 WebUI 聊天页和手机客户端同一套接口（`/api/auth/login`、`/api/assistant/config`、`/api/assistant/chat`），服务端不用改。

- **登录**：填服务器地址和 WebUI 管理员账号密码。密码不保存；会话 Cookie 用系统钥匙串（Electron `safeStorage`）加密后存在本机，下次打开直接进聊天。会话过期自动回登录页。
- **消息**：左栏机器人列表，右边是私聊：头像、左右气泡、五分钟一条时间分隔、通话记录、引用、图片、可点的链接。回车发送，Shift+回车换行，输入法选词的回车不算。回复按 NDJSON 流一条条出来，等回复时显示「对方正在输入…」。
- **自签证书**：HTTPS 证书不受系统信任时弹窗显示指纹，核对后信任，之后只认这一张，换证书会再问。

还没做：语音通话（手机端有）、发图片和文件（服务端聊天接口目前只收文字）、系统通知。

## 结构

| 位置 | 内容 |
| --- | --- |
| `src/main/` | 主进程：窗口、IPC、请求服务端（`api.ts`）、本机状态（`store.ts`） |
| `src/preload/` | 把 `window.diana` 交给界面，界面跑在沙箱里不碰 Node |
| `src/renderer/` | Vue 界面 |
| `src/shared/` | 两边共用的类型和纯函数（地址整理、NDJSON 拆行、时间、链接） |
| `scripts/mock-server.mjs` | 本地开发用的假 Diana |

所有网络请求都从主进程发，Cookie 手动带上，所以界面不跨域、服务端不用开 CORS。界面里的头像和图片走 `diana-media://` 协议，由主进程带 Cookie 去取，只放行登录的那台服务器。

## 开发

需要 Node 22.18 以上。

```bash
cd desktop
npm install
npm run mock     # 另开一个终端：假服务端 http://127.0.0.1:18333，账号 admin / demo
npm run dev      # 开发模式，界面热更新
npm test         # 纯函数单测
npm run build    # 单测 + 类型检查 + 构建到 out/
npm run dist     # 打安装包到 release/（dmg / nsis / AppImage）
```

npm 11 默认拦安装脚本，`package.json` 的 `allowScripts` 已经放行 `electron`（下载 Electron 本体）和 `esbuild`。

冒烟测试：没登录就用环境变量里的账号登录，发一句话，等回复后截图退出。

```bash
DIANA_DESKTOP_USER_DATA=/tmp/diana-desktop-smoke DIANA_DESKTOP_SMOKE=/tmp/smoke.png npx electron .
```

`DIANA_DESKTOP_SMOKE_SERVER`、`_USER`、`_PASSWORD` 默认对着 `npm run mock`。
