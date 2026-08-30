// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { Provider } from "./api";

/**
 * 接入类型，对应后端的 provider。
 *
 * 分两级选择是因为这两件事本来就是正交的：先定用哪套协议跟模型说话，再定具体
 * 找谁要服务。以前只有一层平铺列表，provider 只能作为预设的隐藏副作用写进去，
 * 结果原生 Gemini 和 Anthropic 在界面上根本选不到。
 */
export interface LLMProviderKind {
  id: Provider;
  label: string;
  hint: string;
  /** 原生协议不接受 api_style，后端会以「只有 openai_compatible 支持」报错。 */
  supportsAPIStyle: boolean;
}

export const llmProviderKinds: LLMProviderKind[] = [
  {
    id: "openai_compatible",
    label: "OpenAI 兼容接口",
    hint: "绝大多数服务商、代理和自建网关都提供这套协议",
    supportsAPIStyle: true
  },
  {
    id: "gemini",
    label: "Google Gemini 原生",
    hint: "直连 Gemini API，不经过 OpenAI 兼容层",
    supportsAPIStyle: false
  },
  {
    id: "anthropic",
    label: "Anthropic 原生",
    hint: "直连 Claude Messages API，不经过 OpenAI 兼容层",
    supportsAPIStyle: false
  }
];

export interface LLMServicePreset {
  id: string;
  label: string;
  provider: Provider;
  /** 原生协议留空：后端只允许 openai_compatible 带这个字段。 */
  apiStyle: "responses" | "chat_completions" | "";
  baseURL: string;
  model: string;
  hint: string;
}

export const llmServicePresets: LLMServicePreset[] = [
  // —— OpenAI 兼容接口 ——
  {
    id: "openai",
    label: "OpenAI 官方",
    provider: "openai_compatible",
    apiStyle: "responses",
    baseURL: "https://api.openai.com/v1",
    model: "",
    hint: "OpenAI Responses API"
  },
  {
    id: "deepseek",
    label: "DeepSeek 官方",
    provider: "openai_compatible",
    apiStyle: "chat_completions",
    baseURL: "https://api.deepseek.com",
    model: "",
    hint: "DeepSeek OpenAI 兼容接口"
  },
  {
    id: "gemini",
    label: "Google Gemini（兼容接口）",
    provider: "openai_compatible",
    apiStyle: "chat_completions",
    baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
    model: "",
    hint: "Gemini 的 OpenAI 兼容层，中转站通常也走这个"
  },
  {
    id: "opencode-zen",
    label: "OpenCode Zen",
    provider: "openai_compatible",
    apiStyle: "responses",
    baseURL: "https://opencode.ai/zen/v1",
    model: "",
    hint: "OpenCode 精选模型，按量计费"
  },
  {
    id: "opencode-go",
    label: "OpenCode Go",
    provider: "openai_compatible",
    apiStyle: "chat_completions",
    baseURL: "https://opencode.ai/zen/go/v1",
    model: "",
    hint: "OpenCode 开源模型订阅"
  },
  {
    id: "custom",
    label: "自定义兼容接口",
    provider: "openai_compatible",
    apiStyle: "chat_completions",
    baseURL: "",
    model: "",
    hint: "代理、中转或自建服务"
  },

  // —— Google Gemini 原生 ——
  // 原生这边 base_url 留空即走官方地址；填了是为了代理或自建网关，所以同样
  // 提供一个自定义项，而不是把地址栏藏起来。
  {
    id: "gemini-native",
    label: "Google 官方",
    provider: "gemini",
    apiStyle: "",
    baseURL: "",
    model: "",
    hint: "直连 generativelanguage.googleapis.com"
  },
  {
    id: "gemini-native-custom",
    label: "自定义地址",
    provider: "gemini",
    apiStyle: "",
    baseURL: "",
    model: "",
    hint: "走代理或自建网关时填写"
  },

  // —— Anthropic 原生 ——
  {
    id: "anthropic-native",
    label: "Anthropic 官方",
    provider: "anthropic",
    apiStyle: "",
    baseURL: "",
    model: "",
    hint: "直连 api.anthropic.com"
  },
  {
    id: "anthropic-native-custom",
    label: "自定义地址",
    provider: "anthropic",
    apiStyle: "",
    baseURL: "",
    model: "",
    hint: "走代理或自建网关时填写"
  }
];

/** 某个接入类型下可选的具体服务商。 */
export function presetsForProvider(provider: Provider): LLMServicePreset[] {
  return llmServicePresets.filter((preset) => preset.provider === provider);
}

/**
 * 按已保存的配置反查该选中哪一项。
 *
 * 只按地址匹配会认错：原生 Gemini 和原生 Anthropic 的默认地址都是空串，光看
 * 地址分不出是哪一个，所以必须连 provider 一起比。
 */
export function detectLLMService(baseURL?: string, provider?: Provider): string {
  const normalized = (baseURL ?? "").replace(/\/+$/, "");
  const candidates = provider ? presetsForProvider(provider) : llmServicePresets;
  const matched = candidates.find((preset) => preset.baseURL.replace(/\/+$/, "") === normalized && preset.baseURL !== "");
  if (matched) return matched.id;
  // 地址对不上任何预设时落到该类型的自定义项；兼容层那边它叫 custom。
  const fallback = candidates.find((preset) => preset.id.endsWith("custom"));
  return fallback?.id ?? "custom";
}

/** 类型切换后要落到的默认服务商。 */
export function defaultPresetForProvider(provider: Provider): LLMServicePreset | undefined {
  return presetsForProvider(provider)[0];
}
