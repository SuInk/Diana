import { reactive, watch } from "vue";
import type { CredentialCheck, PluginSettingSpec } from "./api";

export const resolverPlatforms = [
  { key: "bili_sessdata", platform: "bilibili", label: "B 站" },
  { key: "douyin_cookie", platform: "douyin", label: "抖音" },
  { key: "xhs_cookie", platform: "xiaohongshu", label: "小红书" },
] as const;
export const resolverCredentialKeys = [...resolverPlatforms.map((item) => item.key), "ytdlp_cookies_path"] as const;
export type ResolverCredentialKey = typeof resolverCredentialKeys[number];

const mediaKeys = new Set([
  "follow_up_comment", "download_media", "merged_forward", "max_video_mb",
  "max_video_duration_seconds", "max_images", "platform_level_rules", "max_video_height",
  "fetch_title", "max_links", "summary_max_runes",
]);

export function groupResolverSettings(specs: PluginSettingSpec[]) {
  return {
    platforms: specs.filter((spec) => spec.key === "enabled_platforms"),
    credentials: resolverPlatforms.flatMap((platform) => {
      const spec = specs.find((item) => item.key === platform.key);
      return spec ? [{ ...platform, spec }] : [];
    }),
    media: specs.filter((spec) => mediaKeys.has(spec.key)),
    advanced: specs.filter((spec) => spec.key !== "enabled_platforms" && !resolverPlatforms.some((item) => item.key === spec.key) && !mediaKeys.has(spec.key)),
  };
}

export interface ResolverCredentialInput {
  value: string;
  configured: boolean;
  clearing: boolean;
  savedValue?: string;
}
export interface ResolverCredentialStatus {
  phase: "idle" | "pending" | "checking" | "done" | "failed";
  check?: CredentialCheck;
  error?: string;
}

// 请求归属同时绑定弹窗代次与字段修订；即使 transport 未响应 abort，也不能写回旧结果。
export function createResolverCredentialChecks(options: {
  input: (key: ResolverCredentialKey) => ResolverCredentialInput;
  request: (key: ResolverCredentialKey, signal: AbortSignal) => Promise<CredentialCheck | undefined>;
}) {
  const states = reactive(Object.fromEntries(resolverCredentialKeys.map((key) => [key, { phase: "idle" }]))) as Record<ResolverCredentialKey, ResolverCredentialStatus>;
  let generation = 0;
  let active = false;
  const revisions = new Map<ResolverCredentialKey, number>();
  const attempted = new Map<ResolverCredentialKey, number>();
  const edited = new Set<ResolverCredentialKey>();
  const requests = new Map<ResolverCredentialKey, { controller: AbortController; promise: Promise<void> }>();

  function invalidate(key: ResolverCredentialKey, phase: ResolverCredentialStatus["phase"] = "pending") {
    revisions.set(key, (revisions.get(key) ?? 0) + 1);
    requests.get(key)?.controller.abort();
    requests.delete(key);
    states[key] = { phase };
  }

  const unwatch = resolverCredentialKeys.map((key) => watch(
    [() => options.input(key).value, () => options.input(key).clearing, () => options.input(key).configured],
    () => { edited.add(key); invalidate(key); },
    { flush: "sync" },
  ));

  function pause() {
    active = false;
    generation++;
    for (const key of requests.keys()) {
      invalidate(key);
      attempted.delete(key);
    }
  }

  function resume() {
    active = true;
  }

  function close() {
    active = false;
    generation++;
    for (const key of resolverCredentialKeys) invalidate(key, "idle");
    attempted.clear();
    edited.clear();
  }

  function open() {
    close();
    active = true;
  }

  function test(key: ResolverCredentialKey, trigger: "blur" | "manual" = "manual"): Promise<void> {
    if (!active) return Promise.resolve();
    const input = options.input(key);
    if (input.clearing) return Promise.resolve();
    const revision = revisions.get(key) ?? 0;
    if (trigger === "blur" && (key === "ytdlp_cookies_path" || !edited.has(key) || !input.value.trim() || attempted.get(key) === revision)) return Promise.resolve();
    const running = requests.get(key);
    if (running) return running.promise;
    if (!input.value.trim() && !input.configured) return Promise.resolve();
    attempted.set(key, revision);
    const session = generation;
    const controller = new AbortController();
    const ownsRequest = () => active && generation === session && (revisions.get(key) ?? 0) === revision && requests.get(key)?.controller === controller;
    states[key] = { phase: "checking" };
    // 延后一微任务，先登记归属，兼容同步返回或抛错的请求适配器。
    const promise = Promise.resolve().then(() => {
      if (!ownsRequest()) return undefined;
      return options.request(key, controller.signal);
    }).then((check) => {
      if (!ownsRequest()) return;
      if (!check) throw new Error("检测接口未返回此项结果，请重试");
      states[key] = { phase: "done", check };
    }).catch((error: unknown) => {
      if (!ownsRequest()) return;
      states[key] = { phase: "failed", error: error instanceof Error ? error.message : "请求失败，请重试" };
    }).finally(() => {
      if (ownsRequest()) requests.delete(key);
    });
    requests.set(key, { controller, promise });
    return promise;
  }

  return { states, open, close, pause, resume, test, reset: open, dispose() { close(); unwatch.forEach((stop) => stop()); } };
}

export function resolverCredentialSource(input: ResolverCredentialInput): string {
  if (input.clearing) return "待清除，保存后生效";
  if (input.savedValue !== undefined) return input.value !== input.savedValue ? "未保存" : input.value.trim() ? "已配置" : "未配置";
  if (input.value.trim()) return "未保存";
  return input.configured ? "已配置 · 留空沿用" : "未配置";
}

export function resolverCheckLabel(status: ResolverCredentialStatus, file = false): string {
  if (status.phase === "checking") return file ? "正在检查格式" : "正在检测";
  if (status.phase === "failed") return "请求失败";
  if (status.phase === "pending") return "待检测";
  const check = status.check;
  if (!check) return "尚未检测";
  if (file && check.file_format_checked === false) return "未执行文件检查";
  if (file) return check.state === "unconfigured" ? "未配置文件" : check.state === "error" ? "文件检查失败" : check.state === "invalid" ? "文件格式异常" : "文件格式正常";
  switch (check.state) {
    case "valid": return check.account ? `已登录 · ${check.account}` : "已登录";
    case "invalid": return "未登录或已失效";
    case "unverified": return "暂时无法判断";
    case "error": return "请求失败";
    default: return "未配置";
  }
}
