---
name: diana-install
description: 安装或升级 Diana 服务端，支持 Linux、macOS、Windows、安卓 Termux 和 Docker。用于用户要求在目标机器部署 Diana 的场景，不用于配置机器人业务或发布 Diana 版本。
---

# Diana 安装与升级

在用户指定的目标机器安装 Diana；未指定版本时使用最新稳定版，不自行切换到 Canary、Beta 或源码构建。

## 每次执行前读取当前指引

- [README](https://raw.githubusercontent.com/SuInk/Diana/main/README.md)：安装命令、权限和支持平台的依据。
- 按部署方式阅读官方脚本：[Linux / macOS / Termux](https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh)、[Windows PowerShell](https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1)、[Docker](https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh)。只读本次需要的脚本。
- 指定历史版本时，核对该 Release 及对应标签中的文档和脚本，不把当前安装方式直接套用于旧版本。无法获取文档或脚本时说明阻碍，不凭记忆拼接下载地址。

本 Skill 随仓库维护，不固定版本号、资产清单或重复保存安装脚本。执行前重新读取官方资料，避免使用已安装 Skill 的旧缓存指引。

## 安装流程

1. 确认目标机器、系统、架构和现有 Diana 安装方式。保留用户选择的 Docker 或原生部署方式；已有安装沿用原目录、配置和数据，不另起一套实例。
2. 默认以当前用户权限安装，新安装仅监听 `127.0.0.1`；升级保留已有监听配置。不要擅自使用 sudo、开放公网端口、覆盖数据或重置密码。旧系统级安装按脚本指引处理权限，需提权时向用户说明。
3. 使用 README 对应的官方一键命令，让脚本选择版本、校验 SHA-256、备份并处理升级回滚。不要跳过校验；下载、校验或启动失败时检查输出并报告，不把失败当作安装成功，也不反复重装或删除数据。
4. 原生安装完成后运行 `diana doctor` 检查健康状态；若命令尚未进入 PATH，使用安装脚本报告的实际命令路径。Docker 按部署文档检查容器状态和日志，不假定宿主机有 `diana` 命令。确认控制台可访问后再报告完成。

## 安卓 Termux

- 在原生 Termux 终端中识别 `TERMUX_VERSION` / `PREFIX` 和 CPU 架构；当前仅发布 ARM64 的 Android 构建。不要把它当作普通 Linux 下载 Linux 包，也不擅自改为 proot 部署。
- 按 README 先用 `pkg install -y curl tar coreutils` 准备依赖，再执行同一份 `scripts/install.sh`；无需 root 或 sudo。程序安装在 Termux 私有目录，不放到共享存储中执行。
- 说明它是运行在手机上的服务端，不是 APK。安装脚本会尝试获取唤醒锁并生成 Termux:Boot 启动脚本；提醒用户关闭 Android 对 Termux 的电池优化。开机自启需另行安装并打开一次 Termux:Boot，不能仅凭启动脚本存在就声称已启用自启。

## 交付给用户

- 说明实际安装版本、部署方式、安装位置、健康检查结果和控制台地址。远程机器的 `127.0.0.1` 不代表用户当前设备；优先说明 SSH 转发等访问方式，不擅自改成公网监听。
- 提供管理员账号，提醒用户在安装输出中查看并妥善保存首次生成的密码；只在私密交互中按用户需要交付密码，不写入仓库、公开日志或文档。
- 已有安装且未生成新密码时，明确继续使用原密码，不自动重置。
- 模型 API Key 和机器人凭据留给用户在控制台填写，不要求用户把密钥粘贴到聊天中。
- 说明对应部署方式的后续升级方法，以当前 README 为准。
