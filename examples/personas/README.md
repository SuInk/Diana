# 人设示例

可以直接在 WebUI「机器人 → 人设 → 导入」里选中的文件。格式说明见
[配置文档](../../docs/configuration.html#persona-portability)。

这些文件是手写的，不是从某台机器导出来的 —— 就是为了说明这个格式**可以**手写：
没有 ID、没有时间戳、没有任何本机状态。JSON 和 YAML 都收；品格层条目多、每条还带
一句「因为」，写成 YAML 才读得下去（有注释、有多行字符串），见 `diana-soul.yaml`。

| 文件 | 内容 |
| --- | --- |
| `ranran.json` | 单套人设：真人感风格 |
| `oncall.json` | 单套人设：值班助理，简洁风格 |
| `starter-pack.json` | 三套打包在一个文件里，演示 `personas` 数组 |
| `diana-soul.yaml` | 带品格层（`soul`）和表达层（`voice`）的写法，YAML |

导入只增不减：同名但内容不同的会被改成「名字 (2)」，同名且完全一样的会跳过，
不会覆盖你已经调好的人设。
