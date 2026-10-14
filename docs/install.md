# 安装与升级

README 只放了最常用的一条命令。这里是各种情况的细节。

## 原生安装（Linux / macOS）

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

默认无需管理员权限，安装到 `~/.local/share/diana`，创建 `~/.local/bin/diana` 并自动配置用户 PATH；重新打开终端后生效。可用 `--dir PATH` 指定可写目录。

**一台机器只装一个 Diana。** 已有 `/opt/diana`（旧版或 `sudo` 装的）时，直接重跑上面同一条命令即可：脚本发现它后会原地升级，并自动用 `sudo` 重跑自己（会提示输入密码，没有 `sudo` 时打印可复制的命令），不会另装用户目录那一套。想手动指定：`curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sudo sh -s -- --dir /opt/diana --yes`。

- 个人电脑：用户安装即可，服务绑定当前用户，换用户登录会被当成另一台机器。
- 服务器或多人共用：用系统安装（`… | sudo sh -s -- --scope system`），服务以发起安装的用户身份运行，不随登录会话停止。
- Linux 用户安装是 `systemctl --user` 服务：脚本会尝试 `loginctl enable-linger`，开不了会提示你执行 `sudo loginctl enable-linger $USER`，否则注销后服务会停、开机不会自启。
- 其他用户目录里已有安装且当前用户读得到时，脚本会警告（两个实例会抢同一端口、数据库分家），请升级那一份或先迁移。

安装选项现在使用交互选择或命令行参数（无交互加 `--yes` / `-Yes`）；旧安装环境变量不再读取。应用配置路径用 `--config PATH` 指定，原 YAML 文件和数据库继续使用。

## Windows

```powershell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

Windows 默认安装到 `%LOCALAPPDATA%\Diana`，自动设置当前用户 PATH，无需管理员权限。

## Docker

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

在终端里直接运行时，脚本会问装**完整版**还是**基础版**：完整版预装 Chromium、中文字体、ffmpeg、yt-dlp 和 tesseract，网页渲染、截图、媒体下载和 OCR 开箱可用；基础版都不装，拉取约 156 MB，完整版约 619 MB。拿不准就选完整版。

用管道执行（上面那条命令）不会提问，首次部署默认完整版；想要基础版就先声明：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | DIANA_VARIANT=slim sh
```

选择写在部署目录的 `.env` 里（`DIANA_IMAGE=`），重复执行安装脚本不会把它改掉。之后想换一种，在终端里重新运行安装脚本，或直接改这一行。

一键安装默认加入只处理 Diana 容器的独立更新助手，并在 `.env` 生成内部令牌。Docker socket 仅挂给助手，不挂给 Diana；助手的 HTTP 接口不映射到宿主机。WebUI 可手动请求更新镜像；「自动安装并重启」仍默认关闭，开启后才按所选通道定期更新。宿主机必须允许挂载 `/var/run/docker.sock`，该挂载可控制宿主机 Docker，请只在可信部署中使用。已有 Docker 部署要在**原部署目录**重跑上方同一条一键安装命令，旧容器自身无法加装助手。若已有 Watchtower、Portainer 等更新任务，请停用它们对 Diana 的更新，避免绕过 WebUI 开关。不需要更新助手时，可用 `curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | DIANA_DOCKER_SELF_UPDATE=0 sh` 安装或关闭助手。

以后更新只需在同一目录执行：

```sh
docker compose -f docker-compose.yml -f docker-compose.update.yml pull && docker compose -f docker-compose.yml -f docker-compose.update.yml up -d
```

上面的手动命令适用于默认安装；用 `DIANA_DOCKER_SELF_UPDATE=0` 安装的部署只需运行 `docker compose pull && docker compose up -d`。切换通道时还要将 `.env` 中 `DIANA_IMAGE=` 改成对应的滚动标签：Release 用 `latest`、Beta 用 `beta`、Canary 用 `canary`，基础版加 `-slim`；随后重建容器。WebUI 的通道设置不会修改宿主机的镜像标签。固定版本标签不会被助手自动升级；版本面板会说明标签不匹配。Docker 更新由镜像摘要校验，容器会短暂重启；健康检查失败不会自动回退镜像，需在宿主机改回已知版本标签。失败时请查看 `docker compose -f docker-compose.yml -f docker-compose.update.yml logs diana-updater`。

如果 Apple Silicon / ARM64 拉取旧镜像时报 `no matching manifest for linux/arm64/v8`，可临时在 `docker-compose.yml` 的 `services.diana` 下添加 `platform: linux/amd64`（需要 amd64 模拟支持，性能及浏览器兼容性可能受影响），原生 ARM64 镜像发布后删除此项；也可使用上方安装脚本原生部署。构建配置修改不会自动更新线上已有镜像。

## 从其他机器访问控制台

默认只监听本机，要从别的机器打开控制台，安装时带上 `--host`（Windows：`-BindHost`）：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh -s -- --host 0.0.0.0
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1))) -BindHost 0.0.0.0
```

装过了也不要紧，两条路任选：带着 `--host`（Windows：`-BindHost`） 重跑一遍安装命令（顺带升级一次），或者直接把配置文件里 `server.host` 改成 `0.0.0.0` 再重启。

先编辑配置（用户安装无需 sudo；`/opt/diana` 系统安装的配置文件若权限不足才使用 sudo）：

```sh
vim "$(diana config path)"
```

```powershell
notepad (diana config path)
```

找到文件开头的 `server:` 段，把 `host` 那一行从 `127.0.0.1` 改成 `0.0.0.0`（缩进两格，别动其他行）：

```yaml
server:
  host: 0.0.0.0
  port: "18080"
```

改完重启，`diana restart` 会自行处理服务权限：

```sh
diana restart
```

开放外网访问后务必套 HTTPS 反向代理、在防火墙放行端口——控制台有管理权限，不要裸奔在公网上。
