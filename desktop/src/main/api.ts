// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Diana 服务端的 HTTP 客户端，跑在主进程。鉴权和 WebUI 一样靠登录 Cookie：登录时
// 从 Set-Cookie 取出来，之后手动带在每个请求上。请求都从主进程发，界面不跨域，
// 服务端不用为桌面端开 CORS。

import { net, type ClientRequest, type IncomingMessage, type Session } from 'electron'
import { absoluteURL } from '../shared/server.ts'
import { LineSplitter } from '../shared/ndjson.ts'
import type { Bot, ChatHistory, ChatMessage, ModelOption, TaskFrame, TaskSession, TaskSummary, UntrustedCertificate } from '../shared/types.ts'

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
    let done = false
    let failure = ''
    await this.stream('/api/assistant/chat', { profile, text }, (frame) => {
      if (frame.type === 'message' && frame.message) onMessage(this.resolveImages(frame.message as ChatMessage))
      if (frame.type === 'done') {
        done = true
        if (typeof frame.error === 'string') failure = frame.error
      }
    })
    if (!done) throw new ApiError('连接中断了，刷新看看她回了没有')
    if (failure) throw new ApiError(failure)
  }

  // ---------- 任务（服务端的管理对话：带全套工具的 Agent，会话落库） ----------

  async taskSessions(profile: string): Promise<TaskSummary[]> {
    const json = await this.json('GET', '/api/assistant/admin-chat/sessions', { profile })
    return Array.isArray(json.sessions) ? json.sessions : []
  }

  /** 读一条会话；不给 id 时服务端接着这个机器人最近的那条，没有就新建。 */
  async taskSession(profile: string, id = ''): Promise<TaskSession> {
    const query: Record<string, string> = { profile }
    if (id) query.session_id = id
    const json = await this.json('GET', '/api/assistant/admin-chat', query)
    return { ...json, messages: Array.isArray(json.messages) ? json.messages : [] }
  }

  async createTask(profile: string): Promise<string> {
    const json = await this.json('POST', '/api/assistant/admin-chat/sessions', undefined, { profile })
    return String(json.session_id ?? '')
  }

  async deleteTask(id: string): Promise<void> {
    await this.json('DELETE', `/api/assistant/admin-chat/sessions/${encodeURIComponent(id)}`)
  }

  async stopTask(id: string): Promise<void> {
    await this.json('POST', `/api/assistant/admin-chat/${encodeURIComponent(id)}/stop`)
  }

  /** 发给任务会话，执行步骤和回复按帧交给 onFrame，直到 done。 */
  async runTask(id: string, message: string, model: ModelOption | undefined, onFrame: (frame: TaskFrame) => void): Promise<void> {
    let done = false
    await this.stream('/api/assistant/admin-chat', { session_id: id, message, provider_id: model?.provider || undefined, model: model?.model || undefined }, (frame) => {
      if (frame.type === 'done') done = true
      if (frame.type === 'user' || frame.type === 'message' || frame.type === 'progress') onFrame(frame as TaskFrame)
    })
    if (!done) throw new ApiError('连接中断了，刷新看看任务做到哪了')
  }

  /** 能出文字的模型，给任务选模型用；只列出提供商里登记过的。 */
  async models(): Promise<ModelOption[]> {
    const json = await this.json('GET', '/api/llm/config')
    const out: ModelOption[] = []
    for (const channel of Array.isArray(json.profiles) ? json.profiles : []) {
      if (!channel?.id || channel.group === 'image') continue
      const seen = new Set<string>()
      for (const model of Array.isArray(channel.models) ? channel.models : []) {
        const modalities: string[] = model.output_modalities ?? []
        if (!model.id || (modalities.length && !modalities.includes('text')) || seen.has(model.id)) continue
        seen.add(model.id)
        out.push({ provider: channel.id, model: model.id, label: model.name && model.name !== model.id ? model.name : model.id, hint: channel.name || channel.provider || channel.id })
      }
      if (channel.model && !seen.has(channel.model)) out.push({ provider: channel.id, model: channel.model, label: channel.model, hint: channel.name || channel.provider || channel.id })
    }
    return out
  }

  /** NDJSON 流：每行一帧交给 onFrame，心跳续命，一分钟没动静当断线。 */
  private async stream(path: string, body: unknown, onFrame: (frame: { type: string; [key: string]: unknown }) => void): Promise<void> {
    const response = await this.open('POST', path, { body, timeoutMs: 30_000 })
    await this.check(response)
    const handle = (line: string) => onFrame(JSON.parse(line))
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

  private async json(method: string, path: string, query?: Record<string, string>, body?: unknown) {
    const response = await this.open(method, path, { query, body })
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

  private open(method: string, pathOrURL: string, options: { query?: Record<string, string>; body?: unknown; auth?: boolean; timeoutMs?: number } = {}): Promise<Response> {
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
      }, options.timeoutMs ?? connectTimeoutMs)
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
