# Diana

[English](./README.en.md)

Diana 是一个 Go 语言多平台 AI 助手服务，内置 LLM 兼容层、平台适配层、Gin WebUI 和插件管理。当前自带 QQ 的 NapCat / OneBot v11 适配器；WebUI 可管理多个助手实例、模型、平台连接、触发词和内置插件。

## 安装要求

- 使用 QQ 适配器时需要 NapCat，并开启 OneBot v11 反向 WebSocket
- 使用源码安装时需要 Go `1.25.8`、Node.js `22` 和 npm
- 使用 Docker 部署时需要 Docker 或 Docker Compose

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
  -e QQBOT_QQ=123456789 \
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

NapCat 反向 WebSocket 连接宿主机暴露的地址：

```text
ws://127.0.0.1:18080/onebot/v11/ws
```

如果 NapCat 和机器人不在同一台机器，`127.0.0.1` 要换成机器人宿主机 IP 或域名。

## 从源码安装

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
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui-darwin-arm64 ./cmd/webui
./dist/diana-webui-darwin-arm64
```

Intel Mac：

```sh
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui-darwin-amd64 ./cmd/webui
./dist/diana-webui-darwin-amd64
```

也可以直接下载 GitHub Release 中的 `darwin-arm64` 或 `darwin-amd64` 二进制。裸二进制不包含前端资源，普通用户请下载对应平台的 Release 完整包。

## Linux 部署

amd64：

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui-linux-amd64 ./cmd/webui
./dist/diana-webui-linux-amd64
```

