# macOS 原生辅助

本目录包含 macOS 原生辅助程序。桌面 helper 由 macOS CI 编译为通用二进制，随 macOS 完整包分发。

## `desktopctl-helper.swift`（本机桌面控制）

本机桌面执行器：枚举窗口、截图、读取可访问元素、点击、输入、按键与滚动。

### 系统权限

- **Screen Recording**（系统设置 → 隐私与安全性 → 屏幕录制）：截图必需。
- **Accessibility**（辅助功能）：读取元素、点击、输入、按键与滚动必需。只列窗口和截图可不授；未授权时 helper 返回可解释错误。

授权后请完全退出并重启 helper，否则 macOS 可能仍按旧权限运行。

### 编译（本机 macOS）

```bash
swiftc -O -o desktopctl-helper desktopctl-helper.swift
```

### 用法

```bash
./desktopctl-helper permissions # 仅检查权限，不触发授权弹窗
./desktopctl-helper list
./desktopctl-helper screenshot <window_id>
./desktopctl-helper elements <window_id>
./desktopctl-helper scroll <window_id> <x> <y> <delta_x> <delta_y>
./desktopctl-helper click <window_id> <x> <y> [left|right|middle]
./desktopctl-helper type <window_id> <text>
./desktopctl-helper key <window_id> <key>
```

`element-click <window_id> <element_id>` 从标准输入读取先前观察到的完整元素 JSON，复核角色、标签、值和位置后才点击。滚动以像素为单位，水平正值向右、垂直正值向下，单轴最多 1000 像素。

以上 CLI 是本机受信任的底层执行面；Diana 的应用权限、观察版本和人工确认在 Go 控制面执行。不要把 helper 命令作为任意命令执行工具暴露给模型。

Go 单测使用假连接和临时子进程 fixture，不操作真实桌面；真实辅助功能操作仍需授权后单独验收。

### 与 Diana 的关系

Diana 自动查找主程序同目录下的 `desktopctl-helper`；源码部署也可设置 `DIANA_DESKTOP_HELPER` 为编译产物的绝对路径。重启服务，在“设置 → 桌面控制”启用总开关、按需允许输入，并在机器人配置打开 `agent_desktop_control_enabled`。Diana 通过本机子进程调用 helper，不开放远程执行端口；文本通过标准输入传递，不进入命令行参数。

控制台支持应用范围、系统权限状态、具体操作预览与单次确认、持久人工接管，以及任务暂停、恢复、确认、取消。详见 [桌面控制流程与边界](../../docs/desktop-control.md)。取消或超时会终止仍在运行的 helper，但不能撤销已经发送的输入。系统权限不足时返回说明，不自动申请或授予权限。Docker 中不支持操作宿主 macOS 桌面。

## `diana_pdf_vision.swift`

既有 PDF 视觉相关辅助（与桌面控制无关）。

截图使用 ScreenCaptureKit，需要 macOS 14 或更新版本。
