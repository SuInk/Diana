<div align="center">

# Diana

**可自托管的多平台 AI 助手 —— OneBot v11 与 Telegram 同时在线，数据留在自己的机器上。**

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=c83f76)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[官网与文档](https://suink.github.io/Diana/) · [在线演示](https://suink.github.io/Diana/demo/) · [下载最新版本](https://github.com/SuInk/Diana/releases/latest) · [English](./README.en.md)

</div>

<br />

<img src="./docs/assets/diana-webui-overview.png" alt="Diana 控制台总览：通道状态、消息统计、系统资源与实时事件流" width="100%" />

## 目录

- [这是什么](#这是什么)
- [特性](#特性)
- [快速开始](#快速开始)
- [首次配置](#首次配置)
- [通道支持](#通道支持)
- [访问安全](#访问安全)
- [能力与扩展](#能力与扩展)
- [配置文件](#配置文件)
- [部署形态](#部署形态)
- [开发](#开发)
- [项目结构](#项目结构)
- [文档](#文档)
- [许可证](#许可证)

## 这是什么

Diana 是一个用 Go 写的多平台 AI 助手服务：内置 LLM 兼容层、平台适配层、Gin WebUI 和插件系统，编译成单个二进制运行。当前自带 OneBot v11 和 Telegram 两类通道，WebUI 可以管理多个机器人配置、模型分配、群聊策略、插件与内置 Agent。

配置、记忆、日志都存在本机的 SQLite 里，不依赖任何托管服务；每条消息为什么回复、为什么不回复、用了哪些工具、花了多少 token，都能在事件中心查到。

## 特性

| | |
| --- | --- |
| **多通道同时在线** | OneBot v11 与 Telegram 并行运行，回复、图片和提醒始终回到来源通道；会话上下文可按配置隔离或共享 |
| **模型职责拆分** | 对话、视觉理解、意图识别、图片生成分别绑定 Provider 与模型，保存前用真实请求验证 |
| **内置联网搜索** | 无需安装插件，面对时效性内容可以先检索再回答；Exa MCP 优先，Tavily 兜底 |
| **图片文字识别** | 图片可同时走视觉模型与 OCR（LLM 转写 / 自托管 OCR 服务 / 本地 tesseract）；对话模型不支持看图时也能只收识别后的文字。识别结果按图片内容哈希落库，同一张图或表情包只识别一次 |
| **表情包发送** | 从当前群或私聊已经缓存的表情包中检索，Agent 先按名称与图片描述选择，再通过来源通道发送；候选不会跨会话混用 |
| **群级回复策略** | 每个群独立设置回复时段、黑白名单、触发词、人设、群等级门槛和工具权限 |
| **长期记忆与检索** | 近期上下文、压缩摘要、结构化事实和超长历史检索分层工作，控制 token 消耗 |
| **笔记本** | 特意记下来、而且必须准确的东西：梗和黑话、群规和约定、谁的忌口、答应了还没做的事。按会话或全局归属，每次改动留修订记录，删了能恢复；聊到就自动注入上下文，也能在控制台逐条改。与自动抽取的结构化记忆分工：那边是「说过就记住」，这边是「记错了要能改」 |
| **内置 Agent** | Pi 风格的最小工具循环，可调用文件、命令、浏览器工具，并按需装载 Skills 与 MCP 服务 |
| **完整事件审计** | 记录回复原因、模型调用链、token 与错误；操作日志区分操作人 |
| **一键安装与自更新** | 安装器校验 SHA-256、备份数据、健康检查失败自动回滚；控制台可直接升级 |

## 快速开始

### 一键安装（推荐）

安装脚本自动识别系统与架构，下载最新稳定版完整包，核对 `SHA256SUMS`，生成本地管理凭据并启动。**重复执行同一条命令即可安全升级**：升级前备份数据库和当前运行文件，健康检查失败时自动恢复。

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

> macOS 上安装器会把运行时装进安装目录里固定的 `Diana.app`，用固定的 `com.suink.diana` 做本地 Ad-hoc 签名，并把 designated requirement 改写成只认这个标识。这样每次更新换了二进制，系统仍然认它是同一个 Diana：麦克风、完全磁盘访问这些授权不会失效，「隐私与安全性」列表里也不会堆出一排同名条目。没装命令行工具（`codesign` 不可用）时会跳过签名，功能不受影响，只是授权可能要重新点一次。

完成后打开 `http://127.0.0.1:18080`。首次生成的管理员账号和密码会显示在终端，并保存在安装目录的 `config.yaml`——请妥善保存，不要公开该文件。

默认安装目录：Linux / macOS 为 `~/.local/share/diana`，Windows 为 `%LOCALAPPDATA%\Diana`。

安装脚本本身用环境变量传参（这些是安装器的参数，不是应用配置）：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_VERSION=v0.8.9 DIANA_INSTALL_DIR=/opt/diana DIANA_PORT=18081 sh
```

**装在服务器上要从别的机器打开后台，必须设 `DIANA_HOST`。** 默认只绑
`127.0.0.1`——WebUI 带管理权限，装完就对公网敞开不是合理默认，所以这一步是显式的：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_HOST=0.0.0.0 sh
```

监听非回环地址时，记得在防火墙和云厂商安全组里放行端口，并尽量套一层带 TLS 的反向
代理，不要把控制台直接暴露到公网。

已经装过的实例带上新的 `DIANA_HOST` 重跑安装脚本即可改绑定——脚本会顺带重启服务，
改完立即生效（`DIANA_START_AFTER_INSTALL=false` 时不重启，需要自己启动）。也可以直接
改 `config.yaml` 里的 `server.host`，但这样必须手动重启才会生效：

```sh
systemctl --user restart diana.service   # Linux
launchctl kickstart -k "gui/$(id -u)/com.suink.diana"   # macOS
```

常用配置可以在安装时一并写进生成的 `config.yaml`，省得装完再进 WebUI 填一遍：
`LLM_API_KEY`、`LLM_BASE_URL`、`LLM_MODEL`、`LLM_API_FORMAT`、`LLM_IMAGE_MODEL`、
`DIANA_LOCAL_MEDIA_BASE_URL`、`DIANA_NAPCAT_WEBUI_URL`、`DIANA_NAPCAT_WEBUI_TOKEN`。
更多项用 `DIANA_CONFIG_FILE` 指向一份 YAML 片段，内容会原样并进 `config.yaml`：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_HOST=0.0.0.0 LLM_API_KEY=sk-xxx DIANA_CONFIG_FILE=/root/diana-extra.yaml sh
```

注意 `LLM_*` 这些只是安装器参数：它们被写进 `config.yaml` 的 `llm:` 段，而那一段
只在数据库为空时播种一次。装完之后再改这些值不会有任何效果，要去 WebUI 改。

### Docker

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
cp config.example.yaml config.yaml
# 改 config.yaml：管理员密码、token、LLM 配置
docker compose up -d --build
```

`config.yaml` 以只读方式挂进容器，`DIANA_CONFIG` 指向它。不挂也能起，打开 WebUI 走安装向导即可。

<details>
<summary><code>docker run</code> 方式</summary>

```sh
docker build -t diana:latest .

docker run -d \
  --name diana \
  --restart unless-stopped \
  -p 18080:18080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -e DIANA_CONFIG=/app/config.yaml \
  diana:latest
```

</details>

容器启动后同样访问 `http://127.0.0.1:18080`；OneBot v11 客户端连接 `ws://127.0.0.1:18080/onebot/v11/ws`。客户端与 Diana 不在同一台机器时，把 `127.0.0.1` 换成 Diana 宿主机的 IP 或域名。

### Release 完整包

不使用安装脚本时，可从 [GitHub Releases](https://github.com/SuInk/Diana/releases) 下载完整包（Linux/macOS 用 `.tar.gz`，Windows 用 `.zip`）。包内含后端二进制、`frontend-next/dist` 和启动脚本，无需额外构建：Unix 运行 `run.sh`，Windows 运行 `run.bat`。

下载后先校验再替换程序，强制更新也不会绕过校验：

```sh
sha256sum -c SHA256SUMS --ignore-missing
```

> macOS 用 `shasum -a 256 <文件名>`，Windows 用 `Get-FileHash <文件名> -Algorithm SHA256`。

从完整 Release 包运行时，WebUI 可直接安装后续稳定版：下载对应平台的完整包，校验、备份并切换，探活失败自动恢复数据库与旧版本。备份保存在安装目录的 `.diana-updates` 下。容器部署由镜像更新器负责，源码 checkout 走 Git 更新流程。

### 从源码构建

面向开发和自定义部署。需要 Go `1.26.5`、Node.js `22` 和 npm。

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
go mod download

cd frontend-next && npm ci && npm run build && cd ..
go build -o dist/diana-webui ./cmd/webui

./dist/diana-webui
```

本机开发可以一条命令同时起后端和 Vite 前端：

```sh
make dev                                   # 后端 18080，前端 5173
make dev BACKEND_PORT=18081 FRONTEND_PORT=5174
node scripts/dev.mjs                       # 没装 make 时
```

## 首次配置

1. **登录控制台** —— 打开 `http://127.0.0.1:18080`，用终端给出的账号密码登录。
2. **配置模型** —— 在「LLM 配置」页填 Provider 与 API Key，同步模型列表后再选默认模型。无人值守部署也可以在 `config.yaml` 里预置，第一次启动时播种进数据库：

   ```yaml
   llm:
     provider: openai_compatible
     api_key: your-key
     base_url: https://example.com/v1
     model: gpt-5.4-mini
     image_model: gpt-image-2
   ```

   支持的 provider：`openai_compatible`、`gemini`、`anthropic`。支持命名保存多套配置并切换激活项。
3. **连接机器人** —— 在「机器人」页创建配置，填平台账号、主人 ID 和触发词。
4. **检查事件** —— 发一条消息，在事件中心确认回复原因与模型调用链。

<details>
<summary>用 config.yaml 直接拉起一个 OneBot v11 机器人</summary>

```yaml
bot:
  platform: onebot-v11
  enabled: true
  onebot_reverse_ws_endpoint: ws://127.0.0.1:18080/onebot/v11/ws
  onebot_access_token: your-onebot-token
  bot_account: "10001"
  group_triggers: [Diana, diana]

llm:
  provider: openai_compatible
  api_key: your-key
  model: gpt-5.4-mini
```

```sh
./dist/diana-webui --config ./config.yaml
```

启动后私聊直接触发；群聊中 `@机器人` 或以触发词开头才触发。这两段只在数据库为空时播种一次，之后改配置请去 WebUI。

</details>

## 通道支持

| 分类 | 平台 | 接入方式 |
| --- | --- | --- |
| OneBot v11 | OneBot v11 | 反向 WebSocket，由 OneBot v11 客户端连到 Diana（NapCat、Lagrange.Core、go-cqhttp 等同属这一类） |
| Telegram | Telegram Bot API | 官方长轮询，由 Diana 主动出站连接，不需要公网地址和 webhook |

所有启用的配置会同时在线。Telegram 只需要 BotFather 给的 Bot Token；国内网络通常还要在机器人页填代理地址，部署了本地 Bot API server 可填自建地址绕过 50MB 上传限制。

两个平台的能力差异：

- **群等级门槛只对 OneBot v11 生效**。Telegram 没有群等级概念，准入设置里不显示该项。
- **语音消息、@某人** 依赖 OneBot 的 CQ 码，Telegram 上自然降级：正文照发，但不会 @ 到人。
- **本地媒体**：OneBot 侧由接入端拉取 Diana 的 `/media/resolver` 地址；Telegram 拉不到本机地址，改为直接 multipart 上传。

## 访问安全

WebUI 从首次启动起强制登录，本机和公网访问同一套规则。默认管理员账号安全随机生成（`diana#` 加 16 位随机字符串），持久化在 SQLite。

- 首次启动未在 `config.yaml` 的 `admin` 段填密码时会生成随机密码，账号密码仅在该次启动的标准错误日志中显示一次。
- 也可在首次启动前写好 `admin.username` 和 `admin.password`；已有凭据时不会覆盖。
- 登录后可在设置页的「访问安全」中修改账号和密码。账号 2–64 个字符、不含空格与控制字符（`diana#` 只是自动生成账号的形式，不是必须的前缀），密码至少 8 位。
- 所有 `/api` 接口需要登录，会话有效期 30 天；改密会使全部已登录会话失效。设置页可查看登录中的设备并逐个踢下线。

**管理员快速登录**：在「机器人」页开启并配置主人账号后，登录页可获取一个 6 位一次性验证码，主人私聊发给机器人即完成登录，随后机器人回一条带来源 IP 与设备的回执。回执不需要主人做任何动作，作用是别让这条消息被静默吞掉——万一验证码是被诱导转发的，主人当场就能发现并去踢掉会话、改密码。网页没能自动跳转时（轮询被掐断、页面被手机浏览器回收、换了标签页），把已经私聊发出的那个验证码填进登录页输入框即可直接登录。兑换接口按来源限流，防止穷举抢走已确认的配对。验证码 5 分钟有效、一次性使用，需要机器人在线。**服务端不会主动发出验证码**，因此没有匿名请求能触发的骚扰面。

**登录爆破防护**：密码登录与改密共用一份失败预算，按来源计数——连续失败 5 次后开始锁定，退避从 30 秒逐级翻倍到 30 分钟封顶，返回 `429` 并带 `Retry-After`，任意一次成功即清零；另有一层全局兜底挡分布式撞库，锁定会写入操作日志。

> [!IMPORTANT]
> 按来源计数依赖真实客户端 IP。Diana 默认不信任任何反向代理，套了反代之后所有请求的来源地址都会变成反代自己。**公网部署请务必设置 `DIANA_TRUSTED_PROXIES`**（逗号分隔的 IP 或 CIDR），声明后才会解析 `X-Forwarded-For`。会话 cookie 未设 Secure 以兼容内网 HTTP，公网部署请套 HTTPS 反向代理。

豁免路径：登录及快速登录端点、`/api/health`（监控探活）、`/onebot/*`（由 OneBot access token 单独鉴权）、群管理页（自有验证码流程）。

## 能力与扩展

### 准入控制

「机器人」页的「准入控制」和「群管理」页的每个群，都能限制机器人在什么条件下才回复。全局设置为默认值，群级设置整体覆盖全局（不是逐字段合并）。

- **群准入模式**：默认「黑名单」，除禁用群外都工作；切到「白名单」后只在列出的群工作，被拉进别的群不会回话。白名单模式下禁用群列表仍生效。
- **群等级门槛**：指群内活跃度等级（Lv.1~6），不是账号等级（太阳月亮星星，OneBot 协议拿不到）。等级按群独立累积。
- **回复时段**：结束早于开始表示跨夜（如 `22:00-06:00`），两者相同视为全天开放。时区填 IANA 名称（如 `Asia/Shanghai`）。静默期可配提示语，同一会话每小时最多提示一次。
- **豁免 / 屏蔽用户**：豁免用户无视等级与时段；屏蔽用户群聊私聊都不回。
- **主人绕过**：默认开启，建议保持——否则时段或等级配错时你自己也会被挡在门外，OneBot 侧没有补救手段。

部分 OneBot 实现不会在消息事件里带 `level`，Diana 会按需通过 `get_group_member_info` 补齐并缓存 10 分钟。**拿不到等级时默认放行**：各实现返回差异很大，把「读不到」当成「等级 0」拒绝会让整个群失联。需要严格拦截可改成「拦截」，但要清楚代价。

### 插件

「机器人插件」区域可以启用、停用和配置官方内置插件，内置能力无需安装或卸载。

- **链接解析**：解析并发送 B 站、YouTube、X、小红书、抖音的图片或视频；知乎、微博、GitHub 只抓标题描述。大小、时长、清晰度、图集数量可调。各平台 Cookie、yt-dlp Cookie 文件和代理地址直接在插件设置里填，优先于同名环境变量；凭据保存后读接口只回传「已配置」标记。卡片内会显示 `yt-dlp`、`ffmpeg`、`node` 的探测结果，并可通过受控的包管理器安装缺失项。
- **文件解析**：支持 OneBot 文件段和文件直链，提取内容作为 LLM 上下文。覆盖纯文本、配置、常见源码、PDF、Office（docx / xlsx / pptx）、ODF（odt / ods / odp）和 EPUB；文件大小上限在插件设置里按 KB/MB/GB 选择单位填写。
- **音乐增强**：两件事共用一条「把一首歌变成一条语音」的通路。**链接解析**——群里分享的音乐链接直接下成语音发出来，不必切出去开 App；网易云认网页版、单页应用锚点、移动端路径和 `163cn.tv` 短链，QQ 音乐认 `songDetail/<mid>` 与短链，酷狗认 `#hash=...&album_id=...`；歌手页、专辑页、歌单页不会被误当成单曲。**点歌**——「放首稻香」「来一首适合睡前听的」没有共同的关键词，所以不做同义词表，改由模型通过 `diana.music` 工具自行判断要不要点歌、点哪一首；群成员和主人一样可用，可在插件设置里关掉。**多曲库**——网易云、QQ 音乐、酷狗并列，可勾选启用哪几家、点歌先问哪一家。判定的是「搜得到而且放得出来」而不是「搜得到」：一首歌在这家是会员专享、在那家能试听是常事，一家给不出播放地址就自动换下一家；分享的链接放不了时也会按歌名去别家找回来，来源会如实写进给模型的上下文。各家的自建 API 地址和 Cookie 分开填；不填时网易云能拿到可试听曲目，QQ 音乐和酷狗的可用性取决于对方接口当时的策略。时长、文件大小、音质和「是否同时发歌名」都可调；配了 Silk 编码器路径就先转成 Tencent Silk 再发，留空则交给 OneBot 客户端自己转码。语音只在 OneBot v11 上播放正常。
- **表情包发送**：启用内置 Agent 后，把历史里带表情摘要的缓存图片作为候选，排除普通 `[图片]`。Agent 通过 `diana.sticker` 先搜索候选、再发送选中的表情包；插件可配置扫描历史数量、候选返回数量、是否收录未命名的 `[动画表情]`，以及两个默认关闭的共享开关。跨群与跨私聊可分别启用，范围始终限制在同一机器人会话命名空间，不向模型暴露来源群号或用户号。
- **图片文字识别**：图片进上下文前先做一次 OCR，可选 LLM 视觉转写、自托管 OCR 服务（PaddleOCR / RapidOCR）或本地 `tesseract`，后两者完全离线。交付方式可选「图片 + 文字」或「仅文字」——后者让不支持多模态的对话模型也能处理图片消息。
- **联网搜索**：默认安装并启用，可停用和配置但不能卸载。独立于内置 Agent 开关，未开 Agent 时不会同时开放本地文件、命令或浏览器工具。

### 授权登录（OAuth）

LLM 配置页的「授权登录」区域用 OAuth 代替 API Key。控制台跑在服务器上、浏览器未必和它同机，所以回调不强求落回本机：点「登录」后在自己的浏览器里完成授权，把地址栏里那条回调地址整条粘回来即可（只粘其中的 `code` 也认）。登录后在配置档的「凭据方式」里选中对应提供商，令牌会在过期前自动续期；同时填了 API Key 的话，续期失败时会自动回落到它，不至于整个配置档一起哑掉。

提供商是配置项而不是写死的代码：内置 OpenRouter（它的 PKCE 授权本就是给第三方应用用的，换到的是一把归你所有、可随时吊销的 Key），其余可以在「自定义提供商」里填授权地址、令牌地址、Client ID 和 Scope 自行接入，适合自建网关或本项目没有内置的服务。授权地址和令牌地址必须是 https，本机回环地址（`127.0.0.1`）可以用 http。

> [!NOTE]
> 发行版不预置那些需要冒用第一方客户端 Client ID 才能拿到订阅账号登录态的提供商。那类用法超出订阅本身的授权范围，是否使用属于部署者自己的判断——需要的话可以在「自定义提供商」里自行填写。

令牌与 Client Secret 和 API Key 同库同待遇：读接口只回传「是否已登录」「何时过期」，明文一个字节都不出去。

### 内置 Agent

启用后机器人以 [Pi Agent](https://github.com/earendil-works/pi) 风格的最小状态与工具循环处理消息：规划、调用工具、观察结果、最终回复。运行时为 Go 原生实现，完整 Release 包不需要额外安装 Node。

内置工具：`list_files`、`read_file`、`run_command`（工作目录内白名单命令，不经过 shell，带超时与输出截断）、`browser_open` / `browser_text` / `browser_click` / `browser_type` / `browser_screenshot`（通过 Chrome DevTools Protocol）。

Agent 用统一扩展目录管理三类能力：**内置插件**（沿用原有启停与权限规则）、**Skills**（渐进式加载，只把名称和说明放入上下文，需要时再 `skills.read` 读取完整 `SKILL.md`；可从文件、HTTP(S) 或 ZIP 安装，卸载移动到 `.trash/` 便于恢复）、**MCP 服务**（stdio 与 Streamable HTTP，通过连接测试后才写入 `.mcp.json`，`env` 和 `headers` 支持 `${ENV_VAR}`）。

> [!WARNING]
> 扩展管理工具只提供给主人，且只有当前用户消息明确要求变更时才执行——网页、工具结果、Skill 或 MCP 返回内容都不能代替用户授权。建议把 Agent 工作目录配到独立资料目录，不要指向含密钥或生产数据的目录；生产环境用 `DIANA_AGENT_COMMAND_ALLOWLIST` 只放行必要命令。

浏览器工具需要 Chrome/Chromium 开启远程调试端口：`chrome --remote-debugging-port=9222`。

### 第三方 NoneBot 插件

Go 主程序不能直接加载 Python NoneBot 插件，可单独运行一个 NoneBot sidecar：在 NoneBot2 项目中装好插件并配置 OneBot v11 反向 WebSocket driver，然后在 Diana 的「OneBot v11 机器人」页启用 `NoneBot 插件桥`，地址默认 `ws://127.0.0.1:8080/onebot/v11/ws`。Diana 会把 OneBot 事件转发给 sidecar，插件调用 `send_msg`、`get_group_info` 等 API 时再转发回当前 OneBot 客户端。

### 对外 API

外部系统（CI、监控、脚本）可以通过 HTTP 接口让机器人把消息推送到指定会话，把 Diana 当作通知出口。它是一个内置插件（`对外 API`），**默认关闭**：在「插件」页或「设置 → 安全 → 对外 API」卡片上启用后外部调用才放行，停用后立即返回 403。密钥同样在该卡片里创建，明文只在创建时显示一次，存储里只保留 SHA-256 哈希；吊销立即生效，密钥管理不受插件开关影响，可以先备好密钥再开闸。每次调用记录进日志中心（`actor` 为 `openapi:<密钥名>`）。

```sh
curl -X POST http://127.0.0.1:18080/openapi/v1/messages \
  -H "Authorization: Bearer <密钥>" \
  -H "Content-Type: application/json" \
  -d '{"group_id": "123456", "text": "构建 #42 通过"}'
```

- 目标会话用 `group_id` 或 `user_id` 指定；群聊目标同时带 `user_id` 时会 @ 那个人。多通道部署再带 `platform`（如 `onebot-v11`、`telegram`）或 `profile_id` 指路，只有一条启用通道时可省略。
- `GET /openapi/v1/status` 用同一密钥探活，返回运行状态与可投递通道列表。
- 单把密钥默认限流每分钟 60 次，可在插件设置里调整；超限返回 `429` 并带 `Retry-After`。接口只做投递，不触发模型调用。

### 日志中心

「日志中心」页查看持久化的操作日志和错误日志：LLM 配置保存/切换、机器人启停、插件管理、系统更新等动作都会记录，并带 `actor` 操作人（WebUI 默认 `web:<客户端 IP>`，也可由网关通过 `X-Diana-Actor`、`X-Operator`、`X-Forwarded-User` 传入；聊天内模型配置命令记 `qq:<用户账号>`）。

```text
GET /api/logs?kind=operation&limit=100
GET /api/logs?kind=error&limit=100
```

## 配置文件

所有配置都在 `config.yaml` 里，没有配置类环境变量。进程按 `--config`、`DIANA_CONFIG`、工作目录、可执行文件同级目录的顺序查找；一个都没有就全部走内置默认值，直接打开 WebUI 走安装向导。完整字段见 [`config.example.yaml`](./config.example.yaml) 和[配置文档](https://suink.github.io/Diana/configuration.html)。

配置分两层，边界是**这项能不能在 WebUI 里改**：

| 段 | 层级 | 生效方式 |
| --- | --- | --- |
| `server` / `storage` / `admin` / `update` / `napcat` | 基础设施，WebUI 里没有入口 | 每次启动都读，`config.yaml` 是唯一来源 |
| `bot` / `llm` | 业务配置，WebUI 随时可改 | **只在数据库为空时播种一次**，之后以数据库为准 |

`bot` / `llm` 两段是给无人值守部署用的：容器第一次起来时把 token 和 API Key 带进去，省掉手动走向导。数据库里已经有配置之后这两段就不再生效，启动日志会明确打一行 `config: bot section in ... was NOT applied`——不会让你对着一份不生效的配置排查。想重新播种就清空数据库，想改配置就去 WebUI。

```yaml
server:
  # 要从别的机器访问就填 0.0.0.0，只本机用就留 127.0.0.1。
  host: 127.0.0.1
  port: "18080"
  # 可信反向代理的 IP 或 CIDR；设置后才解析 X-Forwarded-For。
  trusted_proxies: []
  frontend_dist: ""

storage:
  db_path: data/diana.db
  # 留空表示只写标准输出。
  log_path: logs/diana.log
  media_dir: ""
  media_max_mb: 0
  media_cache_mb: 0
  # 留空时按接入端反连握手用的地址动态推断。
  local_media_base_url: ""

admin:
  # 两项都留空时首启自动生成账号和强密码，只在日志里打印一次。
  username: ""
  password: ""

update:
  root: "."
  apply_enabled: true
  release_enabled: true
  group_test_enabled: false

napcat:
  webui_url: ""
  webui_token: ""

# 以下两段只在数据库为空时播种一次，字段名和 WebUI 配置接口的 payload 一致。
bot:
  platform: onebot-v11
  enabled: true
  onebot_reverse_ws_endpoint: ws://127.0.0.1:18080/onebot/v11/ws
  onebot_access_token: ""
  bot_account: ""
  owner_id: ""
  group_triggers: [Diana, diana]

llm:
  provider: openai_compatible
  base_url: https://api.example.com/v1
  api_key: ""
  model: ""
```

结构化日志存在 `storage.db_path` 指向的 SQLite 中；`storage.log_path` 仍用于普通运行日志文件。

### 仍然是环境变量的部分

只有两类还走环境变量，它们在 WebUI 里没有对应项，也不存在两个真相源的问题：

- `DIANA_CONFIG` —— 配置文件路径。它不是配置，是指向配置的引导指针。
- 外部集成凭据与外部程序路径 —— 各站点解析用的 `BILI_SESSDATA`、`DOUYIN_CK`、`XHS_CK`、`RESOLVER_PROXY`，搜索服务的 `EXA_API_KEY` / `TAVILY_API_KEY`，以及 `DIANA_FFMPEG_PATH`、`DIANA_SERVICE_MANAGER` 一类的本机环境探测项。链接解析的各平台 Cookie 也可以直接在插件设置里填，那里填的优先于同名环境变量。

## 部署形态

<details>
<summary>systemd</summary>

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
Environment=DIANA_CONFIG=/opt/diana/config.yaml
ExecStart=/opt/diana/diana-webui
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

</details>

<details>
<summary>交叉编译</summary>

```sh
# macOS
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui

# Linux
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
```

```powershell
# Windows
$env:GOOS="windows"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -o dist\diana-webui.exe .\cmd\webui
```

Release 里也有各平台的裸二进制，但**裸二进制不含前端资源**，普通用户请下载对应平台的完整包。

</details>

## 开发

```sh
go test ./...                 # 后端测试
make dev                      # 后端 + Vite 前端热更新
make deps                     # 安装前端依赖
make run                      # 构建前端并以 FRONTEND_DIST 运行后端
make build                    # 生产构建
```

前端在 `frontend-next/`（Vue + TypeScript），是当前唯一受支持的控制台：总览 Dashboard（连接检查清单、今日/24h 消息统计、实时事件流）、SSE 实时推送、三步配置向导和移动端适配，详见 [frontend-next/README.md](./frontend-next/README.md)。

配套后端接口：

```text
GET /api/stats    # Dashboard 统计（进程内计数，重启清零）
GET /api/events   # SSE 实时推送：status / stats / bot_event
GET /api/health   # 版本与运行时长
```

提交前请确保 `gofmt` 干净、`go mod tidy` 无 diff、`go test ./...` 与前端 `vue-tsc` 通过——CI 会卡这几项。开发约定见 [AGENTS.md](./AGENTS.md)。

## 项目结构

```text
.
├── cmd/webui/          # Gin WebUI 和 OneBot endpoint 入口
├── model/assistant/    # 机器人运行时、通道适配、插件与 Agent
├── model/llm/          # LLM 统一接口和 provider adapters
├── model/storage/      # SQLite 存储与消息历史
├── webui/              # WebUI API handler 与鉴权
├── frontend-next/      # Vue + TypeScript 控制台
├── docs/               # GitHub Pages 文档站
├── scripts/            # 安装脚本与开发脚本
└── .github/workflows/  # CI 与 Pages 部署
```

## 文档

| 页面 | 内容 |
| --- | --- |
| [部署](https://suink.github.io/Diana/deploy.html) | 一键安装、Release 完整包、Docker、源码构建、首次登录 |
| [配置](https://suink.github.io/Diana/configuration.html) | 通道接入、模型分配、群聊策略、Agent 工具、安全边界 |
| [实现](https://suink.github.io/Diana/implementation.html) | 系统架构、消息决策链路、记忆分层、媒体与存储 |
| [运维](https://suink.github.io/Diana/operations.html) | 更新回滚、日志备份、故障排查、开发发布 |
| [在线演示](https://suink.github.io/Diana/demo/) | 真实控制台 + 模拟数据，不连接真实机器人 |

## 许可证

本项目使用 `Limited Redistribution License (SuInk)`，详见 [LICENSE](./LICENSE)。
