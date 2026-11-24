// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 桌面端主进程：一个窗口、一套和服务端说话的 IPC。界面跑在沙箱里，不碰 Node，
// 网络请求、Cookie 和证书都在这里管。默认连自己拉起的本机 Diana（local.ts），
// 也可以改连别处部署的 Diana。

import { app, BrowserWindow, ipcMain, protocol, session, shell, type Session } from 'electron'
import { AdminView } from './admin.ts'
import { followTheme, installMenu, material, windowChrome } from './window.ts'
import { join } from 'node:path'
import { writeFileSync } from 'node:fs'
import { ApiError, DianaApi, rejectedCertificates } from './api.ts'
import { LocalServer } from './local.ts'
import { loadCookie, loadState, saveCookie, saveState, type StoredState } from './store.ts'
import { normalizeServer, sameOrigin } from '../shared/server.ts'
import type { ApiResult, AppInfo, ChatFrame, LoginRequest, Rect, SessionState } from '../shared/types.ts'

// 界面里的头像和图片走这个协议，由主进程带上登录 Cookie 去取。必须在 ready 前注册。
const mediaScheme = 'diana-media'
protocol.registerSchemesAsPrivileged([{ scheme: mediaScheme, privileges: { standard: true, secure: true, supportFetchAPI: true } }])

// 冒烟测试和多开调试用独立的数据目录，不碰平时的登录状态。
if (process.env.DIANA_DESKTOP_USER_DATA) app.setPath('userData', process.env.DIANA_DESKTOP_USER_DATA)

// 同一份数据只开一个：第二个实例会和第一个抢本机 Diana 的数据目录，直接把第一个窗口叫到前面。
if (!app.requestSingleInstanceLock()) app.exit(0)
app.on('second-instance', () => {
  if (!window || window.isDestroyed()) return
  if (window.isMinimized()) window.restore()
  window.focus()
})

let state: StoredState
let api: DianaApi | undefined
let window: BrowserWindow | undefined
let appSession: Session
const local = new LocalServer()
const admin = new AdminView(() => state.pins)

function sessionState(): SessionState {
  if (state.mode === 'local') return { mode: 'local', server: api?.server ?? '', username: '', loggedIn: Boolean(api?.cookie) }
  return { mode: 'remote', server: state.server, username: state.username, loggedIn: Boolean(api?.cookie) }
}

// 本机 Diana 每次都现登：密码在本机数据目录里，Cookie 不用存。
async function startLocal(): Promise<SessionState> {
  const credentials = await local.start()
  const next = new DianaApi(appSession, credentials.server)
  await next.login(credentials.username, credentials.password)
  if (state.mode !== 'local') {
    state.mode = 'local'
    saveState(state)
    await saveCookie('')
  }
  if (api?.server !== next.server) admin.destroy(window)
  api = next
  return sessionState()
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
      admin.destroy(window)
      api = next
      // 改连别处就不再需要本机 Diana，停掉，数据留着，切回来接着用。
      if (state.mode === 'local') await local.stop()
      state.mode = 'remote'
      state.server = server
      state.username = request.username.trim()
      saveState(state)
      await saveCookie(next.cookie)
      return sessionState()
    }),
  )
  ipcMain.handle('diana:start-local', () => wrap(startLocal))
  ipcMain.handle('diana:admin-show', (_event, bounds: Rect) =>
    wrap(async () => {
      if (window) await admin.show(window, requireApi(), bounds)
      return null
    }),
  )
  ipcMain.on('diana:admin-place', (_event, bounds: Rect) => admin.place(bounds))
  ipcMain.on('diana:admin-hide', () => admin.hide())
  ipcMain.on('diana:admin-reload', () => admin.reload())
  ipcMain.handle('diana:info', (): AppInfo => ({ version: app.getVersion(), local: local.info }))
  ipcMain.on('diana:reveal-data', () => {
    const dir = local.info?.dataDir
    if (dir) void shell.openPath(dir)
  })
  ipcMain.on('diana:appearance', (event) => {
    event.returnValue = { platform: { darwin: 'mac', win32: 'win' }[process.platform as string] ?? 'linux', material: material() }
  })
  ipcMain.handle('diana:logout', async () => {
    admin.destroy(window)
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
  const bounds = state.window ?? { width: 1200, height: 780 }
  window = new BrowserWindow({
    ...bounds,
    minWidth: 860,
    minHeight: 540,
    title: 'Diana',
    show: false,
    ...windowChrome(),
    webPreferences: {
      preload: join(import.meta.dirname, '../preload/index.cjs'),
      session: appSession,
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
    },
  })
  if (state.window?.maximized) window.maximize()
  followTheme(window)
  window.once('ready-to-show', () => window?.show())
  // 消息里的链接用系统浏览器开，窗口自己永远只显示界面。
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//.test(url)) void shell.openExternal(url)
    return { action: 'deny' }
  })
  window.webContents.on('will-navigate', (event, url) => {
    if (url !== window?.webContents.getURL()) event.preventDefault()
  })
  // 管理后台那层挂在这个窗口上，窗口没了它也跟着扔掉，下次开窗重建。
  window.on('closed', () => admin.destroy())
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

