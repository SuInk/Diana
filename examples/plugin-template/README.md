# diana-plugin-hello

Diana 第三方插件模板。点击仓库页的 **Use this template** 创建你自己的插件仓库。

## 仓库结构

| 文件 | 说明 |
| --- | --- |
| `diana.plugin.json` | 插件清单，安装器按它解析、校验、提示权限 |
| `SKILL.md` | 插件的 Skill 定义，必须含 `name` / `description` frontmatter |
| `prompts/` | 可选的附加提示词片段，在清单 `files` 里引用 |
| `assets/` | 可选的静态资源 |

## 开发 checklist

- [ ] 把 `diana.plugin.json` 里的 `id` 改成 `你的名字.插件名`（不能用 `official.` 前缀）
- [ ] `version` 使用语义化版本，发布新版本时递增
- [ ] `permissions` 如实声明全部权限，**禁止留空**，词表见开发文档第 3.1 节
- [ ] 凭据类设置加 `"secret": true`
- [ ] `files` 白名单只列真正需要分发的路径
- [ ] 改写 `SKILL.md` 的行为指令

## 发布

为每个版本打一个 tag（如 `v1.0.0`），安装链接固定到 tag：

```
https://github.com/<owner>/<repo>/tree/v1.0.0
```

不带 ref 的链接安装的是默认分支最新内容，用户会看到额外风险警告——发布请用 tag。

## 安装

在 Diana 插件页粘贴本仓库链接，确认风险与权限后安装。
