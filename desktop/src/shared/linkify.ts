// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

export interface TextPart {
  text: string
  href?: string
}

const urlPattern = /https?:\/\/[^\s<>"'，。！？、；：）】」』]+/g
// 句末常跟着的英文标点不算网址的一部分。
const trailing = /[.,!?;:)\]]+$/

/** 把正文切成普通文字和网址两种片段，界面按片段渲染，不拼 HTML。 */
export function linkify(text: string): TextPart[] {
  const parts: TextPart[] = []
  let last = 0
  for (const match of text.matchAll(urlPattern)) {
    let url = match[0]
    const tail = url.match(trailing)?.[0] ?? ''
    url = url.slice(0, url.length - tail.length)
    const start = match.index ?? 0
    if (start > last) parts.push({ text: text.slice(last, start) })
    parts.push({ text: url, href: url })
    last = start + url.length
  }
  if (last < text.length) parts.push({ text: text.slice(last) })
  return parts
}