// 冒烟测试：等界面进到聊天（本机模式要等本机 Diana 起来），发一句话，等回复出来截图退出。
// 只在设了 DIANA_DESKTOP_SMOKE（截图路径）时跑。设了 DIANA_DESKTOP_SMOKE_SERVER 就改连那台
// 服务器（通常是 scripts/mock-server.mjs），用 _USER、_PASSWORD 登录。
function runSmoke(win: BrowserWindow, output: string): void {
  const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
  const run = (code: string) => win.webContents.executeJavaScript(code)
  const waitFor = async (selector: string, timeoutMs: number) => {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if (await run(`Boolean(document.querySelector(${JSON.stringify(selector)}))`)) return
      await wait(300)
    }
    throw new Error(`等不到 ${selector}`)
  }
  const fill = (selector: string, value: string) =>
    run(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    el.value = ${JSON.stringify(value)};
    el.dispatchEvent(new Event('input', { bubbles: true }));
  })()`)
  win.webContents.once('did-finish-load', async () => {
    try {
      const remote = process.env.DIANA_DESKTOP_SMOKE_SERVER
      if (remote && !sessionState().loggedIn) {
        await waitFor('#use-remote, #login-server', 90_000)
        await run(`document.querySelector('#use-remote')?.click()`)
        await waitFor('#login-server', 5_000)
        await fill('#login-server', remote)
        await fill('#login-username', process.env.DIANA_DESKTOP_SMOKE_USER ?? 'admin')
        await fill('#login-password', process.env.DIANA_DESKTOP_SMOKE_PASSWORD ?? 'demo')
        await run(`document.querySelector('form.login-form').requestSubmit()`)
      }
      await waitFor('textarea.composer-input', 90_000)
      await wait(800)
      if (process.env.DIANA_DESKTOP_SMOKE_TEXT !== '' && (await run(`!document.querySelector('textarea.composer-input').disabled`))) {
        await fill('textarea.composer-input', process.env.DIANA_DESKTOP_SMOKE_TEXT ?? '在吗？帮我看看今天的安排')
        await run(`document.querySelector('textarea.composer-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))`)
      }
      await wait(Number(process.env.DIANA_DESKTOP_SMOKE_WAIT ?? 3000))
    } catch (error) {
      console.error(`冒烟测试：${(error as Error).message}`)
    }
    writeFileSync(output, (await win.webContents.capturePage()).toPNG())
    app.quit()
  })
}

app.whenReady().then(async () => {
  state = loadState()
  appSession = session.fromPartition('persist:diana')
  const cookie = await loadCookie()
  if (state.mode === 'remote' && state.server && cookie) api = new DianaApi(appSession, state.server, cookie)
  trustPinnedCertificates()
  registerMediaProtocol()
  registerIpc()
  installMenu((command) => window?.webContents.send('diana:command', command))
  createWindow()
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow()
  })
})

// 本机 Diana 跟着 App 走：退出前先等它收尾（落库、断开连接），再真正退出。
let quitting = false
app.on('before-quit', (event) => {
  if (quitting || !local.running) return
  event.preventDefault()
  quitting = true
  void local.stop().finally(() => app.quit())
})

// 终端里 Ctrl+C、系统发 SIGTERM 时也走正常退出，让本机 Diana 跟着停。
for (const signal of ['SIGINT', 'SIGTERM'] as const) process.on(signal, () => app.quit())

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit()
})
