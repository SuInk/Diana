// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 窗口外观和应用菜单：照原生桌面 App 的样子，macOS 侧栏透出系统磨砂（vibrancy），
// Windows 11 用 Mica，标题栏藏起来由界面自己画；系统不支持或用户关了透明效果时
// 退回纯色。界面按 <html data-material> 决定哪里透明（preload 里设）。

import { BrowserWindow, Menu, app, nativeTheme, type BrowserWindowConstructorOptions, type MenuItemConstructorOptions } from 'electron'
import { release } from 'node:os'
import type { AppCommand, Material } from '../shared/types.ts'

/** 界面顶部工具栏的高度，Windows 的系统按钮条要和它对齐。 */
export const toolbarHeight = 52

export function material(): Material {
  if (nativeTheme.prefersReducedTransparency || nativeTheme.shouldUseHighContrastColors) return 'none'
  if (process.platform === 'darwin') return 'vibrancy'
  // Mica 要 Windows 11 22H2（build 22621）及以上。
  const build = /^10\.0\.(\d+)/.exec(release())?.[1]
  if (process.platform === 'win32' && build && Number(build) >= 22621) return 'mica'
  return 'none'
}

function solidBackground(): string {
  return nativeTheme.shouldUseDarkColors ? '#16131a' : '#f2eef1'
}

function overlay() {
  return { color: '#00000000', symbolColor: nativeTheme.shouldUseDarkColors ? '#f1eaef' : '#251d23', height: toolbarHeight }
}

export function windowChrome(): BrowserWindowConstructorOptions {
  const mat = material()
  const base: BrowserWindowConstructorOptions = { backgroundColor: mat === 'none' ? solidBackground() : '#00000000' }
  if (process.platform === 'darwin') {
    return { ...base, titleBarStyle: 'hiddenInset', trafficLightPosition: { x: 18, y: 18 }, vibrancy: mat === 'vibrancy' ? 'sidebar' : undefined, visualEffectState: 'followWindow' }
  }
  if (process.platform === 'win32') {
    return { ...base, titleBarStyle: 'hidden', titleBarOverlay: overlay(), backgroundMaterial: mat === 'mica' ? 'mica' : 'none', autoHideMenuBar: true }
  }
  // Linux 各桌面环境对无边框窗口支持参差，用系统标题栏最稳。
  return { ...base, autoHideMenuBar: true }
}

/** 深浅色切换时 Windows 的系统按钮颜色和纯色背景要跟着变；磨砂由系统自己跟。 */
export function followTheme(win: BrowserWindow): void {
  nativeTheme.on('updated', () => {
    if (win.isDestroyed()) return
    if (material() === 'none') win.setBackgroundColor(solidBackground())
    if (process.platform === 'win32') win.setTitleBarOverlay(overlay())
  })
}

/** 中文应用菜单。需要界面响应的项发 diana:command，快捷键在菜单里注册，界面不再另听键盘。 */
export function installMenu(send: (command: AppCommand) => void): void {
  const mac = process.platform === 'darwin'
  const command = (label: string, accelerator: string, name: AppCommand): MenuItemConstructorOptions => ({ label, accelerator, click: () => send(name) })
  const template: MenuItemConstructorOptions[] = [
    ...(mac
      ? [
          {
            label: 'Diana',
            submenu: [
              { role: 'about', label: '关于 Diana' },
              { type: 'separator' },
              command('设置…', 'CmdOrCtrl+,', 'settings'),
              { type: 'separator' },
              { role: 'hide', label: '隐藏 Diana' },
              { role: 'hideOthers', label: '隐藏其他' },
              { role: 'unhide', label: '全部显示' },
              { type: 'separator' },
              { role: 'quit', label: '退出 Diana' },
            ],
          } satisfies MenuItemConstructorOptions,
        ]
      : [{ label: '文件', submenu: [command('设置…', 'CmdOrCtrl+,', 'settings'), { type: 'separator' }, { role: 'quit', label: '退出' }] } satisfies MenuItemConstructorOptions]),
    {
      label: '编辑',
      submenu: [
        { role: 'undo', label: '撤销' },
        { role: 'redo', label: '重做' },
        { type: 'separator' },
        { role: 'cut', label: '剪切' },
        { role: 'copy', label: '复制' },
        { role: 'paste', label: '粘贴' },
        { role: 'selectAll', label: '全选' },
      ],
    },
    {
      label: '显示',
      submenu: [
        command('消息', 'CmdOrCtrl+1', 'chat'),
        command('管理后台', 'CmdOrCtrl+2', 'admin'),
        command('显示或隐藏侧栏', 'CmdOrCtrl+\\', 'sidebar'),
        { type: 'separator' },
        command('刷新', 'CmdOrCtrl+R', 'reload'),
        ...(app.isPackaged ? [] : [{ role: 'toggleDevTools', label: '开发者工具' } satisfies MenuItemConstructorOptions]),
        { type: 'separator' },
        { role: 'resetZoom', label: '实际大小' },
        { role: 'zoomIn', label: '放大' },
        { role: 'zoomOut', label: '缩小' },
        { type: 'separator' },
        { role: 'togglefullscreen', label: '全屏' },
      ],
    },
    {
      label: '窗口',
      submenu: [{ role: 'minimize', label: '最小化' }, { role: 'zoom', label: '缩放' }, ...(mac ? [{ type: 'separator' } as const, { role: 'front', label: '全部置于顶层' } as const] : [{ role: 'close', label: '关闭' } as const])],
    },
  ]
  Menu.setApplicationMenu(Menu.buildFromTemplate(template))
}
