// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
//
// 桌面控制阶段 1 参考 helper：列出窗口 / 截取窗口。
// 需要 Screen Recording。Accessibility 留到写操作阶段。
// 本文件在 Linux CI 中不编译；仅作 macOS 本机参考实现。

import AppKit
import CoreGraphics
import Foundation

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

func screenshotWindow(id: String) -> (Data?, String?) {
    guard let wid = UInt32(id) else {
        return (nil, "bad_request: invalid window id")
    }
    guard let image = CGWindowListCreateImage(
        .null,
        .optionIncludingWindow,
        wid,
        [.boundsIgnoreFraming, .bestResolution]
    ) else {
        return (nil, "permission_denied: Screen Recording 未授权，或窗口不可截取。请在系统设置 → 隐私与安全性 → 屏幕录制中允许本 helper，然后重启。")
    }
    let rep = NSBitmapImageRep(cgImage: image)
    guard let data = rep.representation(using: .png, properties: [:]) else {
        return (nil, "helper_error: png encode failed")
    }
    return (data, nil)
}

let args = CommandLine.arguments
if args.count < 2 {
    fputs("usage: desktopctl-helper list | screenshot <window_id>\n", stderr)
    exit(2)
}
switch args[1] {
case "list":
    let enc = JSONEncoder()
    enc.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try! enc.encode(["windows": listWindows()])
    FileHandle.standardOutput.write(data)
    fputs("\n", stdout)
case "screenshot":
    guard args.count >= 3 else {
        fputs("screenshot requires window_id\n", stderr)
        exit(2)
    }
    let (data, err) = screenshotWindow(id: args[2])
    if let err = err {
        fputs(err + "\n", stderr)
        exit(1)
    }
    FileHandle.standardOutput.write(data!)
default:
    fputs("unknown command\n", stderr)
    exit(2)
}
