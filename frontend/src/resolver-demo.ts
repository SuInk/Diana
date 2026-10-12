// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { CredentialCheck, PluginManifest } from "./api";

const platformOptions = [
  { value: "bilibili", label: "Bilibili（可下载媒体）" },
  { value: "youtube", label: "YouTube（可下载媒体）" },
  { value: "x", label: "X / Twitter（可下载媒体）" },
  { value: "xiaohongshu", label: "小红书（可下载媒体）" },
  { value: "zhihu", label: "知乎（仅标题）" },
  { value: "weibo", label: "微博（仅标题）" },
  { value: "douyin", label: "抖音（可下载媒体）" },
  { value: "github", label: "GitHub（仅标题）" }
];

export const resolverDemoManifest: PluginManifest & { can_ask_agent: boolean } = {
  id: "official.nonebot-plugin-resolver-go", name: "链接解析", version: "0.3.8",
  description: "官方内置 Go 社交媒体解析器，可提取并发送 B 站、YouTube、X、小红书和抖音的图片或视频，并展开 X 站内长文正文。",
  official: true, built_in: true, can_ask_agent: true,
  permissions: ["network:http", "message:read", "message:write", "filesystem:temp", "process:media"],
  settings: [
    { key: "ask_agent", label: "允许 Agent 回复", description: "允许链接解析结果主动进入正式 Agent 回复流程；关闭后仍会解析和发送确定性结果。", type: "bool", default: true },
    { key: "follow_up_comment", label: "发送后自然点评", description: "解析结果发出后，让机器人按全局回复风格自然回应；关闭后只发解析结果。", type: "bool", default: false },
    { key: "download_media", label: "下载并发送媒体", description: "识别支持的平台后下载视频或提取图集，并通过当前机器人发送；关闭后只提供网页上下文。", type: "bool", default: true },
    { key: "merged_forward", label: "合并转发发送", description: "开启后把图集和多段内容打包成合并转发；关闭后按原来的普通消息发送，不生成转发卡片。", type: "bool", default: true },
    { key: "max_video_mb", label: "视频大小上限", description: "下载和发送的单个视频最大体积。", type: "number", default: 200, min: 5, max: 500, step: 5, unit: "MB" },
    { key: "max_video_duration_seconds", label: "视频时长上限", description: "超过该时长的视频只返回元数据，不执行下载。", type: "number", default: 900, min: 30, max: 3600, step: 30, unit: "秒" },
    { key: "max_images", label: "图集发送上限", description: "单条社交链接最多发送的图片数量。", type: "number", default: 9, min: 1, max: 20, step: 1, unit: "张" },
    { key: "platform_level_rules", label: "平台群等级规则", description: "按平台限制 QQ 群成员触发链接解析的最低群等级；规则从上到下匹配。", type: "platform_level_rules", default: [{ platform: "x", minimum_level: 40, unknown_policy: "deny", owner_bypass: true, mention_bypass: false, enabled: true }], options: platformOptions },
    { key: "max_video_height", label: "视频清晰度上限", description: "下载时选择的最高分辨率。调高后建议同步放宽「视频大小上限」，否则容易下完才因超限被丢弃。", type: "select", default: "720", options: [{ value: "480", label: "480p" }, { value: "720", label: "720p（默认）" }, { value: "1080", label: "1080p" }, { value: "0", label: "不限制（可能很大）" }] },
    { key: "fetch_title", label: "抓取网页标题", description: "关闭后只识别链接平台，不再请求网页获取标题。", type: "bool", default: true },
    { key: "max_links", label: "单条消息解析上限", description: "一条消息里最多解析多少个链接，超出部分忽略。", type: "number", default: 5, min: 1, max: 20, step: 1, unit: "个" },
    { key: "timeout_seconds", label: "单链接抓取超时", description: "抓取单个网页标题的最长等待时间。", type: "number", default: 20, min: 2, max: 120, step: 1, unit: "秒" },
    { key: "browser_render", label: "浏览器渲染兜底", description: "直接抓取拿不到内容时，用本机 Chrome（CDP）渲染页面提取标题和摘要，可利用浏览器里的登录态；需要 Chrome 以 --remote-debugging-port 启动。", type: "bool", default: false },
    { key: "browser_cdp_url", label: "Chrome CDP 地址", description: "浏览器渲染兜底连接的 DevTools 地址。", type: "string", default: "http://127.0.0.1:9222" },
    { key: "enabled_platforms", label: "启用平台", description: "勾选的平台会解析链接并进入模型上下文，默认全部启用。关闭平台只停止解析，不清除登录凭据；凭据可提前配置并独立检测。", type: "multi_select", default: platformOptions.map(option => option.value), options: platformOptions },
    { key: "summary_max_runes", label: "摘要长度上限", description: "喂给模型的单条摘要最大字数。", type: "number", default: 140, min: 60, max: 400, step: 10, unit: "字" },
    { key: "cache_ttl_minutes", label: "解析缓存时长", description: "同一链接在缓存有效期内不重复抓取；0 表示关闭缓存。", type: "number", default: 10, min: 0, max: 120, step: 1, unit: "分钟" },
    { key: "user_agent", label: "抓取 User-Agent", description: "直接抓取网页时使用的 UA，留空使用内置 Chrome UA。", type: "string", default: "" },
    { key: "bili_sessdata", label: "B 站 SESSDATA", description: "所有机器人共用的 B 站 SESSDATA，只填值。已配置时留空沿用，点击清除后保存才删除。", type: "string", default: "", secret: true },
    { key: "douyin_cookie", label: "抖音 Cookie", description: "所有机器人共用的抖音 Cookie。已配置时留空沿用，点击清除后保存才删除；不配置时无法解析需要登录的内容。", type: "string", default: "", secret: true },
    { key: "xhs_cookie", label: "小红书 Cookie", description: "所有机器人共用的小红书 Cookie。已配置时留空沿用，点击清除后保存才删除；不配置时无法解析需要登录的内容。", type: "string", default: "", secret: true },
    { key: "ytdlp_cookies_path", label: "yt-dlp Cookie 文件路径", description: "共享的 Netscape 格式 Cookie 文件路径。检测只检查文件可读性与格式，不代表已登录。", type: "string", default: "" },
    { key: "proxy_url", label: "解析代理", description: "所有机器人解析与 yt-dlp 共用的代理地址，例如 http://127.0.0.1:7890。", type: "string", default: "" },
    { key: "ytdlp_cookies_from_browser", label: "yt-dlp 浏览器凭据来源", description: "所有机器人共用的 yt-dlp 浏览器名称或配置档；留空不读取浏览器 Cookie。", type: "string", default: "" }
  ]
};

