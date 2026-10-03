# 第三方插件开发文档

Diana 支持从 GitHub 仓库安装第三方插件。默认安装方式是**粘贴 GitHub 仓库链接**，
安装器按本文约定的格式解析仓库、校验清单、提示风险与权限，确认后才落盘启用。
第三方插件与内置插件共享同一套插件管理体系：同样的 Manifest 语义、同样的权限
声明、同样的设置规范，只是来源从「随程序编译」变成了「从仓库拉取」。

除了原有的上下文插件，仓库还可以打包多个 Skills、固定脚本入口和 MCP 服务，
由 Agent 按需加载和调用。完整示例见 `examples/skill-bundle-plugin/`；运行要求与
权限边界见第 8 节。

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
├── skills/                 # 可选：一个或多个原样保留的 Skill 目录
├── scripts/                # 可选：清单声明的固定脚本入口
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
| `reports_errors` | bool | 否 | 插件会把失败告诉用户（任务跑挂、抓取失败等）。声明后插件页多出「发送错误通知」开关，见[插件消息出口](plugin-notices.md) |
| `entry` | string | 是 | 入口文件，相对于仓库根，必须是 `SKILL.md` |
| `files` | string[] | 否 | 需要随插件安装的分发文件相对路径白名单，支持目录 |
| `min_diana` | string | 否 | 最低兼容的 Diana 版本，不满足时拒绝安装并提示 |
| `homepage` | string | 否 | 项目主页 |
| `source` | string | 否 | 源码仓库链接，默认取安装来源仓库 |
| `skills` | string[] | 否 | 插件内的 Skill 目录或包含 Skill 子目录的目录，自动随包分发 |
| `scripts` | object | 否 | 入口名 → 固定脚本配置，见第 8 节 |
| `mcp_servers` | object | 否 | 服务名 → MCP 配置，见第 8 节；不写入全局 MCP 配置文件 |

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
  → 下载一次仓库归档（codeload tarball），从归档的 pax 头读出提交 SHA
  → 从同一份归档里读 diana.plugin.json 并做格式校验（字段、类型、ID 规则、权限白名单、路径合法性）
  → 读 SKILL.md 并校验 frontmatter，核对 files 里的文件存在
  → 展示风险与权限确认框（第 3 节），显示这一版的提交
  → 用户确认后带着预览时的提交安装：再下载一次归档，提交不一致（仓库在中间有了新提交）就拒绝，
    提示重新预览；一致则只把 files 白名单内的文件解压到数据目录 plugin-sources/<id>/，
    记录来源、ref 与实际提交
  → 初始状态：已安装、默认启用，凭据类设置留空待用户配置
