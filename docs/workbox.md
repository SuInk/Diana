# 工作区容器（work box）

`run_command` 在一个常驻的 Docker 容器里用 bash 运行命令，主人、管理员和群成员用同一个工具、同一个容器。做法照雨夹雪（mizorewww/pi-extension-telegram-agent）的 `src/sandbox/box.ts`：容器负责隔离，容器里没有任何机密，弄坏了重置即可。

机器人设置里的「工作区容器（run_command）」（`agent_box_access`）有三档：**仅主人**（默认，升级后主人原来的 `run_command` 继续可用）、**所有人**（群成员也能用，同一个群共用一个容器）、**关闭**。安全模式下哪一档都不提供；这台机器没有可用的隔离运行时时也不提供，设置页会写明原因。

## 容器长什么样

- **粒度**：每个机器人的每个群、每个私聊各一个容器 `diana-box-<哈希>` 和一个命名卷 `diana-work-<哈希>`，挂在 `/work`。
- **磁盘上限**：每个会话的 `/work` 卷单独有一个大小上限，是机器人设置「工作区磁盘上限」（`agent_box_disk_limit_mb`），默认 5120 MB，最小 100 MB，`/work/.output` 里的命令输出也算在内。Docker 的 local 卷驱动没有通用的大小配额，所以由 Diana 在容器里自己管：每条命令开始前用 `du` 看一次，已经超限就不执行；命令运行中每 3 秒 `du` 一次，超限就停掉整条命令；`ulimit -f` 再兜住单个文件；`inputs` 放附件进来前也先看剩余空间。超限时工具结果写明用了多少、上限多少，以及可以用 `box_reset`、「清空上下文」或删文件来清理。按间隔检查意味着一条命令在两次检查之间最多能多写几秒钟的量，不是精确到字节的配额。
- **资源占用**：每个正在用的会话占一个容器，每个最多 1 GB 内存、2 核、256 个进程。容器数量和卷大小不设总上限，靠空闲 15 分钟停掉容器兜底。选「所有人」时，同时在用命令的群越多，同时跑着的容器就越多，部署前按机器内存估一下。
- **生命周期**：有人用的时候才启动；空闲 15 分钟后删掉容器、保留卷，下次用时重建，`/work` 里的东西还在。每次用之前检查容器在跑、镜像对得上，不对就删掉重建（`docker run --detach … sleep infinity`），先写防火墙，再建好 `/work/.home`、`/work/inbox`、`/work/.output`。防火墙规则活在容器的网络命名空间里：Diana 记下写规则时容器的 ID 和启动时间，复用前比对，容器被外部重启过或者是上一次运行留下的，就先重写规则再用。网络模式、内存、CPU、进程数和当前配置对不上时也删掉重建。空闲回收和每周重置在动手前会在锁内再确认一次没有命令正在跑。`DIANA_BOX_*` 设置改了要重启 Diana 才生效。
- **隔离参数**：`--user 1000:1000 --cap-drop ALL --security-opt no-new-privileges --read-only --init`，`/tmp` 是 256 MB tmpfs，内存 1 GB、2 核、256 个进程。不挂任何宿主目录、不挂 docker socket，除 `HOME=/work/.home` 外不传环境变量。`--init` 是 Diana 加的：超时被杀的后台进程要有人回收，否则僵尸会占满进程数。
- **联网**：默认开。容器接在专用 bridge 网络 `diana-box` 上，和别的 compose 网络隔开。每次起容器时，Diana 另起一个一次性容器，只带 `NET_ADMIN`，进入 box 的网络命名空间写出站规则：回环（Docker 内置 DNS `127.0.0.11`）放行，`10/8`、`172.16/12`、`192.168/16`、`100.64/10`（Tailscale）、`127/8`、`169.254/16`、`198.18/15`、组播和 IPv6 的 `fc00::/7`、`fe80::/10` 一律拒绝；原生部署时宿主机自己网卡上的地址（包括公网 IPv4 和全局 IPv6）也按单个地址拒绝。规则用 `iptables-restore` / `ip6tables-restore` 一次性替换整张 filter 表，重写时不会有出站全开的间隙。宿主内核写不了 IPv6 规则时（没有 ip6tables 能用的表），Diana 用 `--sysctl net.ipv6.conf.all.disable_ipv6=1` 关掉容器里的 IPv6 重建，之后只靠 IPv4 规则。box 自己没有 `NET_ADMIN`，改不了规则；规则写不上时容器会被删掉，不会留下一个没有防火墙的联网容器。
  **限制**：Diana 自己跑在容器里（Docker 部署）时看不到宿主机的网卡，宿主机的公网地址不在拒绝名单里。宿主机上监听在公网地址的服务需要部署方自己用宿主防火墙挡住 rootless Docker 的出口（slirp4netns 默认已经挡住宿主回环）。
  这一层是雨夹雪没有的：它跑在 Linux rootless Docker 上，slirp4netns 默认就访问不到宿主回环；Docker Desktop 的容器却能经 `host.docker.internal` 直接访问宿主机 127.0.0.1 上的 WebUI、OneBot、VNC 等服务。
