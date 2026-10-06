// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
//
// 桌面控制阶段 1–2 参考 helper：list / screenshot / click / type / key。
// 截图需要 Screen Recording；写操作需要 Accessibility。
// Linux CI 不编译；仅作 macOS 本机参考实现。

import AppKit
import ApplicationServices
import CoreGraphics
import Foundation
import ScreenCaptureKit

struct WindowRecord: Codable {
    let id: String
    let app_name: String
    let bundle_id: String
    let title: String
    let active: Bool
}

func listWindows() -> [WindowRecord] {
    let opts = CGWindowListOption([.optionOnScreenOnly, .excludeDesktopElements])
    guard let info = CGWindowListCopyWindowInfo(opts, kCGNullWindowID) as? [[String: Any]] else {
        return []
    }
    let front = NSWorkspace.shared.frontmostApplication?.bundleIdentifier ?? ""
    var out: [WindowRecord] = []
    for item in info {
        let layer = item[kCGWindowLayer as String] as? Int ?? -1
        if layer != 0 { continue }
        let number = item[kCGWindowNumber as String] as? Int ?? 0
        if number == 0 { continue }
        let owner = item[kCGWindowOwnerName as String] as? String ?? ""
        let title = item[kCGWindowName as String] as? String ?? ""
        let pid = item[kCGWindowOwnerPID as String] as? pid_t ?? 0
        var bundle = ""
        if let app = NSRunningApplication(processIdentifier: pid) {
            bundle = app.bundleIdentifier ?? ""
        }
        out.append(WindowRecord(
            id: String(number),
            app_name: owner,
            bundle_id: bundle,
            title: title,
            active: bundle == front && !front.isEmpty
        ))
    }
    return out
}

func windowBounds(id: String) -> CGRect? {
    guard let wid = UInt32(id) else { return nil }
    let opts = CGWindowListOption([.optionIncludingWindow])
    guard let info = CGWindowListCopyWindowInfo(opts, wid) as? [[String: Any]],
          let item = info.first,
          let bounds = item[kCGWindowBounds as String] as? [String: Any],
          let x = bounds["X"] as? CGFloat,
          let y = bounds["Y"] as? CGFloat,
          let w = bounds["Width"] as? CGFloat,
          let h = bounds["Height"] as? CGFloat else {
        return nil
    }
    return CGRect(x: x, y: y, width: w, height: h)
}

@available(macOS 14.0, *)
func screenshotWindow(id: String) async -> (Data?, String?) {
    guard let wid = UInt32(id) else {
        return (nil, "bad_request: invalid window id")
    }
    guard CGPreflightScreenCaptureAccess() else {
        return (nil, "permission_denied: 请在系统设置 → 隐私与安全性 → 屏幕录制中允许本 helper，然后重启。")
    }
    do {
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let window = content.windows.first(where: { $0.windowID == wid }) else {
            return (nil, "window_unknown: window is not available")
        }
        let filter = SCContentFilter(desktopIndependentWindow: window)
        let config = SCStreamConfiguration()
        config.width = max(1, Int(window.frame.width * CGFloat(filter.pointPixelScale)))
        config.height = max(1, Int(window.frame.height * CGFloat(filter.pointPixelScale)))
        config.showsCursor = false
        let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
        let rep = NSBitmapImageRep(cgImage: image)
        guard let data = rep.representation(using: .png, properties: [:]) else {
            return (nil, "helper_error: png encode failed")
        }
        return (data, nil)
    } catch {
        return (nil, "helper_error: screenshot failed: \(error.localizedDescription)")
    }
}

func requireAccessibility() -> String? {
    let trusted = AXIsProcessTrustedWithOptions([kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary)
    if trusted { return nil }
    return "permission_denied: Accessibility（辅助功能）未授权。请在系统设置 → 隐私与安全性 → 辅助功能中允许本 helper，然后完全退出并重启。"
}

func clickWindow(id: String, x: Double, y: Double, button: String) -> String? {
    if let err = requireAccessibility() { return err }
    guard let bounds = windowBounds(id: id) else {
        return "window_unknown: 找不到窗口 \(id)"
    }
    // 窗口 bounds 为屏幕坐标（原点在左上）；相对坐标转全局。
    let global = CGPoint(x: bounds.origin.x + CGFloat(x), y: bounds.origin.y + CGFloat(y))
    var mouseButton: CGMouseButton = .left
    var downType: CGEventType = .leftMouseDown
    var upType: CGEventType = .leftMouseUp
    switch button.lowercased() {
    case "", "left":
        break
    case "right":
        mouseButton = .right
        downType = .rightMouseDown
        upType = .rightMouseUp
    case "middle":
        mouseButton = .center
        downType = .otherMouseDown
        upType = .otherMouseUp
    default:
        return "bad_request: button 只支持 left/right/middle"
    }
    guard let move = CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: global, mouseButton: mouseButton),
          let down = CGEvent(mouseEventSource: nil, mouseType: downType, mouseCursorPosition: global, mouseButton: mouseButton),
          let up = CGEvent(mouseEventSource: nil, mouseType: upType, mouseCursorPosition: global, mouseButton: mouseButton) else {
        return "helper_error: 无法创建鼠标事件"
    }
    move.post(tap: .cghidEventTap)
    down.post(tap: .cghidEventTap)
    up.post(tap: .cghidEventTap)
    return nil
}

