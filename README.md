<div align="center">

# Diana

**简体中文** · [English](./README.en.md) · [日本語](./README.ja.md) · [한국어](./README.ko.md) · [Français](./README.fr.md) · [Deutsch](./README.de.md) · [Nederlands](./README.nl.md)

**像真人一样聊天，像 Agent 一样干活。**<br />
*Talks like a human, works like an agent.*

自托管单二进制 AI Agent —— 不用 @ 也会看气氛搭话，日常能记事、看图、点歌、扒链接，办事能联网、调 Skills / MCP、派本地编码代理改代码；数据与每一步决策都留在你机器上。

<sub>OneBot v11 · Telegram · QQ 官方机器人 · 微信 · iMessage · 内置控制台 —— 一个实例，同时在线</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=159f6c)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[官网与文档](https://suink.github.io/Diana/) · [在线演示](https://suink.github.io/Diana/demo/) · [下载最新版本](https://github.com/SuInk/Diana/releases/latest)

[![QQ 交流群 1127671893](https://img.shields.io/badge/QQ%20交流群-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview-green.jpg" alt="Diana 控制台总览：通道状态、消息统计、系统资源与实时事件流" width="100%" />

## 为什么是 Diana

市面上的聊天机器人通常只有两个极端：要么不 `@` 就装死、一开主动回复又疯狂刷屏尬聊；要么得配一整套 Python / Node 环境、写满 YAML、坏了还查不出为什么不回。

Diana 把「群聊活人感」和「真能干活的 Agent」塞进一个 Go 单二进制里：一头接主流大模型（OpenAI 兼容接口、Gemini、Anthropic 都行），另一头同时连上你常用的聊天软件，中间自带网页控制台管好一切。

- **像真人一样知进退** —— 群里不用 `@` 也能判断什么时候该插话、什么时候该安静；分层记忆加笔记本记住群里的约定，还会看图、解析视频链接、点歌、发表情包。
- **聊着聊着就把活干了** —— 联网搜索、文件解析、渲染图表都是开箱即用；内置 Agent 支持 Skills、MCP 和浏览器，甚至能直接给本机 Claude Code / Codex 派编码任务。
- **装完就能用，坏了查得清** —— 一条命令启动，家庭内网免公网直连，配置全在浏览器点选；每条消息为什么回、为什么没回、调了哪个模型、花了多少 token，事件中心都有完整记录。
- **数据在手里，权限守边界** —— 配置、记忆、日志全部存在本机 SQLite，没有云端依赖；内置**安全模式**可一键隔离本机命令、写文件与编码代理，放进群聊也不怕提示词注入。

## 三分钟跑起来

**① 安装。** 复制一条命令运行，脚本会自动下载、校验并启动：

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```sh
# 安卓 Termux（仅 ARM64，在 Termux 终端内运行，无需 root）
pkg install -y curl tar coreutils && curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# 或者用 Docker（在想放数据的目录里运行）
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

原生安装不需要管理员权限。**升级也是重跑同一条命令**；以前用 sudo 装过的，脚本会自动接着用原来的安装。更多安装方式见[部署文档](https://suink.github.io/Diana/deploy.html)。

**或者让 AI 按 Skill 安装。** 把下面这段话复制给能在目标机器执行命令的 AI 助手；详细指引统一维护在[安装 Skill](skills/diana-install/SKILL.md)，随仓库发布更新，不必反复修改或复制长 Prompt：

```text
请读取 https://raw.githubusercontent.com/SuInk/Diana/main/skills/diana-install/SKILL.md，按其中的指引帮我在当前机器安装 Diana 最新稳定版。
```

**或者手动下载。** 不想跑脚本就直接下载对应平台的文件（总是最新稳定版）：

| 平台 | 下载 | 用法 |
| --- | --- | --- |
| Windows x64 | [diana-windows-amd64.exe](https://github.com/SuInk/Diana/releases/latest/download/diana-windows-amd64.exe) | 放进单独的文件夹后双击运行，数据存在旁边的 `data` 目录 |
| macOS Apple 芯片 | [diana-macos-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-arm64.tar.gz) | 解压后运行 `./run.sh` |
| macOS Intel | [diana-macos-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-amd64.tar.gz) | 解压后运行 `./run.sh` |
| Linux x64 | [diana-linux-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-amd64.tar.gz) | 解压后运行 `./run.sh` |
| Linux ARM64 | [diana-linux-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-arm64.tar.gz) | 解压后运行 `./run.sh` |
| 安卓（Termux） | [diana-android-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-android-arm64.tar.gz) | 在 Termux 里解压后运行 `./run.sh`；它是跑在手机上的服务端，不是安卓 App。也可用上面的 Termux 一键命令 |

WebUI 已编进程序，不需要装 Node.js。每个文件的 SHA-256 写在同一 Release 的 [`latest.json`](https://github.com/SuInk/Diana/releases/latest/download/latest.json) 里，Release 页面每个文件旁也有显示；历史版本见 [Releases](https://github.com/SuInk/Diana/releases)。手动下载的不会注册 `diana` 命令，管理命令用 `./diana-webui <命令>`（Windows：`diana-webui.exe <命令>`）。

**② 登录控制台。** 浏览器打开 `http://127.0.0.1:18080`（从别的设备访问就换成这台机器的 IP）。管理员账号密码在安装结束时显示在终端里，**只显示一次，请当场保存**；Docker 可用 `docker logs diana` 查看。忘了密码运行 `diana passwd` 重置。

**③ 配置。** 控制台里依次完成三件事：

1. 「LLM 配置」页填上你的 API Key，同步模型列表，选一个默认模型；
2. 「机器人」页选平台、填凭据（各平台要填什么见[支持的平台](#支持的平台)）；
3. 给机器人发条消息试试——私聊直接回，群聊要 @ 它或用触发词开头。

就这些。没回复？事件中心会告诉你原因；`diana doctor` 能检查服务健康。

> [!TIP]
> **从别的电脑打不开控制台？** 默认只允许本机访问。安装命令后面加 `-s -- --host 0.0.0.0`（Windows：`-BindHost 0.0.0.0`）重跑一遍即可，详见[部署文档](https://suink.github.io/Diana/deploy.html)。开放后请套 HTTPS 反向代理，别把控制台直接暴露在公网。


## 支持的平台

启用的机器人配置会同时在线，回复始终回到消息来的那个通道；家庭宽带或内网无需公网 IP 即可运行。

| 平台 | 要准备的凭据 | 连接方向 |
| --- | --- | --- |
| **OneBot v11**（推荐 SnowLuma） | 支持正向 WS、反向 WS、HTTP API + HTTP 事件上报，在机器人页选择连接方式 | 连接方向取决于所选方式，可在局域网使用 |
| **Telegram** | BotFather 的 Bot Token（国内网络通常还要代理地址） | Diana 主动出站，无需公网 |
| **QQ 官方机器人** | 开放平台的 AppID + AppSecret（未上架可用沙箱） | Diana 主动出站，无需公网 |
| **微信** | 不用填：保存机器人后在「接入」里扫码登录（腾讯 iLink Bot，只有私聊） | Diana 主动出站，无需公网 |
| **iMessage** | 常开 Mac 上 BlueBubbles Server 的地址与密码，详见 [iMessage 接入说明](docs/imessage.md) | BlueBubbles webhook → Diana，Mac 能访问到 Diana 即可，可选轮询兜底 |
| **内置机器人** | 不用填：不接任何外部平台，在控制台「聊天」页直接私聊或语音通话 | 仅在控制台内运行，零门槛体验 |

平台接入细节：

- **OneBot v11**：QQ 个人号推荐用 SnowLuma 作接入端（群历史与成员接口已做适配）。反向 WS（默认）接入 `ws://<Diana 主机>:18080/onebot/v11/ws`；正向 WS 可配多个机器人（填同时提供 API 和事件的通用 WS 地址）；HTTP 模式将事件 POST 到 `/onebot/v11/http` 并配对 HMAC-SHA1 `secret`。固定的反向 WS 与 HTTP 上报入口各支持一个启用档，不包含 OneBot v12。
- **Telegram**：主人账号支持数字 ID、用户名与 `@用户名`（详见[Telegram 主人账号](docs/telegram-owner.md)）；媒体格式与群消息撤回按需核查见[Telegram 媒体与撤回说明](docs/telegram-media.md)。
- **iMessage & QQ 官方**：iMessage 回调地址在机器人页直接显示；各平台细微差异（如 QQ 官方机器人默认只收 `@` 消息，需群主调整消息范围）见[配置文档](https://suink.github.io/Diana/configuration.html)。

## 内置能力

这些能力随单二进制自带，在控制台里按需开关，无需折腾额外依赖：

- **主动搭话** —— 不用 `@` 也能判断何时插话、何时保持安静，回不回、为什么，事件中心全有记录（需平台推送全量群消息）。
- **记忆与笔记本** —— 分层记忆稳控 token 消耗；群规、忌口、约定等重要事实记进笔记本，随时可审、可改、可恢复。
- **内置 Agent 与安全模式** —— 可调用文件、命令、浏览器工具并按需装载 Skills 与 MCP 服务；支持一键切换**安全模式**，隔离本机命令、写文件、编码代理与带登录态浏览器，防群聊提示词注入。
- **编码代理** —— 只对主人开放（默认关闭）：在聊天里把改代码、跑测试的长任务派给本机 Claude Code / Codex CLI，扛得住重启，危险操作在聊天里二次确认，详见[多个编码代理](docs/coding-agents.md)。
- **联网搜索** —— 时效性问题默认通过沙盒浏览器并行检索并细读页面，也可在「提供商 → 搜索提供商」全局切换主备搜索源，详见[搜索提供商配置](docs/search-providers.md)。
- **链接与文件解析** —— 自动解析 B 站 / YouTube / X / 小红书 / 抖音视频图片与 [X 用户主页](docs/twitter-profiles.md)，以及群文件、PDF、Office、EPUB 内容。
- **看图与画图出图** —— 视觉模型 + 离线 OCR 让纯文本模型也能「看」图（详见[媒体预处理](docs/media-preprocessing.md)）；表格、流程图、时序图自动渲染成图片发回聊天，缺失字体按需下载（详见[多语言出图](docs/fonts.md)）。
- **点歌、表情包与随机复读** —— 网易云 / QQ 音乐多曲库自动切换（OneBot 发语音、Telegram 发带元数据的原生 MP3 播放器）；从群聊见过的表情包里挑合适的图；支持按群配置纯文字随机跟读。
- **群级策略** —— 每个群独立设置回复时段、黑白名单、人设、触发条件与工具权限。

每项能力的完整说明见[配置文档](https://suink.github.io/Diana/configuration.html)。

## 日常管理

装好之后 `diana` 命令就在 PATH 里：

```sh
diana status    # 运行状态、版本、地址
diana logs -f   # 跟着看日志
diana restart   # 重启服务
diana doctor    # 体检：配置、目录、前端资源、服务健康
```

Docker 部署在宿主机用 `docker exec diana diana <命令>`，例如 `docker exec diana diana logs -f`。

控制台与聊天里的常用运维入口：

- **扩展统一管理**：「扩展」页集中管理插件、Skills 与 MCP，全局定义、按机器人启用（[扩展管理](docs/extensions.md)）。
- **聊天里查用量、切模型**：主人可直接在聊天中查近 24 小时 Token 用量（[用量查询](docs/llm-usage.md)）或切换当前机器人的模型用途（[聊天切换模型](docs/chat-model-switch.md)）。
- **协议操作与记录性能**：群管理协议与回复欲望设置见[协议操作与回复设置](docs/bot-protocol.md)，事件分页与统计说明见[记录查询与性能说明](docs/event-record-performance.md)。

**忘记密码**：密码只以哈希存在数据库里，任何地方都查不回原密码，只能重置。一键安装（Linux / macOS / Windows）直接执行：

```sh
diana passwd
```

它会先停掉服务，生成新的随机密码并只显示这一次，再把服务启动起来；所有已登录的浏览器会被登出，机器人、模型、插件等其他数据不动。执行前会要求输入 `y` 确认，加 `-y` 跳过（脚本里无法交互时必须加）；加 `--username 新账号名` 可以顺便改账号名。

Docker 没法从容器里停掉自己，要在部署目录执行：

```sh
docker compose stop diana
docker compose run --rm diana passwd
docker compose start diana
```

手动运行（`run.sh` 或双击 exe）的进程 `diana passwd` 不会去动，先自己停掉再执行。

**升级**：重跑一遍安装命令，或直接在控制台里点升级。两条路都会先备份数据、校验新版本，健康检查不过自动回滚。Docker 部署则是拉新镜像重建容器。

**卸载原生安装**：`diana uninstall` 只移除当前命令所属安装目录的服务和程序，保留配置、数据库、日志和安装备份（重装即可恢复）；`diana uninstall --purge` 连该目录的数据一起删，不可恢复，会二次确认。其他安装目录和 Docker 容器不受影响。从用户安装迁移到 `/opt/diana` 时，旧用户目录可能仍保留程序和数据；卸载器会提示检测到的其他默认安装目录，需要分别处理。

**卸载 Docker 部署**：在该部署的 Compose 目录运行 `docker compose -f docker-compose.yml down`，停止并移除容器和项目网络；宿主机上的 `data/`、配置文件和镜像仍保留。如部署启用了 `docker-compose.update.yml` 更新助手（当前一键安装默认启用），使用 `docker compose -f docker-compose.yml -f docker-compose.update.yml down`，同时移除更新助手。原生卸载命令不会停止 Docker；只卸载原生版后，Docker 版仍可能在 `18080` 提供控制台。删除数据库属于永久清理，先确认备份和实际挂载路径，不要直接删除不确定归属的目录。

## 配置去哪改

一句话原则：**平时的配置都在控制台网页里改，`config.yaml` 只管服务本身。**

`config.yaml`（在安装目录）负责监听地址、端口、数据路径这类基础设施项，改完要重启。`admin:` 段可以预置首次启动的管理员账号密码，只在数据库里还没有管理员时生效；一键安装确认服务启动后会删掉其中的明文密码。机器人和模型的配置存在数据库里，控制台改了立即生效——`config.yaml` 里的 `bot:` / `llm:` 段只在第一次启动、数据库还是空的时候播种一次，之后再改不会生效（启动日志会明说），这是给无人值守部署准备的。

会话上下文固定按机器人配置档隔离，同平台的不同机器人也不会因群号或用户 ID 相同而共用上下文。旧配置中的 `isolate_platform_contexts` 字段不再生效，原设置接口已移除。此前共享的历史保留在原位置，不会自动分配给各机器人；消息互通及单独启用的跨平台公共记忆不受影响。

媒体缓存支持内容去重与可配置清理，文件和数据库日志按保留策略维护；升级前的数据库备份保留最近 3 天、最多 3 份。配置优先级、历史文件保留、备份与回滚边界见[缓存与磁盘维护](./docs/storage-maintenance.md)。

完整字段和注释见 [`config.example.yaml`](./config.example.yaml)。

## 安全须知

- 控制台强制登录，无匿名接口；密码错误按来源 IP 退避锁定，防爆破。
- API Key、token 落库后不再回传明文，读接口只说「已配置」。
- 套了反向代理必须在 `config.yaml` 里声明 `trusted_proxies`（或给进程设环境变量 `DIANA_TRUSTED_PROXIES`），否则限流和日志里的来源 IP 都会变成代理地址。
- 配好主人账号后可用「快速登录」：登录页取一个 6 位验证码，私聊发给机器人即登录，机器人会回执来源 IP 和设备，被冒用能当场发现。

更多细节（会话管理、豁免路径、Agent 命令沙盒）见[配置文档](https://suink.github.io/Diana/configuration.html)。

## 参与开发

后端 Go（Gin + SQLite），前端 Vue 3 + TypeScript（`frontend/`）。本机起全套开发环境：

```sh
make dev        # 后端 :18080 + Vite 前端 :5173，热更新
go test ./...   # 后端测试
```

提交前保证 `gofmt` 干净、`go mod tidy` 无 diff、后端测试与前端 `vue-tsc` 通过，CI 会检查这几项。开发环境、测试和 PR 要求的完整说明见 [CONTRIBUTING.md](./CONTRIBUTING.md)，发布流程等约定见 [AGENTS.md](./AGENTS.md)。

### 提交 PR

非常欢迎 PR！修 Bug、补文档、加功能都行；改动较大时建议先开 Issue 聊一下思路，免得白忙。

- **一个 PR 只做一件事**，从最新的 `main` 拉分支，不要夹带无关改动或格式化。
- **标题写实际改动**，格式 `type(scope): 用户能感知的变化`，例如 `fix(onebot): WebSocket 加心跳，死连接不再占着唯一的连接位`。`type` 常用 `feat` / `fix` / `security` / `docs` / `chore`；不要只写版本号或编号。
- **正文写清楚**：改了什么、为什么改、对用户或开发者的影响、验证方式；修 Bug 还要写根因。没实际跑过的部分如实说明。
- **CI 全绿再合并**：Go 测试、前端构建、各平台交叉编译和 Docker 镜像都要通过。行为有变化时同步更新 `docs/` 或 README。
- 引用其他 PR 时带上标题或一句话摘要，不要只写裸编号 `#123`。
- 标题和正文不写 `Co-Authored-By` 之类的代理署名尾注。

## 文档

| | |
| --- | --- |
| [部署](https://suink.github.io/Diana/deploy.html) | 各种安装方式、服务器部署、首次登录 |
| [配置](https://suink.github.io/Diana/configuration.html) | 通道接入、模型分配、群策略、内置能力、安全边界 |
| [实现](https://suink.github.io/Diana/implementation.html) | 架构、消息决策链路、记忆分层 |
| [链路](https://suink.github.io/Diana/pipeline.html) | 回复触发与断线补拉、回复流程、Agent 运行、浏览器机制、发送前审核（含流程图） |
| [记忆检索](docs/memory-recall.md) | 长期与跨平台召回、关联扩展、按需查阅和隐私边界 |
| [插件与订阅](docs/plugin-sharing.md) | 全局共享配置、旧配置迁移与跨机器人多目标通知 |
| [运维](https://suink.github.io/Diana/operations.html) | 更新回滚、日志备份、故障排查 |
| [在线演示](https://suink.github.io/Diana/demo/) | 真实控制台 + 模拟数据，随便点 |

## 许可证

`Limited Redistribution License (SuInk)`，详见 [LICENSE](./LICENSE)。

### 第三方授权

第三方服务可通过[受信应用私聊登录](docs/trusted-app-login.md)复用主人身份确认。默认关闭，可在「设置 → 账号与安全 → 第三方授权」开启并管理应用、一次性连接密钥和指定机器人；应用自行管理登录会话。
