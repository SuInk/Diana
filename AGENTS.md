# Diana Agent Instructions

## 语言

始终用中文回复用户，长任务中的进度更新和最终总结也一样。代码、标识符、提交消息和 PR 标题沿用各自已有的语言惯例。

## 提交与 PR

### 提交

- 提交消息正文不得包含 `Co-Authored-By`、`Claude-Session` 等任何代理署名尾注。
- 只暂存自己改过的文件（`git add <路径>`，不用 `git add -A`）。同一仓库可能有别的 Claude 会话在工作；提交前先看 `git status`，不碰与本次改动无关的已暂存内容。

### PR 标题与正文

- PR 标题与正文描述改动本身，不署代理名。
- PR 标题必须描述实际改动，不能只写版本号或编号。
- PR 正文开头必须是一段说明，并包含 `## Why` 小节，否则 PR 正文的 CI 检查会失败。
- PR 正文必须包含：改了什么、为什么改、用户或开发者影响、Bug 根因（修复类 PR）、验证方式。
- 在对话、Release 或其他文档中引用 PR 时，不能只放裸编号，例如只写 `#4`；必须同时给出该 PR 的改动标题或一句话摘要。

### 合并流程

用户说「合并」或要求合并时：

1. rebase 到最新的 `origin/main` 并解决冲突。
2. 推送后等待所有 CI 检查跑完。等待后重新查一次状态，不得拿过期的运行结果报告「CI 通过」。
3. 用 `gh pr merge --squash --delete-branch` 压缩合并。
4. 清理本地分支和 worktree。

无法开启自动合并时，等 CI 跑完后手动合并。CI 全绿后不再二次确认，也不反复复查。

## 排查与诊断

- 读过相关代码或查过 miku（SSH）上的生产日志/trace 之前，不得断言根因。不确定就直说并继续查；QQ API 变更这类外部平台行为用网页搜索核实。
- 之前任务里的只读限制不沿用到新任务。用户要求时，可以在 miku 上做端到端测试。

## 测试

- Go：本地用 `scripts/test-serial.sh` 跑改动涉及的包（不传参数时自动按相对 `origin/main` 的改动挑包，也可以传包路径），默认跑一遍普通测试（本机时区），改了并发代码时用 `DIANA_TEST_RACE=1` 打开 `-race`。PR 的 CI 跑全仓库 `go test ./...`（UTC），本地不再每次跑全量。全仓库 `go test -race ./...` 只在打 `v*` 标签发版时由 Go Race Test 跑一次，挡在 Docker 发布和 GitHub Release 前面；PR 和 Canary 不跑。
- 不要直接起 `go test`：同一台机器上常有多个会话同时跑测试，脚本让它们全机按先来后到排队、限并发（默认 4，`DIANA_TEST_PROCS` 可调）并降优先级，避免整机卡死。排队等待时不要另开测试绕过。同一 worktree 再次调用时，还在排队的上一次调用会以退出码 3 结束，表示被新请求取代，不算测试失败。
- 前端：改了常量或草稿/配置键后，运行沙箱内的单元测试。
- 新建测试文件前先确认文件是否已存在，避免覆盖。
- 行为改动要用当前模型做生产回放验证，不用 gpt-5.4-mini 这类过时模型；注意 429 限流。

## 前端与 UI

与管理界面现有的交互模式保持一致。新设置放在相关设置旁边，例如模型相关选项放在聊天模型下面。做复杂布局前先简要说明打算放在哪里。改完在浏览器里（demo 模式）目视验证，截图时保持浏览器面板可见。

## 版本发布要求

用户要求“发版本”“发布版本”或更新 GitHub Release 时，必须完成下面的发布流程，不能只创建标签或沿用 GitHub 自动生成的 `What's Changed`。

合并到 main 后 CI 自动发布的 Canary（`vX.Y.Z-canary.N`）不适用本节流程：不改 `model/version/VERSION`、不手写更新说明，也不要手动创建或推送 `-canary.N` 标签。详见 [docs/update-channels.md](docs/update-channels.md)。

### 版本号要求

- 用户未明确指定版本号时，以最新稳定版本为基准，默认只递增最小的修订号（patch），例如 `v0.8.0` 发布为 `v0.8.1`。
- 普通功能、优化、重构和 Bug 修复均按小更新处理，不得自行提升次版本号或主版本号。
- 只有用户明确指定，或确认需要表达不兼容升级时，才可以提升次版本号或主版本号。
- 确定版本号后，必须同步更新 `model/version/VERSION`，保证源码基线和发布标签一致。该文件是没有注入 `-ldflags` 时的版本回落来源；标签构建时 CI 会校验两者相同，不一致会直接失败。

### 插件版本号

- 插件清单里的 `Version` 默认只递增修订号（patch），例如 `0.3.0` 发布为 `0.3.1`。新增格式支持、修 Bug、调设置项都按小更新处理。
- 只有插件能力发生结构性变化（例如换掉整套设置项、改变对外行为契约），或者用户明确要求时，才提升次版本号或主版本号。

