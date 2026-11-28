// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 本地开发用的假 Diana：只实现桌面端用到的登录、机器人列表和聊天接口，回复是写死的。
// npm run mock 起在 127.0.0.1:18333，账号 admin / demo。
import { createServer } from 'node:http'

const port = Number(process.env.PORT ?? 18333)
const token = 'mock-session'
const now = () => Math.floor(Date.now() / 1000)
const avatar = (color, letter) =>
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="${color}"/><text x="32" y="42" font-size="30" text-anchor="middle" fill="#fff" font-family="sans-serif">${letter}</text></svg>`

const bots = [
  { id: 'diana', name: '嘉然', platform: 'qq', avatar_url: '/mock/avatar/diana.svg' },
  { id: 'miyu', name: '美海', platform: 'telegram', avatar_url: '/mock/avatar/miyu.svg' },
]
const history = {
  diana: [
    { id: 'h1', role: 'user', text: '早呀', at: now() - 3 * 3600 },
    { id: 'h2', role: 'assistant', text: '早～今天起得挺早嘛', at: now() - 3 * 3600 + 20 },
    { id: 'h3', role: 'user', kind: 'call', text: '[语音通话] 通话 3 分 12 秒', at: now() - 2 * 3600 },
    { id: 'h4', role: 'assistant', text: '刚才说的那篇文章在这里：https://example.com/article ，有空看看', at: now() - 600 },
  ],
  miyu: [],
}

function json(res, status, value) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(value))
}

function body(req) {
  return new Promise((resolve) => {
    let text = ''
    req.on('data', (chunk) => (text += chunk))
    req.on('end', () => resolve(text ? JSON.parse(text) : {}))
  })
}

createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`)
  const authed = (req.headers.cookie ?? '').includes(`diana_session=${token}`)
  if (url.pathname === '/api/auth/login' && req.method === 'POST') {
    const { username, password } = await body(req)
    if (username !== 'admin' || password !== 'demo') return json(res, 401, { error: '用户名或密码不对' })
    res.setHeader('Set-Cookie', `diana_session=${token}; Path=/; HttpOnly; SameSite=Lax`)
    return json(res, 200, { ok: true })
  }
  if (url.pathname.startsWith('/mock/avatar/')) {
    if (!authed) return json(res, 401, { error: '需要登录' })
    const svg = url.pathname.includes('diana') ? avatar('#e0578f', '然') : avatar('#4f8ef7', '海')
    res.writeHead(200, { 'Content-Type': 'image/svg+xml' })
    return res.end(svg)
  }
  if (!authed) return json(res, 401, { error: '需要 WebUI 管理员登录', auth_required: true })
  if (url.pathname === '/api/assistant/config') return json(res, 200, { profiles: bots })
  if (url.pathname === '/api/assistant/chat' && req.method === 'GET') {
    const profile = url.searchParams.get('profile')
    const bot = bots.find((item) => item.id === profile)
    if (!bot) return json(res, 200, { info: { problem: '找不到这个机器人' }, messages: [] })
    const info = { bot_id: bot.id, bot_name: bot.name, platform: bot.platform }
    if (bot.id === 'miyu') info.problem = '这个机器人还没配置主人账号，先在 WebUI 里填好再来聊'
    return json(res, 200, { info, messages: history[bot.id] ?? [] })
  }
  if (url.pathname === '/api/assistant/chat' && req.method === 'POST') {
    const { profile, text } = await body(req)
    const list = (history[profile] ??= [])
    list.push({ id: `u${Date.now()}`, role: 'user', text, at: now() })
    res.writeHead(200, { 'Content-Type': 'application/x-ndjson; charset=utf-8' })
    const replies = ['在的在的！', `你说「${text}」对吧，我看了下：\n上午十点组会\n下午三点前交周报`]
    for (const [index, reply] of replies.entries()) {
      await new Promise((resolve) => setTimeout(resolve, 700))
      const message = { id: `a${Date.now()}-${index}`, role: 'assistant', text: reply, at: now() }
      list.push(message)
      res.write(`${JSON.stringify({ type: 'message', message })}\n`)
    }
    return res.end(`${JSON.stringify({ type: 'done' })}\n`)
  }
  json(res, 404, { error: 'not found' })
}).listen(port, '127.0.0.1', () => console.log(`mock Diana on http://127.0.0.1:${port}  (admin / demo)`))
