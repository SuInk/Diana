// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { LineSplitter } from '../src/shared/ndjson.ts'
import { absoluteURL, normalizeServer, sameOrigin } from '../src/shared/server.ts'
import { formatChatTime, needsTimeSeparator } from '../src/shared/time.ts'
import { linkify } from '../src/shared/linkify.ts'

test('NDJSON 行被分块和多字节字符切开时照样拼回来', () => {
  const bytes = new TextEncoder().encode('{"type":"message","text":"你好"}\n{"type":"done"}')
  const splitter = new LineSplitter()
  const lines = []
  // 一个字节一块，「你」「好」的 UTF-8 都会被切开。
  for (const byte of bytes) lines.push(...splitter.push(Uint8Array.of(byte)))
  lines.push(...splitter.flush())
  assert.deepEqual(lines.map((line) => JSON.parse(line).type), ['message', 'done'])
  assert.equal(JSON.parse(lines[0]).text, '你好')
})

test('空行和心跳之间的空白不当成一帧', () => {
  const splitter = new LineSplitter()
  assert.deepEqual(splitter.push(new TextEncoder().encode('\n  \n{"type":"heartbeat"}\n')), ['{"type":"heartbeat"}'])
  assert.deepEqual(splitter.flush(), [])
})

test('服务器地址补协议、去路径，乱填报错', () => {
  assert.equal(normalizeServer(' 192.168.1.10:18080/console '), 'http://192.168.1.10:18080')
  assert.equal(normalizeServer('https://diana.example.com/'), 'https://diana.example.com')
  assert.throws(() => normalizeServer(''), /先填/)
  assert.throws(() => normalizeServer('ftp://x'), /不对/)
})

test('图片地址补全，并且只放行同一台服务器', () => {
  assert.equal(absoluteURL('http://10.0.0.2:18080', '/api/avatar/1.png'), 'http://10.0.0.2:18080/api/avatar/1.png')
  assert.equal(absoluteURL('http://10.0.0.2:18080', 'https://cdn.example.com/a.png'), 'https://cdn.example.com/a.png')
  assert.equal(sameOrigin('http://10.0.0.2:18080', 'http://10.0.0.2:18080/a.png'), true)
  assert.equal(sameOrigin('http://10.0.0.2:18080', 'http://10.0.0.2:9999/a.png'), false)
  assert.equal(sameOrigin('http://10.0.0.2:18080', 'not a url'), false)
})

test('五分钟以上才插时间分隔', () => {
  assert.equal(needsTimeSeparator(undefined, 100), true)
  assert.equal(needsTimeSeparator(100, 100 + 299), false)
  assert.equal(needsTimeSeparator(100, 100 + 300), true)
})

test('时间按今天、昨天、今年、往年写', () => {
  const now = new Date(2026, 10, 23, 15, 0)
  const at = (d) => Math.floor(d.getTime() / 1000)
  assert.equal(formatChatTime(at(new Date(2026, 10, 23, 9, 5)), now), '09:05')
  assert.equal(formatChatTime(at(new Date(2026, 10, 22, 23, 59)), now), '昨天 23:59')
  assert.equal(formatChatTime(at(new Date(2026, 0, 2, 8, 0)), now), '1月2日 08:00')
  assert.equal(formatChatTime(at(new Date(2025, 11, 31, 8, 0)), now), '2025年12月31日 08:00')
})

test('网址切成链接片段，句末标点和中文标点不算进去', () => {
  assert.deepEqual(linkify('看 https://example.com/a?b=1。然后'), [
    { text: '看 ' },
    { text: 'https://example.com/a?b=1', href: 'https://example.com/a?b=1' },
    { text: '。然后' },
  ])
  assert.deepEqual(linkify('see https://example.com.'), [{ text: 'see ' }, { text: 'https://example.com', href: 'https://example.com' }, { text: '.' }])
  assert.deepEqual(linkify('没有链接'), [{ text: '没有链接' }])
})
