import type { Provider } from "./api";

export interface LLMServicePreset {
  id: string;
  label: string;
  provider: Provider;
  apiStyle: "responses" | "chat_completions";
  baseURL: string;
  model: string;
  hint: string;
}

export const llmServicePresets: LLMServicePreset[] = [
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
    label: "Google Gemini",
    provider: "openai_compatible",
    apiStyle: "chat_completions",
    baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
    model: "",
    hint: "Gemini OpenAI 兼容接口"
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
  }
];

export function detectLLMService(baseURL?: string): string {
  const normalized = (baseURL ?? "").replace(/\/+$/, "");
  return llmServicePresets.find((preset) => preset.baseURL.replace(/\/+$/, "") === normalized)?.id ?? "custom";
}
