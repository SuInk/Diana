// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { SearchProvider } from "./api";

export const searchProviderTypes = [
  { value: "exa_mcp", label: "Exa MCP", hint: "Exa 或兼容的 MCP 服务" },
  { value: "tavily", label: "Tavily API", hint: "Tavily 或兼容的搜索 API" },
  { value: "http", label: "自定义 HTTP API", hint: "GET / POST JSON 接口，可映射结果字段" },
  { value: "search_mcp", label: "自定义搜索 MCP", hint: "指定搜索工具和参数名称" },
  { value: "browser", label: "浏览器搜索", hint: "用隔离浏览器读取搜索结果，需要 Chrome / Chromium" }
];

export function searchProviderTypeLabel(type: string): string {
  return searchProviderTypes.find(option => option.value === type)?.label ?? type;
}

export function searchProviderAddress(url: string): string {
  try { return new URL(url).host; } catch { return url; }
}

export function searchProviderPreset(type: SearchProvider["type"]): Partial<SearchProvider> {
  if (type === "exa_mcp") return { url: "https://mcp.exa.ai/mcp?tools=web_search_exa", tool: "web_search_exa", query_param: "", results_param: "" };
  if (type === "tavily") return { url: "https://api.tavily.com/search", tool: "", query_param: "", results_param: "" };
  if (type === "browser") return { url: "https://www.google.com/search", tool: "", query_param: "q", results_param: "" };
  if (type === "http") return { url: "", tool: "", query_param: "query", results_param: "count", http_config: { method: "POST", auth_type: "none", title_path: "title" } };
  return { url: "", tool: "search", query_param: "query", results_param: "num_results" };
}

export function searchProviderKeyHint(provider: SearchProvider): string {
  if (provider.type === "browser") return "无需 API Key，使用服务器上的隔离 Chrome / Chromium。";
  if (provider.type === "exa_mcp") {
    try {
      const url = new URL(provider.url);
      if (url.hostname === "mcp.exa.ai") return "默认 Exa 公共 MCP 无需 API Key；使用自己的端点时按服务要求填写。";
    } catch { /* Address validation happens when saving or testing. */ }
  }
  if (provider.type === "tavily") return "需要 Tavily API Key；新账号可使用服务商提供的免费额度，额度以账户为准。";
  if (provider.type === "http" && provider.http_config?.auth_type === "none") return "当前不发送认证请求头；需要在请求参数传密钥时，可在固定参数里使用 {api_key}。";
  return "按搜索服务要求填写 API Key；已存密钥留空沿用。";
}

export function parseSearchParams(text: string): Record<string, unknown> {
  if (!text.trim()) return {};
  let value: unknown;
  try { value = JSON.parse(text); } catch { throw new Error("固定请求参数必须是有效 JSON 对象"); }
  if (!value || Array.isArray(value) || typeof value !== "object") throw new Error("固定请求参数必须是 JSON 对象");
  return value as Record<string, unknown>;
}

export function changeSearchProviderType(provider: SearchProvider, type: SearchProvider["type"]): SearchProvider {
  if (provider.type === type) return provider;
  const oldDefault = searchProviderPreset(provider.type).url;
  const preset = searchProviderPreset(type);
  const url = !provider.url || provider.url === oldDefault ? preset.url ?? "" : provider.url;
  return { ...provider, ...preset, type, url, http_config: type === "http" ? preset.http_config : undefined };
}
