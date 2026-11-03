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

// 视频接口，和后端 model/llm/video_presets.go 的预设一一对应。hint 说明提供商的
// Key 和地址怎么填：密钥只放提供商配置，插槽参数里没有密钥。
export const videoAPIOptions: AppSelectOption[] = [
  { value: "", label: "OpenAI Sora 格式（/videos）", hint: "OpenAI 及照搬 Sora 接口的服务" },
  { value: "kling", label: "可灵", hint: "API Key 填 Key 或 AccessKey:SecretKey；海外地址 https://api-singapore.klingai.com" },
  { value: "jimeng", label: "即梦（火山视觉，下线中）", hint: "API Key 填 AccessKeyID:SecretAccessKey；模型填 req_key，如 jimeng_t2v_v30" },
  { value: "vidu", label: "Vidu", hint: "海外地址 https://api.vidu.com" },
  { value: "seedance", label: "火山方舟 Seedance", hint: "BytePlus 地址 https://ark.ap-southeast.bytepluses.com/api/v3" },
  { value: "runway", label: "Runway", hint: "分辨率直接作 ratio，如 1280x720" },
  { value: "luma", label: "Luma（Agents API）", hint: "Key 以 luma-api- 开头，模型如 ray-3.2" },
  { value: "minimax", label: "MiniMax 海螺（v1）", hint: "海外地址 https://api.minimax.io" },
  { value: "minimax_v2", label: "MiniMax 海螺 H3（v2）", hint: "分辨率档位和时长必填" },
  { value: "custom", label: "自定义", hint: "照下面的字段描述接口，可先套用预设" }
];
// 新建视频提供商时的默认地址，和后端 model/llm/video_presets.go 里各预设的 BaseURL 一致；
// 地址留空就走这里，所以界面只拿它当占位。Sora 格式留空走 OpenAI 官方地址。
export const videoServiceBaseURLs: Record<string, string> = {
  "": "https://api.openai.com/v1",
  kling: "https://api-beijing.klingai.com",
  jimeng: "https://visual.volcengineapi.com",
  vidu: "https://api.vidu.cn",
  seedance: "https://ark.cn-beijing.volces.com/api/v3",
  runway: "https://api.dev.runwayml.com",
  luma: "https://agents.lumalabs.ai/v1",
  minimax: "https://api.minimax.cn",
  minimax_v2: "https://api.minimax.cn",
  custom: ""
};
const videoTaskAPIs = new Set(videoAPIOptions.map(option => option.value).filter(value => value !== ""));
const videoRatioPlaceholder: Record<string, string> = {
  kling: "16:9 / 9:16 / 1:1", jimeng: "16:9 / 4:3 / 1:1 / 3:4 / 9:16 / 21:9", vidu: "16:9 / 9:16 / 3:4 / 4:3 / 1:1",
  seedance: "16:9 / 4:3 / 1:1 / 9:16 / 21:9 / adaptive", luma: "16:9 / 9:16 / 1:1 / 4:3 / 21:9", minimax_v2: "16:9 / 9:16 / adaptive"
};
const videoResolutionPlaceholder: Record<string, string> = {
  kling: "720p / 1080p", vidu: "540p / 720p / 1080p", seedance: "480p / 720p / 1080p", luma: "540p / 720p / 1080p",
  minimax: "512P / 720P / 768P / 1080P", minimax_v2: "480P / 768P / 2K"
};

