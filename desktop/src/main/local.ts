// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 本机 Diana：桌面端默认自己带一个服务端。安装包里放着内嵌了 WebUI 的 diana-webui，
// 打开 App 时在用户数据目录里拉起来，只监听 127.0.0.1，退出 App 时一起停。管理员账号
// 首次启动时随机生成，存在数据目录里，桌面端拿它自动登录，用户不用知道。

import { app } from 'electron'
import { spawn, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { join } from 'node:path'

const preferredPort = 18080
const readyTimeoutMs = 90_000
const stopTimeoutMs = 10_000

interface LocalSettings {
  port: number
  username: string
  password: string
}

export interface LocalCredentials {
  server: string
  username: string
  password: string
}

function serverDir(): string {
  const dir = join(app.getPath('userData'), 'server')
  mkdirSync(join(dir, 'data', 'logs'), { recursive: true })
  return dir
}

/** 安装包里的程序放在 resources/bin；开发时用 `npm run server` 编到 desktop/resources/bin/<系统>-<架构>。 */
export function serverBinary(): string {
  if (process.env.DIANA_DESKTOP_SERVER_BIN) return process.env.DIANA_DESKTOP_SERVER_BIN
  const name = process.platform === 'win32' ? 'diana-webui.exe' : 'diana-webui'
  if (app.isPackaged) return join(process.resourcesPath, 'bin', name)
  return join(app.getAppPath(), 'resources', 'bin', `${builderOS()}-${process.arch}`, name)
}

/** 和 electron-builder 的 ${os} 一致，打包配置按它挑对应平台的程序。 */
function builderOS(): string {
  if (process.platform === 'darwin') return 'mac'
  if (process.platform === 'win32') return 'win'
  return 'linux'
}

function portFree(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const probe = createServer()
    probe.once('error', () => resolve(false))
    probe.listen(port, '127.0.0.1', () => probe.close(() => resolve(true)))
  })
}

function anyFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address()
      probe.close(() => (typeof address === 'object' && address ? resolve(address.port) : reject(new Error('没拿到空闲端口'))))
    })
  })
}

// 端口第一次选定后就固定下来：QQ 这类反向连接要填 Diana 的地址，每次变就得重配。
// 只有被别的程序占了才换一个，并记下新端口。
async function choosePort(saved?: number): Promise<number> {
  if (saved && (await portFree(saved))) return saved
  if (!saved && (await portFree(preferredPort))) return preferredPort
  return anyFreePort()
}

function loadSettings(dir: string): Partial<LocalSettings> {
  try {
    return JSON.parse(readFileSync(join(dir, 'desktop.json'), 'utf8'))
  } catch {
    return {}
  }
}

// config.yaml 每次启动按 desktop.json 重写。字符串用 JSON 写法，本身就是合法的
// YAML 双引号标量，Windows 路径里的反斜杠也不用另外转义。
function writeConfig(dir: string, settings: LocalSettings): string {
  const path = join(dir, 'config.yaml')
  const data = join(dir, 'data')
  const q = JSON.stringify
  const yaml = [
    '# 由 Diana 桌面端生成，每次启动都会重写；要改设置请在管理后台里改。',
    'server:',
    '  host: "127.0.0.1"',
    `  port: ${q(String(settings.port))}`,
    'storage:',
    `  db_path: ${q(join(data, 'diana.db'))}`,
    `  log_path: ${q(join(data, 'logs', 'diana.log'))}`,
    'admin:',
    `  username: ${q(settings.username)}`,
    `  password: ${q(settings.password)}`,
    // 程序跟着桌面端安装包一起升级，不让它自己下载替换。
    'update:',
    '  apply_enabled: false',
    '  release_enabled: false',
    '',
  ].join('\n')
  writeFileSync(path, yaml, { mode: 0o600 })
  return path
}

export class LocalServer {
  private child?: ChildProcess
  private starting?: Promise<LocalCredentials>
  private credentials?: LocalCredentials

  /** 已经在跑就直接返回；没跑就拉起来，等到能登录为止。 */
  start(): Promise<LocalCredentials> {
    if (this.child && this.credentials) return Promise.resolve(this.credentials)
    this.starting ??= this.launch().finally(() => (this.starting = undefined))
    return this.starting
  }

  private async launch(): Promise<LocalCredentials> {
    const binary = serverBinary()
    if (!existsSync(binary)) {
      throw new Error(app.isPackaged ? `安装包里缺少 Diana 程序（${binary}），重新安装试试` : `还没编译本机 Diana：先在 desktop 目录跑 npm run server（找的是 ${binary}）`)
    }
    const dir = serverDir()
    const saved = loadSettings(dir)
    const settings: LocalSettings = {
      port: await choosePort(saved.port),
      username: saved.username || 'desktop',
      password: saved.password || randomBytes(24).toString('base64url'),
    }
    writeFileSync(join(dir, 'desktop.json'), JSON.stringify(settings, null, 2), { mode: 0o600 })
    const config = writeConfig(dir, settings)
    const server = `http://127.0.0.1:${settings.port}`

    // 启动阶段的输出（配置错误、端口冲突这类）写到 launch.log，起不来时把末尾报给界面。
    const logPath = join(dir, 'data', 'logs', 'launch.log')
    const log = openSync(logPath, 'w')
    const child = spawn(binary, ['--config', config], { cwd: dir, stdio: ['ignore', log, log], windowsHide: true })
    closeSync(log)
    this.child = child
    let exited: string | undefined
    child.once('exit', (code, signal) => {
      exited = signal ? `被信号 ${signal} 结束` : `退出码 ${code}`
      if (this.child === child) {
        this.child = undefined
        this.credentials = undefined
      }
    })
    child.once('error', (error) => (exited = error.message))

    const deadline = Date.now() + readyTimeoutMs
    while (Date.now() < deadline) {
      if (exited) throw new Error(`本机 Diana 没起来（${exited}）：${tail(logPath)}`)
      if (await reachable(server)) {
        this.credentials = { server, username: settings.username, password: settings.password }
        return this.credentials
      }
      await new Promise((resolve) => setTimeout(resolve, 300))
    }
    child.kill()
    throw new Error(`本机 Diana 一分半钟还没启动好，日志在 ${logPath}`)
  }

  /** 退出 App 或切到别的服务器时停掉；先让它自己收尾，超时再强杀。 */
  async stop(): Promise<void> {
    const child = this.child
    if (!child || child.exitCode !== null) return
    this.child = undefined
    this.credentials = undefined
    const done = new Promise<void>((resolve) => child.once('exit', () => resolve()))
    child.kill('SIGTERM')
    const timer = setTimeout(() => child.kill('SIGKILL'), stopTimeoutMs)
    await done
    clearTimeout(timer)
  }

  get running(): boolean {
    return Boolean(this.child && this.credentials)
  }
}

async function reachable(server: string): Promise<boolean> {
  try {
    const response = await fetch(`${server}/api/auth/status`, { signal: AbortSignal.timeout(2000) })
    return response.ok
  } catch {
    return false
  }
}

function tail(path: string): string {
  try {
    const lines = readFileSync(path, 'utf8').trim().split('\n')
    return lines.slice(-5).join('\n') || '没有输出'
  } catch {
    return '没有输出'
  }
}
