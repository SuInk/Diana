# macOS 原生辅助

本目录放仅在 macOS 本机编译/运行的参考实现。Linux CI 不编译这里的 Swift。

## `desktopctl-helper.swift`（桌面控制 · 阶段 1–2）

本机桌面执行器的参考实现：枚举窗口、截图、点击、输入与按键。

### 系统权限

- **Screen Recording**（系统设置 → 隐私与安全性 → 屏幕录制）：截图必需。
- **Accessibility**（辅助功能）：点击、输入、按键必需。阶段 1 只读可不授；阶段 2 写操作未授权时 helper 返回可解释错误。

授权后请完全退出并重启 helper，否则 macOS 可能仍按旧权限运行。

### 编译（本机 macOS）

```bash
swiftc -O -o desktopctl-helper desktopctl-helper.swift
```

### 用法

```bash
./desktopctl-helper list
./desktopctl-helper screenshot <window_id>
./desktopctl-helper click <window_id> <x> <y> [left|right|middle]
./desktopctl-helper type <window_id> <text>
./desktopctl-helper key <window_id> <key>
```

Go 单测使用假连接（`desktopctl.MockAdapter`），不调用本 helper。

### 与 Diana 的关系

Helper 通过桌面控制协议连到 Diana（见 `docs/desktop-control.md`）。打开策略总开关、按需打开 `write_enabled`、连接 helper，并在机器人配置打开 `agent_desktop_control_enabled` 后，主人才会看到桌面工具。

## `diana_pdf_vision.swift`

既有 PDF 视觉相关辅助（与桌面控制无关）。

截图使用 ScreenCaptureKit，需要 macOS 14 或更新版本。
