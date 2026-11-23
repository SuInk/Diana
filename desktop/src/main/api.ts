// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Diana 服务端的 HTTP 客户端，跑在主进程。鉴权和 WebUI 一样靠登录 Cookie：登录时
// 从 Set-Cookie 取出来，之后手动带在每个请求上。请求都从主进程发，界面不跨域，
// 服务端不用为桌面端开 CORS。

import { net, type ClientRequest, type IncomingMessage, type Session } from 'electron'
import { absoluteURL } from '../shared/server.ts'
import { LineSplitter } from '../shared/ndjson.ts'
import type { Bot, ChatHistory, ChatMessage, UntrustedCertificate } from '../shared/types.ts'

const connectTimeoutMs = 10_000
// 服务端每 15 秒发一次心跳，一分钟没动静就当连接断了。
const streamIdleTimeoutMs = 60_000

export class ApiError extends Error {
  constructor(
    message: string,
    readonly unauthorized = false,
    readonly certificate?: UntrustedCertificate,
  ) {
    super(message)
  }
}

/** 证书校验失败时记下指纹，请求报错后拿来问用户。由 index.ts 的 setCertificateVerifyProc 填。 */
export const rejectedCertificates = new Map<string, string>()

interface Response {
  status: number
  headers: Record<string, string | string[]>
  message: IncomingMessage
  request: ClientRequest
}

export class DianaApi {
  constructor(
    private readonly session: Session,
    readonly server: string,
    public cookie = '',
  ) {}

  async login(username: string, password: string): Promise<void> {
    const response = await this.open('POST', '/api/auth/login', { body: { username, password }, auth: false })
    await this.check(response)
    await readAll(response.message)
    const raw = response.headers['set-cookie']
    const list = Array.isArray(raw) ? raw : raw ? [raw] : []
    const cookies = list
      .map((line) => line.split(';', 1)[0].trim())
      .filter((pair) => pair.includes('=') && !pair.endsWith('='))
    if (!cookies.length) throw new ApiError('登录成功了但没拿到会话 Cookie，确认服务器是 Diana WebUI')
    this.cookie = cookies.join('; ')
  }

  async bots(): Promise<Bot[]> {
    const json = await this.json('GET', '/api/assistant/config')
    const profiles = Array.isArray(json.profiles) && json.profiles.length ? json.profiles : [json]
    return profiles
      .map((item: Record<string, unknown>) => {
        const id = String(item.id ?? '')
        const name = String(item.name ?? '')
        const avatar = String(item.avatar_url ?? '')
        return { id, name: name || id, platform: String(item.platform ?? ''), avatar: avatar ? absoluteURL(this.server, avatar) : undefined }
      })
      .filter((bot: Bot) => bot.id)
  }

  async history(profile: string): Promise<ChatHistory> {
    const json = await this.json('GET', '/api/assistant/chat', { profile })
    const info = json.info ?? {}
    if (info.owner_avatar_url) info.owner_avatar_url = absoluteURL(this.server, info.owner_avatar_url)
    const messages: ChatMessage[] = Array.isArray(json.messages) ? json.messages.map((m: ChatMessage) => this.resolveImages(m)) : []
    return { info, messages }
  }

  /** 发一句话，回复一条条交给 onMessage。连接中途断了或服务端报错都抛 ApiError。 */
  async send(profile: string, text: string, onMessage: (message: ChatMessage) => void): Promise<void> {
    const response = await this.open('POST', '/api/assistant/chat', { body: { profile, text } })
    await this.check(response)
    let done = false
    let failure = ''
    const handle = (line: string) => {
      const frame = JSON.parse(line)
      if (frame.type === 'message' && frame.message) onMessage(this.resolveImages(frame.message))
      if (frame.type === 'done') {
        done = true
        if (typeof frame.error === 'string') failure = frame.error
      }
    }
    const splitter = new LineSplitter()
    await new Promise<void>((resolve, reject) => {
      let idle: NodeJS.Timeout
      const arm = () => {
        clearTimeout(idle)
        idle = setTimeout(() => {
          response.request.abort()
          reject(new ApiError('一分钟没收到服务器的消息，连接可能断了'))
        }, streamIdleTimeoutMs)
      }
      arm()
      response.message.on('data', (chunk: Buffer) => {
        arm()
        try {
          splitter.push(chunk).forEach(handle)
        } catch (error) {
          clearTimeout(idle)
          reject(new ApiError(`服务器返回了看不懂的内容：${(error as Error).message}`))
        }
      })
      response.message.on('end', () => {
        clearTimeout(idle)
        try {
          splitter.flush().forEach(handle)
          resolve()
        } catch (error) {
          reject(new ApiError(`服务器返回了看不懂的内容：${(error as Error).message}`))
        }
      })
      response.message.on('error', (error: Error) => {
        clearTimeout(idle)
        reject(new ApiError(`连接中断了：${error.message}`))
      })
    })
    if (!done) throw new ApiError('连接中断了，刷新看看她回了没有')
    if (failure) throw new ApiError(failure)
  }

