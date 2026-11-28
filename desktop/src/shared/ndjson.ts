// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/**
 * 把分块到达的字节拼成完整的行。HTTP 分块可能把一行 JSON 或一个多字节字符切开，
 * 所以先按流解码再按换行切，最后一段留到下一块或 flush。
 */
export class LineSplitter {
  private readonly decoder = new TextDecoder()
  private rest = ''

  push(chunk: Uint8Array): string[] {
    this.rest += this.decoder.decode(chunk, { stream: true })
    const lines = this.rest.split('\n')
    this.rest = lines.pop() ?? ''
    return lines.filter((line) => line.trim() !== '')
  }

  flush(): string[] {
    const tail = this.rest + this.decoder.decode()
    this.rest = ''
    return tail.trim() ? [tail] : []
  }
}
