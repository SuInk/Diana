// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { SearchProvider } from "./api";

export const searchProviderTypes = [
  { value: "exa_mcp", label: "Exa MCP", hint: "Exa 或兼容的 MCP 服务" },
  { value: "tavily", label: "Tavily API", hint: "Tavily 或兼容的搜索 API" },
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
  return { url: "", tool: "search", query_param: "query", results_param: "num_results" };
}