func typeText(id: String, text: String) -> String? {
    if let err = requireAccessibility() { return err }
    _ = id // 阶段 2：假定调用方已把目标窗口置于前台；后续可加 activate。
    guard let source = CGEventSource(stateID: .hidSystemState) else {
        return "helper_error: 无法创建事件源"
    }
    for scalar in text.unicodeScalars {
        let utf16 = Array(String(scalar).utf16)
        guard let down = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: true),
              let up = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: false) else {
            return "helper_error: 无法创建键盘事件"
        }
        utf16.withUnsafeBufferPointer { buf in
            down.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: buf.baseAddress)
            up.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: buf.baseAddress)
        }
        down.post(tap: .cghidEventTap)
        up.post(tap: .cghidEventTap)
    }
    return nil
}

func keyCode(for name: String) -> (CGKeyCode, CGEventFlags)? {
    let n = name.lowercased()
    var flags: CGEventFlags = []
    var key = n
    if key.contains("cmd+") || key.contains("command+") {
        flags.insert(.maskCommand)
        key = key.replacingOccurrences(of: "cmd+", with: "").replacingOccurrences(of: "command+", with: "")
    }
    if key.contains("shift+") {
        flags.insert(.maskShift)
        key = key.replacingOccurrences(of: "shift+", with: "")
    }
    if key.contains("alt+") || key.contains("option+") {
        flags.insert(.maskAlternate)
        key = key.replacingOccurrences(of: "alt+", with: "").replacingOccurrences(of: "option+", with: "")
    }
    if key.contains("ctrl+") || key.contains("control+") {
        flags.insert(.maskControl)
        key = key.replacingOccurrences(of: "ctrl+", with: "").replacingOccurrences(of: "control+", with: "")
    }
    let map: [String: CGKeyCode] = [
        "return": 36, "enter": 36, "tab": 48, "escape": 53, "esc": 53,
        "delete": 51, "backspace": 51, "space": 49,
        "left": 123, "right": 124, "down": 125, "up": 126,
        "a": 0, "c": 8, "v": 9, "x": 7, "z": 6,
    ]
    guard let code = map[key] else { return nil }
    return (code, flags)
}

func pressKey(id: String, key: String) -> String? {
    if let err = requireAccessibility() { return err }
    _ = id
    guard let (code, flags) = keyCode(for: key) else {
        return "bad_request: 不支持的按键 \(key)"
    }
    guard let source = CGEventSource(stateID: .hidSystemState),
          let down = CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: true),
          let up = CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: false) else {
        return "helper_error: 无法创建按键事件"
    }
    down.flags = flags
    up.flags = flags
    down.post(tap: .cghidEventTap)
    up.post(tap: .cghidEventTap)
    return nil
}

let args = CommandLine.arguments
if args.count < 2 {
    fputs("usage: desktopctl-helper list | screenshot <id> | click <id> <x> <y> [button] | type <id> <text> | key <id> <key>\n", stderr)
    exit(2)
}

func fail(_ message: String) -> Never {
    fputs(message + "\n", stderr)
    exit(1)
}

switch args[1] {
case "list":
    let enc = JSONEncoder()
    enc.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try! enc.encode(["windows": listWindows()])
    FileHandle.standardOutput.write(data)
    fputs("\n", stdout)
case "screenshot":
    guard args.count >= 3 else { fail("screenshot requires window_id") }
    guard #available(macOS 14.0, *) else {
        fputs("unsupported: screenshot requires macOS 14 or later\n", stderr)
        exit(1)
    }
    Task {
        let (data, err) = await screenshotWindow(id: args[2])
        if let err = err { fputs(err + "\n", stderr); exit(1) }
        FileHandle.standardOutput.write(data!)
        exit(0)
    }
    dispatchMain()
case "click":
    guard args.count >= 5 else { fail("click requires window_id x y") }
    let button = args.count >= 6 ? args[5] : "left"
    guard let x = Double(args[3]), let y = Double(args[4]) else { fail("bad_request: x/y must be numbers") }
    if let err = clickWindow(id: args[2], x: x, y: y, button: button) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.click\"}")
case "type":
    guard args.count >= 4 else { fail("type requires window_id text") }
    let text = args[3...].joined(separator: " ")
    if let err = typeText(id: args[2], text: text) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.type\"}")
case "key":
    guard args.count >= 4 else { fail("key requires window_id key") }
    if let err = pressKey(id: args[2], key: args[3]) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.key\"}")
default:
    fail("unknown command")
}
