# 文件配置（设计稿）

状态：设计稿，尚未实现。本稿记录方案取舍，实现前先在这里定稿。

## 目标

- 用户可以只改文件来配置机器人和单个群，改完自动生效，不必打开 WebUI。
- 不迁移现有 SQLite 配置；没有配置文件时行为与现在完全一致。
- 文件、WebUI、聊天里的 `bot_config` 三个入口不互相覆盖。

## 参照

| 方案 | 权威来源 | 冲突处理 |
| --- | --- | --- |
| Koishi / AstrBot / OpenClaw | 文件唯一来源，WebUI 与 CLI 都写回文件 | 不存在冲突，但要整体迁出数据库 |
| Grafana provisioning / Home Assistant | 数据库运行，文件预置 | 文件写了的字段在界面锁定 |
| 导入导出 | 数据库 | 导入时比对差异，手动确认 |

Diana 选第二种：文件是**可选叠加层**。配置已经全部在 SQLite 里，整体迁出风险大，而叠加层可以按字段渐进接入。

## 文件布局

```
<data>/config.d/
  bots/<机器人名或 ID>.yaml
```

```yaml
# bots/diana.yaml
bot:                      # 机器人级，键名与 bot_config / WebUI 字段一致
  behavior:
    max_reply_chars: 300
  plugins:
    official.resolver:
      enabled: true
      settings:
        platforms: [bilibili, douyin]
        translate: false
      cookie: ${secret:resolver_bilibili}   # 凭据只能用引用
groups:
  "*":                    # 所有群的默认值
    plugins:
      official.resolver:
        settings: { translate: false }
  "12345":                # 具体群覆盖 "*"
    enabled: true
    triggers: [然然]
    behavior:
      random_repeat_enabled: false
```

## 优先级

内置默认 < 文件 `bot` < WebUI/聊天机器人级 < 文件 `groups."*"` < 文件 `groups."<群号>"` < WebUI/聊天群级

例外：**文件里写了的字段在 WebUI 和聊天里只读**，界面显示「由配置文件管理」，`bot_config` 修改时返回同样提示。所以同一字段实际只有一个来源，上面的顺序只决定不同字段如何叠加。

## 字段范围

- 只开放一份白名单 schema，与 `bot_config` 共用（`behaviorSettings`、群设置、插件 `PluginSettingSpec`）。新增可配字段时三个入口同时获得。
- 连接参数（OneBot 地址、平台 AppSecret）首期不进文件，修改需要重连，沿用 WebUI。
- 凭据只能写成 `${env:NAME}` 或 `${secret:NAME}` 引用，明文直接拒绝加载，方便用户把文件放进 git 或备份。

## 加载与热更新

- 启动时加载；运行中用 fsnotify 监听目录，500ms 防抖。
- 用同一份 schema 校验整份文件。**校验失败时保留上一份可用配置**，在 WebUI 顶部和 `app_logs`（action `file_config_rejected`）报告行号和原因，不让机器人带着半份配置运行。
- 按字段比较差异后应用；只记录真正变化的键（action `file_config_applied`，来源 `file`），便于回溯。
- 文件被删除：叠加层整体撤销，回到数据库里的值，WebUI 解锁。

## 升级兼容

- 升级后没有 `config.d/` 目录时行为与升级前完全一致：不自动生成文件，不迁移、不改写 SQLite 里的任何配置。
- 降级回旧版本时，旧版本不认识 `config.d/`，直接忽略它，继续用数据库里的值；叠加层从不写回数据库，所以降级不会留下半截状态。
- 文件里出现当前版本不认识的键（来自更新的版本或拼写错误）：整份文件按校验失败处理并报告，保留上一份可用配置，不静默丢弃。
- WebUI 和 `bot_config` 的存量接口与字段不变；锁定只对文件里写了的字段生效，旧客户端提交被锁字段时返回明确错误，而不是静默覆盖。
- 插件清单删改设置项后，文件里的旧键沿用现有 `sanitizePluginSettings` 的容错规则（多选值只去掉已下线的选项），并在报告里提示。
- 凭据引用 `${secret:…}` 解析失败时只让该字段回退到数据库值，不影响其余字段。

## 实施顺序

1. 抽出统一字段注册表（键、类型、范围、层级、谁能改），`bot_config` 和 WebUI 表单都从这里读。
2. 加载器 + 校验 + 叠加到 `effectiveConfigForEvent` / 插件 `scopedPluginSettings`，先只读。
3. WebUI 锁定显示与 `bot_config` 拒写。
4. 热更新与错误展示。
5. WebUI 提供「导出为配置文件」，降低上手成本。

## 未决问题

- 文件里的机器人按名字还是 ID 匹配：名字可读但会被改名；倾向 ID，允许 `name:` 备注。
- 群级 `"*"` 是否覆盖 WebUI 里已有的群单独设置：当前方案中 WebUI 群级优先，若用户期望「文件统一管所有群」需要另设 `force: true`。