const credentials = [
  { key: "bili_sessdata", label: "B 站 SESSDATA" },
  { key: "douyin_cookie", label: "抖音 Cookie" },
  { key: "xhs_cookie", label: "小红书 Cookie" },
  { key: "ytdlp_cookies_path", label: "yt-dlp Cookie 文件" }
];
const secretKeys = resolverDemoManifest.settings!.filter(spec => spec.secret).map(spec => spec.key);

type DemoSettings = Record<string, unknown>;
interface DemoSettingsPayload {
  settings?: DemoSettings;
  clear_secrets?: string[];
}

export function mergeResolverDemoSettings(saved: DemoSettings, payload: DemoSettingsPayload, mode: "test" | "save"): DemoSettings {
  // 测试叠加草稿；保存替换普通设置，仅保留未修改的秘密（恢复默认也遵循此约定）。
  const settings = { ...(mode === "test" ? saved : {}), ...payload.settings };
  const cleared = new Set((payload.clear_secrets ?? []).map(key => key.trim()));
  for (const key of secretKeys) {
    const draft = payload.settings?.[key];
    if (draft === undefined || (typeof draft === "string" && !draft.trim())) {
      if (key in saved) settings[key] = saved[key];
      else delete settings[key];
    }
    if (cleared.has(key)) delete settings[key];
  }
  if (mode === "test") for (const key of cleared) delete settings[key];
  return settings;
}

