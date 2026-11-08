# Chromium / Chrome 浏览器依赖

本页讲 Diana 自己起的一次性浏览器（读网页默认有头但看不见，见下文「有头还是无头」）。另外两档：想让 Diana 操作你日常浏览器里带登录态的页面，见[浏览器控制扩展](browser-control.md)；想给 Diana 一个它自己的、你能看见也能上手的常驻浏览器，见[内置浏览器](browser-builtin.md)。

网页渲染统一使用 Chromium / Google Chrome。插件不再下载或回退到 Obscura；此前手动安装的 Obscura 文件不会被自动删除。

## 读网页先走 TinyFish

搜索设置里配了 TinyFish（填了 key、来源列表里没停用）时，`browser_render` 和群里链接的自动读取先调 TinyFish 的 fetch 接口：一次 HTTP 请求拿到整页 markdown、最终网址、描述和站内链接，不起浏览器。这是照参考项目 [mizorewww/pi-extension-telegram-agent](https://github.com/mizorewww/pi-extension-telegram-agent) 的 `search` 工具做的，那边读页只走这一个接口。整页按网址缓存 15 分钟，模型对同一页换 `find` 再读不重新抓。

fetch 报错（被拦、超时 25 秒、空正文、HTTP 错误）才退回下面的本地 Chrome；没配 TinyFish 的部署照旧全走 Chrome。网页截图、GitHub Release 的官方 API 核验不受影响。TinyFish 不跟页面里脚本触发的延迟跳转，这类过渡页只读到过渡页本身。

生产 11-05～07 的 `browser_render` 走本地 Chrome，一次中位 10.5 秒、p90 15.6 秒。同一批常见网址走 fetch 接口，在 miku 上实测单页 1.4～7.4 秒，多数 2～4 秒；三页并行一次调用 3.7 秒。

## 渲染已有文件

要求把有权使用的已保存 HTML/SVG 出图时，`render_media` 可以直接用 `path` 指定工作目录文件；Markdown、Mermaid 和静态 SVG 使用 `render` 的同名参数。`send_file` 也支持从 `path` 读取已有 UTF-8 文本，以 `filename` 指定交付文件名。三个工具都在运行时完整读取文件，`content` 与 `path` 二选一，不需要模型分页读取再复制全文，也不需要打开 `file://` 或启动临时 HTTP 服务。

例如：`render_media({"format":"html","path":"downloads/cijian-m3.html"})`。把文件路径或 `file-read-needed` 等非 HTML 文字填进 HTML 的 `content` 会明确报错，不再生成只有这串文字的图片。

文件读取与页面脚本执行分开：运行时读取指定文件，页面仍在一次性离线浏览器里执行，不能自行读取宿主机文件或登录态。主人可以读取工作区文件；普通群成员只能读取在本群保存、且内容未改变的文件，权限按机器人、平台和群聊隔离，不按上传者隔离。普通私聊成员不能读共享工作区。工作目录外路径、越界软链接、运行时凭据配置仍不可用。原有插件开关、文件大小和图表长度限制不变。外链资源和相对路径资源不会自动打包，HTML 仍须自包含；需要宿主机命令时沿用 `host_access` 的授权和审核，而不是关闭所有工具的隔离。

群成员先用 `save_to_workspace(source="chat", message_id=...)` 保存本群附件，或以 `source="url"` 保存公网文件，再把返回的 `path` 原样交给渲染/文本交付工具。下载仍检查公网地址，不开放 MCP 暂存。普通成员只能指定文件名，不能指定任意目录、覆盖文件或写入长期区；文件自动放入 `downloads/groups/<群作用域>/`，沿用下载目录 7 天清理。写入开关、安全模式和文件交付插件的“仅主人可用”等设置仍生效。

保存到工作区的文件（包括由现有媒体生成链落盘的成品）会登记本群访问权限，记录持久化在受保护的 `.diana/group-file-access/`，重启不丢失。读取时核对内容摘要：被其他会话覆盖后，旧权限不会读到新内容。仅知道路径不代表有权限；旧文件、手工放进工作区的文件或通用文件工具写入的文件没有群归属记录，不自动共享，需要从本群原消息或公网来源重新保存。

验证：2026-11-01 使用 miku 上此次失败的原 HTML（437,849 字节）在本地走真实文件读取与 Chromium 截图，得到正常页面并目视确认；图片为 1000×4000，仍受原有 4000 像素高度上限约束。使用出问题时的 `devin/swe-2` 和生产提供商配置，在重建的失败场景中回放新工具说明与 schema，模型直接选择 `path`，未复述 HTML。未执行模型返回的发送调用，未修改生产服务；本地测试覆盖完整文件交付、占位输入拒绝、身份、大小及路径/凭据边界。

群成员链路也以同一原 HTML 和 `devin/swe-2` 回放：模型先调用 `save_to_workspace`，实际保存到隔离测试工作区，再把返回的路径交给 `render_media`；真实权限检查和文件读取通过，没有向平台发图。回归覆盖同群不同成员共享、重启后读取、跨群/跨平台/跨机器人和私聊拒绝、跨群引用拒绝、旧文件及内容变更拒绝、禁止覆盖/MCP/长期区、写入开关和安全模式。

## 插件安装

在「插件 → 网页渲染 → 依赖管理」中安装并刷新状态：

- Linux：通过 apk、apt、dnf/yum 或 pacman 安装 `chromium`。
- macOS：通过 Homebrew 安装 Google Chrome。Homebrew 的 Chromium cask 已被禁用，因此不提供该失效安装路径。
- Windows：通过 winget 或 Chocolatey 安装 Google Chrome。

已有 Chrome/Chromium 时直接复用。包管理器安装需要对应系统权限；glibc 的 Linux（amd64 / arm64）上包管理器装不了时，会退回下载 Google 的 Chrome for Testing 稳定版，放到 `DIANA_BROWSER_DIR`（未设置时是数据库所在目录下的 `browser/`，再退到用户缓存目录），不需要系统权限。其他情况会显示原因，可手动安装后刷新。安装成功后同时验证本地截图与网页沙箱/CDP，而非只检查版本号。源码或原生部署应以普通用户运行浏览器；中文字体可在插件依赖管理中一键下载；实际出图时也会按文字补齐缺失的多语言字体，无需管理员权限，详见[字体下载与多语言出图](fonts.md)。

## Docker

官方运行镜像预装 Chromium、fontconfig 和 Noto CJK 字体，并以 UID 10001 运行 Diana：入口以 root 启动，把挂进来的 `data/` 交给 UID 10001（Linux 上 Docker 自动创建的宿主机目录归 root，不修正就写不进数据库和日志），再降权启动主程序；用 `--user` 指定用户时跳过这一步，权限由部署方负责。因此镜像本身的默认用户是 root：`docker exec` 默认以 root 进入，在容器里手动执行 `diana` 命令请加 `-u diana`，否则可能在 `data/` 里留下主程序写不了的文件；Kubernetes 等要求 `runAsNonRoot` 的环境需显式设置 `runAsUser: 10001`，并自行保证数据卷对该 UID 可写（例如 `fsGroup: 10001`）。仓库的 `docker-compose.yml` 默认拉取预构建镜像，已配置专用 seccomp 规则。首次在部署目录执行一键脚本，自动下载 Compose 文件与 `scripts/docker/chromium-seccomp.json` 并启动（需已安装并启动 Docker，含 Compose v2）：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

以后在同一目录更新：

```sh
docker compose pull && docker compose up -d
```

从克隆的仓库本地构建：

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

已有 `docker run --name diana` 部署迁移到 Compose 时，先确认 Compose 使用原来的 `data/` 和可选配置文件路径（新部署只挂 `data/`，配置文件放在 `data/config.yaml`），再停止并删除旧容器（保留宿主机数据目录），以免同名容器冲突。

seccomp 配置必须保存在宿主机，Docker 在创建容器时读取它；它不是挂载给应用的配置文件。旧版尚未包含预装浏览器，需要升级到包含浏览器的镜像。

默认 Docker seccomp 会阻止 Chromium 创建其沙箱所需的命名空间，表现为 `Operation not permitted` 或 CDP 启动失败。项目配置保留默认拒绝策略，只在 Moby 默认配置基础上额外允许 `clone`、`setns`、`unshare`。不需要 `--privileged`、`SYS_ADMIN` 或 `seccomp=unconfined`，网页渲染也不添加 `--no-sandbox`。

浏览器页「更多设置 → 浏览器沙箱」决定 Diana 拉起的、会打开外部网页的 Chromium（网页渲染、浏览器搜索、内置浏览器）要不要关沙箱；只渲染本地 HTML 的出图保持原样（简单截图一直不带沙箱，带脚本的出图在 root 又降不了权时放行，其余跟档位走），改了立即生效，已在运行的内置浏览器要重启才换档：

- **仅沙箱**（默认）：始终带沙箱，起不来就报错、不降级，报错和依赖页都会提示怎么办。
- **自动**：以 root 运行又降不了权时直接关；其余情况先带沙箱启动，因沙箱起不来（seccomp 拦了 `clone`/`unshare`、宿主禁用用户命名空间、proot 等）而退出时，去掉沙箱重试一次并记 WARN 日志，之后这个进程里都不再带沙箱，换档后重新判断。
- **关闭**：始终带 `--no-sandbox`。

Chromium 不允许 root 带沙箱启动。Diana 以 root 运行（比如直接用 root 登录装的服务）时，带沙箱的浏览器改由 `diana` 系统用户启动，沙箱照开，不用配置：账号不存在就用 `useradd`/`adduser` 建一个不能登录、家目录为 `/var/lib/diana-browser` 的系统账号；每次运行的临时目录交给它。数据目录它进不去（root 装的常在 `/root` 下）时，内置浏览器的数据目录放到 `/var/lib/diana-browser/browser-box`，依赖页下载的 Chrome 装到 `/var/lib/diana-browser/browser`；之前下载到数据目录里的 Chrome 它够不着，依赖页会提示重新安装一次。建不出账号（没有 `useradd`、只读根文件系统）时照旧以 root 启动，按档位处理。本来就不带沙箱的简单截图仍以 root 启动。

Termux 不受档位约束、始终关闭：安卓不给普通 App 开用户命名空间，带沙箱一个浏览器都起不来。不开沙箱时页面隔离只剩内网屏蔽和临时 profile，群里发来的网页一旦利用浏览器漏洞就不再被挡住，能用普通用户运行、能放开命名空间就别关。主人也可以在私聊里让 Diana 用 `console_config` 的 `browser_source.sandbox` 改，需要确认后才生效。宿主机另有 AppArmor 或用户命名空间禁令时仍需按管理员策略处理，插件探测会显示实际失败原因。

Docker 默认的 `/dev/shm` 只有 64MB，Chrome 渲染重页面时容易写满它导致渲染进程崩溃。Linux 上启动的所有 Chrome 都带 `--disable-dev-shm-usage`，共享内存改走临时目录，因此 Compose 里不需要再设 `shm_size`，`docker run` 也不用加 `--shm-size`。

本地 HTML 截图与外部网页读取走不同启动路径，因此截图成功不代表网页沙箱可用；依赖页现已分别验证两者。所有渲染使用临时浏览器配置，不读取用户日常浏览器登录态。

## 有头还是无头

无头 Chrome 即使改了 UA、去掉了 `navigator.webdriver`，在渲染、GPU、屏幕尺寸这些细节上仍会被网站认出来，搜索引擎和不少站点因此拦截、弹验证码或返回空结果。所以两类场景分开：

一次性浏览器只用有头，读网页（群里发的链接自动读取、模型的 `browser_render`、搜索结果页）和本地渲染（HTML、SVG 出图、截图、字体探测）都一样：Chrome 以 `--no-startup-window` 启动，页面用 CDP 在后台另开，窗口放在屏幕外，macOS 上也不抢前台；Linux 没有图形会话时在 Xvfb 虚拟屏上开，多次渲染共用同一块按需拉起的虚拟屏。凑不出屏幕时（Linux 既没有 `DISPLAY` 也没装 `xvfb`）直接报错，不退回无头。

安卓 Termux 里没有进程沙盒、也拉不起 Xvfb，启动方式另有一套，见 [在 Termux 里用浏览器](termux.md)。

Docker：完整版镜像预装 `xvfb`，读网页直接有头；slim 镜像没有 `xvfb`，一次性浏览器起不来，需要先装上： `docker exec -u root <容器名> sh -c 'apt-get update && apt-get install -y xvfb'`。

## 隔离、并发与超时

- **每次一份临时 profile。** HTML 截图每次都起一个新的 Chrome 进程，profile 建在系统临时目录下、权限 0700、用完即删。网页读取（`browser_render`）和搜索结果页复用一个常驻 Chrome 进程，每页在独立的隐身上下文里打开、用完即销毁，隔离效果和一次性 profile 一样，省掉每次 1～2 秒的启动；进程闲置 3 分钟自动退出，起不来时退回一次性进程。两次调用之间、两个群之间、和主人的内置浏览器之间都不共用 Cookie 或登录态，群成员触发也碰不到主人的账号。
- **稳住就收尾，不等满观察窗。** 渲染先看页面有没有「后手」：文档 `complete`、网络静止（`DIANA_HEADLESS_BROWSER_NETWORK_IDLE_MS`，默认 750 毫秒没有请求）、DOM 连续不动够宽限期（`DIANA_HEADLESS_BROWSER_SETTLED_GRACE_MS`，默认 2500 毫秒），就收尾。只有一种页面例外：正文还不到 1500 字、而且排着会在观察窗内到期的一次性 `setTimeout`——这是「Loading…please wait」几秒后跳转那类过渡页的形状，它们等到最短观察窗（`DIANA_HEADLESS_BROWSER_VIRTUAL_TIME_MS`，默认 8 秒）或跳转发生。`setInterval` 心跳不算后手；正文几千字的内容页常年排着轮询、埋点的一次性回调，也不再为它们多等。以前只要页面挂着任意一个定时器就等满 8 秒，生产上渲染等待中位 10 秒、正文中位却只有 600 字。
- **同时最多 3 个。** 一次性浏览器每个都是一整个 Chrome 进程，网页读取（`browser_render`）和简单 HTML 截图（Chrome `--screenshot`）共用这份名额；带脚本或动画的出图走 `html_capture`，目前不占这份名额。满了就排队，最多等 20 秒，排不上交回「同时运行的一次性浏览器已达上限」，不会无限堆进程拖垮机器。排队时间不算进渲染超时。内存宽裕的机器可以用环境变量 `DIANA_HEADLESS_BROWSER_MAX_CONCURRENT` 调高（上限 16）。
- **容器里有 init 回收子进程。** 官方镜像以 `tini` 作为 PID 1（`ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/diana-entrypoint", "/app/diana-webui"]`）。没有它时 Chromium 退出后过继给 PID 1 的子进程没人回收，每次渲染都留下一串僵尸进程；`docker run` 和 Compose 都不用再加 `--init` / `init: true`，加了也无妨。
- **渲染有硬超时。** 默认 25 秒（`DIANA_HEADLESS_BROWSER_TIMEOUT_MS`，最多 60 秒）；到期收尾时拿已经抓到的内容，一点内容都没有就报错，浏览器进程随即结束。调用方取消时进程同样立即结束。

## seccomp 配置来源

`scripts/docker/chromium-seccomp.json` 基于 [Moby profiles 默认 seccomp](https://github.com/moby/profiles/blob/245180c51918481c0525424b3ee025d2b435d46c/seccomp/default.json)，上游提交 `245180c51918481c0525424b3ee025d2b435d46c`。修改仅为末尾增加上述三个调用的允许规则；`clone3` 继续沿用上游限制。原始 Apache-2.0 许可保存在 `scripts/docker/LICENSE.moby-profiles`。更新上游基线时须保留许可证并重新测试 Linux amd64/arm64 沙箱启动。
