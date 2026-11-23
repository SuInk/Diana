// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 桌面端主进程：一个窗口、一套和服务端说话的 IPC。界面跑在沙箱里，不碰 Node，
// 网络请求、Cookie 和证书都在这里管。

import { app, BrowserWindow, ipcMain, protocol, session, shell, type Session } from 'electron'
import { join } from 'node:path'
import { writeFileSync } from 'node:fs'
import { ApiError, DianaApi, rejectedCertificates } from './api.ts'
import { loadCookie, loadState, saveCookie, saveState, type StoredState } from './store.ts'
import { normalizeServer, sameOrigin } from '../shared/server.ts'
import type { ApiResult, ChatFrame, LoginRequest, SessionState } from '../shared/types.ts'

// 界面里的头像和图片走这个协议，由主进程带上登录 Cookie 去取。必须在 ready 前注册。
const mediaScheme = 'diana-media'
protocol.registerSchemesAsPrivileged([{ scheme: mediaScheme, privileges: { standard: true, secure: true, supportFetchAPI: true } }])

// 冒烟测试和多开调试用独立的数据目录，不碰平时的登录状态。
if (process.env.DIANA_DESKTOP_USER_DATA) app.setPath('userData', process.env.DIANA_DESKTOP_USER_DATA)

let state: StoredState
let api: DianaApi | undefined
let window: BrowserWindow | undefined
let appSession: Session

function sessionState(): SessionState {
  return { server: state.server, username: state.username, loggedIn: Boolean(api?.cookie) }
}

async function wrap<T>(run: () => Promise<T>): Promise<ApiResult<T>> {
  try {
    return { ok: true, value: await run() }
  } catch (error) {
    if (error instanceof ApiError) {
      if (error.unauthorized && api) {
        api.cookie = ''
        void saveCookie('')
      }
      return { ok: false, error: error.message, unauthorized: error.unauthorized, certificate: error.certificate }
    }
    return { ok: false, error: (error as Error).message }
  }
}

function requireApi(): DianaApi {
  if (!api?.cookie) throw new ApiError('还没登录', true)
  return api
}

function registerIpc(): void {
  ipcMain.handle('diana:state', () => sessionState())
  ipcMain.handle('diana:login', (_event, request: LoginRequest) =>
    wrap(async () => {
      const server = normalizeServer(request.server)
      const host = new URL(server).hostname
      if (request.trustFingerprint) state.pins[host] = request.trustFingerprint
      const next = new DianaApi(appSession, server)
      await next.login(request.username.trim(), request.password)
      api = next
      state.server = server
      state.username = request.username.trim()
      saveState(state)
      await saveCookie(next.cookie)
      return sessionState()
    }),
  )
  ipcMain.handle('diana:logout', async () => {
    if (api) api.cookie = ''
    await saveCookie('')
  })
  ipcMain.handle('diana:bots', () => wrap(() => requireApi().bots()))
  ipcMain.handle('diana:history', (_event, profile: string) => wrap(() => requireApi().history(profile)))
  ipcMain.handle('diana:send', (event, requestId: string, profile: string, text: string) =>
    wrap(async () => {
      const emit = (frame: ChatFrame) => {
        if (!event.sender.isDestroyed()) event.sender.send('diana:frame', frame)
      }
      try {
        await requireApi().send(profile, text, (message) => emit({ requestId, type: 'message', message }))
        emit({ requestId, type: 'done' })
      } catch (error) {
        emit({ requestId, type: 'done', error: (error as Error).message })
        throw error
      }
      return null
    }),
  )
}

function registerMediaProtocol(): void {
  appSession.protocol.handle(mediaScheme, async (request) => {
    const target = new URL(request.url).searchParams.get('u') ?? ''
    if (!api?.cookie || !sameOrigin(api.server, target)) return new Response('forbidden', { status: 403 })
    try {
      const media = await api.media(target)
      return new Response(new Uint8Array(media.body), { status: media.status, headers: { 'Content-Type': media.type, 'Cache-Control': 'private, max-age=3600' } })
    } catch {
      return new Response('unavailable', { status: 502 })
    }
  })
}

