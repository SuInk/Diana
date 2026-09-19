# 第三方插件开发文档

Diana 支持从 GitHub 仓库安装第三方插件。默认安装方式是**粘贴 GitHub 仓库链接**，
安装器按本文约定的格式解析仓库、校验清单、提示风险与权限，确认后才落盘启用。
第三方插件与内置插件共享同一套插件管理体系：同样的 Manifest 语义、同样的权限
声明、同样的设置规范，只是来源从「随程序编译」变成了「从仓库拉取」。

参考实现见 `examples/plugin-template/`，这是一个可以直接「Use this template」
创建新插件仓库的 GitHub 模板。

## 1. 仓库格式要求

### 1.1 目录结构

```
<repo-root>/
├── diana.plugin.json     # 必需：插件清单（Manifest）
├── SKILL.md                # 必需：插件的 Skill 定义（frontmatter + 指令）
├── README.md               # 必需：人类可读的说明与安装指引
├── prompts/                # 可选：附加提示词片段，清单里引用
├── assets/                 # 可选：随插件分发的静态资源
└── LICENSE                 # 建议：许可证
```

硬性要求：

- 仓库根目录必须有 `diana.plugin.json`。没有清单文件的仓库不能被安装，
  安装器直接报「不是有效的 Diana 插件仓库」，不做猜测式兜底。
- `SKILL.md` 必须包含 `name` 和 `description` frontmatter，规则与扩展页的
  Skill 导入一致。缺少任一字段即格式校验失败。
- 清单里引用的相对路径文件必须真实存在于仓库中，缺文件直接拒绝安装。
- 不区分大小写匹配 `diana.plugin.json` / `DIANA.PLUGIN.JSON` 之外的别名；
  只认这一个文件名。

### 1.2 清单文件 `diana.plugin.json`

清单字段与代码中的 `PluginManifest` 一一对应：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 全局唯一插件 ID，小写字母、数字、`.`、`-`、`_`；建议 `作者名.插件名` 形式。`official.` 前缀保留给官方内置插件，第三方声明会被拒绝 |
| `name` | string | 是 | 展示名 |
| `version` | string | 是 | 语义化版本，`x.y.z`，不允许 `v` 前缀 |
| `description` | string | 是 | 一句话说明插件做什么；会展示在安装确认框里 |
| `permissions` | string[] | 是 | 权限声明，见第 3 节。**禁止为空数组**——宁可多声明也不能不声明 |
| `platforms` | string[] | 否 | 完整支持的聊天平台 ID；留空表示未声明，保持兼容 |
| `platform_notes` | object | 否 | 平台 ID → 该平台上的能力说明 |
| `settings` | object[] | 否 | 设置项声明，见第 4 节 |
| `entry` | string | 是 | 入口文件，相对于仓库根，必须是 `SKILL.md` |
| `files` | string[] | 否 | 需要随插件安装的分发文件相对路径白名单，支持目录 |
| `min_diana` | string | 否 | 最低兼容的 Diana 版本，不满足时拒绝安装并提示 |
| `homepage` | string | 否 | 项目主页 |
| `source` | string | 否 | 源码仓库链接，默认取安装来源仓库 |

第三方清单不允许出现的字段：`official`、`built_in`、`internal`、`default_disabled`、
`can_ask_agent`。这些是内置插件的语义，第三方声明一律视为格式错误。

### 1.3 安装链接格式

默认安装入口是 GitHub 仓库链接，支持三种形态：

```
https://github.com/<owner>/<repo>                    # 默认分支最新提交
https://github.com/<owner>/<repo>/tree/<tag>         # 指定 tag（推荐发布方式）
https://github.com/<owner>/<repo>/tree/<commit>      # 固定 commit（可复现安装）
```

短链 `github.com/<owner>/<repo>` 自动补 `https://`。只支持 `github.com` 域名；
其他 Git 托管地址给出明确报错，不做开放重定向。

**推荐发布方式**：插件作者为每个版本打 tag（如 `v1.0.0`），安装链接固定到
tag。不带 ref 的链接安装的是默认分支最新提交，内容与权限声明随时可能变化，
安装确认框会对此显示额外警告。

## 2. 安装流程

```
粘贴仓库链接
  → 解析 owner/repo/ref，只允许 github.com
  → 从 raw.githubusercontent.com 拉取 diana.plugin.json
  → 格式校验（JSON Schema：字段、类型、ID 规则、权限白名单、路径合法性）
  → 拉取 SKILL.md 并校验 frontmatter
  → 核对 files 白名单内文件存在
  → 展示风险与权限确认框（第 3 节）
  → 用户确认后下载仓库归档（codeload tarball），校验 ref 与清单版本一致
  → 解压到数据目录 plugin-sources/<id>/，记录来源与 ref
  → 初始状态：已安装、默认启用，凭据类设置留空待用户配置
```

要点：

