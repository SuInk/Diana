// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { AppSelectOption } from "../components/AppSelect.vue";

export type MediaParamField = { key: string; label: string; placeholder: string; options?: AppSelectOption[]; multiline?: boolean; hint?: string };
// 语音接口的形态，和后端 model/llm/speech.go 的 SpeechAPI 对应。名单对照 AstrBot
// 和 SillyTavern 的语音提供商取并集；其余自建服务用「自定义请求」套模板。
const speechAPIOptions: AppSelectOption[] = [
  { value: "", label: "按地址自动" },
  { value: "openai", label: "OpenAI 兼容（/audio/speech）", hint: "OpenAI、硅基流动、Kokoro-FastAPI 等" },
  { value: "elevenlabs", label: "ElevenLabs" },
  { value: "breeze", label: "Breeze" },
  { value: "mimo", label: "小米 MiMo" },
  { value: "minimax", label: "MiniMax" },
  { value: "volcengine", label: "火山引擎（豆包语音）" },
  { value: "dashscope", label: "阿里云百炼", hint: "qwen-tts、CosyVoice、Sambert" },
  { value: "fishaudio", label: "Fish Audio" },
  { value: "azure", label: "Azure 语音" },
  { value: "gemini", label: "Gemini TTS" },
  { value: "novelai", label: "NovelAI" },
  { value: "gpt_sovits", label: "GPT-SoVITS v2（api_v2）" },
  { value: "gsvi", label: "GPT-SoVITS-Inference（GSVI）" },
  { value: "custom_http", label: "自定义请求模板", hint: "AllTalk、XTTS、VITS、Chatterbox 等" }
];
const transcriptionAPIOptions: AppSelectOption[] = [
  { value: "", label: "按地址自动" },
  { value: "openai", label: "OpenAI 兼容（/audio/transcriptions）", hint: "Whisper、硅基流动、Xinference、faster-whisper 等" },
  { value: "elevenlabs", label: "ElevenLabs" },
  { value: "mimo", label: "小米 MiMo" }
];
// 按服务地址认接口，和后端 ResolveSpeechAPI 的名单一致；界面只用它决定显示哪些参数。
const speechAPIHosts: [string, string][] = [
  ["elevenlabs.io", "elevenlabs"], ["breeze.blue", "breeze"], ["xiaomimimo.com", "mimo"], ["minimax.io", "minimax"], ["minimax.chat", "minimax"],
  ["minimaxi.chat", "minimax"], ["minimaxi.com", "minimax"], ["openspeech.bytedance.com", "volcengine"],
  ["dashscope.aliyuncs.com", "dashscope"], ["dashscope-intl.aliyuncs.com", "dashscope"], ["fish.audio", "fishaudio"],
  ["fish-audio.cn", "fishaudio"], ["tts.speech.microsoft.com", "azure"], ["generativelanguage.googleapis.com", "gemini"],
  ["novelai.net", "novelai"]
];
export const builtinSpeechAPIs: Record<string, string> = {
  "builtin:edge-tts": "edge",
  "builtin:google-translate-tts": "google_translate",
  "builtin:whisper-cli": "whisper_cli"
};

// 自定义请求的现成模板，对应 SillyTavern 里那几家自建服务的请求格式。
export const customSpeechTemplates: { value: string; label: string; params: Record<string, string> }[] = [
  { value: "xtts", label: "XTTS（xtts-api-server）", params: { path: "tts_to_audio/", body: '{"text":"{{text}}","speaker_wav":"{{voice}}","language":"{{lang}}"}', response: "audio" } },
  { value: "alltalk", label: "AllTalk v2", params: { path: "api/tts-generate", body_type: "form", body: "text_input={{text}}&character_voice_gen={{voice}}&language={{lang}}&text_filtering=standard&narrator_enabled=false&output_file_name=diana&output_file_timestamp=true&autoplay=false", response: "url:output_file_url" } },
  { value: "chatterbox", label: "Chatterbox TTS Server", params: { path: "tts", body: '{"text":"{{text}}","voice_mode":"predefined","predefined_voice_id":"{{voice}}","output_format":"wav","speed_factor":{{speed}},"language":"{{lang}}"}', response: "audio" } },
  { value: "vits", label: "vits-simple-api", params: { path: "voice/vits", body_type: "form", body: "text={{text}}&id={{voice}}&format=wav&lang={{lang}}&length={{speed}}", response: "audio" } },
  { value: "sbvits2", label: "Style-Bert-VITS2", params: { path: "voice", body_type: "query", body: "text={{text}}&model_id=0&speaker_id={{voice}}&language={{lang}}", response: "audio" } },
  { value: "silero", label: "Silero（SillyTavern Extras）", params: { path: "generate", body: '{"text":"{{text}}","speaker":"{{voice}}","session":"diana"}', response: "audio" } },
  { value: "cosyvoice", label: "CosyVoice 整合包（v3ucn）", params: { path: "", body: '{"text":"{{text}}","speaker":"{{voice}}"}', response: "audio" } },
  { value: "gsvi-legacy", label: "GPT-SoVITS-Inference 旧版 /tts", params: { method: "GET", path: "tts", body_type: "query", body: "text={{text}}&cha_name={{voice}}&text_language={{lang}}&speed={{speed}}", response: "audio" } },
  { value: "tts-webui", label: "TTS WebUI（OpenAI 形状）", params: { path: "v1/audio/speech", body: '{"model":"{{model}}","voice":"{{voice}}","input":"{{text}}","response_format":"wav","speed":{{speed}}}', response: "audio" } }
];

