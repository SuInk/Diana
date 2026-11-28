// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

/** 服务器上的图片交给主进程带 Cookie 去取，界面本身不直接跨域请求。 */
export function mediaURL(url: string | undefined): string | undefined {
  return url ? `diana-media://load/?u=${encodeURIComponent(url)}` : undefined
}
