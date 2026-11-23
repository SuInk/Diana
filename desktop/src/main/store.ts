// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 本机保存的登录状态：地址、用户名、信任过的证书指纹、窗口大小放普通文件；会话
// Cookie 用系统钥匙串（safeStorage）加密后单独存，系统不支持加密时不落盘，下次重新登录。
// 密码从来不存。

import { app, safeStorage } from 'electron'
import { readFileSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

export interface WindowBounds {
  x?: number
  y?: number
  width: number
  height: number
  maximized?: boolean
}

export interface StoredState {
  server: string
  username: string
  /** host → 用户确认过的证书指纹 */
  pins: Record<string, string>
  window?: WindowBounds
}

const empty: StoredState = { server: '', username: '', pins: {} }

function dir(): string {
  const path = app.getPath('userData')
  mkdirSync(path, { recursive: true })
  return path
}

export function loadState(): StoredState {
  try {
    const value = JSON.parse(readFileSync(join(dir(), 'state.json'), 'utf8'))
    return { ...empty, ...value, pins: { ...(value.pins ?? {}) } }
  } catch {
    return { ...empty, pins: {} }
  }
}

export function saveState(state: StoredState): void {
  writeFileSync(join(dir(), 'state.json'), JSON.stringify(state, null, 2), { mode: 0o600 })
}

export async function loadCookie(): Promise<string> {
  if (!(await safeStorage.isAsyncEncryptionAvailable())) return ''
  try {
    const { result } = await safeStorage.decryptStringAsync(readFileSync(join(dir(), 'session.bin')))
    return result
  } catch {
    return ''
  }
}

export async function saveCookie(cookie: string): Promise<void> {
  const path = join(dir(), 'session.bin')
  if (!cookie || !(await safeStorage.isAsyncEncryptionAvailable())) {
    rmSync(path, { force: true })
    return
  }
  writeFileSync(path, await safeStorage.encryptStringAsync(cookie), { mode: 0o600 })
}