const formatOptions = (values: string[], fallback: string): AppSelectOption[] => values.map((value) => ({ value, label: value || `默认（${fallback}）` }));

export function mediaParamFields(role: string, api: string, builtin = false): MediaParamField[] {
  if (role === "video") {
    return [
      { key: "size", label: "分辨率", placeholder: "1280x720" },
      { key: "seconds", label: "默认时长（秒）", placeholder: "由服务决定" },
      { key: "timeout_seconds", label: "任务总时限（秒）", placeholder: "600" },
      { key: "poll_interval_seconds", label: "轮询间隔（秒）", placeholder: "5" }
    ];
  }
  if (role === "stt") {
    const fields: MediaParamField[] = [];
    if (!builtin) fields.push({ key: "api", label: "接口", placeholder: "按地址自动", options: transcriptionAPIOptions });
    if (api === "whisper_cli") {
      fields.push(
        { key: "command", label: "命令", placeholder: "whisper-cli" },
        { key: "model_path", label: "模型文件", placeholder: "/path/to/ggml-small.bin" }
      );
    }
    fields.push({ key: "language", label: "语言", placeholder: "留空自动识别，如 zh" });
    if (api === "openai") fields.push({ key: "prompt", label: "提示词", placeholder: "可选，专有名词" });
    fields.push(
      { key: "timeout_seconds", label: "超时（秒）", placeholder: "90" },
      { key: "max_duration_seconds", label: "时长上限（秒）", placeholder: "300" },
      { key: "private_enabled", label: "私聊语音", placeholder: "转写", options: [{ value: "", label: "转写" }, { value: "false", label: "不转写" }] }
    );
    return fields;
  }
  const fields: MediaParamField[] = [];
  if (!builtin) fields.push({ key: "api", label: "接口", placeholder: "按地址自动", options: speechAPIOptions });
  const voicePlaceholder: Record<string, string> = {
    openai: "alloy / 说话人", elevenlabs: "voice_id", breeze: "voice_id，如 voc_kgj4fsfnqmf4", mimo: "mimo_default / 冰糖 / 茉莉", minimax: "voice_id，如 female-shaonv",
    volcengine: "音色代号，如 BV001_streaming", dashscope: "Cherry / longxiaochun", fishaudio: "声音模型 reference_id",
    azure: "zh-CN-XiaoxiaoNeural", gemini: "Kore / Puck", novelai: "Aini", edge: "zh-CN-XiaoxiaoNeural",
    google_translate: "语言代码，如 zh-CN", gsvi: "整合包里的角色名", custom_http: "填进模板的 {{voice}}"
  };
  if (api !== "gpt_sovits") fields.push({ key: "voice", label: api === "google_translate" ? "语言" : "音色", placeholder: voicePlaceholder[api] ?? "音色" });
  const formats: Record<string, string[]> = {
    openai: ["", "mp3", "wav", "opus", "aac", "flac"], elevenlabs: ["", "mp3", "wav", "opus"], breeze: ["", "mp3", "wav"], mimo: ["", "wav", "mp3"],
    minimax: ["", "mp3", "wav", "flac"], volcengine: ["", "mp3", "wav", "ogg_opus"], dashscope: ["", "mp3", "wav"],
    fishaudio: ["", "mp3", "wav", "opus"], azure: ["", "mp3", "wav", "opus"], custom_http: ["", "wav", "mp3", "ogg"]
  };
  const fallbackFormat: Record<string, string> = { mimo: "wav", custom_http: "wav" };
  if (formats[api]) fields.push({ key: "format", label: "格式", placeholder: "默认", options: formatOptions(formats[api], fallbackFormat[api] ?? "mp3") });
  if (api !== "google_translate" && api !== "novelai") fields.push({ key: "speed", label: "语速", placeholder: api === "breeze" ? "0.5–2.0，默认 1.0" : "1.0" });
  if (["openai", "breeze", "mimo", "gemini", "azure"].includes(api)) fields.push({ key: "instructions", label: api === "azure" ? "说话风格" : "语气说明", placeholder: api === "azure" ? "cheerful / sad" : "可选" });
  const extra: Record<string, MediaParamField[]> = {
    breeze: [{ key: "language", label: "语言", placeholder: "可选，如 zh / en" }],
    gpt_sovits: [
      { key: "ref_audio_path", label: "参考音频路径", placeholder: "GPT-SoVITS 能读到的路径" },
      { key: "prompt_text", label: "参考音频文本", placeholder: "参考音频对应的原文" },
      { key: "text_lang", label: "合成语言", placeholder: "zh" },
      { key: "prompt_lang", label: "参考音频语言", placeholder: "zh" }
    ],
    gsvi: [
      { key: "version", label: "模型版本", placeholder: "v4" },
      { key: "emotion", label: "情绪", placeholder: "默认" },
      { key: "text_lang", label: "合成语言", placeholder: "中文" },
      { key: "prompt_lang", label: "参考音频语言", placeholder: "中文" }
    ],
    minimax: [
      { key: "group_id", label: "GroupId", placeholder: "旧版接口需要，新版可留空" },
      { key: "emotion", label: "情绪", placeholder: "happy / sad，可选" }
    ],
    volcengine: [
      { key: "app_id", label: "App ID", placeholder: "控制台应用的 appid（必填）" },
      { key: "cluster", label: "集群", placeholder: "volcano_tts（v1 接口）" },
      { key: "resource_id", label: "资源 ID", placeholder: "填了走 v3，如 seed-tts-1.0" }
    ],
    azure: [{ key: "region", label: "区域", placeholder: "eastus（提供商没填地址时用）" }],
    edge: [
      { key: "pitch", label: "音高", placeholder: "+0Hz" },
      { key: "volume", label: "音量", placeholder: "+0%" }
    ],
    custom_http: [
      { key: "template", label: "套用模板", placeholder: "选一个填好下面几项", options: [{ value: "", label: "不套用" }, ...customSpeechTemplates.map(({ value, label }) => ({ value, label }))] },
      { key: "method", label: "请求方法", placeholder: "POST", options: [{ value: "", label: "POST" }, { value: "GET", label: "GET" }] },
      { key: "path", label: "路径", placeholder: "接在提供商地址后面，如 tts_to_audio/" },
      { key: "body_type", label: "参数形式", placeholder: "JSON", options: [{ value: "", label: "JSON" }, { value: "form", label: "表单" }, { value: "query", label: "查询串" }] },
      { key: "body", label: "请求模板", placeholder: '{"text":"{{text}}","speaker":"{{voice}}"}', multiline: true, hint: "可用 {{text}} {{voice}} {{model}} {{speed}} {{format}} {{lang}}" },
      { key: "response", label: "音频位置", placeholder: "audio", hint: "audio＝响应体就是音频；base64:路径、hex:路径、url:路径，如 url:data.file" },
      { key: "language", label: "{{lang}} 的值", placeholder: "zh" }
    ],
    google_translate: []
  };
  fields.push(...(extra[api] ?? []));
  fields.push(
    { key: "timeout_seconds", label: "超时（秒）", placeholder: "60" },
    { key: "send_mode", label: "发送模式", placeholder: "按需", options: [{ value: "", label: "按需：用户要语音时才发" }, { value: "always", label: "常驻：回复都用语音发" }], hint: "常驻时带链接、图片、@ 或超过字数上限的回复仍发文字，合成失败也回退文字。只对 QQ 生效。" },
    { key: "max_chars", label: "单次字数上限", placeholder: "500" }
  );
  return fields;
}