function trustPinnedCertificates(): void {
  appSession.setCertificateVerifyProc((request, callback) => {
    if (request.verificationResult === 'net::OK') return callback(-3)
    const pinned = state.pins[request.hostname]
    if (pinned && pinned === request.certificate.fingerprint) return callback(0)
    rejectedCertificates.set(request.hostname, request.certificate.fingerprint)
    callback(-2)
  })
}

function createWindow(): void {
  const bounds = state.window ?? { width: 1080, height: 720 }
  window = new BrowserWindow({
    ...bounds,
    minWidth: 760,
    minHeight: 500,
    title: 'Diana',
    show: false,
    backgroundColor: '#f7f4f5',
    titleBarStyle: process.platform === 'darwin' ? 'hiddenInset' : 'default',
    webPreferences: {
      preload: join(import.meta.dirname, '../preload/index.cjs'),
      session: appSession,
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
    },
  })
  if (state.window?.maximized) window.maximize()
  window.once('ready-to-show', () => window?.show())
  // 消息里的链接用系统浏览器开，窗口自己永远只显示界面。
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//.test(url)) void shell.openExternal(url)
    return { action: 'deny' }
  })
  window.webContents.on('will-navigate', (event, url) => {
    if (url !== window?.webContents.getURL()) event.preventDefault()
  })
  window.on('close', () => {
    if (!window) return
    const maximized = window.isMaximized()
    state.window = { ...window.getNormalBounds(), maximized }
    saveState(state)
  })
  if (process.env.ELECTRON_RENDERER_URL) void window.loadURL(process.env.ELECTRON_RENDERER_URL)
  else void window.loadFile(join(import.meta.dirname, '../renderer/index.html'))
  if (process.env.DIANA_DESKTOP_SMOKE) runSmoke(window, process.env.DIANA_DESKTOP_SMOKE)
}

// 冒烟测试：没登录就用环境变量里的账号登录，然后发一句话，等回复出来截图退出。
// 只在设了 DIANA_DESKTOP_SMOKE（截图路径）时跑，对着 scripts/mock-server.mjs 用。
function runSmoke(win: BrowserWindow, output: string): void {
  const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
  const fill = (selector: string, value: string) => `(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return false;
    el.value = ${JSON.stringify(value)};
    el.dispatchEvent(new Event('input', { bubbles: true }));
    return true;
  })()`
  win.webContents.once('did-finish-load', async () => {
    const run = (code: string) => win.webContents.executeJavaScript(code)
    await wait(1200)
    if (await run(fill('#login-server', process.env.DIANA_DESKTOP_SMOKE_SERVER ?? 'http://127.0.0.1:18333'))) {
      await run(fill('#login-username', process.env.DIANA_DESKTOP_SMOKE_USER ?? 'admin'))
      await run(fill('#login-password', process.env.DIANA_DESKTOP_SMOKE_PASSWORD ?? 'demo'))
      await run(`document.querySelector('form.login-form').requestSubmit()`)
      await wait(1500)
    }
    await run(fill('textarea.composer-input', '在吗？帮我看看今天的安排'))
    await run(`document.querySelector('textarea.composer-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))`)
    await wait(Number(process.env.DIANA_DESKTOP_SMOKE_WAIT ?? 3000))
    writeFileSync(output, (await win.webContents.capturePage()).toPNG())
    app.quit()
  })
}

app.whenReady().then(async () => {
  state = loadState()
  appSession = session.fromPartition('persist:diana')
  const cookie = await loadCookie()
  if (state.server && cookie) api = new DianaApi(appSession, state.server, cookie)
  trustPinnedCertificates()
  registerMediaProtocol()
  registerIpc()
  createWindow()
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow()
  })
})

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit()
})
