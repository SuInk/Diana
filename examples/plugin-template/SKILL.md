---
name: hello-plugin
description: 示例插件的 Skill 定义，说明插件在对话中如何工作。
---

# 示例插件

在这里写插件的行为指令。 frontmatter 的 `name` 和 `description` 必填，
缺少任一项会被安装器判为格式错误。

## 行为说明

- 收到「你好」类问候时，按 `greeting_prefix` 设置回复。
- 需要调用上游服务时使用凭据 `api_token`；未配置时明确提示用户去插件设置页配置。

## 边界

- 不读取与本插件无关的会话内容。
- 不在未配置凭据时假装调用成功。