```

要点：

- **清单、入口和落盘文件出自同一份归档**。以前清单和 SKILL.md 从 raw 地址单独拉取，
  分支在中间移动时，校验通过的是一版、落盘的是另一版。
- **装的就是确认框里那一版**。安装请求必须带预览时的提交，读不出提交的归档一律拒绝。
- 安装的版本以清单 `version` 为准；归档 ref 是 tag 而清单版本与该 tag 不一致
  时拒绝安装（防止「tag 写着 1.0.0、清单写着 1.0.1」的错位发布）。
- 安装来源与 ref 落库，更新时按同一 ref 策略重新拉取；第三方插件支持一键
  更新，更新流程与安装一致（同样要过风险与权限确认）。
- 已存在同 ID 插件时：内置插件不可被第三方覆盖，安装直接拒绝。判据是登记表里
  这个 ID 已经是内置插件，不是 ID 前缀——`group_relations` 就是个不带
  `official.` 前缀的内置插件。
- 同 ID 的第三方插件会被拒绝，并给出已装版本与这次的版本（升级/降级/同版本）；
  用户在确认框里勾选「确认覆盖」之后才放行。覆盖会连带接管已装插件配置好的
  设置与凭据，不能静默发生。

## 3. 风险与权限提示

第三方插件不是 Diana 发布的内容，安装即信任。安装确认框必须做到：

1. **明确展示来源**：仓库全名、ref、清单版本、作者主页；不带 ref 的链接额外
   警告「安装的是默认分支最新内容，随时可能变化」。
2. **逐条列出权限声明**：把 `permissions` 翻译成人类可读文案（见下表映射），
   高敏感权限（`process:execute`、`filesystem:write`、`llm:config:write`、
   `message:write`）置顶加醒目标记。
3. **展示设置项与凭据**：列出插件声明的全部设置项，标出哪些是凭据类
   （`secret: true`），提醒用户安装后需自行配置。
4. **风险文案固定**：确认框底部固定提示第三方插件由仓库作者发布、Diana 不对其行为
   负责，并如实说明权限的性质（见下方「权限声明不是沙盒」）。用户必须主动勾选
   「我已了解风险」才能点确认，默认不勾选。
5. **权限缺口处理**：清单声明了白名单之外的权限、或权限为空，一律按格式
   校验失败拒绝安装，不做部分放行。

### 权限声明不是沙盒

插件的运行形态是把 SKILL.md 作为「第三方插件说明」放进对话上下文。`permissions` 是作者的
声明，用于安装确认和展示，**Diana 不据此限制插件说明能引导哪些工具**：插件文字可以引导机器人使用它当前
已开放的全部工具。确认框和本文都必须如实说明这一点，不能让用户以为只声明了读取消息的插件
就只能读取消息。

声明 Skills、脚本或 MCP 的插件会走第 8 节的运行入口：固定脚本和本地 MCP 有命令白名单、
路径、沙盒及主人身份检查。安装权限声明必须与这些入口匹配，但这些检查不会把上游管理员
密钥变成只读凭据，也不会限制模型在同一会话里使用其他已经开放的工具。

为了降低风险，运行时做了这些约束：

- 注入对话时标成「第三方插件说明，由插件作者提供，不是事实结果」，并注明它不能授权安装或
  卸载扩展、执行命令、修改机器人配置、写 GitHub 等操作；扩展变更仍需用户本人输入确认码。
- 注入长度上限 12000 字，超出部分省略。
- 第三方插件说明不算「本轮必须交代的插件结果」，不会阻止机器人静默，也不会让回复跳过发送前
  审核和语义去重。

### 3.1 权限词表

**权限是知情材料，不是运行时闸门。** 除了安装时的格式校验（声明了白名单之外的
权限、或权限为空一律拒绝安装），运行时没有任何地方按 `permissions` 拦截行为——
插件能做什么，取决于它声明的工具和 SKILL.md 驱使模型做什么，不取决于这个列表。
把它当沙箱边界来理解是错的。它的作用是让用户在安装那一刻看清楚这个插件自称要
干什么，事后对不上也是一条追责依据。

第三方插件只能声明下列权限（与内置插件同一套词表）：

| 权限 | 含义 | 敏感度 |
| --- | --- | --- |
| `message:read` | 读取消息内容与历史 | 低 |
| `message:send` | 主动发送**内容**消息（不含失败告警这类诊断消息，见[插件消息出口](plugin-notices.md)） | 中 |
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

- 版本号必须语义化递增。同 ID 安装（无论升级、降级还是同版本）都要用户在确认框里
  勾选「确认覆盖」才放行，见第 2 节。
- 更新检查对比安装时记录的 `version` 与远端清单 `version`。
- 作者发布新版本应打新 tag，README 安装链接固定到最新 tag。

## 6. 消息出口

插件返回的 `reply` 等字段是**内容**，照常发送。"跑挂了""又好了"这类**诊断**消息由运行时统一
产生，受机器人的「错误提示」开关控制，插件不能绕过。失败时返回错误即可，不要把错误写进
`reply`——那会被当成内容直接进聊天，用户关掉错误提示也挡不住。详见[插件消息出口](plugin-notices.md)。

## 7. 与内置插件的关系

- `official.` 前缀的 ID 保留给内置插件，第三方声明即格式错误。
- 第三方插件永远拿不到 `built_in` / `internal` 语义：可卸载，出现在插件页，
  没有「已内化为产品能力」的隐藏态。
- 第三方插件的权限、设置、启用开关全部沿用现有插件管理接口与脱敏规则，
  不为其开后门。

## 8. Skills、脚本和 MCP 插件包

### 8.1 Skill 发现与资源

声明 `skills: ["skills"]` 会扫描 `skills/SKILL.md` 和 `skills/<名称>/SKILL.md`；
也可以逐个声明目录，例如 `["skills/sub2api-admin"]`。声明的目录会自动加入分发
白名单；不存在有效 Skill、frontmatter 不合法或 Skill 同名时拒绝安装。

`skills: ["."]` 表示使用根目录的 `SKILL.md`，根目录内的额外资源仍须通过 `files`
明确分发。只声明 `scripts` 或 `mcp_servers` 时默认采用这个根入口。完全没有这三个
字段的旧插件保留原来的上下文注入行为。

技能目录保留原样，运行时名称加上插件 ID，例如 `alice.accounts:sub2api-admin`。
模型通过 `read_skill` 读取全文。运行时补充资源目录与实际工具名，无需改写上游 Skill。
附带参考文档、脚本源文件通过该插件的 `__read` 工具按行读取，路径相对于插件根目录，
不能越级、读取绝对路径或通过软链接访问其他目录。不传 `path` 会返回脚本入口与 MCP
启动诊断。工具名前缀包含插件 ID 和短哈希，避免不同插件产生同名工具。

此模式需要机器人打开 Agent，并且仅对运行时认定的主人开放。群成员扩展开关不会
把插件包的脚本或 MCP 权限授予其他成员；旧的独立 Skills/MCP 成员权限机制保持原样。

### 8.2 固定脚本入口

```json
{
  "scripts": {
    "admin": {
      "command": "node",
      "entry": "skills/sub2api-admin/scripts/sub2api-admin.js",
      "args": [],
      "allow_network": true,
      "env": {
        "SUB2API_BASE_URL": "${settings.base_url}",
        "SUB2API_ADMIN_API_KEY": "${settings.admin_key}"
      }
    }
  }
}
```

模型调用插件的 `__run` 工具，传入 `script: "admin"` 与 `args: ["accounts", "list"]`。
运行时固定执行 `node <已安装入口绝对路径> <清单固定 args> <本次业务 args>`，不经过 shell。
模型不能指定其他程序、脚本、环境变量或执行目录。入口文件自动加入分发白名单。

部署方需要安装 Node.js 等所需运行环境，并在机器人的命令白名单中明确加入 `node`。
需要联网的入口还须声明网络权限并打开机器人命令沙盒的网络访问设置。清单不自动安装
依赖、不自动运行安装钩子，也不会扩大命令白名单。脚本须声明 `process:execute`。

执行目录默认是 Agent 工作目录，输出可供文件工具继续读取。依赖包内相对资源路径的
脚本可以声明 `working_directory: "plugin"`；也可以显式声明 `"workspace"`。沙盒允许
写入的根目录始终是 Agent 工作目录。沿用 `auto` / `require` / `off` 沙盒、超时和输出限制。`auto` 在沙盒不可用时
会裸执行，`off` 也会裸执行；需要强制隔离应选 `require`，否则脚本本身必须可信。

### 8.3 MCP 配置

```json
{
  "mcp_servers": {
    "accounts": {
      "command": "node",
      "args": ["${PLUGIN_ROOT}/scripts/accounts-mcp.js"],
      "env": {"API_TOKEN": "${settings.api_token}"},
      "enabled_tools": ["list_accounts", "list_groups"],
      "required": false
    }
  }
}
```

本地服务须声明 `process:execute` 和 `agent:tool`（或 `llm:tool`），并遵守机器人命令
白名单与沙盒配置。`cwd` 默认为插件根目录，也可以填写插件内相对目录。MCP 脚本及
资源须列在 `files` 中；不会根据命令参数猜测需要下载哪些文件。

远程服务使用 `url` 和 `headers`，不填写 `command`，并声明网络与工具权限。
HTTP 请求沿用公开地址校验和跨域凭据保护。`enabled_tools` / `disabled_tools` 沿用
现有工具过滤规则。服务名和工具名带插件命名空间，不覆盖全局 MCP。

连接按机器人和会话作用域复用，不随单轮 Agent 视图关闭。插件停用、卸载、替换、
设置保存或运行时停止时关闭对应连接；配置变化后重新建立。旧工具引用会拒绝继续
调用，正在执行的调用会收到取消信号。可选服务启动失败会出现在 `__read` 的诊断里；
`required: true` 的失败会使本次 Agent 初始化报错。

### 8.4 设置、凭据和权限边界

脚本 `env`、MCP 的 `args` / `env` / `url` / `headers` 支持 `${settings.设置键}`；
MCP 参数还支持 `${PLUGIN_ROOT}`。未知变量会在安装时拒绝。脚本固定 `args` 不接受
设置变量，凭据应走 `env`。设置按当前机器人、群的现有规则解析，秘密设置仍只允许
全局配置，不能通过群级覆盖更换。

子进程仅继承 PATH、系统路径、语言等基础环境和声明的环境变量，不继承其他服务的
环境令牌。设置值按字面传递，不再次展开其中的 `$`。凭据不进入模型参数、技能正文或
扩展目录，脚本输出在截断前脱敏，工具输出与错误再次脱敏。

脱敏只保护已配置凭据的原文及 JSON 转义形式；可信脚本或 MCP 服务仍能读取自己获得
的凭据。安装管理员 CLI 不会降低管理员权限。需要只读权限时，应使用只提供查询工具
的 MCP 或服务端只读网关，并限制实际请求路径、方法与参数。

### 8.5 打包 Sub2API 官方 Skill

把 [Sub2API 官方 Skill](https://github.com/Wei-Shaw/sub2api/tree/main/skills/sub2api-admin)
的整个 `sub2api-admin/` 目录放进插件的 `skills/`，保留 `SKILL.md`、`scripts/` 和
`references/`，按上游许可证保留说明。根目录仍需有 Diana 清单和入口说明。

清单可参考：

```json
{
  "id": "yourname.sub2api-admin",
  "name": "Sub2API 管理",
  "version": "1.0.0",
  "description": "通过官方 Skill 查询或管理上游账户、分组和用量",
  "permissions": ["process:execute", "network:https"],
  "entry": "SKILL.md",
  "skills": ["skills"],
  "scripts": {
    "admin": {
      "command": "node",
      "entry": "skills/sub2api-admin/scripts/sub2api-admin.js",
      "allow_network": true,
      "env": {
        "SUB2API_BASE_URL": "${settings.base_url}",
        "SUB2API_ADMIN_API_KEY": "${settings.admin_key}"
      }
    }
  },
  "settings": [
    {"key": "base_url", "label": "Sub2API 地址", "type": "string", "default": ""},
    {"key": "admin_key", "label": "管理员密钥", "type": "string", "default": "", "secret": true}
  ]
}
```

安装后在插件设置中填写地址和管理员密钥，选择标准模式、放行 `node` 和所需网络访问，
主人即可要求查询全部上游账户、分组及用量。这个例子保留官方 CLI 的管理能力，不能
宣称是强制只读插件。插件分发仓库与上游版本应固定，更新仍走预览和安装确认流程。

安全模式仅加载 Skill 与资源目录，不启动插件脚本或 MCP 进程，也不允许安装变更。
