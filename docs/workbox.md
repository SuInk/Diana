# 工作区容器（work box）

`run_command` 在一个常驻的 Docker 容器里用 bash 运行命令，主人、管理员和群成员用同一个工具，所有会话共用这一个容器，每个会话在里面有自己的目录和 uid。做法照雨夹雪（mizorewww/pi-extension-telegram-agent）的 `src/sandbox/box.ts`：容器负责隔离，容器里没有任何机密，弄坏了重置即可。

机器人设置里的「工作区容器（run_command）」（`agent_box_access`）有三档：**仅主人**（默认，升级后主人原来的 `run_command` 继续可用）、**所有人**（群成员也能用，同一个群共用一个工作区目录）、**关闭**。安全模式下哪一档都不提供；这台机器没有可用的隔离运行时时也不提供，设置页会写明原因。

## 容器长什么样

- **粒度**：整台机器一个容器 `diana-sandbox`、一个命名卷 `diana-sandbox`，挂在 `/work`。每个会话范围（机器人 × 平台 × 群或私聊对象）在卷里有自己的目录 `/work/s-<哈希>`，归一个专属 uid（从 10000 起分配，一个范围一个、永不复用），权限 `0700`；`/work` 本身归 root、`0711`，各范围进得去自己的目录，列不出、读不到、写不进别人的，也不能在 `/work` 下直接建文件。范围和 uid 的对应记在宿主机工作目录的 `.diana/box-scopes.json` 里，不放卷里：放在容器里的话，任何范围都能改它把自己指到别人的 uid 上。
- **磁盘上限**：每个范围目录单独有一个大小上限，是机器人设置「工作区磁盘上限」（`agent_box_disk_limit_mb`），默认 5120 MB，最小 100 MB，范围目录里 `.output` 的命令输出也算在内。Docker 的 local 卷驱动没有通用的大小配额，所以由 Diana 在容器里自己管：每条命令开始前用 `du` 看一次，已经超限就不执行；命令运行中每 3 秒 `du` 一次，超限就停掉整条命令；`ulimit -f` 再兜住单个文件；`inputs` 放附件进来前也先看剩余空间。超限时工具结果写明用了多少、上限多少，以及可以用 `box_reset`、「清空上下文」或删文件来清理。按间隔检查意味着一条命令在两次检查之间最多能多写几秒钟的量，不是精确到字节的配额。
- **资源占用**：所有会话共用一个容器，最多 2 GB 内存、2 核、256 个进程；每个范围的进程数另按 uid 限成容器上限的一半（`ulimit` 的 NPROC），一个范围的失控并发占不满整个容器。代价是资源不再按会话隔开：一个会话跑很重的命令（比如转长视频、装大依赖）会拖慢同时在跑的其他会话，内存超限时容器里的命令会被一起杀掉。卷大小不设总上限，只按范围限。
- **生命周期**：有人用的时候才启动；所有范围都空闲 15 分钟后删掉容器、保留卷，下次用时重建，各范围目录里的东西还在。每次用之前检查容器在跑、镜像对得上，不对就删掉重建（`docker run --detach … sleep infinity`），先写防火墙，再以 root 把 `/work` 收成 `0711`；一个范围第一次在这个容器里用时，root 只建 `/work/s-<哈希>` 这一层、交给范围 uid，里面的 `.home`、`inbox`、`.output`、`tmp` 由范围 uid 自己建，root 不碰范围 uid 能控制的任何路径。防火墙规则活在容器的网络命名空间里：Diana 记下写规则时容器的 ID 和启动时间，复用前比对，容器被外部重启过或者是上一次运行留下的，就先重写规则再用。网络模式、内存、CPU、进程数和当前配置对不上时也删掉重建。空闲回收在动手前会在锁内再确认一次没有任何范围的命令正在跑。`DIANA_BOX_*` 设置改了要重启 Diana 才生效。
- **隔离参数**：`--cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER --security-opt no-new-privileges --read-only --init`，`/tmp` 是 256 MB tmpfs，内存 2 GB、2 核、256 个进程。留下的三项能力只给 Diana 自己以 root 进容器建范围目录用：命令一律以范围 uid 运行，Docker 不给非 root 用户任何能力，`no-new-privileges` 又挡住 setuid，命令里 `/proc/self/status` 的 `CapEff` 是全 0。不挂任何宿主目录、不挂 docker socket；容器本身不带环境变量，`HOME`、`TMPDIR`、`UV_CACHE_DIR`、`NPM_CONFIG_PREFIX`、`PATH` 在每次 exec 时按范围给，都指向范围自己的目录。`--init` 是 Diana 加的：超时被杀的后台进程要有人回收，否则僵尸会占满进程数。
- **联网**：默认开。容器接在专用 bridge 网络 `diana-sandbox` 上，和别的 compose 网络隔开。每次起容器时，Diana 另起一个一次性容器，只带 `NET_ADMIN`，进入 box 的网络命名空间写出站规则：回环（Docker 内置 DNS `127.0.0.11`）放行，`10/8`、`172.16/12`、`192.168/16`、`100.64/10`（Tailscale）、`127/8`、`169.254/16`、`198.18/15`、组播和 IPv6 的 `fc00::/7`、`fe80::/10` 一律拒绝；原生部署时宿主机自己网卡上的地址（包括公网 IPv4 和全局 IPv6）也按单个地址拒绝。规则用 `iptables-restore` / `ip6tables-restore` 一次性替换整张 filter 表，重写时不会有出站全开的间隙。宿主内核写不了 IPv6 规则时（没有 ip6tables 能用的表），Diana 用 `--sysctl net.ipv6.conf.all.disable_ipv6=1` 关掉容器里的 IPv6 重建，之后只靠 IPv4 规则。box 自己没有 `NET_ADMIN`，改不了规则；规则写不上时容器会被删掉，不会留下一个没有防火墙的联网容器。
  **限制**：Diana 自己跑在容器里（Docker 部署）时看不到宿主机的网卡，宿主机的公网地址不在拒绝名单里。宿主机上监听在公网地址的服务需要部署方自己用宿主防火墙挡住 rootless Docker 的出口（slirp4netns 默认已经挡住宿主回环）。
  这一层是雨夹雪没有的：它跑在 Linux rootless Docker 上，slirp4netns 默认就访问不到宿主回环；Docker Desktop 的容器却能经 `host.docker.internal` 直接访问宿主机 127.0.0.1 上的 WebUI、OneBot、VNC 等服务。