- **先校验、后下载归档**。清单和 Skill 不合格就不触发完整下载。
- 安装的版本以清单 `version` 为准；归档 ref 是 tag 而清单版本与该 tag 不一致
  时拒绝安装（防止「tag 写着 1.0.0、清单写着 1.0.1」的错位发布）。
- 安装来源与 ref 落库，更新时按同一 ref 策略重新拉取；第三方插件支持一键
  更新，更新流程与安装一致（同样要过风险与权限确认）。
- 已存在同 ID 插件时：内置插件不可被第三方覆盖；同 ID 第三方插件按版本比较
  提示升级/降级，不允许静默覆盖。

## 3. 风险与权限提示

第三方插件不是 Diana 发布的内容，安装即信任。安装确认框必须做到：

1. **明确展示来源**：仓库全名、ref、清单版本、作者主页；不带 ref 的链接额外
   警告「安装的是默认分支最新内容，随时可能变化」。
2. **逐条列出权限声明**：把 `permissions` 翻译成人类可读文案（见下表映射），
   高敏感权限（`process:execute`、`filesystem:write`、`llm:config:write`、
   `message:write`）置顶加醒目标记。
3. **展示设置项与凭据**：列出插件声明的全部设置项，标出哪些是凭据类
   （`secret: true`），提醒用户安装后需自行配置。
4. **风险文案固定**：确认框底部固定提示「第三方插件由仓库作者发布，Diana
   不对其行为负责；插件获得的权限在启用期间持续生效」。用户必须主动勾选
   「我已了解风险」才能点确认，默认不勾选。
5. **权限缺口处理**：清单声明了白名单之外的权限、或权限为空，一律按格式
   校验失败拒绝安装，不做部分放行。

### 3.1 权限词表

第三方插件只能声明下列权限（与内置插件同一套词表）：

| 权限 | 含义 | 敏感度 |
| --- | --- | --- |
| `message:read` | 读取消息内容与历史 | 低 |
| `message:send` | 主动发送消息 | 中 |
| `message:write` | 修改、撤回已发消息 | 高 |
| `notice:read` | 读取群公告、通知事件 | 低 |
| `network:http` / `network:https` | 发起 HTTP / HTTPS 请求 | 中 |
| `llm:generate` / `llm:multiple` | 调用模型生成（单次 / 多次） | 中 |
| `llm:tool` | 注册为模型可调用工具 | 中 |
| `llm:config:write` | 修改模型配置 | 高 |
| `file:parse` | 解析附件文档 | 低 |
| `file:write` | 写入文件 | 高 |
| `filesystem:temp` | 使用临时目录 | 低 |
| `filesystem:write` | 写入工作目录 | 高 |
| `process:execute` | 执行外部命令 | 高 |
| `process:media` | 调用媒体处理工具 | 中 |
| `browser:render` / `browser:headless` | 使用浏览器渲染 | 中 |
| `sandbox:ephemeral` | 使用一次性沙箱 | 低 |
| `task:persistent` / `task:notify` | 创建持久任务 / 任务通知 | 中 |
| `agent:tool` | 作为 Agent 工具被调用 | 中 |
| `github:issues:read/write` 等 | GitHub API 细粒度权限 | 中 |
| `platform:group:read` / `platform:group:moderate:*` | 群信息 / 群管理 | 高 |
| `knowledge:read` / `plugin:list` | 读取知识库 / 插件清单 | 低 |
| `audit:write` | 写审计日志 | 低 |

新权限词只能在 Diana 主版本里新增；插件申报词表之外的值按格式错误拒绝。

## 4. 设置项规范

`settings` 与代码中的 `PluginSettingSpec` 一致：

| 字段 | 说明 |
| --- | --- |
| `key` | 设置键，小写字母、数字、`_`，同一插件内唯一 |
| `label` | 展示名 |
| `description` | 说明文案 |
| `type` | `bool` / `number` / `string` / `text` / `select` / `multi_select` / `size` / `platform_level_rules` |
| `default` | 默认值，必填 |
| `min` / `max` / `step` / `unit` | 数值型约束 |
| `options` | `select` 类必填，`{value, label}` 列表 |
| `secret` | 凭据类标记；读接口不回传明文，前端用密码框 + 「已配置」徽章 |

校验失败（未知类型、缺默认值、`select` 无 options、`secret` 与非 string 类型
混用等）按格式错误拒绝安装。

## 5. 版本与更新

- 版本号必须语义化递增；同 ID 降级安装需要用户在确认框里二次确认。
- 更新检查对比安装时记录的 `version` 与远端清单 `version`。
- 作者发布新版本应打新 tag，README 安装链接固定到最新 tag。

## 6. 与内置插件的关系

- `official.` 前缀的 ID 保留给内置插件，第三方声明即格式错误。
- 第三方插件永远拿不到 `built_in` / `internal` 语义：可卸载，出现在插件页，
  没有「已内化为产品能力」的隐藏态。
- 第三方插件的权限、设置、启用开关全部沿用现有插件管理接口与脱敏规则，
  不为其开后门。
