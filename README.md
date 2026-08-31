<div align="center">

# Diana

**住在群聊里、会主动搭话的 AI Agent —— 不用 @ 也能自然插话，还会搜索、点歌、发表情包、记住群里的约定；自托管单二进制，数据不出你的机器。**

<sub>OneBot v11 · Telegram · QQ 官方机器人 · 钉钉 · 飞书 · 企业微信 —— 一个实例，同时在线</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=c83f76)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[官网与文档](https://suink.github.io/Diana/) · [在线演示](https://suink.github.io/Diana/demo/) · [下载最新版本](https://github.com/SuInk/Diana/releases/latest) · [English](./README.en.md)

</div>

<br />

<img src="./docs/assets/diana-webui-overview.png" alt="Diana 控制台总览：通道状态、消息统计、系统资源与实时事件流" width="100%" />

## Diana 是什么

你有一个想接进群聊的 AI：让它在 QQ 群里搭话、在 Telegram 里点歌、在飞书里回答同事的问题。Diana 就是中间这一层——一个 Go 写成的单二进制服务，一头对接大模型（OpenAI 兼容接口、Gemini、Anthropic 都行），另一头同时对接六种聊天平台，中间有一个网页控制台管理一切。

它为「自己养一个机器人」的人设计：

- **装完就能用** —— 一条命令安装，浏览器里点几下完成配置，不需要写代码。
- **数据在你手里** —— 配置、聊天记忆、日志全部存在本机 SQLite，没有云端依赖。
- **每一步都能解释** —— 机器人为什么回、为什么没回、调了哪个模型、花了多少 token，控制台事件中心里都查得到。
- **能力开箱即备** —— 联网搜索、链接/文件解析、点歌、表情包、OCR、长期记忆都是内置的，开关在控制台里。

## 三分钟跑起来

**① 安装。** 一条命令，脚本自动识别系统、下载最新版、校验 SHA-256 并启动服务：

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Docker（预构建镜像，无需 clone 仓库）
docker run -d --name diana --restart unless-stopped \
  -p 18080:18080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  ghcr.io/suink/diana:latest
```

**② 登录控制台。** 打开 `http://127.0.0.1:18080`。管理员账号密码在刚才的终端输出里（Docker 方式用 `docker logs diana` 查看；脚本安装的还会写进安装目录的 `config.yaml`，别把这个文件给别人）。

**③ 配置。** 控制台里依次完成三件事：

1. 「LLM 配置」页填上你的 API Key，同步模型列表，选一个默认模型；
2. 「机器人」页选平台、填凭据（各平台要填什么见[支持的平台](#支持的平台)）；
3. 给机器人发条消息试试——私聊直接回，群聊要 @ 它或用触发词开头。

就这些。没回复？事件中心会告诉你原因；`diana doctor` 能检查服务健康。

> [!TIP]
> **装在服务器上？** 默认只监听本机，要从别的机器打开控制台，安装时带上 `DIANA_HOST`：
>
> ```sh
> curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | DIANA_HOST=0.0.0.0 sh
> ```
>
> ```powershell
> $env:DIANA_HOST="0.0.0.0"; irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
> ```
>
> 装过了也不要紧，带着 `DIANA_HOST` 重跑一遍就能改。开放外网访问后务必套 HTTPS 反向代理、在防火墙放行端口——控制台有管理权限，不要裸奔在公网上。

<details>
<summary>Docker 细节 / 手动下载 / 源码构建</summary>

**Docker：** 镜像随每个版本发布（`ghcr.io/suink/diana:latest` 及版本号 tag）。OneBot 客户端连 `ws://<宿主机>:18080/onebot/v11/ws`。想预置配置（无人值守部署），把改好的 `config.yaml` 以只读方式挂到 `/app/config.yaml`；仓库里也有 `docker-compose.yml` 可以本地构建。升级拉新镜像重建容器即可，数据都在挂出来的 `data/` 里。

**手动下载：** 从 [Releases](https://github.com/SuInk/Diana/releases) 下载你平台的**完整包**（`.tar.gz` / `.zip`，含前端资源和启动脚本），校验 `SHA256SUMS` 后运行 `run.sh` / `run.bat`。裸二进制不含前端，是给自定义部署用的。

**从源码：** 需要 Go 1.26+ 与 Node.js 22。

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
cd frontend-next && npm ci && npm run build && cd ..
go build -o dist/diana-webui ./cmd/webui
./dist/diana-webui
```

更多部署形态（systemd、交叉编译、无人值守预置配置）见[部署文档](https://suink.github.io/Diana/deploy.html)。

</details>

## 支持的平台

启用的机器人配置会同时在线，回复始终回到消息来的那个通道。

| 平台 | 要准备的凭据 | 连接方向 |
| --- | --- | --- |
| **OneBot v11**（NapCat、Lagrange.Core、go-cqhttp 等） | OneBot 客户端里配好反向 WebSocket 指向 Diana，双方约定一个 access token | 客户端 → Diana，无需公网 |
| **Telegram** | BotFather 的 Bot Token（国内网络通常还要代理地址） | Diana 主动出站，无需公网 |
| **QQ 官方机器人** | 开放平台的 AppID + AppSecret（未上架可用沙箱） | Diana 主动出站，无需公网 |
| **钉钉** | 应用的 Client ID + Client Secret（Stream 模式） | Diana 主动出站，无需公网 |
| **飞书** | App ID + App Secret + Verification Token（加密推送再加 Encrypt Key） | 平台回调 → Diana，**需要公网地址** |
| **企业微信** | 企业 ID + AgentId + Secret + 回调 Token/EncodingAESKey | 平台回调 → Diana，**需要公网地址** |

飞书和企业微信要填到对方后台的回调地址，机器人页会直接显示出来。各平台功能上的细微差异（比如 QQ 官方机器人只收得到 @ 消息、语音仅 OneBot 可用）见[配置文档](https://suink.github.io/Diana/configuration.html)。

## 内置能力

这些能力随程序自带，在控制台里按需开关和配置，不需要安装插件：

- **主动搭话** —— 不用 @ 也能自己判断什么时候插话、什么时候保持安静，回不回、为什么，事件中心都有记录（依赖平台推送全量群消息，QQ 官方机器人和钉钉只推 @ 消息，这两处不适用）。
- **联网搜索** —— 时效性问题先检索再回答，Exa 优先、Tavily 兜底。
- **链接解析** —— B 站 / YouTube / X / 小红书 / 抖音的视频图片直接解析发到群里。
- **文件解析** —— 群文件、PDF、Office、EPUB 的内容提取后交给模型理解。
- **点歌** —— 「放首稻香」就能收到一条语音；网易云、QQ 音乐、酷狗多曲库自动切换。
- **表情包** —— 从群里见过的表情包中挑一张合适的发出去。
- **图片识别** —— 视觉模型 + OCR（支持完全离线的本地方案），纯文本模型也能「看」图。
- **画图出图** —— 表格、流程图、时序图这类纯文本讲不清的东西，模型自己判断该出图时渲染成图片发出来（Markdown / Mermaid / SVG，需要「网页渲染」插件提供的无头浏览器）。
- **记忆与笔记本** —— 分层记忆控制 token 消耗；重要的事（群规、忌口、约定）记进笔记本，可审可改可恢复。
- **群级策略** —— 每个群独立的回复时段、黑白名单、人设和工具权限。
- **内置 Agent** —— 最小工具循环，可用文件、命令、浏览器工具，支持装载 Skills 和 MCP 服务。
- **对外 API** —— 让 CI、监控脚本借机器人的嘴发通知（默认关闭，Bearer 密钥鉴权）。

每项能力的完整说明见[配置文档](https://suink.github.io/Diana/configuration.html)。

## 日常管理

装好之后 `diana` 命令就在 PATH 里：

```sh
diana status    # 运行状态、版本、地址
diana logs -f   # 跟着看日志
diana restart   # 重启服务
diana doctor    # 体检：配置、目录、前端资源、服务健康
```

**升级**：重跑一遍安装命令，或直接在控制台里点升级。两条路都会先备份数据、校验新版本，健康检查不过自动回滚。Docker 部署则是拉新镜像重建容器。

**卸载**：`diana uninstall` 移除服务和程序、保留数据（重装即可恢复）；`diana uninstall --purge` 连数据一起删，不可恢复，会二次确认。

## 配置去哪改

一句话原则：**平时的配置都在控制台网页里改，`config.yaml` 只管服务本身。**

`config.yaml`（在安装目录）负责监听地址、端口、数据路径、管理员初始密码这类基础设施项，改完要重启。机器人和模型的配置存在数据库里，控制台改了立即生效——`config.yaml` 里的 `bot:` / `llm:` 段只在第一次启动、数据库还是空的时候播种一次，之后再改不会生效（启动日志会明说），这是给无人值守部署准备的。

完整字段和注释见 [`config.example.yaml`](./config.example.yaml)。

## 安全须知

- 控制台强制登录，无匿名接口；密码错误按来源 IP 退避锁定，防爆破。
- API Key、token、OAuth 令牌落库后不再回传明文，读接口只说「已配置」。
- 套了反向代理必须在 `config.yaml` 里声明 `trusted_proxies`（或给进程设环境变量 `DIANA_TRUSTED_PROXIES`），否则限流和日志里的来源 IP 都会变成代理地址。
- 配好主人账号后可用「快速登录」：登录页取一个 6 位验证码，私聊发给机器人即登录，机器人会回执来源 IP 和设备，被冒用能当场发现。

更多细节（会话管理、豁免路径、Agent 命令白名单）见[配置文档](https://suink.github.io/Diana/configuration.html)。

## 参与开发

后端 Go（Gin + SQLite），前端 Vue 3 + TypeScript（`frontend-next/`）。本机起全套开发环境：

```sh
make dev        # 后端 :18080 + Vite 前端 :5173，热更新
go test ./...   # 后端测试
```

提交前保证 `gofmt` 干净、`go mod tidy` 无 diff、后端测试与前端 `vue-tsc` 通过，CI 会检查这几项。目录结构、发布流程等约定见 [AGENTS.md](./AGENTS.md)。

## 文档

| | |
| --- | --- |
| [部署](https://suink.github.io/Diana/deploy.html) | 各种安装方式、服务器部署、首次登录 |
| [配置](https://suink.github.io/Diana/configuration.html) | 通道接入、模型分配、群策略、内置能力、安全边界 |
| [实现](https://suink.github.io/Diana/implementation.html) | 架构、消息决策链路、记忆分层 |
| [运维](https://suink.github.io/Diana/operations.html) | 更新回滚、日志备份、故障排查 |
| [在线演示](https://suink.github.io/Diana/demo/) | 真实控制台 + 模拟数据，随便点 |

## 许可证

`Limited Redistribution License (SuInk)`，详见 [LICENSE](./LICENSE)。
