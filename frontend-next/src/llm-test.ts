// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { LLMConfig } from "./api";

export function testModelIDs(profile: LLMConfig): string[] {
  const seen = new Set<string>();
  const models: string[] = [];
  for (const id of [profile.model ?? "", ...(profile.models ?? []).map((model) => model.id)]) {
    const value = id.trim();
    if (!value || seen.has(value)) continue;
    seen.add(value);
    models.push(value);
  }
  return models;
}

export function randomTestModel(profile: LLMConfig, random = Math.random): string {
  const models = testModelIDs(profile);
  if (models.length === 0) return "";
  const candidate = random();
  const sample = Number.isFinite(candidate) ? candidate : 0;
  const index = Math.min(models.length - 1, Math.max(0, Math.floor(sample * models.length)));
  return models[index];
}

export function describeLLMTestError(error: unknown, provider: string, model: string): string {
  const reason = error instanceof Error ? error.message.trim() : "未知错误";
  const responseBody = error && typeof error === "object" && "responseBody" in error
    ? String(error.responseBody ?? "").trim()
    : "";
  const target = [provider.trim(), model.trim()].filter(Boolean).join(" · ");
  const prefix = target ? `${target} 测试失败` : "模型测试失败";
  if (responseBody) {
    const bodyMessage = responseBodyMessage(responseBody);
    if (bodyMessage) {
      return `${prefix}：${bodyMessage}${reason && reason !== bodyMessage ? `（${reason}）` : ""}`;
    }
    if (gatewayErrorPage(responseBody)) {
      // 代理返回的 HTML 错误页不是模型的错误，整段贴出来只会把真正的原因淹掉。
      // 只留一句出处，剩下的让用户去运行记录里看服务端原始错误。
      return `${prefix}：${reason || "HTTP 请求失败"}。这段响应来自反向代理或网关（${gatewayErrorPage(responseBody)}），不是模型返回的内容——上游可能没应答，或者后端在请求期间退出；请到“运行记录”搜索 llm_test 查看服务端原始错误。`;
    }
    return `${prefix}：${reason || "HTTP 请求失败"}\n\n响应正文：\n${formatResponseBody(responseBody)}`;
  }
  if (/^后端出错（HTTP 5\d\d）$/.test(reason)) {
    return `${prefix}：${reason}。网关没有返回模型错误正文，可能是后端在请求期间退出或代理中断；请到“运行记录”搜索 llm.test 查看服务端原始错误。`;
  }
  return `${prefix}：${reason || "上游没有返回错误详情"}`;
}

function responseBodyMessage(body: string): string {
  try {
    const payload = JSON.parse(body) as {
      message?: unknown;
      detail?: unknown;
      error?: unknown;
    };
    const nested = payload.error && typeof payload.error === "object" && "message" in payload.error
      ? (payload.error as { message?: unknown }).message
      : undefined;
    for (const value of [nested, payload.message, typeof payload.error === "string" ? payload.error : undefined, payload.detail]) {
      if (typeof value === "string" && value.trim()) return value.trim();
    }
  } catch {
    // 非 JSON body 由原文展示兜底。
  }
  return "";
}

const maxTestErrorBodyRunes = 4_000;

function formatResponseBody(body: string): string {
  let formatted = body;
  try {
    formatted = JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    // 纯文本和 HTML 原样展示；Vue 文本插值会转义内容，不会执行代理错误页。
  }
  const runes = [...formatted];
  if (runes.length <= maxTestErrorBodyRunes) return formatted;
  return `${runes.slice(0, maxTestErrorBodyRunes).join("")}\n…（响应正文过长，已截断）`;
}

// gatewayErrorPage 判断这段正文是不是代理/网关的 HTML 错误页，是就返回一句出处描述。
//
// Cloudflare 这类网关在源站 5xx 时会把正文整个换成自己的错误页。那几千字 HTML
// 和模型没有任何关系，却会把对话框撑满，真正有用的 reason 反而被推到看不见的地方。
function gatewayErrorPage(body: string): string {
  const head = body.slice(0, 2_000).toLowerCase();
  if (!head.startsWith("<!doctype html") && !head.startsWith("<html") && !head.includes("<html")) return "";
  const title = /<title[^>]*>([^<]{1,120})<\/title>/i.exec(body.slice(0, 4_000));
  return title ? title[1].trim() : "HTML 错误页";
}
