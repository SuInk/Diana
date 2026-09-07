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
