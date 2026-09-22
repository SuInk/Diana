# 内置浏览器

Diana 自己的那个浏览器：一个常驻的 Chrome/Chromium 进程，profile 落在数据目录下的
`browser-box/profile` 里，登录态跨重启保留。WebUI 的「浏览器」页能看到它的实时画面，
也能直接用鼠标键盘操作——登录、过验证码、临时接管，都由你自己来。

它和另外两档不重叠：

| | 无头浏览器（`browser_*` 接外部 CDP） | 浏览器控制扩展（`browser_ext_*`） | 内置浏览器（本页） |
| --- | --- | --- | --- |
| 浏览器 | 一次性 Chromium，每次全新 profile | 用户日常浏览器 | Diana 自己的常驻浏览器 |
| 登录态 | 没有 | 用户的，Diana 不碰 Cookie | 用户在这个浏览器里自己登的，留在 profile 目录 |
| 用户能不能看见 | 看不见 | 就在自己浏览器里 | WebUI 里有实时画面，能直接上手 |
| 默认 | 开启 | 全关，逐项授权 | 关闭，开了才有进程 |

打开这一档之后，`browser_open` / `browser_text` / `browser_click` / `browser_type` /
`browser_screenshot` 这组工具会自动接到内置浏览器上，不再指向机器人配置里那个外部
CDP 地址。两个前提：WebUI 里启用了内置浏览器，机器人自己的
`agent_browser_box_enabled` 也打开了。

## 实时画面是怎么来的

默认无头（`headful` 关闭），这是容器里唯一能跑起来的模式。画面走 CDP 的 `Page.startScreencast`：浏览器把每一帧渲染结果编成 JPEG 推给 Diana，
Diana 再转发到 WebUI。所以无头模式照样有画面，容器里不需要虚拟显示器，也不需要
noVNC。反过来，你在画面上的鼠标键盘操作走 `Input.dispatch*` 送回浏览器，落到页面上
和真人点是同一条输入管线。

中文输入走的是 `Input.insertText`：输入法上屏的是整段文字，不是一串按键。

`headful` 只在有图形界面的机器上才打得开。官方 Docker 镜像里没有 X server，也没有
虚拟显示，Linux 上没有 `DISPLAY` / `WAYLAND_DISPLAY` 时保存这一项会当场被拒绝，理由
就写在返回里——不是存下去再让浏览器起不来。想在容器里用有头，得自己补一个虚拟显示
（Xvfb 之类）并把 `DISPLAY` 传进来；但实时画面本来就不依赖它，多数情况没有必要。

## 人工接管

你在画面上点一下、敲一下键、或者在地址栏里跳转，接管就自动打开；也可以用页面上的
「我来操作 / 交还给机器人」按钮显式切。接管打开时：

- 模型那一侧拿不到 CDP 地址，`browser_*` 工具当场报「用户正在人工接管内置浏览器」；
- 画面和输入照常，因为那正是你要用的；
- 交还之后机器人才能继续。

这条边界的意义在于：这个浏览器里有你亲手登录的账号，人和机器人不该同时去点同一个
页面。

## 站点边界

内置浏览器不做「白名单为空就一个站都不许」的失败关闭——那会让这一档没法用来浏览。
取而代之的是黑名单：`denied_hosts` 里的站点永远打不开，写法与浏览器控制扩展一致
（`example.com` 只匹配这一个主机名，`*.example.com` 匹配子域但不含主域本身）。
另外只接受 `http` 与 `https`，`file://` 和 `chrome://` 一律拒绝——前者能读到容器里的
文件，后者能翻出浏览器自己的设置页。

启动参数沿用无头渲染那条路的沙盒加固，其中包含
`--host-resolver-rules=MAP localhost ~NOTFOUND, ...`：内置浏览器解析不到
`localhost`、`*.local` 和 `host.docker.internal`，也就碰不到同一台机器上的内网服务。

## 部署

- **完整版容器镜像**自带 chromium，打开开关即可。
- **slim 镜像**没有浏览器，在宿主机执行
  `docker exec -u root <容器名> apk add --no-cache chromium font-noto-cjk` 之后再开。
- **裸机部署**会按常见安装路径找 Chrome/Chromium，也认 WebUI 下载的 Chrome for Testing。
- profile 在 `<数据目录>/browser-box/profile`。用 compose 的默认挂载时它跟着 `./data`
  走，容器重建后登录态还在；换句话说，**这个目录等价于一份浏览器登录态，备份和权限
  按敏感数据对待**。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/browser-box/status` | 配置、运行状态、接管状态、profile 目录 |
| PUT | `/api/browser-box/settings` | 覆盖配置；关掉总开关会结束进程 |
| POST | `/api/browser-box/start` / `/stop` | 手动起停 |
| POST | `/api/browser-box/takeover` | 切换人工接管 |
| GET/POST | `/api/browser-box/tabs` | 列出、新开标签页 |
| DELETE | `/api/browser-box/tabs/:id` | 关掉一个标签页 |
| GET | `/api/browser-box/live` | 实时画面 WebSocket：出去是画面帧，进来是鼠标键盘事件 |

实时画面端点在 `/api` 下，走 WebUI 会话鉴权，并且只接受同源升级请求：能连上它就等于
能看你的浏览器。

## 资源开销

一个常驻 Chrome 大约吃 200–400 MB 内存，空闲时 CPU 接近 0。实时画面只在 WebUI 那一页
打开时才推帧，关掉页面就停；每帧是质量 60 的 JPEG，1280×800 下通常几十 KB。
