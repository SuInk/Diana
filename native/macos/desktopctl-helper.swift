// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
//
// Diana 本机桌面 helper：list / screenshot / click / type / key。
// 截图需要 Screen Recording；写操作需要 Accessibility。
// 由 macOS CI 编译，并随 macOS 完整包分发。

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
        config.width = max(1, Int(window.frame.width))
        config.height = max(1, Int(window.frame.height))
        config.showsCursor = false
        config.ignoreShadowsSingleWindow = true
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
    let trusted = AXIsProcessTrusted()
    if trusted { return nil }
    return "permission_denied: Accessibility（辅助功能）未授权。请在系统设置 → 隐私与安全性 → 辅助功能中允许本 helper，然后完全退出并重启。"
}

// 写入只能指向当前最前的应用窗口；不猜测后台窗口，也不向全局前台发键盘事件。
func foregroundTargetPID(id: String) -> pid_t? {
    guard let wanted = UInt32(id),
          let front = NSWorkspace.shared.frontmostApplication,
          let info = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]],
          let window = info.first(where: { ($0[kCGWindowLayer as String] as? Int) == 0 }),
          let number = window[kCGWindowNumber as String] as? UInt32,
          let pid = window[kCGWindowOwnerPID as String] as? pid_t,
          number == wanted, pid == front.processIdentifier else { return nil }
    return pid
}

func clickWindow(id: String, x: Double, y: Double, button: String) -> String? {
    if let err = requireAccessibility() { return err }
    guard let targetPID = foregroundTargetPID(id: id) else {
        return "window_unknown: 目标窗口不是当前前台窗口，请先手动切换到该窗口"
    }
    guard let bounds = windowBounds(id: id) else {
        return "window_unknown: 找不到窗口 \(id)"
    }
    guard x.isFinite, y.isFinite, x >= 0, y >= 0, x < Double(bounds.width), y < Double(bounds.height) else {
        return "bad_request: 点击坐标必须位于目标窗口内"
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
    move.postToPid(targetPID)
    down.postToPid(targetPID)
    up.postToPid(targetPID)
    return nil
}

func typeText(id: String, text: String) -> String? {
    if let err = requireAccessibility() { return err }
    guard let targetPID = foregroundTargetPID(id: id) else {
        return "window_unknown: 目标窗口不是当前前台窗口，请先手动切换到该窗口"
    }
    guard let source = CGEventSource(stateID: .hidSystemState) else {
        return "helper_error: 无法创建事件源"
    }
    for scalar in text.unicodeScalars {
        guard foregroundTargetPID(id: id) == targetPID else {
            return "takeover: 前台窗口已改变，停止输入"
        }
        if let err = requireNonSecureFocus(id: id) { return err }
        let utf16 = Array(String(scalar).utf16)
        guard let down = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: true),
              let up = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: false) else {
            return "helper_error: 无法创建键盘事件"
        }
        utf16.withUnsafeBufferPointer { buf in
            down.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: buf.baseAddress)
            up.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: buf.baseAddress)
        }
        down.postToPid(targetPID)
        up.postToPid(targetPID)
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
    guard let targetPID = foregroundTargetPID(id: id) else {
        return "window_unknown: 目标窗口不是当前前台窗口，请先手动切换到该窗口"
    }
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
    down.postToPid(targetPID)
    up.postToPid(targetPID)
    return nil
}


