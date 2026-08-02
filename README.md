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

cd frontend
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

每个平台的 GitHub Release 同时提供完整包（Linux/macOS 为 `.tar.gz`，Windows 为 `.zip`）。完整包包含后端二进制、新版 `frontend-next/dist`、旧版前端回退资源和启动脚本；解压后无需安装 Go、Node.js、npm 或源码即可运行。Unix 平台运行 `run.sh`，Windows 平台运行 `run.bat`。

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

WebUI 从首次启动起强制登录，本机和公网访问使用相同规则。默认管理员账号为 `admin@diana.local`。

- 首次启动未提供 `DIANA_ADMIN_PASSWORD` 时，Diana 会生成安全随机密码，并仅在该次启动的标准错误日志中显示一次。
- 也可在首次启动时注入 `DIANA_ADMIN_PASSWORD=你的密码`；已有凭据时不会覆盖数据库中的密码。
- 登录后可在设置页的「访问安全」中修改密码（至少 8 位）。

开启后所有 `/api` 接口需要登录，会话有效期 30 天；改密会使全部已登录会话失效。

**主人 QQ 配对登录**：在「机器人」页开启「允许主人通过 QQ 私聊确认登录控制台」并配置主人 QQ 后，登录页会生成 6 位一次性验证码。主人把验证码私聊发送给机器人，保持网页开启即可自动登录。仅配置的主人 QQ 私聊有效，群聊和其他账号无效；验证码 5 分钟有效，匹配后立即作废，登录会话只能由生成验证码的网页领取。需要机器人在线。豁免路径：登录及 QQ 配对端点、`/api/health`（监控探活）、`/onebot/*`（由 OneBot access token 单独鉴权）、群管理页（自有群验证码流程）。会话 cookie 未设 Secure 以兼容内网 HTTP，公网部署请套 HTTPS 反向代理。

**多机器人与平台**：机器人页顶部可创建、复制和切换多个机器人配置，每个配置独立保存平台、QQ、主人、人设、触发规则及模型分配。当前可选 NapCat、Lagrange.Core 和 go-cqhttp，均通过 OneBot V11 反向 WebSocket 适配器接入；运行时一次启用一个机器人配置。

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
3. 默认内置 Go 版 `nonebot-plugin-resolver`，用于解析 B 站、YouTube、X、小红书、抖音等链接并作为上下文交给 LLM。
4. 默认内置 Go 文件解析插件，支持 QQ 文件段和文本类文件链接，提取内容作为 LLM 上下文。
5. 默认内置 `LLM 配置技能`，主人可用自然语言修改当前配置的 provider 和模型，例如“把提供商切到 gemini”“把模型换成 gemini-2.5-pro”“以后用 anthropic 的 claude-sonnet-4-5”；指定模型会先通过后端模型列表校验，列表里没有就不会保存。

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
| `FRONTEND_DIST` | 自动探测 | 前端构建产物目录；未设置时优先 `frontend-next/dist`，回退 `frontend/dist` |
| `DIANA_SEND_RETRY_ATTEMPTS` | `3` | 单条消息发送重试次数（1–5） |
| `DIANA_SEND_CHUNK_INTERVAL_MS` | `300` | 分段回复的段间间隔（毫秒） |
| `DIANA_ERROR_REPLY_PREFIX` | `出错了：` | 聊天内错误提示前缀 |
| `LOG_PATH` | 空 | 日志文件路径；设置后同时输出到 stdout 和文件 |
| `DIANA_LOG_PATH` | 空 | `LOG_PATH` 的兼容别名 |
| `APP_DB_PATH` | `data/diana.db` | 本地 SQLite 配置数据库路径 |
| `DIANA_ADMIN_PASSWORD` | 自动随机生成 | WebUI 管理员首次初始化密码；账号固定为 `admin@diana.local`，之后以 SQLite 中的凭据为准 |
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
| `DIANA_OWNER_ID` | 空 | 主人 QQ 号 |
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
cd frontend
npm run dev
```

生产构建：

```sh
cd frontend
npm run build
cd ..
go build -o dist/diana-webui ./cmd/webui
```

## 新版 WebUI（frontend-next）

`frontend-next/` 是控制台的组件化重构版本，新增总览 Dashboard（连接检查清单、今日/24h 消息统计、实时事件流）、SSE 实时推送、三步配置向导和移动端适配。与旧版并存：

```sh
make deps-next      # 安装依赖
make run-next       # 构建新版前端并以 FRONTEND_DIST=frontend-next/dist 运行后端
```

开发模式：`make backend` + `make frontend-next`（Vite 端口 5174）。详见 [frontend-next/README.md](./frontend-next/README.md)。

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