export function resolveSpeechAPI(explicit = "", baseURL = ""): string {
  if (explicit) return explicit;
  try {
    const host = new URL(baseURL).hostname.toLowerCase();
    for (const [suffix, api] of speechAPIHosts) {
      if (host === suffix || host.endsWith(`.${suffix}`)) return api;
    }
    if (host.startsWith("translate.google.")) return "google_translate";
  } catch {}
  return "openai";
}

const behaviorKeys = new Set(["send_mode", "max_chars", "private_enabled", "max_duration_seconds"]);
export function isSpeechBehaviorParam(key: string): boolean { return behaviorKeys.has(key); }

export function speechServiceFields(role: "tts" | "stt", params: Record<string, string>, baseURL: string): MediaParamField[] {
  return mediaParamFields(role, resolveSpeechAPI(params.api, baseURL)).filter(field => !isSpeechBehaviorParam(field.key));
}

export function updateSpeechParam(params: Record<string, string>, key: string, value: string): Record<string, string> {
  const next = { ...params };
  if (key === "template") {
    const template = customSpeechTemplates.find(item => item.value === value);
    if (!template) return next;
    for (const field of ["method", "path", "body_type", "body", "response"]) delete next[field];
    return { ...next, ...template.params };
  }
  if (value.trim()) next[key] = value.trim();
  else delete next[key];
  return next;
}