- **命令**：`bash -c`，工作目录 `/work`，stdout 和 stderr 合并。超时默认 60 秒，最长 600 秒；到点由容器里的 GNU `timeout` 杀掉整个进程组（退出码 124 报超时；137 是被 SIGKILL，耗时到了超时时间的算超时，没到的比如内存超限不算），调用被取消时 Diana 按记下的进程组号杀掉。完整输出直接写进容器里的 `/work/.output/`，只留最新 20 份，还在跑的命令的那份不删；Diana 只读回尾部窗口，照工具结果的统一规则按整行分页，放不下时写明一共多少行、给了哪几行、全文路径和续读方式。
- **文件进出**：`run_command` 的 `inputs` 把工作目录里的文件（群成员只能用本群保存过的）放进 `/work/inbox`；`send_file` 的 `path` 写 `/work/...` 就从容器里取，先取真实路径、确认仍在 `/work` 内、是普通文件、不超过上限。
- **重置**：`box_reset` 删除容器和卷；每周一 05:00（本机时区）自动清空一次，有命令在跑的容器等下一次检查。上次清空的时间就是卷的创建时间，不另存状态。主人发「清空上下文」（相当于雨夹雪的 `/new`）时连带清空本会话的容器。三种清空都只动当前会话的卷。
- **镜像**：`deploy/box/Dockerfile`，编进程序里。第一次使用时 Diana 在本机执行 `docker build`，标签是 `diana-box:<Dockerfile 哈希前 12 位>`，所以 Dockerfile 一改就会重建镜像和容器。镜像约 2 GB：Node 24、uv、Python（含 PIL、numpy、requests）、git、curl、ripgrep、fd、sqlite3、ffmpeg、ImageMagick、poppler、Chromium 和 CJK 字体。`pip`、`npm`、`uv` 装的东西落在 `/work/.home` 里，清空时一起删掉。

## 各平台

| 部署方式 | 能不能用 | 说明 |
| --- | --- | --- |
| macOS 原生 | 能 | Docker Desktop、colima、OrbStack 都行，守护进程在虚拟机里。宿主回环由上面的防火墙挡住。注意虚拟机不是一道完整的墙：Docker Desktop 默认把 `/Users` 共享进虚拟机，容器逃逸到虚拟机后能读写宿主的 `/Users`。box 本身不挂任何宿主目录，这只在出现容器逃逸漏洞时才有影响。 |
| Linux 原生 | rootless Docker 才能用 | 照雨夹雪，`docker info` 的 SecurityOptions 里必须有 `rootless`。root 守护进程的 socket 等于宿主 root 权限，Diana 不用它。Diana 依次尝试 `DIANA_BOX_DOCKER_HOST`、docker CLI 的默认连接（`DOCKER_HOST`、`DOCKER_CONTEXT` 或当前上下文）和 `$XDG_RUNTIME_DIR/docker.sock`。 |
| Docker 部署 | 挂上 rootless socket 就能用 | 发布镜像带 docker CLI，默认连 `unix:///run/diana-box/docker.sock`。把宿主机上 rootless Docker 的 socket（一般是 `/run/user/<UID>/docker.sock`）挂到这个路径即可，属主不是 root 时入口脚本会把 socket 的属组加给容器里的 diana 用户，见 `docker-compose.yml` 里的注释。没挂时不提供 `run_command`。**挂上之后，Diana 容器里跑的所有代码（包括插件和编码代理）都能控制那个宿主用户**：rootless Docker 的 socket 等于那个用户的权限，所以要给 rootless Docker 单独建一个不放别的东西的宿主用户。不要挂 Linux 宿主的 `/var/run/docker.sock`：那是 root 守护进程，Diana 会拒绝，入口脚本也不会给它加组。例外是 Docker Desktop：它的 `/var/run/docker.sock` 通往虚拟机里的守护进程，Diana 会接受，这时 Diana 容器能控制整个 Docker Desktop 虚拟机（也就能碰到共享进虚拟机的 `/Users`）。宿主机公网地址不在拒绝名单里，见上面「联网」的限制。 |
| Termux | 不能 | 没有 Docker，不提供 `run_command`。 |
| Windows | 能 | Docker Desktop。 |

用不了时不会退回宿主机执行，`run_command` 直接不提供；启动日志会写明原因。

## 部署方的环境变量

| 变量 | 默认 | 作用 |
| --- | --- | --- |
| `DIANA_BOX_DOCKER_HOST` | 原生部署为空（用 docker CLI 的默认连接，Linux 再试 rootless socket）；Docker 发布镜像为 `unix:///run/diana-box/docker.sock` | 只认本机 socket（`unix://`、`npipe://`），远端守护进程不用 |
| `DIANA_BOX_IMAGE` | 内置镜像 | 自定义镜像需要部署方自己先构建好；防火墙容器要用镜像里的 `iptables` |
| `DIANA_BOX_MEMORY` | `1g` | |
| `DIANA_BOX_CPUS` | `2` | |
| `DIANA_BOX_PIDS` | `256` | |
| `DIANA_BOX_NETWORK` | 开 | 设成 `off` 时用 `--network none`，不再写防火墙 |

## 安全边界

- 容器里只有用户主动放进来的东西：聊天附件和命令自己下载的文件。Diana 的数据库、配置、API Key、Cookie、MCP 配置、编码代理登录态都不会进去。
- 同一个群的人共用一个容器，能看到彼此留在 `/work` 里的文件。这是雨夹雪的设计。
- 要在宿主机上做的事（改 Diana 的文件、跑宿主机上的脚本、访问内网）走 `host_access`，执行前要过审核。
- install_package 和插件声明的脚本、MCP 不在容器里运行，仍按「插件脚本沙盒」设置在宿主机上运行，见[扩展](extensions.md)。