function videoParamFields(api: string): MediaParamField[] {
  const fields: MediaParamField[] = [
    { key: "api", label: "接口", placeholder: "OpenAI Sora 格式", options: videoAPIOptions, hint: videoAPIOptions.find(option => option.value === api)?.hint }
  ];
  if (api === "custom") {
    fields.push(
      { key: "template", label: "套用预设", placeholder: "不套用", options: [{ value: "", label: "不套用" }, ...videoAPIOptions.filter(option => videoTaskAPIs.has(option.value) && option.value !== "custom").map(({ value, label }) => ({ value, label }))], hint: "下面留空的字段沿用预设" },
      { key: "auth", label: "鉴权", placeholder: "Bearer", options: [{ value: "", label: "默认" }, { value: "bearer", label: "Bearer" }, { value: "header", label: "自定义头" }, { value: "kling", label: "可灵 JWT（AK:SK）" }, { value: "volc_v4", label: "火山 V4 签名（AK:SK）" }, { value: "none", label: "不鉴权" }] },
      { key: "auth_header", label: "鉴权头", placeholder: "Authorization" },
      { key: "auth_prefix", label: "Key 前缀", placeholder: "如 Token，自动补空格" },
      { key: "submit_method", label: "提交方法", placeholder: "POST", options: [{ value: "", label: "POST" }, { value: "PUT", label: "PUT" }] },
      { key: "submit_path", label: "提交路径", placeholder: "接在提供商地址后面，如 v1/videos" },
      { key: "submit_body", label: "提交请求体", placeholder: '{"model":"{{model}}","prompt":"{{prompt}}","duration":{{seconds}}}', multiline: true, hint: "可用 {{prompt}} {{model}} {{seconds}} {{size}} {{width}} {{height}} {{ratio}} {{resolution}} {{frames}}；变量为空时整项省略" },
      { key: "image_submit_path", label: "图生视频路径", placeholder: "留空沿用提交路径" },
      { key: "image_submit_body", label: "图生视频请求体", placeholder: '{"prompt":"{{prompt}}","image":"{{image}}"}', multiline: true, hint: "{{image}} 是链接或 data URL，{{image_raw}} 是链接或裸 Base64，另有 {{image_base64}} {{image_mime}}" },
      { key: "id_path", label: "任务 ID 位置", placeholder: "data.task_id" },
      { key: "status_method", label: "查询方法", placeholder: "GET", options: [{ value: "", label: "GET" }, { value: "POST", label: "POST" }] },
      { key: "status_path", label: "查询路径", placeholder: "v1/tasks/{{id}}" },
      { key: "status_body", label: "查询请求体", placeholder: "GET 留空", multiline: true },
      { key: "status_field", label: "状态位置", placeholder: "data.status" },
      { key: "status_map", label: "状态对照", placeholder: "succeed=completed, failed=failed, processing=in_progress", hint: "统一状态是 queued / in_progress / completed / failed；没列的按常见状态名猜" },
      { key: "video_url_path", label: "成品地址位置", placeholder: "data.videos.0.url", hint: "数组可写 outputs[type=video].url；多个位置用 | 隔开" },
      { key: "error_path", label: "失败原因位置", placeholder: "data.message" },
      { key: "code_path", label: "业务码位置", placeholder: "code，HTTP 200 里也报错的厂商才填" },
      { key: "code_ok", label: "成功业务码", placeholder: "0" },
      { key: "code_message_path", label: "业务错误说明", placeholder: "message" },
      { key: "download_auth", label: "下载带鉴权", placeholder: "不带", options: [{ value: "", label: "默认" }, { value: "false", label: "不带" }, { value: "true", label: "带" }] }
    );
  }
  fields.push({ key: "size", label: "分辨率", placeholder: "1280x720" });
  if (videoTaskAPIs.has(api) && api !== "runway") {
    fields.push(
      { key: "ratio", label: "画面比例", placeholder: videoRatioPlaceholder[api] ?? "留空按分辨率推算", hint: "模型自己给了尺寸时按尺寸推算" },
      { key: "resolution", label: "分辨率档位", placeholder: videoResolutionPlaceholder[api] ?? "留空按分辨率推算，如 720p" }
    );
  }
  fields.push(
    { key: "seconds", label: "默认时长（秒）", placeholder: "由服务决定" },
    { key: "timeout_seconds", label: "任务总时限（秒）", placeholder: "600" },
    { key: "poll_interval_seconds", label: "轮询间隔（秒）", placeholder: "5" }
  );
  return fields;
}

export function mediaParamFields(role: string, api: string, builtin = false): MediaParamField[] {
  if (role === "video") return videoParamFields(api);
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
    { key: "send_mode", label: "发送模式", placeholder: "按需", options: [{ value: "", label: "按需：用户要语音时才发" }, { value: "always", label: "常驻：回复都用语音发" }], hint: "常驻时带链接、图片、@ 的回复仍发文字；仅 QQ。" },
    { key: "max_chars", label: "单次字数上限", placeholder: "500" }
  );
  return fields;
}


