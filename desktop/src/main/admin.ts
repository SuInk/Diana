// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 管理后台就是服务端自带的 WebUI，嵌在主窗口右边的圆角面板里显示，不另开窗口。
// 用 WebContentsView 叠在界面上：界面量好占位区域的位置发过来，这里照着摆；
// 离开管理后台时只隐藏不销毁，回来还停在原来那页。
//
// 单开一个会话分区，把这边的登录 Cookie 放进去，打开就是登录状态；视图只在这台
// 服务器内跳转，外链交给系统浏览器。

import { BrowserWindow, WebContentsView, nativeTheme, session, shell } from 'electron'
import type { DianaApi } from './api.ts'
import { sameOrigin } from '../shared/server.ts'
import type { Rect } from '../shared/types.ts'

const radius = 12

export class AdminView {
  private view?: WebContentsView
  private server = ''

  constructor(private readonly pins: () => Record<string, string>) {}

  async show(win: BrowserWindow, api: DianaApi, bounds: Rect): Promise<void> {
    if (!this.view || this.server !== api.server) await this.create(win, api)
    this.place(bounds)
    this.view!.setVisible(true)
  }

  place(bounds: Rect): void {
    this.view?.setBounds({ x: Math.round(bounds.x), y: Math.round(bounds.y), width: Math.max(0, Math.round(bounds.width)), height: Math.max(0, Math.round(bounds.height)) })
  }

  hide(): void {
    this.view?.setVisible(false)
  }

  reload(): void {
    this.view?.webContents.reload()
  }

  /** 换服务器或退出登录时扔掉，下次打开按新的登录重建。 */
  destroy(win?: BrowserWindow): void {
    if (!this.view) return
    if (win && !win.isDestroyed()) win.contentView.removeChildView(this.view)
    this.view.webContents.close()
    this.view = undefined
    this.server = ''
  }

  private async create(win: BrowserWindow, api: DianaApi): Promise<void> {
    this.destroy(win)
    const adminSession = session.fromPartition('persist:diana-admin')
    adminSession.setCertificateVerifyProc((request, callback) => {
      if (request.verificationResult === 'net::OK') return callback(-3)
      callback(this.pins()[request.hostname] === request.certificate.fingerprint ? 0 : -2)
    })
    for (const pair of api.cookie.split(';')) {
      const index = pair.indexOf('=')
      if (index <= 0) continue
      await adminSession.cookies.set({ url: api.server, name: pair.slice(0, index).trim(), value: pair.slice(index + 1).trim(), httpOnly: true, sameSite: 'lax' })
    }
    const view = new WebContentsView({ webPreferences: { session: adminSession, contextIsolation: true, sandbox: true, nodeIntegration: false } })
    view.setBorderRadius(radius)
    view.setBackgroundColor(nativeTheme.shouldUseDarkColors ? '#16131a' : '#f7f4f5')
    view.setVisible(false)
    const server = api.server
    view.webContents.setWindowOpenHandler(({ url }) => {
      if (/^https?:\/\//.test(url)) void shell.openExternal(url)
      return { action: 'deny' }
    })
    view.webContents.on('will-navigate', (event, url) => {
      if (sameOrigin(server, url)) return
      event.preventDefault()
      if (/^https?:\/\//.test(url)) void shell.openExternal(url)
    })
    win.contentView.addChildView(view)
    this.view = view
    this.server = server
    void view.webContents.loadURL(server)
  }
}