func axValue(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success else { return nil }
    return value
}
func axBounds(_ element: AXUIElement) -> CGRect? {
    guard let position = axValue(element, kAXPositionAttribute), CFGetTypeID(position) == AXValueGetTypeID(),
          let size = axValue(element, kAXSizeAttribute), CFGetTypeID(size) == AXValueGetTypeID() else { return nil }
    var point = CGPoint.zero; var extent = CGSize.zero
    guard AXValueGetValue(position as! AXValue, .cgPoint, &point),
          AXValueGetValue(size as! AXValue, .cgSize, &extent) else { return nil }
    return CGRect(origin: point, size: extent)
}
func targetAXWindow(id: String) -> AXUIElement? {
    guard let wid = UInt32(id), let bounds = windowBounds(id: id),
          let info = CGWindowListCopyWindowInfo(.optionIncludingWindow, wid) as? [[String: Any]],
          let pid = info.first?[kCGWindowOwnerPID as String] as? pid_t else { return nil }
    let app = AXUIElementCreateApplication(pid)
    AXUIElementSetMessagingTimeout(app, 1)
    guard let windows = axValue(app, kAXWindowsAttribute) as? [AXUIElement] else { return nil }
    // Never guess between two overlapping windows in the same application.
    let matches = windows.filter { element in
        guard let rect = axBounds(element) else { return false }
        return abs(rect.minX-bounds.minX)<1 && abs(rect.minY-bounds.minY)<1 && abs(rect.width-bounds.width)<1 && abs(rect.height-bounds.height)<1
    }
    return matches.count == 1 ? matches[0] : nil
}
struct AccessibleElement: Codable, Equatable {
    let id: String
    let role: String
    let label: String
    let value: String
    let x: Double
    let y: Double
    let width: Double
    let height: Double
    let enabled: Bool
}
func readElements(id: String) -> ([AccessibleElement], [String: AXUIElement], Bool, String?) {
    if let err = requireAccessibility() { return ([], [:], false, err) }
    guard let root = targetAXWindow(id: id), let bounds = windowBounds(id: id) else {
        return ([], [:], false, "window_unknown: 无法唯一匹配窗口的辅助功能元素")
    }
    var rows: [AccessibleElement] = []; var refs: [String: AXUIElement] = [:]
    var truncated = false; var visited = 0
    func visit(_ node: AXUIElement, _ path: String, _ depth: Int) {
        guard visited < 500, depth <= 16 else { truncated = true; return }
        visited += 1
        let role = axValue(node, kAXRoleAttribute) as? String ?? ""
        let subrole = axValue(node, kAXSubroleAttribute) as? String ?? ""
        // Do not read secure values or descend into secure controls.
        if role == "AXSecureTextField" || subrole == "AXSecureTextField" { return }
        if let rect = axBounds(node), !rect.isEmpty, bounds.contains(rect) {
            let label = axValue(node, kAXTitleAttribute) as? String ?? axValue(node, kAXDescriptionAttribute) as? String ?? ""
            let value = axValue(node, kAXValueAttribute) as? String ?? ""
            rows.append(AccessibleElement(id: path, role: role, label: String(label.prefix(256)), value: String(value.prefix(512)), x: Double(rect.minX-bounds.minX), y: Double(rect.minY-bounds.minY), width: Double(rect.width), height: Double(rect.height), enabled: axValue(node, kAXEnabledAttribute) as? Bool ?? false))
            refs[path] = node
        }
        let children = axValue(node, kAXChildrenAttribute) as? [AXUIElement] ?? []
        for (index, child) in children.prefix(500).enumerated() { visit(child, path + "." + String(index), depth+1) }
        if children.count > 500 { truncated = true }
    }
    visit(root, "0", 0)
    return (rows, refs, truncated, nil)
}
func requireNonSecureFocus(id: String) -> String? {
    guard let pid = foregroundTargetPID(id: id) else { return "window_unknown: 目标窗口不在前台" }
    let app = AXUIElementCreateApplication(pid)
    guard let raw = axValue(app, kAXFocusedUIElementAttribute), CFGetTypeID(raw) == AXUIElementGetTypeID() else {
        return "permission_denied: 无法核实焦点控件，请人工接管"
    }
    let focused = raw as! AXUIElement
    if (axValue(focused, kAXSubroleAttribute) as? String) == "AXSecureTextField" || (axValue(focused, kAXRoleAttribute) as? String) == "AXSecureTextField" {
        return "permission_denied: 安全输入框需要人工接管"
    }
    return nil
}
func clickElement(id: String, elementID: String, expected: AccessibleElement) -> String? {
    guard foregroundTargetPID(id: id) != nil else { return "window_unknown: 目标窗口不在前台" }
    let (rows, refs, _, error) = readElements(id: id)
    if let error = error { return error }
    guard let row = rows.first(where: { $0.id == elementID && $0.enabled }), let element = refs[elementID] else {
        return "stale_observation: 元素不可用，请重新读取"
    }
    guard row == expected else { return "stale_observation: 目标元素已经变化，请重新读取" }
    var actions: CFArray?
    if AXUIElementCopyActionNames(element, &actions) == .success, let names = actions as? [String], names.contains(kAXPressAction) {
        guard foregroundTargetPID(id: id) != nil else { return "takeover: 前台窗口已改变" }
        return AXUIElementPerformAction(element, kAXPressAction as CFString) == .success ? nil : "helper_error: 元素点击失败，请核实现场"
    }
    return clickWindow(id: id, x: row.x+row.width/2, y: row.y+row.height/2, button: "left")
}
func scrollWindow(id: String, x: Double, y: Double, dx: Int32, dy: Int32) -> String? {
    if let err = requireAccessibility() { return err }
    guard let pid = foregroundTargetPID(id: id), let bounds = windowBounds(id: id) else { return "window_unknown: 目标窗口不在前台" }
    guard x.isFinite, y.isFinite, x >= 0, y >= 0, x < Double(bounds.width), y < Double(bounds.height),
          dx >= -1000, dx <= 1000, dy >= -1000, dy <= 1000, dx != 0 || dy != 0 else { return "bad_request: 滚动位置或距离无效" }
    guard let event = CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2, wheel1: -dy, wheel2: -dx, wheel3: 0) else { return "helper_error: 无法创建滚动事件" }
    event.location = CGPoint(x: bounds.minX+CGFloat(x), y: bounds.minY+CGFloat(y))
    event.postToPid(pid)
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

