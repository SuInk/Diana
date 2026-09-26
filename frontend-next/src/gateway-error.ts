// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// gatewayErrorPage 判断这段正文是不是代理/网关的 HTML 错误页，是就返回一句出处描述。
//
// Cloudflare 这类网关在源站 5xx 时会把正文整个换成自己的错误页。那几千字 HTML
// 和后端要说的原因没有任何关系，直接贴出来只会把真正有用的信息淹掉。
export function gatewayErrorPage(body: string): string {
  const head = body.slice(0, 2_000).toLowerCase();
  if (!head.startsWith("<!doctype html") && !head.startsWith("<html") && !head.includes("<html")) return "";
  const title = /<title[^>]*>([^<]{1,120})<\/title>/i.exec(body.slice(0, 4_000));
  return title ? title[1].trim() : "HTML 错误页";
}

// serverErrorBase 是 5xx 且拿不到后端原因时的那一句开头。
export function serverErrorBase(status: number): string {
  return `后端出错（HTTP ${status}）`;
}

// describeServerFailure 给「5xx 但正文里没有后端原因」补一句去向。
//
// 只写「后端出错（HTTP 502）」时，用户既不知道原因被谁吞了，也不知道去哪看。后端
// 每次报错都记进了运行记录，那里有完整的上游地址、状态码和响应片段。
export function describeServerFailure(status: number, responseBody: string): string {
  const base = serverErrorBase(status);
  const page = gatewayErrorPage(responseBody.trim());
  if (page) {
    return `${base}：后端的错误说明被反向代理或网关换成了它自己的错误页（${page}），原始原因请到「运行记录」查看。`;
  }
  if (!responseBody.trim()) {
    return `${base}：没有收到错误说明，可能是代理中断了请求或后端在处理期间退出，原始原因请到「运行记录」查看。`;
  }
  return base;
}