// 新建语音提供商时先选服务。地址和后端 model/llm/speech*.go 里的默认地址一致：留空就走这里，
// 所以界面只拿它当占位提示；本机服务没有 Key 也得填一个，因为配置保存要求 Key 非空。
export type SpeechServiceOption = { value: string; label: string; hint: string; baseURL: string; local?: boolean; stt?: boolean };
export const speechServices: SpeechServiceOption[] = [
  { value: "openai", label: "OpenAI 兼容", hint: "OpenAI、硅基流动、Kokoro-FastAPI 等走 /audio/speech 的服务", baseURL: "https://api.openai.com/v1", stt: true },
  { value: "minimax", label: "MiniMax", hint: "国内账号把域名换成 api.minimaxi.com", baseURL: "https://api.minimax.io/v1" },
  { value: "volcengine", label: "火山引擎（豆包语音）", hint: "还要在下方填 App ID", baseURL: "https://openspeech.bytedance.com/api/v1/tts" },
  { value: "dashscope", label: "阿里云百炼", hint: "qwen-tts、CosyVoice、Sambert", baseURL: "https://dashscope.aliyuncs.com" },
  { value: "elevenlabs", label: "ElevenLabs", hint: "", baseURL: "https://api.elevenlabs.io/v1", stt: true },
  { value: "fishaudio", label: "Fish Audio", hint: "", baseURL: "https://api.fish.audio/v1" },
  { value: "azure", label: "Azure 语音", hint: "地址留空时按下方填的区域拼官方地址", baseURL: "" },
  { value: "gemini", label: "Gemini TTS", hint: "", baseURL: "https://generativelanguage.googleapis.com/v1beta" },
  { value: "mimo", label: "小米 MiMo", hint: "", baseURL: "https://api.xiaomimimo.com/v1", stt: true },
  { value: "breeze", label: "Breeze", hint: "", baseURL: "https://api.breeze.blue/v1" },
  { value: "novelai", label: "NovelAI", hint: "", baseURL: "https://api.novelai.net" },
  { value: "gpt_sovits", label: "GPT-SoVITS v2（本机）", hint: "api_v2 接口", baseURL: "http://127.0.0.1:9880", local: true },
  { value: "gsvi", label: "GPT-SoVITS-Inference（本机）", hint: "GSVI 整合包", baseURL: "http://127.0.0.1:8000", local: true },
  { value: "custom_http", label: "自定义请求模板", hint: "AllTalk、XTTS、VITS、Chatterbox 等自建服务", baseURL: "", local: true }
];

// speechAPILabel 是接口形态的显示名，不带括号里的路径说明，供列表摘要用。
export function speechAPILabel(role: "tts" | "stt", api: string): string {
  if (api === "whisper_cli") return "本机 Whisper";
  const option = (role === "stt" ? transcriptionAPIOptions : speechAPIOptions).find((item) => item.value === api);
  return (option?.label ?? api).replace(/（\/audio\/[a-z]+）$/, "");
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

export function speechServiceFields(role: "tts" | "stt" | "video", params: Record<string, string>, baseURL: string): MediaParamField[] {
  // 视频接口不按地址猜，没选就是 Sora 格式。
  if (role === "video") return mediaParamFields("video", params.api ?? "");
  return mediaParamFields(role, resolveSpeechAPI(params.api, baseURL)).filter(field => !isSpeechBehaviorParam(field.key));
}

export function updateSpeechParam(params: Record<string, string>, key: string, value: string): Record<string, string> {
  const next = { ...params };
  // 视频的「套用预设」也叫 template，值是预设名，照普通参数存。
  const template = key === "template" ? customSpeechTemplates.find(item => item.value === value) : undefined;
  if (template) {
    for (const field of ["method", "path", "body_type", "body", "response"]) delete next[field];
    return { ...next, ...template.params };
  }
  if (value.trim()) next[key] = value.trim();
  else delete next[key];
  return next;
}
