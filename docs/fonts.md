# 字体下载与多语言出图

系统已经有可用字体时直接复用。没有中文字体时，可以在「插件 → 网页渲染 / 群关系图 → 依赖管理」点击 `cjk-font` 安装；首次实际出图时也会按可见文字自动补齐字体。打开依赖面板本身不会下载。

## 按需覆盖

| 文字 | 缺失时的字体 |
| --- | --- |
| 中文、日文、韩文 | Noto Sans CJK SC（约 16 MiB） |
| 拉丁字母、希腊文、西里尔字母 | Noto Sans |
| 阿拉伯文 | Noto Sans Arabic |
| 希伯来文 | Noto Sans Hebrew |
| 泰文 | Noto Sans Thai |
| 天城文（如印地文） | Noto Sans Devanagari |
| 孟加拉文 | Noto Sans Bengali |
| 泰米尔文 | Noto Sans Tamil |
| 常见符号 | Noto Sans Symbols 2 |
| Emoji | 优先系统字体，缺失时使用单色 Noto Emoji |

只下载实际需要且缺失的包，不会每次把整套字体重装。字体检查针对当前文字的字形，而不是只检查文件名。未列出的文字还会尝试系统字体；仍无法覆盖时明确提示缺少的 Unicode 字符，不假装支持所有语言。字体字形覆盖也不等于支持所有新 Emoji 组合；下载的 Noto Emoji 是单色，并非彩色 Emoji。

这些处理适用于 Diana 生成的关系图、Markdown / Mermaid / SVG 图片。读取外部网页正文不需要下载字体，也不会把用户文字发给字体下载服务。

## 存储和验证

字体来自固定提交的 Noto 官方仓库，下载时校验固定 SHA-256，并解析字体检查所需字形。文件先写入临时文件，通过验证后才替换正式文件，失败或取消不会留下半个已安装字体。下载有两分钟预算，父请求取消时会停止；失败可在依赖管理中重试。

字体与上游 OFL 许可证保存在当前服务用户的缓存目录下 `diana/fonts/`：Linux 遵循 `XDG_CACHE_HOME`，默认 `~/.cache`；macOS 通常为 `~/Library/Caches`；Windows 通常为 `%LOCALAPPDATA%`。目录必须可写，无需修改系统字体目录或注册表。已有的 `DIANA_CJK_FONT` / `DIANA_RELATION_FONT` 显式指定错误时会提示修正，不会擅自覆盖用户指定文件。

安装后立即更新字体缓存与依赖状态，不必重启。截图时把已验证字体复制到一次性渲染目录；Linux 只为该浏览器进程补充 fontconfig 配置，系统配置不变。截图等待字体实际加载完成，避免字体未就绪时生成白图。

关系图的单字体 Go 绘制路径不负责复杂连写。阿拉伯文、印度文字、组合字符、Emoji 或单字体缺字时，会交给已启用的 Chromium 网页渲染插件完成多字体排版；插件未启用时会明确说明限制。

## 验证

2026-09-18 在非 root Docker 容器中屏蔽系统字体目录，验证了真实下载、校验、安装后直接生成关系图及 Chromium 截图。多语言样张覆盖上表除独立符号字体外的主要文字，以及肤色修饰、旗帜和 ZWJ Emoji 示例，并进行了图片目视检查。符号字体包含按需选择与字形检查，不保证每一种符号或组合都被覆盖。

运行回归测试：

```sh
go test ./model/assistant ./model/agent -run 'Test.*(CJKFont|RenderFont|Screenshot|Relation)'
```

源字体许可随下载文件保存，也保存在 `model/assistant/render_assets/`；小型 CJK 回归字形样本仅用于测试，不作为运行字体。
