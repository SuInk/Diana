# 参与贡献

感谢你愿意为 Diana 出力！修 Bug、补文档、加功能都欢迎。改动较大（新平台、新能力、改数据结构或对外行为）时，建议先开 Issue 说一下思路，免得白忙。

> [!NOTE]
> Diana 使用 [Limited Redistribution License (SuInk)](./LICENSE)，不是 OSI 意义上的开源许可，禁止商业使用。向本仓库提交 PR，即表示你同意你的贡献按该许可随仓库分发。

### 许可要点

以 [LICENSE](./LICENSE) 中文版本为准。许可只授予明确列出的权利，简单说就是在非商业前提下：

- **可以**自部署原版或修改版供自己使用，包括跑机器人；可以附带许可全文再分发；修改后的代码只能私下共享。
- **不得**删改 LICENSE、版权声明和作者署名；不得公开发布修改后的代码，或基于修改版构建的程序、安装包、Docker 镜像和 Release；不得改名换品牌，或把修改版当独立项目维护。

**提 PR 不用另外申请**：作者在此按 LICENSE 第 1 条给予明确许可，你可以 fork 本仓库、把修改推到自己的 fork，用来向本仓库提 PR 或讨论改动。fork 仅限这个用途，第 1 条 a–c 项的限制照样适用。

其他未列出的用途须先取得作者明确许可。

## 目录速览

| 路径 | 内容 |
| --- | --- |
| `cmd/webui` | 主程序入口（`diana-webui`），前端产物在构建时嵌入 `cmd/webui/webdist` |
| `webui/` | HTTP 接口、控制台后端、各平台机器人接入 |
| `model/` | 核心逻辑：`assistant`（回复决策链路）、`agent`、`llm`、`storage`、`updater` 等 |
| `internal/` | 内部工具包（Code Mode、沙盒、日志等） |
| `frontend/` | Vue 3 + TypeScript 控制台 |
| `mobile/` | Flutter 客户端 |
| `scripts/` | 开发、测试、安装、构建脚本 |
| `docs/` | 文档站（GitHub Pages）与专题说明 |
| `skills/` | 随仓库发布的 Skill，例如 `diana-install` |

## 准备环境

- Go：版本以 `go.mod` 为准，CI 使用 `.github/workflows/ci.yml` 里的 `GO_VERSION`。
- Node.js 22 与 npm（前端、Code Mode 脚本）。
- Git、Make。Windows 上可用 `scripts/dev.cmd` 代替 `make dev`。

```sh
make deps   # go mod download + 前端 npm ci
make dev    # 后端 :18080 + Vite 前端 :5173，热更新
```

端口被占用时可改：`make dev BACKEND_PORT=18081 FRONTEND_PORT=5174`。其他常用命令见 `make help`。

### 用 demo 模式看界面

前端带一份模拟数据，不需要后端、不需要登录，适合改 UI 时目视检查：

```sh
cd frontend
VITE_DEMO_MODE=true npm run dev
```

