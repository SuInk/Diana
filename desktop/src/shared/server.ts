// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/** 用户填的服务器地址整理成 scheme://host:port：没写协议按 http，去掉路径。 */
export function normalizeServer(input: string): string {
  let text = input.trim()
  if (!text) throw new Error('先填服务器地址')
  if (!text.includes('://')) text = `http://${text}`
  let url: URL
  try {
    url = new URL(text)
  } catch {
    throw new Error('服务器地址不对，形如 http://192.168.1.10:8080')
  }
  if (!url.hostname || (url.protocol !== 'http:' && url.protocol !== 'https:')) {
    throw new Error('服务器地址不对，形如 http://192.168.1.10:8080')
  }
  return url.origin
}

/** 头像、图片地址：相对地址按服务器补全。 */
export function absoluteURL(server: string, url: string): string {
  return new URL(url, `${server}/`).toString()
}

/** 只放行登录的那台服务器上的地址，免得界面借图片协议去读别的站。 */
export function sameOrigin(server: string, url: string): boolean {
  try {
    return new URL(url).origin === new URL(server).origin
  } catch {
    return false
  }
}