if args.count > 2, let expected = ProcessInfo.processInfo.environment["DIANA_TARGET_BUNDLE"], !expected.isEmpty {
    guard listWindows().contains(where: { $0.id == args[2] && $0.bundle_id == expected }) else {
        fail("window_unknown: 目标窗口所属应用已变化")
    }
}
switch args[1] {
case "permissions":
    print("{\"screen_recording\":\(CGPreflightScreenCaptureAccess()),\"accessibility\":\(AXIsProcessTrusted())}")
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
case "elements":
    guard args.count >= 3 else { fail("bad_request: elements requires window_id") }
    let (rows, _, truncated, error) = readElements(id: args[2])
    if let error = error { fail(error) }
    struct Payload: Codable { let window_id: String; let elements: [AccessibleElement]; let truncated: Bool }
    FileHandle.standardOutput.write(try! JSONEncoder().encode(Payload(window_id: args[2], elements: rows, truncated: truncated)))
case "element-click":
    guard args.count >= 4 else { fail("bad_request: element-click requires window_id element_id") }
    guard let expected = try? JSONDecoder().decode(AccessibleElement.self, from: FileHandle.standardInput.readDataToEndOfFile()) else { fail("bad_request: element-click requires expected element JSON on stdin") }
    if let err = clickElement(id: args[2], elementID: args[3], expected: expected) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.click\"}")
case "scroll":
    guard args.count >= 7, let x = Double(args[3]), let y = Double(args[4]), let dx = Int32(args[5]), let dy = Int32(args[6]) else { fail("bad_request: scroll requires window_id x y delta_x delta_y") }
    if let err = scrollWindow(id: args[2], x: x, y: y, dx: dx, dy: dy) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.scroll\"}")
case "click":
    guard args.count >= 5 else { fail("click requires window_id x y") }
    let button = args.count >= 6 ? args[5] : "left"
    guard let x = Double(args[3]), let y = Double(args[4]) else { fail("bad_request: x/y must be numbers") }
    if let err = clickWindow(id: args[2], x: x, y: y, button: button) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.click\"}")
case "type":
    guard args.count >= 4 else { fail("type requires window_id text") }
    let text = args[3] == "--stdin" ? String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8) ?? "" : args[3...].joined(separator: " ")
    if let err = requireNonSecureFocus(id: args[2]) { fail(err) }
    if let err = typeText(id: args[2], text: text) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.type\"}")
case "key":
    guard args.count >= 4 else { fail("key requires window_id key") }
    if let err = requireNonSecureFocus(id: args[2]) { fail(err) }
    if let err = pressKey(id: args[2], key: args[3]) { fail(err) }
    print("{\"ok\":true,\"op\":\"window.key\"}")
default:
    fail("unknown command")
}
