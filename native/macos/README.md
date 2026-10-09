# macOS 原生辅助

本目录放仅在 macOS 本机编译/运行的参考实现。Linux CI 不编译这里的 Swift。

## `desktopctl-helper.swift`（桌面控制 · 阶段 1）

本机只读桌面执行器的参考实现：枚举窗口并截取指定窗口。截图使用 ScreenCaptureKit，需要 macOS 14 或更新版本。

### 系统权限

- **Screen Recording**（系统设置 → 隐私与安全性 → 屏幕录制）：阶段 1 截图必需。未授权时 helper 应返回可解释错误，而不是空白图。
- **Accessibility**：阶段 1 **不需要**。后续点击/输入才会用到。

授权后请完全退出并重启 helper，否则 macOS 可能仍按旧权限运行。

### 编译（本机 macOS）

```bash
swiftc -O -o desktopctl-helper desktopctl-helper.swift
```

Go 单测使用假连接（`desktopctl.MockAdapter` / fake Conn），不调用本 helper，也不依赖真实显示器。

### 与 Diana 的关系

Helper 通过桌面控制协议连到 Diana（见 `docs/desktop-control.md`）。Diana 侧默认关闭；打开策略总开关、连接 helper，并在机器人配置打开 `agent_desktop_control_enabled` 后，主人才会看到 `desktop_windows` / `desktop_screenshot`。

## `diana_pdf_vision.swift`

既有 PDF 视觉相关辅助（与桌面控制无关）。