  /** 取服务器上的图片，给界面的 diana-media:// 协议用。 */
  async media(url: string): Promise<{ status: number; type: string; body: Buffer }> {
    const response = await this.open('GET', url)
    const body = await readAllBuffer(response.message)
    const type = response.headers['content-type']
    return { status: response.status, type: String(Array.isArray(type) ? type[0] : type ?? 'application/octet-stream'), body }
  }

  private resolveImages(message: ChatMessage): ChatMessage {
    if (message.images?.length) message.images = message.images.map((url) => absoluteURL(this.server, url))
    return message
  }

  private async json(method: string, path: string, query?: Record<string, string>) {
    const response = await this.open(method, path, { query })
    await this.check(response)
    const text = await readAll(response.message)
    try {
      const value = JSON.parse(text)
      return value && typeof value === 'object' ? value : {}
    } catch {
      throw new ApiError('服务器返回的不是 JSON，确认地址指向 Diana WebUI')
    }
  }

  private async check(response: Response): Promise<void> {
    if (response.status >= 200 && response.status < 300) return
    const text = await readAll(response.message).catch(() => '')
    let message = ''
    try {
      const value = JSON.parse(text)
      if (typeof value?.error === 'string') message = value.error
    } catch {
      // 不是 JSON 就用状态码。
    }
    throw new ApiError(message || `请求失败：HTTP ${response.status}`, response.status === 401)
  }

  private open(method: string, pathOrURL: string, options: { query?: Record<string, string>; body?: unknown; auth?: boolean } = {}): Promise<Response> {
    const url = new URL(pathOrURL, `${this.server}/`)
    for (const [key, value] of Object.entries(options.query ?? {})) url.searchParams.set(key, value)
    const host = url.host
    return new Promise((resolve, reject) => {
      const request = net.request({ method, url: url.toString(), session: this.session, useSessionCookies: false, redirect: 'follow' })
      request.setHeader('Accept', 'application/json, application/x-ndjson')
      if (options.auth !== false && this.cookie) request.setHeader('Cookie', this.cookie)
      const timer = setTimeout(() => {
        request.abort()
        reject(new ApiError(`连接 ${host} 超时`))
      }, connectTimeoutMs)
      request.on('response', (message) => {
        clearTimeout(timer)
        resolve({ status: message.statusCode, headers: message.headers, message, request })
      })
      request.on('error', (error) => {
        clearTimeout(timer)
        reject(describeNetworkError(error, url))
      })
      if (options.body !== undefined) {
        request.setHeader('Content-Type', 'application/json')
        request.write(JSON.stringify(options.body))
      }
      request.end()
    })
  }
}

function describeNetworkError(error: Error, url: URL): ApiError {
  const code = error.message
  if (code.includes('ERR_CERT')) {
    const fingerprint = rejectedCertificates.get(url.hostname)
    if (fingerprint) return new ApiError(`${url.hostname} 的证书不受系统信任`, false, { host: url.hostname, fingerprint })
    return new ApiError(`和 ${url.hostname} 的 HTTPS 握手失败（${code}）`)
  }
  if (code.includes('ERR_CONNECTION_REFUSED')) return new ApiError(`连不上 ${url.host}：端口没在监听，确认 Diana 在跑、地址和端口没填错`)
  if (code.includes('ERR_NAME_NOT_RESOLVED')) return new ApiError(`找不到 ${url.hostname} 这台主机`)
  if (code.includes('ERR_INTERNET_DISCONNECTED') || code.includes('ERR_ADDRESS_UNREACHABLE')) return new ApiError(`连不上 ${url.host}：网络不通`)
  return new ApiError(`连不上 ${url.host}（${code}）`)
}

function readAllBuffer(message: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    message.on('data', (chunk: Buffer) => chunks.push(chunk))
    message.on('end', () => resolve(Buffer.concat(chunks)))
    message.on('error', reject)
  })
}

async function readAll(message: IncomingMessage): Promise<string> {
  return (await readAllBuffer(message)).toString('utf8')
}
