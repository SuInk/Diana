# 命令行

一键安装后，`diana` 命令就在部署主机的 PATH 里。它是给主人在终端里用的，不是聊天指令。

| 命令 | 作用 |
| --- | --- |
| `diana` | 不带子命令时启动 WebUI 服务 |
| `diana status` | 运行状态、版本、地址和运行时长 |
| `diana restart` | 重启安装器管理的服务，自行处理服务权限 |
| `diana doctor` | 体检：配置、目录、前端资源和服务健康 |
| `diana config path` | 打印 `config.yaml` 所在位置 |
| `diana config check` | 校验 `config.yaml` |
| `diana logs [--lines N] [-f]` | 看日志，`-f` 持续跟随 |
| `diana uninstall [--purge]` | 卸载程序和服务，默认保留数据；`--purge` 连数据一起删，会二次确认 |
| `diana version` | 打印版本号 |
| `diana help` | 列出全部子命令 |

`uninstall` 只在一键安装的部署里可用；Docker 部署改用容器自己的命令管理。

## 为什么单独写一页

README 里早就有这几条命令，但能力知识库不收录 README（见[能力知识库](capability-knowledge.md)）。2026-10-01 有人问机器人「你终端能敲 diana 命令吗」，`capabilities` 检索不到，机器人就连着两轮说 Diana 没有 CLI 子命令。所以命令行单独成页，编进程序供检索；增减子命令时同步改这里和 `cmd/webui/cli.go` 里的帮助文本。