线上的[在线演示](https://suink.github.io/Diana/demo/)就是同一份 demo 构建。

## 开发约定

- **一个 PR 只做一件事**，从最新的 `main` 拉分支，不夹带无关改动或整文件格式化。
- **代码格式**：Go 代码保持 `gofmt` 干净；改了依赖要让 `go mod tidy` 无 diff。
- **注释写「为什么」**：说明设计原因和边界，不复述代码在做什么。较长的设计思路放进 `docs/`。
- **UI 与现有交互保持一致**：新设置放在相关设置旁边（例如模型相关选项放在聊天模型下面）。改完在浏览器里实际看一遍，PR 里附截图。
- **行为有变化就同步文档**：更新 `docs/` 下对应专题或 README；安装、升级流程变化时同步 `skills/diana-install/SKILL.md`。
- **面向用户的文案**中，QQ 个人号接入端只推荐 SnowLuma。
- **不提交隐私数据**：不要提交绝对家目录路径（`/Users/<名字>`、`/home/<名字>`）、API Key、Token、私钥，也不要用真实 QQ 号或群号做测试数据——请用合成的五位数。CI 的 Public Repo Audit 会拦这些，推送前可本地运行：

  ```sh
  make audit-public
  ```

## 测试

### Go

```sh
scripts/test-serial.sh               # 只跑相对 origin/main 改动过的包
scripts/test-serial.sh ./model/llm   # 指定包，参数原样交给 go test
scripts/test-serial.sh ./...         # 全仓库
```

脚本会让同一台机器上的多次测试排队、限并发并降优先级，避免整机卡死；排队时请耐心等，不要另起 `go test` 绕过。改了并发相关代码时加 `DIANA_TEST_RACE=1` 打开 `-race`。CI 在 UTC 时区下跑全仓库 `go test ./...`，如果你的测试和时间有关，请注意时区差异。

新建测试文件前先确认同名文件是否已存在，避免覆盖。

### 前端

```sh
cd frontend
npm test        # 单元测试（src/**/*.test.mjs）
npm run build   # vue-tsc 类型检查 + 生产构建（会先跑 npm test）
```

改了常量、草稿或配置键时，务必跑一遍单元测试。

### 其他

- 改了 `scripts/codemode`：`npm ci --ignore-scripts --prefix scripts/codemode && npm run check --prefix scripts/codemode`。
- 改了安装脚本：`sh -n scripts/install.sh && python3 scripts/test_install.py`；PowerShell 脚本用 `pwsh scripts/test_install.ps1`。
- 改了提示词、工具描述、回复决策这类影响模型行为的逻辑，单测不够：请用当前在用的模型对真实场景做回放，在 PR 里写明测了什么、结果如何。

## 提交 PR

### 标题

写实际改动和用户能感知的变化，不要只写版本号或编号。推荐两种写法：

- 直接一句话说清楚，例如：`OneBot 消息里地址残缺的重复图片段直接丢掉，@ 机器人带图不再整条回复失败`
- 或 `type(scope): 描述`，例如：`fix(onebot): WebSocket 加心跳，死连接不再占着唯一的连接位`，`type` 常用 `feat` / `fix` / `security` / `docs` / `chore`

### 正文

**开头必须是一段说明文字**（至少 20 个字，不能是标题、列表、引用或表格），后面再用 `## Why` 小节写原因。PR 合并时会 squash，这段话会落到 `main` 的提交里，Release 说明也从这里取「为什么改」；缺了这段，PR Check 会失败。

正文需要写清：

- 改了什么；
- 为什么改；
- 对用户或开发者的影响；
- 修 Bug 时写根因；
- 怎么验证的——跑了哪些测试、截图、回放结果。没实际跑过的部分请如实说明。

可以参考这个模板：

```markdown
群里有人 @ 机器人并附带图片时，若 OneBot 端推来的重复图片段缺少地址，整条回复会失败。这个 PR 在解析阶段丢掉这类残缺段。

## Why

根因：……

## 改动

- ……

## 影响

- ……

## 验证

- `scripts/test-serial.sh ./webui`
- ……
```

### 其他要求

- 引用其他 PR 时带上标题或一句话摘要，不要只写裸编号（如 `#123`）。
- 提交消息和 PR 不写 `Co-Authored-By` 之类的代理署名尾注。
- CI 需要全部通过：PR 正文检查、Public Repo Audit、Go 测试（含 gofmt、`go mod tidy`）、前端类型检查与构建、各平台交叉编译、Docker 镜像。

## 发版

发版由维护者完成，贡献者**不需要**改 `model/version/VERSION`，也不要推送任何 `v*` 标签。合并到 `main` 后 CI 会自动发布 Canary 版本，流程见 [docs/update-channels.md](docs/update-channels.md)。

## 反馈与交流

- Bug 与功能建议：[GitHub Issues](https://github.com/SuInk/Diana/issues)。报 Bug 时请附上版本号（`diana status`）、部署方式、复现步骤，以及控制台事件中心里相关的记录；贴日志和配置前先去掉 API Key、Token 和账号信息。
- 安全问题请不要公开提 Issue，先私下联系维护者。
- QQ 交流群：[1127671893](https://qm.qq.com/q/4o6zEBs4h2)。