function waitForDemoProbe(ms: number, signal?: AbortSignal | null): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason ?? new DOMException("Aborted", "AbortError")); return; }
    const abort = () => {
      clearTimeout(timer);
      reject(signal?.reason ?? new DOMException("Aborted", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", abort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", abort, { once: true });
  });
}

// 只识别显式合成输入，不读取文件、不访问账号接口，也不把任意非空输入当作有效凭据。
// demo-valid / demo-invalid / demo-unverified / demo-error / demo-http-error；可加 :slow / :fast。
export async function resolverDemoTestResponse(
  saved: DemoSettings, payload: DemoSettingsPayload, credentialKeys: string[], signal?: AbortSignal | null
): Promise<Response> {
  const credentialKey = credentialKeys[0];
  if (credentialKeys.length > 1 || (credentialKeys.length === 1 && !credentials.some(item => item.key === credentialKey))) {
    return Response.json({ error: "credential_key 必须是一个受支持的凭据键" }, { status: 400 });
  }
  const settings = mergeResolverDemoSettings(saved, payload, "test");
  const selected = credentials.filter(item => credentialKey === undefined || item.key === credentialKey);
  let delay = 0;
  let httpError = false;
  const checks: CredentialCheck[] = selected.map(item => {
    const value = typeof settings[item.key] === "string" ? (settings[item.key] as string).trim() : "";
    if (!value) return { ...item, configured: false, state: "unconfigured", message: "未填写" };
    const fixture = /^demo-(valid|invalid|unverified|error|http-error)(?::(slow|fast))?$/.exec(value);
    delay = Math.max(delay, fixture?.[2] === "slow" ? 2500 : fixture?.[2] === "fast" ? 100 : 650);
    const result: CredentialCheck = { ...item, configured: true, state: "unverified", message: "演示模式不会检测真实凭据。请使用合成输入 demo-valid、demo-invalid、demo-unverified 或 demo-error；请勿填写真实 Cookie。" };
    const file = item.key === "ytdlp_cookies_path";
    if (file && !fixture) {
      result.file_format_checked = false;
      result.message = "演示模式不会读取真实文件，未执行文件格式检查。请使用 demo-valid、demo-invalid 或 demo-unverified 合成输入。";
    }
    switch (fixture?.[1]) {
      case "valid":
        result.state = file ? "unverified" : "valid";
        result.message = file ? "演示：文件可读，是 Netscape 格式；是否仍在登录状态要等 yt-dlp 实际下载时才知道。" : "演示：已登录（合成结果，未访问平台）";
        if (!file) result.account = "合成演示账号";
        break;
      case "invalid":
        result.state = "invalid";
        result.message = file ? "演示：文件不是 Netscape 格式的 Cookie，yt-dlp 读不了。请用浏览器扩展按 Netscape 格式导出。" : "演示：平台明确返回未登录，需要重新复制 Cookie（合成结果，未访问平台）。";
        if (item.key === "xhs_cookie") result.message += "小红书 Cookie 需要包含 HttpOnly 的 web_session；请从开发者工具 Network 面板的 Cookie 请求头整段复制，document.cookie 拿不到它。此处仅为合成长文本，不代表真实检测结果。";
        break;
      case "unverified":
        result.message = file ? "演示：文件格式正常，但不能据此判断站点登录状态。" : "演示：账号接口暂时无法确认登录状态；这不等于 Cookie 已失效。";
        break;
      case "error":
        result.state = "error";
        result.message = "演示：账号接口拒绝了请求（HTTP 429），可能是风控，暂时判断不了凭据好坏。请稍后重试；不要因此清除现有 Cookie。";
        break;
      case "http-error":
        httpError = true;
        break;
    }
    return result;
  });
  await waitForDemoProbe(delay, signal);
  return httpError
    ? Response.json({ error: "演示：检测服务暂时不可用（合成 HTTP 503），请稍后重试。" }, { status: 503 })
    : Response.json({ credentials: checks });
}