arm64：

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui-linux-arm64 ./cmd/webui
./dist/diana-webui-linux-arm64
```

后台运行建议使用下面的 systemd 示例。

## Windows 部署

PowerShell：

```powershell
$env:GOOS="windows"
$env:GOARCH="amd64"
$env:CGO_ENABLED="0"
go build -o dist\diana-webui-windows-amd64.exe .\cmd\webui
.\dist\diana-webui-windows-amd64.exe
```

Windows 下也可以直接下载 GitHub Release 中的 `windows-amd64.exe`。裸二进制不包含前端资源，普通用户请下载对应平台的 Release 完整包。

### Release 完整包

每个平台的 GitHub Release 同时提供完整包（Linux/macOS 为 `.tar.gz`，Windows 为 `.zip`）。完整包包含后端二进制、`frontend-next/dist` 和启动脚本；解压后无需安装 Go、Node.js、npm 或源码即可运行。Unix 平台运行 `run.sh`，Windows 平台运行 `run.bat`。

Release 同时提供 `SHA256SUMS`。下载后应先校验再解压或替换程序；强制更新也不会绕过该校验：

```sh
sha256sum -c SHA256SUMS --ignore-missing
```

macOS 可使用 `shasum -a 256 <文件名>` 与 `SHA256SUMS` 对照；Windows 可使用 `Get-FileHash <文件名> -Algorithm SHA256`。

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

**主人 QQ 配对登录**：在「机器人」页开启「允许主人通过 QQ 私聊确认登录控制台」并配置主人 QQ 后，登录页会生成 6 位一次性验证码。主人把验证码私聊发送给机器人，保持网页开启即可自动登录。仅配置的主人 QQ 私聊有效，群聊和其他账号无效；验证码 5 分钟有效，匹配后立即作废，登录会话只能由生成验证码的网页领取。需要机器人在线。豁免路径：登录及 QQ 配对端点、`/api/health`（监控探活）、`/onebot/*`（由 OneBot access token 单独鉴权）、群管理页（自有群验证码流程）。会话 cookie 未设 Secure 以兼容内网 HTTP，公网部署请套 HTTPS 反向代理。

**多机器人与平台**：机器人页可创建、复制和切换多个机器人配置，每个配置独立保存平台、账号、主人、人设、触发规则及模型分配；列表按聊天平台分类，可用顶部标签筛选。运行时一次启用一个机器人配置。

当前支持两类平台：

| 分类 | 平台 | 接入方式 |
| --- | --- | --- |
| QQ | NapCat、Lagrange.Core、go-cqhttp | OneBot V11 反向 WebSocket，由接入端连到 Diana |
| Telegram | Telegram | 官方 Bot API 长轮询，由 Diana 主动出站连接 |

Telegram 只需要 BotFather 给的 Bot Token，不需要公网地址，也不用配置 webhook；国内网络通常还要在机器人页填写代理地址。部署了本地 Bot API server 时可填自建地址，绕过 50MB 上传限制。

两个平台的能力差异：

- **群等级门槛只对 QQ 生效**。Telegram 没有群等级这个概念，准入设置里的等级门槛在 Telegram 机器人上不显示，后台也不会去查群成员信息。
- **语音消息、@某人** 依赖 OneBot 的 CQ 码，在 Telegram 上会自然降级：欢迎语正文照发，但不会 @ 到人。
- **本地媒体**：OneBot 侧由接入端来拉 Diana 的 `/media/resolver` 地址；Telegram 拉不到本机地址，改为直接 multipart 上传。

## WebUI 日志中心

WebUI 的“日志中心”页可查看持久化的操作日志和错误日志。操作日志会记录 LLM 配置保存/切换、机器人启停、插件管理、系统更新等动作；错误日志会记录这些接口返回失败时的错误信息。日志会带 `actor` 操作人：WebUI 默认记录 `web:<客户端 IP>`，也可由网关通过 `X-Diana-Actor`、`X-Operator`、`X-Forwarded-User` 等请求头传入；QQ 内置 LLM 配置技能记录 `qq:<用户 QQ>`。

```text
GET /api/logs?kind=operation&limit=100
GET /api/logs?kind=error&limit=100
```

这些结构化日志存储在 `APP_DB_PATH` 指向的 SQLite 数据库中；`LOG_PATH` 仍用于普通运行日志文件输出。

## 配置 NapCat

本项目直接提供 OneBot v11 反向 WebSocket endpoint：

```text
ws://127.0.0.1:18080/onebot/v11/ws
```

在 NapCat 中添加 OneBot v11 反向 WebSocket，连接地址填写上面的地址。如果配置了 access token，NapCat 和本项目必须使用同一个 token。

机器人启动示例：

```sh
QQBOT_ENABLED=true \
ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws \
ONEBOT_ACCESS_TOKEN=your-onebot-token \
QQBOT_QQ=123456789 \
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

WebUI 的“QQ 机器人配置”页可以启用内置 Agent。启用后，机器人会使用 Codex CLI 风格的“模型规划、工具调用、观察结果、最终回复”循环处理消息。

当前内置工具：

- `list_files`：列出 Agent 工作目录内文件。
- `read_file`：读取 Agent 工作目录内文本文件。
- `run_command`：在 Agent 工作目录内执行白名单命令，不经过 shell，带超时和输出截断。
- `browser_open` / `browser_text` / `browser_click` / `browser_type` / `browser_screenshot`：通过 Chrome DevTools Protocol 操纵浏览器。

浏览器工具需要 Chrome/Chromium 开启远程调试端口，例如：

```sh
chrome --remote-debugging-port=9222
```

建议把 `Agent 工作目录` 配到独立的资料目录，不要直接指向包含密钥或生产数据的目录。命令执行能力风险较高，生产环境建议配置 `DIANA_AGENT_COMMAND_ALLOWLIST` 只允许必要命令。

## WebUI 安装插件

打开 WebUI 后进入“机器人插件”区域：

1. 查看官方内置插件。
2. 点击安装或启用。
3. 默认内置 Go 社交媒体解析器，可解析并发送 B 站、YouTube、X、小红书、抖音的图片或视频；大小、时长、清晰度、图集数量可以在插件设置中调整。知乎、微博、GitHub 只抓标题描述，不下载媒体，排除平台列表里已按「可下载媒体 / 仅标题」标注。

   各平台的 Cookie、yt-dlp Cookie 文件和代理地址都可以直接在插件设置里填写，不必再改环境变量重启容器；填写后优先于同名环境变量。凭据保存后读接口只回传「已配置」标记，不返回明文，留空提交表示沿用原值，要清除需点设置项旁的「清除」。

   插件页顶部会显示 `yt-dlp`、`ffmpeg`、`node` 三个外部依赖的探测结果，缺失时对应平台无法下载。
4. 默认内置 Go 文件解析插件，支持 QQ 文件段和文本类文件链接，提取内容作为 LLM 上下文。
5. 默认内置 `LLM 配置技能`，主人可用自然语言修改当前配置的 provider 和模型，例如“把提供商切到 gemini”“把模型换成 gemini-2.5-pro”“以后用 anthropic 的 claude-sonnet-4-5”；指定模型会先通过后端模型列表校验，列表里没有就不会保存。
6. `联网搜索` 是官方可选插件，默认不安装。安装后，对话模型可以调用 `web_search.search` 获取实时网页信息；首选免费 Exa MCP，失败时可用已配置 API Key 的 Tavily 自动回退。搜索插件独立于内置 Agent 开关，未开启 Agent 时不会同时开放本地文件、命令或浏览器工具。

## 使用第三方 NoneBot 插件

Go 主程序不能直接加载 Python NoneBot 插件。要使用第三方 NoneBot2 插件时，推荐单独运行一个 NoneBot sidecar：

1. 在 NoneBot2 项目中安装第三方插件。
2. 给 NoneBot 配置 OneBot v11 反向 WebSocket driver。
3. 在 Diana WebUI 的“QQ 机器人”页面启用 `NoneBot 插件桥`。
4. `NoneBot 反向 WebSocket` 默认填写：

```text
ws://127.0.0.1:8080/onebot/v11/ws
```

Diana 会把 NapCat 收到的 OneBot 事件转发给 NoneBot sidecar；第三方插件调用 `send_msg`、`get_group_info` 等 OneBot API 时，Diana 会再转发给 NapCat。这样第三方插件仍然在原生 NoneBot2 运行环境中工作。

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
| `DIANA_LOCAL_MEDIA_BASE_URL` | 当前服务的 `/media/resolver` | NapCat 可访问的 Diana 媒体地址；分容器部署可设为 `http://diana:18080/media/resolver` |
| `DIANA_BILI_SESSDATA` | 空 | B 站登录 Cookie 中的 `SESSDATA`；WebUI 插件设置优先 |
| `DIANA_DOUYIN_CK` | 空 | 抖音 Cookie；抖音解析必需，WebUI 插件设置优先 |
| `DIANA_XHS_CK` | 空 | 小红书 Cookie；小红书解析必需，WebUI 插件设置优先 |
| `DIANA_YTDLP_COOKIES` | 空 | yt-dlp Netscape Cookie 文件路径；WebUI 插件设置优先 |
| `DIANA_RESOLVER_PROXY` | 空 | 社交媒体解析与 yt-dlp 使用的代理地址；WebUI 插件设置优先 |
| `APP_DB_PATH` | `data/diana.db` | 本地 SQLite 配置数据库路径 |
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
| `ONEBOT_REVERSE_WS_ENDPOINT` | `ws://127.0.0.1:<PORT>/onebot/v11/ws` | 给 NapCat 连接的反向 WebSocket 地址 |
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
Environment=QQBOT_QQ=123456789
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
├── frontend/               # Vue + TypeScript 前端
├── frontend-next/          # 新版组件化前端（Dashboard / SSE / 配置向导）
├── model/llm/              # LLM 统一接口和 provider adapters
├── model/assistant/            # QQ 机器人运行时、OneBot 通道、插件系统
├── webui/                  # WebUI API handler
├── .github/workflows/      # GitHub Actions CI/CD
├── LICENSE
└── go.mod
```

## 许可证

本项目使用 `Limited Redistribution License (SuInk)`，详见 [LICENSE](./LICENSE)。