### 发布 PR 与标签

- 发布前必须等待 Go、前端、各平台交叉编译和 Docker CI 全部通过，再合并 PR。
- Release 标签必须指向已经合并到默认分支的提交，不能直接标记未合并的功能分支。

### Release 更新说明

- Release 标题使用 `Diana vX.Y.Z`。
- Release 正文使用中文：先写一句版本定位和本次更新摘要，再按实际内容分组。
- 不得只保留 GitHub 自动生成的提交或 PR 列表。每一项必须描述用户能感知的行为变化，以及必要的技术边界。
- 根据版本内容选择以下章节；没有内容的章节可以省略：
  - `新增功能`
  - `优化改进`
  - `Bug 修复`
  - `行为说明`
  - `破坏性变更`
  - `升级指南`
  - `测试覆盖`
  - `安装与下载`
  - `相关链接`
- 修复版本必须写清：故障表现、根因、修复后的行为，以及哪些相邻行为保持不变。
- 如果存在升级前置条件或首次手动升级边界，必须明确说明。例如旧版本尚未包含 Release 自更新器时，不能暗示它可以通过 WebUI 自更新。
- 升级指南必须分别覆盖适用的部署方式：完整 Release 包、Docker、源码部署。
- Release 的 `安装与下载` 和 `升级指南` 必须以发布基线的 README 为准，直接提供与 README 一致、可执行的一键安装命令（macOS/Linux Shell、Windows PowerShell），并保持 Docker 命令、权限要求及升级方式一致。发布前逐项核对，不得只写手动下载解压步骤或仅放 README 链接代替命令；README 没有提供的一键方式不得自行编造。
- 手动下载：Windows 是单个 `diana-windows-amd64.exe`，双击运行；macOS、Linux、Termux 是 `.tar.gz` 完整包，含内嵌了 WebUI 的单个程序（`diana-webui`）及启动脚本，解压后运行 `run.sh`。都无需单独部署 WebUI 或安装 Node.js。`安装与下载` 必须给出按平台的下载直链表，并写明安卓包是 Termux 里跑的服务端、不是 App。不修改历史 Release 资产。发布前以实际产物核对这些说明。
- PR 链接只能作为补充，不能代替更新点；引用时必须带标题或摘要，不能在相关链接中只写裸 PR 编号。
- Release 正文末尾应提供完整版本对比链接，例如 `vX.Y.Z...vA.B.C`。

### 发布验证

- 发布前至少运行并通过：`gofmt -l .`、`git diff --check`、`scripts/test-serial.sh ./...`（全仓库）、前端依赖安装与生产构建。涉及依赖变化时还要执行安全审计。
- 发布 PR 的 CI 必须验证 Go 测试、Vue 构建、Linux amd64/arm64、Darwin amd64/arm64、Windows amd64 和 Docker 镜像。
- 标签构建完成后，确认 GitHub Release 不是 Draft；正式版不得是 Prerelease，Beta/RC 标签必须标记为 Prerelease，并检查所有预期资产均已上传。
- Release 必须包含各支持平台的下载文件、`SHA256SUMS` 和自更新清单 `latest.json`。当前 CI 的预期资产数量为 8（5 个 `.tar.gz` 完整包，含 Termux 用的 `diana-android-arm64.tar.gz`；1 个 `diana-windows-amd64.exe`；2 个元数据文件）；若工作流调整了产物矩阵，应按工作流重新计算。
- 完整包统一使用 `diana-<系统>-<架构>.tar.gz`，包内只放一个 `diana-webui`，不附带前端目录或 `diana-webui-<系统>-<架构>` 兼容副本；Windows 直接发布 `diana-windows-amd64.exe`，卸载脚本编在程序里。不再为旧版自更新器保留兼容：v0.9.0 及更早的 Windows 控制台只认 `.zip`，v0.8.153 及更早的控制台要求包里有 `frontend-next/dist`，这些版本首次升级必须重跑一键安装或手动安装；发布说明必须明确此边界，不能暗示旧版 WebUI 可直接完成迁移。
- 发布后必须实际下载 Darwin ARM64 完整包、`diana-windows-amd64.exe` 及 `SHA256SUMS`，独立计算 SHA-256 并确认一致，同时检查归档内只有一个 `diana-webui` 程序（前端已内嵌）和启动脚本。
- 面向用户的 macOS 完整包使用 `macos` 而非 `darwin`，例如 `diana-macos-arm64.tar.gz`；内部 Go 交叉编译继续使用 `GOOS=darwin`。
- Docker 发布需要确认版本标签成功生成；正式版更新 `latest`，Beta/RC 更新 `beta`，Canary 更新 `canary`，预发布不得覆盖 `latest`（包括对应的 `-slim` 标签）。
- 在 CI、Release 资产和校验全部完成前，不得向用户宣称版本已经发布成功。
- 发布结束后清理本地产生的临时说明文件、TypeScript/Vite 构建缓存和临时预览服务，确保 `git status --short` 干净。
