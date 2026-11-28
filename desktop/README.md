# Diana 桌面客户端

macOS、Windows、Linux 上的 Diana 桌面 App，Electron + Vue。默认是一个通用 Agent 工作台：开任务、看它调工具干活、随时停下或接着聊；和机器人私聊、管理后台是次要入口。对接的是服务端已有的接口（`/api/auth/login`、`/api/assistant/admin-chat`、`/api/assistant/chat`、`/api/llm/config`）。

- **任务（默认）**：服务端的管理对话接口，一个带全套工具（文件、命令、浏览器、搜索、插件、改配置）的 Agent。左栏「新任务」和按日期分组的历史任务；对话流里回复上方挂着这一轮的执行步骤（工具名、路径或命令摘要、耗时、成没成），正在跑时实时冒出来，Esc 或停止按钮随时停。输入框能选模型，下方显示当前权限（标准 / 安全模式，能否执行命令、写文件、联网）。任务存在服务端数据库里，重启服务、重新登录都还在；不设总时长，长任务跑到完。
- **本机 Diana（默认）**：安装包自带内嵌了 WebUI 的 `diana-webui`。打开 App 就在用户数据目录的 `server/` 里把它拉起来，只监听 `127.0.0.1`，自动登录，不用填地址和账号；退出 App 时等它收尾后一起停。端口第一次选定后固定（优先 18080，被占了换一个），QQ 这类反向连接不用反复改地址。管理员账号首次启动随机生成，存在 `server/desktop.json`（权限 0600）。程序跟着安装包升级，自更新关闭。
- **窗口**：照原生桌面 App 的样子：macOS 侧栏透出系统磨砂（vibrancy），Windows 11 用 Mica，标题栏藏起来，红绿灯 / 系统按钮让在工具栏两头；系统关了透明效果时退回纯色。主内容是右边一块圆角面板，侧栏可以收起（⌘\\）。中文应用菜单：⌘N 新任务、⌘1 机器人私聊、⌘2 管理后台、⌘, 设置、⌘R 刷新。
- **管理后台**：侧栏「管理后台」把当前服务器的 WebUI 嵌在主面板里显示（`src/main/admin.ts`，WebContentsView 叠在界面留出的位置上），沿用这边的登录；进去时侧栏自动收起，出来还原。新装的本机 Diana 里机器人默认停用，聊天页顶部会给「去管理后台设置」。
- **设置**：侧栏底部「设置」：当前连接、切换本机 / 其他服务器、本机 Diana 的地址和数据目录、版本。
- **连接其他服务器**：设置里「连接…」，填服务器地址和 WebUI 管理员账号密码。登上后本机 Diana 停掉（数据留着），登录页的「改用本机 Diana」切回来。密码不保存；会话 Cookie 用系统钥匙串（Electron `safeStorage`）加密后存在本机，下次打开直接进聊天。会话过期自动回登录页。
- **机器人私聊**：侧栏底部进入，工具栏切换机器人，右边是私聊：头像、左右气泡、五分钟一条时间分隔、通话记录、引用、图片、可点的链接。回车发送，Shift+回车换行，输入法选词的回车不算。回复按 NDJSON 流一条条出来，等回复时显示「对方正在输入…」。
- **自签证书**：HTTPS 证书不受系统信任时弹窗显示指纹，核对后信任，之后只认这一张，换证书会再问。

还没做：语音通话（手机端有）、发图片和文件（服务端聊天接口目前只收文字）、系统通知。

## 结构

| 位置 | 内容 |
| --- | --- |
| `src/main/` | 主进程：窗口外观和菜单（`window.ts`）、IPC、请求服务端（`api.ts`）、本机 Diana（`local.ts`）、嵌入的管理后台（`admin.ts`）、本机状态（`store.ts`） |
| `src/preload/` | 把 `window.diana` 交给界面，界面跑在沙箱里不碰 Node |
| `src/renderer/` | Vue 界面 |
| `src/shared/` | 两边共用的类型和纯函数（地址整理、NDJSON 拆行、时间、链接） |
| `scripts/mock-server.mjs` | 本地开发用的假 Diana |
| `scripts/build-server.mjs` | 编译自带的 `diana-webui` 到 `resources/bin/<os>-<arch>/` |

所有网络请求都从主进程发，Cookie 手动带上，所以界面不跨域、服务端不用开 CORS。界面里的头像和图片走 `diana-media://` 协议，由主进程带 Cookie 去取，只放行登录的那台服务器。

## 开发

需要 Node 22.18 以上。

```bash
cd desktop
npm install
npm run server   # 编译本机 Diana（当前系统和架构）；要别的平台：npm run server -- mac-x64 win-x64
npm run mock     # 另开一个终端：假服务端 http://127.0.0.1:18333，账号 admin / demo
npm run dev      # 开发模式，界面热更新
npm test         # 纯函数单测
npm run build    # 单测 + 类型检查 + 构建到 out/
npm run dist     # 打安装包到 release/（dmg / nsis / AppImage），先用 npm run server 编好各目标平台的程序
```

npm 11 默认拦安装脚本，`package.json` 的 `allowScripts` 已经放行 `electron`（下载 Electron 本体）和 `esbuild`。

冒烟测试：等进到聊天（默认拉起本机 Diana），发一句话，等回复后截图退出。机器人停用、输入框不可用时只截图。

```bash
DIANA_DESKTOP_USER_DATA=/tmp/diana-desktop-smoke DIANA_DESKTOP_SMOKE=/tmp/smoke.png npx electron .
```

设了 `DIANA_DESKTOP_SMOKE_SERVER` 就改连那台服务器（比如 `npm run mock` 的 `http://127.0.0.1:18333`），用 `_USER`、`_PASSWORD`（默认 admin / demo）登录。`DIANA_DESKTOP_SMOKE_TEXT` 换发送内容，设成空就不发。
