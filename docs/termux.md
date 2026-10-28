# 在 Termux 里用浏览器

在安卓手机的 Termux 里跑 Diana 时，网页读取（`browser_render`）、HTML 出图和内置浏览器都要一个
Chromium。Termux 主仓库没有它，要从 tur-repo 装：

```sh
pkg install -y tur-repo
pkg install -y chromium
```

WebUI「依赖」页的一键安装在 Termux 里执行的就是这两条。装完不用配路径：Diana 认出 Termux 后先找
`$PREFIX/bin/chromium-browser` 和 `$PREFIX/bin/chromium`，再按 `PATH` 找；装在别处时用
`DIANA_HEADLESS_BROWSER_EXECUTABLE` 指过去。

## 和普通 Linux 不一样的地方

- **没有进程沙盒。** 安卓不给普通 App 开用户命名空间，Chromium 的沙盒起不来，所以在 Termux 里启动的
  Chromium 都带 `--no-sandbox`。页面仍然在 Termux 自己的 App 沙盒里，访问 localhost、内网域名的屏蔽照旧。
  普通 Linux 和 Docker 不受影响，仍然开沙盒。
- **默认无头。** Termux 里装得上 Xvfb，但它的 socket 落在 `$PREFIX/tmp/.X11-unix`，Diana 不去拉它。没有
  图形会话时，网页读取和内置浏览器都改成无头运行。无头更容易被搜索引擎和部分站点认出来、弹验证码。
  内置浏览器的实时画面走 CDP screencast，无头一样能在 WebUI 里看、能接管。
- **有图形会话就有头。** 用 termux-x11 之类开了桌面、并设好 `DISPLAY` 再启动 Diana，就照常开有头。

## 怎么认出 Termux

`PREFIX` 指向 `/data/data/com.termux/...`、设了 `TERMUX_VERSION`，或者 `/data/data/com.termux/files/usr`
存在，任一条成立就算。所以在 Termux 里自己编译（`GOOS=android`）和直接跑发布包的 linux/arm64 程序都认得出。

## 已知限制

- Termux 里的 Chromium 是社区打包的，版本可能落后桌面版，个别站点会因此出问题。
- 手机内存紧张时，安卓可能直接杀掉 Chromium 进程。一次性渲染下次调用会重新拉起，内置浏览器下次用到时也会重启。
- 一键安装不带中文字体，截图里的中文显示成方块时，需要自己给 Termux 装上中文字体。
