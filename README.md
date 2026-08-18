# Diana

[English](./README.en.md)

[官网与文档](https://suink.github.io/Diana/) · [在线演示](https://suink.github.io/Diana/demo/) · [下载最新版本](https://github.com/SuInk/Diana/releases/latest)

Diana 是一个 Go 语言多平台 AI 助手服务，内置 LLM 兼容层、平台适配层、Gin WebUI 和插件管理。当前自带 QQ 的 OneBot v11 适配器；WebUI 可管理多个助手实例、模型、平台连接、触发词和内置插件。

## 安装要求

- 使用 QQ 适配器时需要支持 OneBot v11 反向 WebSocket 的客户端
- 一键安装支持 Linux、macOS 的 amd64/arm64，以及 64 位 Windows；无需 Go、Node.js、npm 或源码
- 使用 Docker 部署时需要 Docker 或 Docker Compose
- 只有手动从源码构建时才需要 Go `1.26.5`、Node.js `22` 和 npm

## 一键安装（推荐）

安装脚本会自动识别系统与架构，下载最新稳定版完整包，核对 `SHA256SUMS`，生成本地管理凭据并启动 Diana。重复执行同一命令即可安全升级；升级前会备份数据库和当前运行文件，健康检查失败时自动恢复。

Linux / macOS：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

安装完成后访问：

```text
http://127.0.0.1:18080
```

Linux 和 macOS 默认安装到 `~/.local/share/diana`，Windows 默认安装到 `%LOCALAPPDATA%\Diana`。首次生成的管理员账号和密码会显示在终端，并保存在安装目录的 `runtime.env`；请妥善保存，不要公开该文件。

可以通过环境变量选择版本、安装目录、端口或仅安装不启动。例如：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_VERSION=v0.8.9 DIANA_INSTALL_DIR=/opt/diana DIANA_PORT=18081 sh
```

### Release 完整包

不使用安装脚本时，也可以从 [GitHub Releases](https://github.com/SuInk/Diana/releases) 下载完整包。Linux/macOS 使用 `.tar.gz`，Windows 使用 `.zip`；包内包含后端二进制、`frontend-next/dist` 和启动脚本，无需额外构建。Unix 平台运行 `run.sh`，Windows 平台运行 `run.bat`。

Release 同时提供 `SHA256SUMS`。下载后应先校验再解压或替换程序；强制更新也不会绕过该校验：

```sh
sha256sum -c SHA256SUMS --ignore-missing
```

macOS 可使用 `shasum -a 256 <文件名>` 与 `SHA256SUMS` 对照；Windows 可使用 `Get-FileHash <文件名> -Algorithm SHA256`。

从完整 Release 包运行时，WebUI 可直接安装后续稳定版本：后端会下载当前系统和架构对应的完整包，校验、备份并切换版本；探活失败会自动恢复数据库与旧版本。备份和最近一次结果保存在安装目录的 `.diana-updates` 下。容器部署仍由镜像更新器负责，源码 checkout 仍使用 Git 更新流程。

## Docker 部署

构建镜像：

```sh
docker build -t diana:latest .
```

运行容器：

```sh
docker run -d \
  --name diana \
  --restart unless-stopped \
  -p 18080:18080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -e LOG_PATH=/app/logs/diana.log \
  -e DIANA_ADMIN_PASSWORD=change-this-admin-password \
  -e QQBOT_ENABLED=true \
  -e ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws \
  -e ONEBOT_ACCESS_TOKEN=your-onebot-token \
  -e QQBOT_QQ=10001 \
  -e LLM_PROVIDER=openai_compatible \
  -e LLM_API_KEY=your-key \
  -e LLM_MODEL=gpt-4o-mini \
  -e LLM_USER_AGENT=codex-cli/0.142.0 \
  diana:latest
```

Docker Compose：

```sh
cp docker-compose.yml docker-compose.local.yml
# 修改 docker-compose.local.yml 中的 token、QQ 号和 LLM 配置
docker compose -f docker-compose.local.yml up -d --build
```

容器启动后访问：

```text
http://127.0.0.1:18080
```

OneBot v11 客户端连接宿主机暴露的反向 WebSocket 地址：

```text
ws://127.0.0.1:18080/onebot/v11/ws
```

如果 OneBot v11 客户端和 Diana 不在同一台机器，`127.0.0.1` 要换成 Diana 宿主机 IP 或域名。

## 手动从源码构建

源码构建主要面向开发、调试和自定义部署。普通用户优先使用上面的一键安装脚本或 Release 完整包。

```sh
git clone <your-repo-url> diana
cd diana

go mod download

cd frontend-next
npm ci
npm run build
cd ..

go build -o dist/diana-webui ./cmd/webui
```

启动：

```sh
./dist/diana-webui
```

默认 WebUI：

```text
http://127.0.0.1:18080
```

## macOS 部署

Apple Silicon：

```sh
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
./dist/diana-webui
```

Intel Mac：

```sh
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
./dist/diana-webui
```

也可以直接下载 GitHub Release 中的 `darwin-arm64` 或 `darwin-amd64` 二进制。裸二进制不包含前端资源，普通用户请下载对应平台的 Release 完整包。

## Linux 部署

amd64：

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
./dist/diana-webui
```

arm64：

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
./dist/diana-webui
```

后台运行建议使用下面的 systemd 示例。

## Windows 部署

PowerShell：

```powershell
$env:GOOS="windows"
$env:GOARCH="amd64"
$env:CGO_ENABLED="0"
go build -o dist\diana-webui.exe .\cmd\webui
.\dist\diana-webui.exe
```

Windows 下也可以直接下载 GitHub Release 中的 `windows-amd64.exe`。裸二进制不包含前端资源，普通用户请下载对应平台的 Release 完整包。

## 快速运行

开发或本机测试可以一键同时启动 Go 后端和 Vite 前端：

```sh
make dev
```

默认后端是 `http://127.0.0.1:18080`，前端是 `http://127.0.0.1:5173`；Vite 会代理 `/api` 和 `/onebot` 到后端。端口可用环境变量调整：

```sh
make dev BACKEND_PORT=18081 FRONTEND_PORT=5174
```

没有安装 `make` 时，也可以直接使用跨平台 Node 脚本：

```sh
node scripts/dev.mjs
```

只运行后端或生产构建时：

```sh
make backend
make build
```

## 配置 LLM

可以在 WebUI 中配置，也可以使用环境变量：

```sh
LLM_PROVIDER=openai_compatible \
LLM_API_KEY=your-key \
LLM_BASE_URL=https://example.com/v1 \
LLM_MODEL=gpt-4o-mini \
LLM_USER_AGENT=codex-cli/0.142.0 \
LLM_IMAGE_MODEL=gpt-image-1 \
LLM_MAX_OUTPUT_TOKENS=1024 \
./dist/diana-webui
```

支持的 provider：

- `openai_compatible`
- `gemini`
- `anthropic`

WebUI 的 LLM 配置页会直接显示当前保存的 API Key，方便本地控制台复制和修改；普通 `GET /api/llm/config` 默认仍不返回密钥，前端会用 `include_secrets=true` 显式读取完整配置。

## WebUI 访问安全

WebUI 从首次启动起强制登录，本机和公网访问使用相同规则。默认管理员账号会安全随机生成，格式为 `diana#` 加 16 位随机字符串，并持久化到 SQLite。

- 首次启动未提供 `DIANA_ADMIN_PASSWORD` 时，Diana 会生成安全随机密码；随机账号和密码仅在该次启动的标准错误日志中显示一次。
- 也可在首次启动时注入 `DIANA_ADMIN_USERNAME=diana#你的账号` 和 `DIANA_ADMIN_PASSWORD=你的密码`；已有凭据时不会覆盖数据库中的账号或密码。
- 登录后可在设置页的「访问安全」中修改账号和密码（账号须以 `diana#` 开头，密码至少 8 位）。

开启后所有 `/api` 接口需要登录，会话有效期 30 天；改密会使全部已登录会话失效。

**管理员快速登录**：在「机器人」页开启总开关并配置主人账号后，两种方式各自独立开关。「私聊确认」默认开启：网页显示 6 位验证码，主人私聊发给机器人，机器人回报本次登录的来源 IP 与设备并请主人回复「确认 <验证码>」才放行（「确认」是日常高频词，必须带码才能放行，避免随口一句就批掉登录），回复「取消」可当场拒绝（验证码只在网页上显示，机器人不会把它回声一遍）——停这一步是为了让主人看清是谁在登录，而不是凭一串数字盲签。「验证码下发」默认关闭：它由服务端主动把验证码推送到主人账号，而该端点无需任何凭证即可触发，暴露在公网时会被匿名请求用来骚扰主人账号，或靠占满 60 秒冷却窗口把主人本人挡在门外；确实需要时再开。两种方式的验证码都是 5 分钟有效、一次性使用，需要当前机器人在线。

**登录爆破防护**：密码登录与改密共用一份失败预算，按来源计数：连续失败 5 次后开始锁定，退避从 30 秒逐级翻倍到 30 分钟封顶，返回 `429` 并带 `Retry-After`，任意一次成功即清零；另有一层全局兜底挡分布式撞库。锁定会写入操作日志。注意按来源计数依赖真实客户端 IP：默认不信任任何反向代理，套了反代之后所有请求的来源地址都是反代自己，需用 `DIANA_TRUSTED_PROXIES`（逗号分隔的 IP 或 CIDR）声明可信代理，声明后才会解析 `X-Forwarded-For`。

豁免路径：登录及管理员快速登录端点、`/api/health`（监控探活）、`/onebot/*`（由 OneBot access token 单独鉴权）、群管理页（自有群验证码流程）。会话 cookie 未设 Secure 以兼容内网 HTTP，公网部署请套 HTTPS 反向代理。

**多机器人与平台**：机器人页可创建、复制和切换多个机器人配置，每个配置独立保存平台、账号、主人、人设、触发规则及模型分配；列表按聊天平台分类，可用顶部标签筛选。所有启用的 QQ 与 Telegram 配置会同时在线，回复、图片和提醒始终路由回消息来源通道。跨平台会话上下文默认按配置隔离，也可在机器人列表顶部关闭隔离。

当前支持两类平台：

| 分类 | 平台 | 接入方式 |
| --- | --- | --- |
| QQ | OneBot v11 | 反向 WebSocket，由 OneBot v11 客户端连到 Diana |
| Telegram | Telegram | 官方 Bot API 长轮询，由 Diana 主动出站连接 |

Telegram 只需要 BotFather 给的 Bot Token，不需要公网地址，也不用配置 webhook；国内网络通常还要在机器人页填写代理地址。部署了本地 Bot API server 时可填自建地址，绕过 50MB 上传限制。

两个平台的能力差异：

- **群等级门槛只对 QQ 生效**。Telegram 没有群等级这个概念，准入设置里的等级门槛在 Telegram 机器人上不显示，后台也不会去查群成员信息。
- **语音消息、@某人** 依赖 OneBot 的 CQ 码，在 Telegram 上会自然降级：欢迎语正文照发，但不会 @ 到人。
- **本地媒体**：OneBot 侧由接入端来拉 Diana 的 `/media/resolver` 地址；Telegram 拉不到本机地址，改为直接 multipart 上传。

## WebUI 日志中心

WebUI 的“日志中心”页可查看持久化的操作日志和错误日志。操作日志会记录 LLM 配置保存/切换、机器人启停、插件管理、系统更新等动作；错误日志会记录这些接口返回失败时的错误信息。日志会带 `actor` 操作人：WebUI 默认记录 `web:<客户端 IP>`，也可由网关通过 `X-Diana-Actor`、`X-Operator`、`X-Forwarded-User` 等请求头传入；机器人内置的聊天模型配置命令记录 `qq:<用户 QQ>`。

```text
GET /api/logs?kind=operation&limit=100
GET /api/logs?kind=error&limit=100
```

这些结构化日志存储在 `APP_DB_PATH` 指向的 SQLite 数据库中；`LOG_PATH` 仍用于普通运行日志文件输出。

## 配置 OneBot v11

本项目直接提供 OneBot v11 反向 WebSocket endpoint：

```text
ws://127.0.0.1:18080/onebot/v11/ws
```

在兼容客户端中添加 OneBot v11 反向 WebSocket，连接地址填写上面的地址。如果配置了 access token，客户端和 Diana 必须使用同一个 token。NapCat、Lagrange.Core、go-cqhttp 等实现使用同一平台配置，不再作为不同平台分别创建。

机器人启动示例：

```sh
QQBOT_ENABLED=true \
ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws \
ONEBOT_ACCESS_TOKEN=your-onebot-token \
QQBOT_QQ=10001 \
DIANA_GROUP_TRIGGERS=嘉然,然然,Diana,diana \
LLM_PROVIDER=openai_compatible \
LLM_API_KEY=your-key \
LLM_MODEL=gpt-4o-mini \
LLM_USER_AGENT=codex-cli/0.142.0 \
./dist/diana-webui
```

启动后，私聊会直接触发；群聊中 `@机器人` 或以触发词开头会触发。

## 准入控制

「机器人」页的「准入控制」和「群管理」页的每个群，都可以限制机器人在什么条件下才回复。全局设置为默认值，群级设置整体覆盖全局（不是逐字段合并）。

- **群准入模式**：默认「黑名单」，除禁用群外都工作，与旧版行为一致；切到「白名单」后只在列出的群工作，被拉进其它群不会回话。白名单模式下禁用群列表仍然生效。
- **QQ 群等级门槛**：指群内活跃度等级（Lv.1~6），不是 QQ 账号等级（太阳月亮星星，OneBot 协议拿不到）。等级按群独立累积，同一个人在不同群的等级不同。
- **回复时段**：结束时间早于开始时间表示跨夜，例如 `22:00-06:00`；两者相同视为全天开放。时区填 IANA 名称（如 `Asia/Shanghai`），留空用服务器本地时区。静默期可以配一句提示语，同一会话每小时最多提示一次。
- **豁免 / 屏蔽用户**：豁免用户无视等级与时段；屏蔽用户在群聊和私聊都不回。
- **主人绕过**：默认开启，建议保持——否则时段或等级配错时，你自己也会被挡在门外，QQ 侧没有补救手段。

关于群等级的两点说明：

部分 OneBot 实现不会在消息事件里带 `level`，Diana 会在需要时通过 `get_group_member_info` 补齐，结果缓存在内存里（按「群号+QQ号」区分，10 分钟有效，重启后靠日常聊天自动重建）。

拿不到等级时**默认放行**。各实现返回的 `level` 差异很大，把「读不到」当成「等级 0」去拒绝会让整个群失联。需要严格拦截时可以把「等级读不到时」改成「拦截」，但要清楚代价。

## 内置 Agent

WebUI 的“QQ 机器人配置”页可以启用内置 Agent。启用后，机器人会使用 [Pi Agent](https://github.com/earendil-works/pi) 风格的最小状态与工具循环处理消息：模型规划、工具调用、观察结果、最终回复。运行时仍由 Go 原生实现，因此完整 Release 包不需要额外安装 Node。

当前内置工具：

- `list_files`：列出 Agent 工作目录内文件。
- `read_file`：读取 Agent 工作目录内文本文件。
- `run_command`：在 Agent 工作目录内执行白名单命令，不经过 shell，带超时和输出截断。
- `browser_open` / `browser_text` / `browser_click` / `browser_type` / `browser_screenshot`：通过 Chrome DevTools Protocol 操纵浏览器。

Agent 使用统一扩展目录管理三类能力：

- **内置插件**：WebUI 中已有的官方插件会默认出现在 `extensions.list`，原有启停、配置和权限规则保持不变。
- **Skills**：按 Agent Skills 的渐进式加载方式，只把名称和说明放入上下文，需要使用时再通过 `skills.read` 读取完整 `SKILL.md`。主人可让 Agent 从完整 `SKILL.md`、公开 HTTP(S) 地址或 ZIP 包安装；托管 Skill 保存到工作目录的 `.agents/skills/`，卸载时移动到 `.trash/` 以便恢复。
- **MCP 服务**：支持 stdio 和 Streamable HTTP；主人可通过自然语言安装、替换、启用、停用或卸载，服务通过连接测试后才写入 `.mcp.json`，发现到的工具会立即加入当前会话。`env` 和 `headers` 可使用 `${ENV_VAR}`，配置文件按私密文件权限写入；自安装的 stdio 服务只继承运行所需的最小环境，凭据必须在 `env` 中显式声明。

扩展管理工具只提供给主人，并且只有当前用户消息明确要求变更时才能执行。网页、工具结果、Skill 或 MCP 返回内容都不能代替用户授权；其他用户仍只能使用其权限范围内已经启用的能力。

浏览器工具需要 Chrome/Chromium 开启远程调试端口，例如：

```sh
chrome --remote-debugging-port=9222
```

建议把 `Agent 工作目录` 配到独立的资料目录，不要直接指向包含密钥或生产数据的目录。命令执行能力风险较高，生产环境建议配置 `DIANA_AGENT_COMMAND_ALLOWLIST` 只允许必要命令。

## WebUI 管理插件

打开 WebUI 后进入“机器人插件”区域：

1. 查看官方内置插件。
2. 内置能力无需安装或卸载，可以直接启用、停用并调整设置。
3. 默认内置 Go 社交媒体解析器，可解析并发送 B 站、YouTube、X、小红书、抖音的图片或视频；大小、时长、清晰度、图集数量可以在插件设置中调整。知乎、微博、GitHub 只抓标题描述，不下载媒体，排除平台列表里已按「可下载媒体 / 仅标题」标注。

   各平台的 Cookie、yt-dlp Cookie 文件和代理地址都可以直接在插件设置里填写，不必再改环境变量重启容器；填写后优先于同名环境变量。凭据保存后读接口只回传「已配置」标记，不返回明文，留空提交表示沿用原值，要清除需点设置项旁的「清除」。

   “链接解析”插件卡片内会显示 `yt-dlp`、`ffmpeg`、`node` 三个外部依赖的探测结果，并可通过受控的系统包管理器直接安装缺失项。
4. 默认内置 Go 文件解析插件，支持 QQ 文件段和文本类文件链接，提取内容作为 LLM 上下文。
5. `联网搜索` 是默认安装并启用的内置能力，对话模型可以直接调用 `web_search.search` 获取实时网页信息；首选免费 Exa MCP，失败时可用已配置 API Key 的 Tavily 自动回退。它可以停用和配置，但不能安装或卸载；搜索能力独立于内置 Agent 开关，未开启 Agent 时不会同时开放本地文件、命令或浏览器工具。

主人通过自然语言修改当前 provider 和模型的能力属于机器人本身，不作为插件展示。在“机器人 → 模型 → 聊天内模型管理”中可以按机器人独立开关；目标模型保存前会通过后端模型列表校验。

## 使用第三方 NoneBot 插件

Go 主程序不能直接加载 Python NoneBot 插件。要使用第三方 NoneBot2 插件时，推荐单独运行一个 NoneBot sidecar：

1. 在 NoneBot2 项目中安装第三方插件。
2. 给 NoneBot 配置 OneBot v11 反向 WebSocket driver。
3. 在 Diana WebUI 的“QQ 机器人”页面启用 `NoneBot 插件桥`。
4. `NoneBot 反向 WebSocket` 默认填写：

```text
ws://127.0.0.1:8080/onebot/v11/ws
```

Diana 会把客户端收到的 OneBot 事件转发给 NoneBot sidecar；第三方插件调用 `send_msg`、`get_group_info` 等 OneBot API 时，Diana 会再转发给当前 OneBot v11 客户端。这样第三方插件仍然在原生 NoneBot2 运行环境中工作。

## 常用环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `18080` | WebUI 和 OneBot endpoint 监听端口 |
| `FRONTEND_DIST` | 自动探测 | 前端构建产物目录；未设置时使用 `frontend-next/dist` |
| `DIANA_SEND_RETRY_ATTEMPTS` | `3` | 单条消息发送重试次数（1–5） |
| `DIANA_SEND_CHUNK_INTERVAL_MS` | `300` | 分段回复的段间间隔（毫秒） |
| `DIANA_ERROR_REPLY_PREFIX` | `出错了：` | 聊天内错误提示前缀 |
| `LOG_PATH` | 空 | 日志文件路径；设置后同时输出到 stdout 和文件 |
| `DIANA_LOG_PATH` | 空 | `LOG_PATH` 的兼容别名 |
| `DIANA_MEDIA_DIR` | `data/media` | 入站图片持久化目录；识图用本地文件的 base64 提交 |
| `DIANA_MEDIA_MAX_MB` | `10` | 单张入站图片下载上限 |
| `DIANA_MEDIA_CACHE_MB` | `512` | 图片目录总量上限，超出后按最后使用时间淘汰 |
| `DIANA_LOCAL_MEDIA_BASE_URL` | 当前服务的 `/media/resolver` | OneBot v11 客户端可访问的 Diana 媒体地址；分容器部署可设为 `http://diana:18080/media/resolver` |
| `DIANA_BILI_SESSDATA` | 空 | B 站登录 Cookie 中的 `SESSDATA`；WebUI 插件设置优先 |
| `DIANA_DOUYIN_CK` | 空 | 抖音 Cookie；抖音解析必需，WebUI 插件设置优先 |
| `DIANA_XHS_CK` | 空 | 小红书 Cookie；小红书解析必需，WebUI 插件设置优先 |
| `DIANA_YTDLP_COOKIES` | 空 | yt-dlp Netscape Cookie 文件路径；WebUI 插件设置优先 |
| `DIANA_RESOLVER_PROXY` | 空 | 社交媒体解析与 yt-dlp 使用的代理地址；WebUI 插件设置优先 |
| `APP_DB_PATH` | `data/diana.db` | 本地 SQLite 配置数据库路径 |
| `DIANA_RELEASE_UPDATE_ENABLED` | `true` | 允许完整 Release 包下载、校验、备份并自更新；源码和容器部署不会启用包替换 |
| `DIANA_ADMIN_USERNAME` | 自动随机生成 | WebUI 管理员首次初始化账号；默认为 `diana#` 加 16 位随机字符串，之后以 SQLite 中的凭据为准 |
| `DIANA_ADMIN_PASSWORD` | 自动随机生成 | WebUI 管理员首次初始化密码；之后以 SQLite 中的凭据为准 |
| `LLM_PROVIDER` | `openai_compatible` | LLM provider |
| `LLM_API_KEY` | 空 | LLM API Key |
| `LLM_BASE_URL` | 空 | OpenAI-compatible 自定义 Base URL |
| `LLM_MODEL` | 空 | 模型 ID（openai_compatible 无默认，需在 WebUI 选择或此处指定） |
| WebUI LLM 配置集 | 多配置 | 支持命名保存多套 LLM 配置，并切换当前激活项 |
| `LLM_USER_AGENT` | `codex-cli/0.142.0` | OpenAI-compatible User-Agent；可用于模拟 Codex CLI |
| `LLM_IMAGE_MODEL` | provider 默认值 | 生图模型；OpenAI-compatible 默认 `gpt-image-1`，Gemini 默认 `imagen-4.0-generate-001` |
| `LLM_TEMPERATURE` | 空 | temperature |
| `LLM_MAX_OUTPUT_TOKENS` | `1024` | Responses API 最大输出 token 数 |
| `LLM_TIMEOUT_MS` | `30000` | LLM 请求超时，单位毫秒 |
| `QQBOT_ENABLED` | `false` | 启动时是否自动启用机器人 |
| `ONEBOT_REVERSE_WS_ENDPOINT` | `ws://127.0.0.1:<PORT>/onebot/v11/ws` | 给 OneBot v11 客户端连接的反向 WebSocket 地址 |
| `ONEBOT_ACCESS_TOKEN` | 空 | OneBot access token |
| `NONEBOT_BRIDGE_ENABLED` | `false` | 是否启用第三方 NoneBot 插件桥 |
| `NONEBOT_BRIDGE_ENDPOINT` | `ws://127.0.0.1:8080/onebot/v11/ws` | NoneBot sidecar 的反向 WebSocket 地址 |
| `NONEBOT_BRIDGE_TOKEN` | 空 | NoneBot 插件桥 access token |
| `QQBOT_QQ` | 空 | 机器人 QQ 号 |
| `DIANA_OWNER_ID` | 空 | 主人 QQ 号（Telegram 上填数字用户 ID） |
| `DIANA_GROUP_TRIGGERS` | `嘉然,然然,Diana,diana` | 群聊触发词 |
| `DIANA_SYSTEM_PROMPT` | 内置提示词 | 机器人系统提示词 |
| `DIANA_MAX_INPUT_CHARS` | `2000` | 单次输入最大字符数 |
| `DIANA_MAX_REPLY_CHARS` | `3500` | 单次回复最大字符数 |
| `DIANA_DIRECT_REPLY_CHUNK_SIZE` | `500` | 文本分段发送字符数 |
| `DIANA_MAX_BOT_CONCURRENCY` | `5` | 全局并发数 |
| `DIANA_AGENT_ENABLED` | `false` | 是否启用内置 Agent |
| `DIANA_AGENT_WORK_DIR` | `.` | Agent 可访问的工作目录 |
| `AGENT_WORK_DIR` | `.` | `DIANA_AGENT_WORK_DIR` 的兼容别名 |
| `DIANA_AGENT_MAX_STEPS` | `4` | Agent 单次回复最大工具循环步数，最高 `8` |
| `DIANA_AGENT_COMMAND_ALLOWLIST` | 常见开发命令 | Agent `run_command` 可执行命令，逗号分隔；填 `*` 允许全部命令 |
| `DIANA_AGENT_COMMAND_TIMEOUT_MS` | `10000` | Agent 本地命令执行超时，最高 `60000` |
| `DIANA_AGENT_SKILL_ROOTS` | `.agents/skills,skills` | Agent Skill 搜索目录，逗号分隔；自安装内容固定写入工作目录下的 `.agents/skills` |
| `DIANA_AGENT_MCP_CONFIG` | `.mcp.json` | MCP 服务配置文件；相对路径以 Agent 工作目录为基准 |
| `DIANA_AGENT_BROWSER_CDP_URL` | `http://127.0.0.1:9222` | 浏览器工具连接的 Chrome DevTools 地址 |
| `AGENT_BROWSER_CDP_URL` | 同上 | `DIANA_AGENT_BROWSER_CDP_URL` 的兼容别名 |
| `DIANA_AGENT_BROWSER_TIMEOUT_MS` | `15000` | 浏览器工具调用超时，最高 `60000` |

## systemd 示例

先创建日志目录：

```sh
sudo mkdir -p /var/log/diana
sudo chown -R $USER:$USER /var/log/diana
```

```ini
[Unit]
Description=Diana
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/diana
Environment=PORT=18080
Environment=LOG_PATH=/var/log/diana/diana.log
Environment=QQBOT_ENABLED=true
Environment=ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws
Environment=ONEBOT_ACCESS_TOKEN=change-me
Environment=QQBOT_QQ=10001
Environment=LLM_PROVIDER=openai_compatible
Environment=LLM_API_KEY=change-me
Environment=LLM_MODEL=gpt-4o-mini
Environment=LLM_USER_AGENT=codex-cli/0.142.0
ExecStart=/opt/diana/diana-webui
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

## 开发命令

后端测试：

```sh
go test ./...
```

前端开发：

```sh
cd frontend-next
npm run dev
```

生产构建：

```sh
cd frontend-next
npm run build
cd ..
go build -o dist/diana-webui ./cmd/webui
```

## WebUI（frontend-next）

`frontend-next/` 是当前唯一受支持的控制台，包含总览 Dashboard（连接检查清单、今日/24h 消息统计、实时事件流）、SSE 实时推送、三步配置向导和移动端适配：

```sh
make deps           # 安装依赖
make run            # 构建前端并以 FRONTEND_DIST=frontend-next/dist 运行后端
```

开发模式直接使用 `make dev`。详见 [frontend-next/README.md](./frontend-next/README.md)。

配套新增后端接口：

```text
GET /api/stats    # Dashboard 统计（进程内计数，重启清零）
GET /api/events   # SSE 实时推送：status / stats / bot_event
GET /api/health   # 版本与运行时长
```

## 目录

```text
.
├── cmd/webui/              # Gin WebUI 和 OneBot endpoint 入口
├── frontend-next/          # Vue + TypeScript WebUI（Dashboard / SSE / 配置向导）
├── model/llm/              # LLM 统一接口和 provider adapters
├── model/assistant/            # QQ 机器人运行时、OneBot 通道、插件系统
├── webui/                  # WebUI API handler
├── .github/workflows/      # GitHub Actions CI/CD
├── LICENSE
└── go.mod
```

## 许可证

本项目使用 `Limited Redistribution License (SuInk)`，详见 [LICENSE](./LICENSE)。
