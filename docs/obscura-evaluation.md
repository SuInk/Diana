# Obscura 轻量浏览器实测（2026-09-18，已停止采用）

后续已按部署决策改用 Chromium / Google Chrome，移除 Obscura 自动安装与运行回退。以下为实验记录，不是当前安装指南。

追加容器验证：官方 0.2.2 ARM64 镜像可运行；GitHub 固定短等待两次成功，networkidle0 两次超时。发布镜像默认用户为 root，本次测试显式使用 UID 65532。该版本不加载系统字体，超过 8 MiB 的网页字体会被拒绝；使用 12 KiB 的中文字体子集后截图中文字正常。动态测试页的 1.8 秒延迟内容需要显式等待，默认策略会提前返回。远程 CDP + chromedp 等待测试超时。因此不继续推进该方案。

针对 [Docker 浏览器依赖与渲染稳定性需求](https://github.com/SuInk/Diana/issues/566)，本次先验证 Obscura，尚未改动 Docker 镜像。

## 环境与版本

- macOS ARM64，Obscura v0.2.1 普通渲染发行包。
- 发行包 SHA-256：`5233da6426ec16667d7e4374b824189c6dfb3b325e5cf3fb5f04c7bc48b52a0f`，与 Diana 固定校验值一致。
- 上游：https://github.com/h4ckf0r0day/obscura/releases/tag/v0.2.1
- 普通版接受 `--stealth` 指纹选项，但完整 TLS 模拟需要上游独立的 stealth 构建；本次未测试完整 stealth 构建。

## 结果

| 场景 | 结果 |
| --- | --- |
| `example.com`，Diana 实际 Render 接口 | 成功，约 0.76 秒；独立 CLI 测量最大驻留内存约 37 MiB |
| GitHub Diana 仓库，CLI `networkidle0 --timeout 30 --stealth` | 返回正确仓库标题与 README 内容，约 32 秒，最大驻留内存约 284 MiB |
| GitHub Diana 仓库，Diana 当前默认 Render 接口 | 25 秒上下文超时，未返回页面 |
| GitHub Diana 仓库，CLI `--wait-until load --wait 1 --timeout 20 --stealth` | 返回正确仓库页面，约 12 秒，最大驻留内存约 258 MiB |
| Diana `CaptureHTMLScreenshot` 接口 | 生成有效 640×240 PNG，JavaScript 修改生效；默认字体下中文出现方框，CDP 关闭阶段有 executor 日志 |
| Docker | 本机 Docker CLI 存在，但 daemon 未运行；未完成 Linux 容器运行测试 |

上述是单次网络实测，不是性能保证或反爬成功率统计。两次 GitHub CLI 测试与默认接口测试的参数不同，不能将耗时差异全部归因于某一个选项。

## 集成判断

可以作为轻量读取引擎候选，但当前实现尚不适合直接宣称 Docker 开箱即用：

1. 上游 Linux 发行流程构建 `*-unknown-linux-gnu`，基于 Ubuntu 22.04；Diana 当前运行镜像是 Alpine 3.22/musl。需要选择 glibc 运行镜像或验证兼容层，不能直接复制发行二进制。
2. 需明确网页就绪判断：GitHub 长连接/资源加载场景下，严格等待网络空闲容易耗尽外层超时。短等待可用于受控策略，但不代表 SPA 已加载完整。
3. 需补齐字体并做中文截图验收；“PNG 有效”不能替代文字可读性检查。
4. 上游发行包同时包含 `obscura` 与 `obscura-worker`；容器集成应保留完整运行组件，再验证非 root、资源限制及 amd64/arm64 行为。
5. 之后再测试完整 stealth 构建，分别记录网络失败、超时、站点拒绝与页面解析失败。公开 GitHub API 回退应独立实现，不能用提高超时替代。

建议：保持当前镜像基线，下一批用 glibc 镜像做可复现的 Obscura 容器验证，通过后再决定替换或预装方式。