- **命令**：`bash -c`，以范围 uid 运行，工作目录是范围目录（工具结果里的 `cwd`），stdout 和 stderr 合并。超时默认 60 秒，最长 600 秒；到点由容器里的 GNU `timeout` 杀掉整个进程组（退出码 124 报超时；137 是被 SIGKILL，耗时到了超时时间的算超时，没到的比如内存超限不算），调用被取消时 Diana 按记下的进程组号杀掉。完整输出直接写进范围目录的 `.output/`，只留最新 20 份，还在跑的命令的那份不删；Diana 只读回尾部窗口，照工具结果的统一规则按整行分页，放不下时写明一共多少行、给了哪几行、全文路径和续读方式。
- **文件进出**：`run_command` 的 `inputs` 把工作目录里的文件（群成员只能用本群保存过的）放进范围目录的 `inbox/`；`send_file` 的 `path` 写 `/work/...` 就以范围 uid 从容器里取，先取真实路径、确认仍在本范围目录内、是普通文件、不超过上限。老写法 `/work/<文件>`（以前每个会话的根就是 `/work`）按本范围目录下的同名文件算。
- **重置**：`box_reset` 以范围 uid 停掉本范围的所有进程、删光本范围目录，共享容器和别的范围不受影响；主人发「清空上下文」（相当于雨夹雪的 `/new`）时连带清空本会话的范围。不再每周自动清空：范围目录现在是会话的持久工作区，磁盘靠每范围上限兜住。
- **镜像**：`deploy/box/Dockerfile`，编进程序里。第一次使用时 Diana 在本机执行 `docker build`，标签是 `diana-sandbox:<Dockerfile 哈希前 12 位>`，所以 Dockerfile 一改就会重建镜像和容器。镜像约 2 GB：Node 24、uv、Python（含 PIL、numpy、requests）、git、curl、ripgrep、fd、sqlite3、ffmpeg、ImageMagick、poppler、Chromium 和 CJK 字体。`pip`、`npm`、`uv` 装的东西落在范围目录的 `.home` 里，清空该范围时一起删掉。

## 从旧版升级

旧版每个会话一个容器 `diana-box-<哈希>`、一个卷 `diana-work-<哈希>`。升级后 Diana 在后台把每个旧卷的内容并进共享卷里对应的范围目录（不覆盖迁移前范围里已经写过的文件），交给范围 uid，然后删掉旧容器和旧卷（旧版的专用网络 `diana-box` 和旧镜像 `diana-box:<哈希>` 不再使用，留着无害，可以手动 `docker network rm diana-box`、`docker image rm`）；旧容器里还有命令在跑时等下一分钟再搬。迁移在一个不联网、只挂这两个卷的一次性容器里做。这段迁移代码自 2026-11 起保留 6 个月。

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
| `DIANA_BOX_MEMORY` | `2g` | 所有会话共用这一份 |
| `DIANA_BOX_CPUS` | `2` | |
| `DIANA_BOX_PIDS` | `256` | |
| `DIANA_BOX_NETWORK` | 开 | 设成 `off` 时用 `--network none`，不再写防火墙 |

## 安全边界

- 容器里只有用户主动放进来的东西：聊天附件和命令自己下载的文件。Diana 的数据库、配置、API Key、Cookie、MCP 配置、编码代理登录态都不会进去。
- 同一个群的人共用一个范围目录，能看到彼此留在里面的文件。这是雨夹雪的设计。不同群、不同私聊之间靠范围 uid 和 `0700` 目录隔开，看不到彼此的文件；但共用一个容器，`/proc` 里能看到别的范围正在跑的进程的命令行，命令行里不要放敏感内容。
- 要在宿主机上做的事（改 Diana 的文件、跑宿主机上的脚本、访问内网）走 `host_access`，执行前要过审核。
- install_package 和插件声明的脚本、MCP 不在容器里运行，仍按「插件脚本沙盒」设置在宿主机上运行，见[扩展](extensions.md)。
