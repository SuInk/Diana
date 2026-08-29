// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

export type Provider = "openai_compatible" | "gemini" | "anthropic";

export interface LLMRoleBinding {
  bot_id?: string;
  bot_name?: string;
  role: "chat" | "vision" | "intent" | "image";
  role_label: string;
  model: string;
}

export interface LLMConfig {
  id?: string;
  name?: string;
  group?: string;
  description?: string;
  updated_at?: string;
  profiles?: LLMConfig[];
  provider: Provider;
  api_style?: "responses" | "chat_completions";
  api_key?: string;
  api_key_configured?: boolean;
  api_key_preview?: string;
  /** 指向某个已授权登录的提供商。填了它就用授权令牌，API Key 变成可选的兜底。 */
  oauth_provider?: string;
  base_url?: string;
  models?: LLMModelInfo[];
  model: string;
  image_model?: string;
  user_agent?: string;
  headers?: Record<string, string>;
  temperature?: number | null;
  /** 用户手填的覆盖值；0 或缺省表示按当前模型自动判断。 */
  context_window_tokens?: number;
  max_context_tokens?: number;
  /** 只读回显：机器人模型分配里指向这套配置的用途，用来说明改它会影响谁。 */
  role_bindings?: LLMRoleBinding[];
  /** 只读回显：当前模型实际生效的窗口与请求上限，以及窗口的来源。 */
  effective_context_window_tokens?: number;
  effective_max_context_tokens?: number;
  context_window_source?: "user" | "fallback";
  /** 只读回显：模型清单里记的窗口，只作参考值，不参与计算。 */
  catalog_context_window_tokens?: number;
  max_output_tokens?: number;
  timeout_ms?: number;
}

export interface GenerateResponse {
  provider: Provider;
  model?: string;
  text: string;
  usage?: {
    input_tokens?: number;
    output_tokens?: number;
    total_tokens?: number;
  };
}

export interface ImageGenerateResponse {
  provider: Provider;
  model?: string;
  images: string[];
}

export interface LLMModelInfo {
  id: string;
  name?: string;
  object?: string;
  owned_by?: string;
  created?: number;
  input_modalities?: string[];
  output_modalities?: string[];
}

export interface LLMModelsResponse {
  models: LLMModelInfo[];
}

export interface LLMProviderDefinition {
  id: string;
  name: string;
  protocol: "openai-completions" | "openai-responses" | "anthropic-messages" | "gemini" | string;
  baseUrl?: string;
  enabled: boolean;
}

export interface LLMModelDefinition {
  id: string;
  providerId: string;
  modelId: string;
  name: string;
  contextWindow?: number;
  maxTokens?: number;
  capabilities?: Record<string, boolean>;
}

export interface LLMProviderCatalog {
  providers: LLMProviderDefinition[];
  models: LLMModelDefinition[];
}

/** 群聊触发称呼的匹配松紧。loose 出现即触发；smart 在明显是谈论机器人时放行给插话判定；strict 还要求称呼位于句首或句尾。 */
export type AliasTriggerMode = "loose" | "smart" | "strict";

export interface BotProfileConfig {
  id?: string;
  name?: string;
  platform?: string;
  avatar_url?: string;
  active_profile_id?: string;
  profiles?: BotProfileConfig[];
  /** 默认开启；关闭后不同平台可复用相同会话键中的上下文。 */
  isolate_platform_contexts?: boolean;
  enabled: boolean;
  onebot_reverse_ws_endpoint: string;
  onebot_access_token?: string;
  onebot_access_token_configured?: boolean;
  /** Telegram 走官方 Bot API 长轮询，凭据与 OneBot 完全不同。 */
  telegram_bot_token?: string;
  telegram_bot_token_configured?: boolean;
  telegram_api_base_url?: string;
  telegram_proxy_url?: string;
  nonebot_bridge_enabled?: boolean;
  nonebot_bridge_endpoint?: string;
  nonebot_bridge_token?: string;
  nonebot_bridge_token_configured?: boolean;
  bot_account?: string;
  owner_id?: string;
  owner_login_enabled?: boolean;
  owner_llm_config_enabled?: boolean;
  group_triggers?: string[];
  /** 触发称呼的匹配松紧；不设等同 smart。 */
  group_trigger_mode?: AliasTriggerMode;
  disabled_groups?: string[];
  /** 群准入模式与白名单；不设等同 blacklist，行为与旧配置一致。 */
  group_admission?: GroupAdmission;
  /** 全局回复门槛（等级/时段/用户名单）；不设表示无门槛。 */
  reply_gate?: ReplyGate | null;
  welcome_enabled?: boolean;
  welcome_message?: string;
  system_prompt?: string;
  response_mode?: "quiet" | "standard" | "active" | "custom";
  reply_style?: "groupmate" | "assistant" | "gentle" | "lively" | "concise" | "catgirl" | "roleplay";
  /** 机器人怎么称呼自己；留空跟随表达风格自带的说法。 */
  self_reference?: string;
  /** 句尾语气词候选，逗号分隔。填多个由模型按当下语气挑，留空跟随表达风格。 */
  sentence_enders?: string;
  /** 记录完整模型上下文、工具参数和调用结果；默认关闭。 */
  debug_mode_enabled?: boolean;
  /** 回复行为个性化：on 每条都带、off 从不带、auto 交给模型自己判断；缺省等价于 on。 */
  reply_reference_mode?: "on" | "off" | "auto";
  mention_user_mode?: "on" | "off" | "auto";
  markdown_to_plain?: boolean;
  error_notify_enabled?: boolean;
  error_reply_prefix?: string;
  send_retry_attempts?: number;
  send_chunk_interval_ms?: number;
  /** 按用途分配模型：chat/vision/intent/image → 渠道（或渠道分组）+模型。 */
  model_roles?: Record<string, { profile_id?: string; group?: string; model: string; provider_id?: string; model_id?: string }>;
  /** 用模型识别其他机器人的自动回复并阻断机器人互聊；缺省等价于开启。 */
  bot_reply_loop_detection_enabled?: boolean;
  /** 直接回复是否也做发送前账号安全审核；主动回复始终审核，不受此开关影响。 */
  reply_account_safety_audit_enabled?: boolean;
  /** 词典是否跨群共用一本；默认按会话隔离。 */
  glossary_shared_scope_enabled?: boolean;
  /** 提示词增强开关；缺省等价于开启。 */
  prompt_inject_time?: boolean;
  prompt_inject_plaintext_rules?: boolean;
  prompt_inject_group_sender?: boolean;
  prompt_chinese_slang_hint?: boolean;
  prompt_chinese_slang_text?: string;
  prompt_plaintext_rules_text?: string;
  prompt_time_template?: string;
  prompt_group_sender_template?: string;
  prompt_image_only_text?: string;
  prompt_wake_only_text?: string;
  /** 群聊未显式唤醒机器人时，用于判断是否应主动回复。 */
  proactive_reply_router_prompt?: string;
  /** 主动回复路由放行后，注入最终回复模型的生成约束。 */
  proactive_reply_prompt?: string;
  /** 主动回复路由放行后的确定性采样率，范围 0~1。 */
  proactive_reply_chance?: number;
  /** 主动回复最低置信度，范围 0~1，默认 0.9。 */
  proactive_reply_threshold?: number;
  /** 普通群聊只要能生成有效内容就允许自然插话。 */
  natural_interjection_enabled?: boolean;
  max_input_chars?: number;
  max_reply_chars?: number;
  /** 自然分条：按模型自己排的换行把回复分成几条发。关掉后只认 <dianabr>；缺省等价于开启。 */
  natural_reply_split_enabled?: boolean;
  social_reply_enabled?: boolean;
  /** 最多分几条；分出来超过它就退回粗一档，退到底就整条发。 */
  reply_max_bubbles?: number;
  direct_reply_chunk_size?: number;
  /** 正文超过多少字改用合并转发卡片。 */
  forward_reply_threshold?: number;
  /** 切出超过多少块改用合并转发卡片。 */
  forward_reply_chunk_threshold?: number;
  recall_reply_auto_delete_enabled?: boolean;
  recall_reply_auto_delete_delay_seconds?: number;
  max_context_tokens?: number;
  recent_history_token_budget?: number;
  recent_context_limit?: number;
  /** 持久化提取稳定事实、偏好和会话摘要；缺省等价于开启。 */
  long_term_memory_enabled?: boolean;
  /** 允许在同一机器人下检索其他群的非敏感记忆和聊天历史；缺省关闭。 */
  cross_group_memory_enabled?: boolean;
  dict_segment_enabled?: boolean;
  semantic_search_enabled?: boolean;
  max_bot_concurrency?: number;
  request_timeout_ms?: number;
  agent_enabled?: boolean;
  agent_max_steps?: number;
  agent_command_allowlist?: string[];
  agent_command_timeout_ms?: number;
  agent_browser_cdp_url?: string;
  agent_browser_timeout_ms?: number;
}

export interface PluginSettingOption {
  value: string;
  label: string;
}

export interface PluginSettingSpec {
  key: string;
  label: string;
  description?: string;
  type: "bool" | "number" | "string" | "select" | "multi_select" | "text" | "size";
  default: unknown;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  options?: PluginSettingOption[];
  /** 多行文本框的建议行高；模板类设置比默认四行更高。 */
  rows?: number;
  /** 凭据类设置；读接口不返回明文，提交空串表示保持原值。 */
  secret?: boolean;
}

export interface PluginManifest {
  id: string;
  name: string;
  version: string;
  description: string;
  official: boolean;
  built_in: boolean;
  default_disabled?: boolean;
  permissions?: string[];
  settings?: PluginSettingSpec[];
}

export interface PluginState {
  manifest: PluginManifest;
  installed: boolean;
  enabled: boolean;
  /** 用户显式覆盖的设置值，默认值以 manifest.settings 声明为准。 */
  settings?: Record<string, unknown>;
  /** 凭据是否已配置；明文永远不会下发。 */
  secrets_configured?: Record<string, boolean>;
}

export interface RepositoryIssueCreateInput {
  repository: string;
  title: string;
  body?: string;
  labels?: string[];
  allow_duplicate?: boolean;
  confirmation_token?: string;
  candidate_number?: number;
}

export interface RepositoryIssueSummary {
  number: number;
  title: string;
  state: string;
  url: string;
  labels?: string[];
  updated_at?: string;
}

export interface RepositoryIssueCreateResult {
  ok: boolean;
  outcome?: string;
  repository?: string;
  failure_code?: string;
  message: string;
  issue?: RepositoryIssueSummary;
  candidates?: RepositoryIssueSummary[];
  requires_confirmation?: boolean;
  confirmation_token?: string;
  idempotent?: boolean;
  reconciled?: boolean;
  redactions?: number;
}

export interface RepositoryIssueDraft {
  id: string;
  platform?: string;
  profile_id?: string;
  group_id: string;
  repository: string;
  requester_id: string;
  requester_name?: string;
  input: { title?: string; body?: string; labels?: string[] };
  status: "pending" | "created" | "cancelled";
  issue_number?: number;
  issue_url?: string;
  resolved_by?: string;
  created_at: string;
  updated_at: string;
}

export interface ResolverDependency {
  name: string;
  purpose: string;
  available: boolean;
  path?: string;
  version?: string;
  /** 不可用时说明卡在哪一步；没法一键安装的依赖只靠「需手动安装」说不清原因。 */
  detail?: string;
  installable: boolean;
  installer?: string;
}

export interface PluginDependencyResponse {
  /** 按插件 ID 分组，界面据此决定在哪张卡片上显示。 */
  plugins: Record<string, ResolverDependency[]>;
}

export interface ResolverDependencyInstallResponse {
  dependency: ResolverDependency;
  /** 按插件 ID 分组，只包含这次受影响的那一组。 */
  plugins: Record<string, ResolverDependency[]>;
  installer?: string;
}

export interface BotGroupConfig {
  bot_profile_id?: string;
  group_id: string;
  enabled: boolean;
  enabled_set?: boolean;
  group_triggers?: string[];
  /** 本群触发称呼的匹配松紧；空串或不设表示沿用全局配置。 */
  group_trigger_mode?: AliasTriggerMode | "";
  /** 群专属人设；留空沿用全局系统提示词。 */
  system_prompt?: string;
  /** 留空时跟随机器人全局回复模式。 */
  response_mode?: "" | "quiet" | "standard" | "active" | "custom";
  /** 留空时跟随机器人全局表达风格。 */
  reply_style?: "" | "groupmate" | "assistant" | "gentle" | "lively" | "concise" | "catgirl" | "roleplay";
  /** 留空时跟随机器人全局设置。 */
  self_reference?: string;
  sentence_enders?: string;
  welcome_enabled?: boolean;
  welcome_message?: string;
  max_context_tokens?: number;
  recent_history_token_budget?: number;
  recent_context_limit?: number;
  max_reply_chars?: number;
  /** 本群的自然分条开关；不设表示跟随机器人。 */
  natural_reply_split_enabled?: boolean;
  /** 本群最多分几条。 */
  reply_max_bubbles?: number;
  /** 本群单条聊天消息的字数硬上限。 */
  direct_reply_chunk_size?: number;
  /** 本群正文超过多少字改用合并转发卡片。 */
  forward_reply_threshold?: number;
  /** 本群切出超过多少块改用合并转发卡片。 */
  forward_reply_chunk_threshold?: number;
  proactive_reply_chance?: number;
  proactive_reply_threshold?: number;
  /** 本群是否开启自然插话模式。 */
  natural_interjection_enabled?: boolean;
  minimum_reply_member_level?: number;
  /** 查看撤回消息后的回复是否自动撤回。 */
  recall_reply_auto_delete_enabled?: boolean;
  /** 自动撤回前的保留时间，单位为秒。 */
  recall_reply_auto_delete_delay_seconds?: number;
  plugin_overrides?: Record<string, boolean>;
  /** 按插件、按字段保存的群级非密钥设置覆盖；缺失字段沿用全局。 */
  plugin_setting_overrides?: Record<string, Record<string, unknown>>;
  /** 本群专属回复时间、屏蔽账号与准入门槛；不设表示跟随全局。 */
  reply_gate?: ReplyGate | null;
  updated_at?: string;
}

export interface BotGroupSummary extends BotGroupConfig {
  group_name?: string;
  avatar_url?: string;
  member_count?: number;
  max_member_count?: number;
  configured: boolean;
  joined: boolean;
}

/** 群准入模式：blacklist 为默认（除禁用群外都工作），whitelist 只在指定群工作。 */
export type GroupAdmissionMode = "blacklist" | "whitelist";

export interface GroupAdmission {
  mode?: GroupAdmissionMode;
  allowed_groups?: string[];
}

export interface ReplyGate {
  /** 群等级门槛，0 表示不限。 */
  min_group_level?: number;
  /** 等级拿不到时的策略，默认 allow（放行）。 */
  level_unknown_policy?: "allow" | "deny";
  exempt_users?: string[];
  blocked_users?: string[];
  active_hours_enabled?: boolean;
  /** HH:MM；结束早于开始表示跨夜。 */
  active_start?: string;
  active_end?: string;
  /** IANA 时区名，留空用服务器本地时区。 */
  timezone?: string;
  /** 静默期主人是否仍可用，默认 true。 */
  owner_bypass?: boolean | null;
  /** 静默期提示语，留空表示完全不出声。 */
  quiet_reply?: string;
}

export interface BotGroupAdminChallengeResponse {
  group_id: string;
  user_id: string;
  expires_at: string;
  message: string;
}

export interface BotGroupAdminConfigResponse {
  group_id: string;
  user_id?: string;
  token?: string;
  expires_at?: string;
  config: BotGroupConfig;
  plugins: PluginState[];
}

export interface UpdateStatus {
  root: string;
  branch?: string;
  remote_name?: string;
  remote_url?: string;
  head_commit?: string;
  head_subject?: string;
  dirty: boolean;
  ahead?: number;
  behind?: number;
  upstream?: string;
	updating?: boolean;
	last_fetched_at?: string;
  last_update_at?: string;
  last_update_text?: string;
	last_update_status?: "downloaded" | "healthy" | "rolled_back" | "failed" | string;
	last_update_version?: string;
	last_update_error?: string;
	update_available?: boolean;
	restart_required?: boolean;
	download_ready?: boolean;
	downloaded_version?: string;
	downloaded_at?: string;
	update_phase?: "preparing" | "checksum" | "downloading" | "extracting" | "ready";
	download_percent?: number;
	downloaded_bytes?: number;
	download_total?: number;
}

export interface UpdateResult {
  status: UpdateStatus;
  fetched: boolean;
  updated: boolean;
  forced?: boolean;
  applied?: boolean;
  restart_required?: boolean;
	downloaded?: boolean;
  previous_commit?: string;
  target_commit?: string;
  output?: string;
  at: string;
}

export type AppLogKind = "operation" | "error" | "debug";
export type AppLogLevel = "info" | "error";

export interface AppLogEntry {
  id: string;
  kind: AppLogKind;
  level: AppLogLevel;
  action: string;
  message: string;
  detail?: string;
  actor?: string;
  target?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface AppLogsResponse {
  logs: AppLogEntry[];
}

export interface BotEvent {
  at: string;
  kind: string;
  platform?: string;
  profile_id?: string;
  user_id?: string;
  sender_name?: string;
  group_id?: string;
  message_id?: string;
  text?: string;
  reply?: string;
  error?: string;
  handled: boolean;
  outcome?: string;
  decision?: "replied" | "not_replied" | "pending" | "error" | string;
  reason?: string;
  duration_ms?: number;
}

export interface BotChannelStatus {
  profile_id?: string;
  platform?: string;
  name?: string;
  connected: boolean;
  account_status_known?: boolean;
  account_online?: boolean;
  account_good?: boolean;
  account_status_message?: string;
  endpoint: string;
  self_id?: string;
  last_error?: string;
  connection_epoch?: number;
  connection_owner?: string;
  duplicate_connections?: number;
  last_rejected_client?: string;
  last_connection_event?: string;
  last_connection_event_time?: string;
  updated_at: string;
}

export interface BotStatus {
  running: boolean;
  config: BotProfileConfig;
  channel: BotChannelStatus;
  channels?: BotChannelStatus[];
  nonebot_bridge: {
    enabled: boolean;
    connected: boolean;
    endpoint?: string;
    last_error?: string;
    updated_at: string;
  };
  plugins: PluginState[];
  recent_events?: BotEvent[];
  active_workers: number;
  /** 正在跑的后台子任务（生成图片、文档 OCR 等）。 */
  subagent_tasks?: SubagentTask[];
  active_subagent_tasks?: number;
  /** 入站队列积压。排查「机器人怎么不理我」最直接的指标。 */
  pending_events?: number;
  last_error?: string;
  updated_at: string;
}

/** 运行中的后台子任务。跑完即从状态里消失，历史记录见事件详情的 subtasks。 */
export interface SubagentTask {
  id: string;
  kind: string;
  name: string;
  phase: string;
  completed?: number;
  total?: number;
  started_at: string;
  updated_at: string;
}

export interface OneBotGroupTestResponse {
  group_id: string;
  message?: string;
  message_id?: string;
  sent: boolean;
  send_result?: Record<string, unknown>;
  channel: BotStatus["channel"];
  recent_events?: NonNullable<BotStatus["recent_events"]>;
  status: BotStatus;
}

export interface BotFeatureFlags {
  group_test: boolean;
}

export interface BotPlatform {
  id: string;
  name: string;
  protocol: string;
  /** 聊天平台本身，用于分组；同一分类下通常只是不同协议实现。 */
  category: string;
  category_label: string;
  description?: string;
}

const inflightRequests = new Map<string, Promise<unknown>>();
const responseCache = new Map<string, { at: number; data: unknown }>();

function requestPath(url: string): string {
  const [path] = url.split("?");
  return path || url;
}

function requestSearch(url: string): string {
  return url.includes("?") ? url.slice(url.indexOf("?") + 1) : "";
}

function isCacheableRead(method: string, path: string): boolean {
  return method === "GET" || path === "/api/llm/models";
}

function isMutatingRequest(method: string, path: string): boolean {
  return method !== "GET" && method !== "HEAD" && path !== "/api/llm/models";
}

function cacheTTL(method: string, url: string): number {
  const path = requestPath(url);
  const search = requestSearch(url);
  if (search.includes("refresh=1") || search.includes("include_secrets=true")) return 0;
  if (method === "POST" && path === "/api/llm/models") return 30_000;
  if (method !== "GET") return 0;
  switch (path) {
    case "/api/auth/status":
    case "/api/health":
      return 5_000;
    case "/api/system/version":
    case "/api/assistant/platforms":
    case "/api/assistant/features":
      return 60_000;
    case "/api/llm/config":
    case "/api/assistant/config":
    case "/api/assistant/plugins":
      return 4_000;
    case "/api/assistant/plugins/dependencies":
      return 15_000;
    case "/api/assistant/groups":
      return 8_000;
    // 昵称基本不变，缓存久一点，编辑器里几行私聊对象就不用各打一次请求了。
    case "/api/assistant/user-names":
      return 60_000;
    case "/api/assistant/tasks":
      return 3_000;
    case "/api/assistant/status":
    case "/api/stats":
      return 2_000;
    default:
      return 0;
  }
}

function requestCacheKey(method: string, url: string, body?: BodyInit | null): string {
  return `${method} ${url} ${typeof body === "string" ? body : ""}`;
}

function invalidateAPICache(): void {
  responseCache.clear();
}

// ApiError 把「后端根本没答话」和「后端答了但不同意」分开。两者混在一起时，
// 后端挂掉会被登录页当成密码错误报出来——用户拿着对的密码被告知密码不对。
export type ApiErrorKind = "offline" | "server" | "auth" | "request";

export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  readonly status: number;

  constructor(message: string, kind: ApiErrorKind, status = 0) {
    super(message);
    this.name = "ApiError";
    this.kind = kind;
    this.status = status;
  }

  // unreachable 表示这次请求压根没拿到后端的判断：网络层没通，或者网关替它回了话。
  // 密码对不对，这种时候没有任何人验证过。
  get unreachable(): boolean {
    return this.kind === "offline" || this.kind === "server";
  }
}

export function isBackendUnreachable(err: unknown): boolean {
  return err instanceof ApiError && err.unreachable;
}

function apiErrorForStatus(status: number, message: string): ApiError {
  if (status >= 500) {
    return new ApiError(message || `后端出错（HTTP ${status}）`, "server", status);
  }
  if (status === 401 || status === 403) {
    return new ApiError(message || `HTTP ${status}`, "auth", status);
  }
  return new ApiError(message || `HTTP ${status}`, "request", status);
}

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase();
  const path = requestPath(url);
  const key = requestCacheKey(method, url, init?.body);
  const ttl = cacheTTL(method, url);
  if (isCacheableRead(method, path)) {
    const cached = responseCache.get(key);
    if (cached && ttl > 0 && Date.now() - cached.at < ttl) {
      return cached.data as T;
    }
    const pending = inflightRequests.get(key);
    if (pending) {
      return pending as Promise<T>;
    }
  }

  const pending = (async () => {
    let response: Response;
    try {
      response = await fetch(url, {
        headers: {
          "Content-Type": "application/json",
          ...(init?.headers ?? {})
        },
        ...init
      });
    } catch {
      // fetch 只在网络层失败时抛：服务没起、端口不通、被拦下来了。
      throw new ApiError("连不上 Diana 后端服务", "offline");
    }
    const data = (await response.json().catch(() => ({}))) as T & { error?: string; auth_required?: boolean };
    if (!response.ok) {
      // 会话过期或未登录：广播事件让 App 切到登录界面，而不是每个视图各自报错。
      if (response.status === 401 && data.auth_required && !url.startsWith("/api/auth/")) {
        window.dispatchEvent(new CustomEvent("diana:unauthorized"));
      }
      throw apiErrorForStatus(response.status, data.error ?? "");
    }
    if (isMutatingRequest(method, path)) {
      invalidateAPICache();
    } else if (ttl > 0) {
      responseCache.set(key, { at: Date.now(), data });
    }
    return data;
  })();

  if (isCacheableRead(method, path)) {
    inflightRequests.set(key, pending);
  }
  try {
    return (await pending) as T;
  } finally {
    inflightRequests.delete(key);
  }
}

export interface AuthStatus {
  auth_required: boolean;
  authenticated: boolean;
  username?: string;
}

export function getAuthStatus(): Promise<AuthStatus> {
  return requestJSON<AuthStatus>("/api/auth/status");
}

export function login(username: string, password: string): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>("/api/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password })
  });
}

export function logout(): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>("/api/auth/logout", { method: "POST" });
}

export function changeCredentials(currentPassword: string, newUsername: string, newPassword: string): Promise<{ ok: boolean; username: string }> {
  return requestJSON<{ ok: boolean; username: string }>("/api/auth/password", {
    method: "POST",
    body: JSON.stringify({ current_password: currentPassword, new_username: newUsername, new_password: newPassword })
  });
}

export interface AuthSession {
  id: string;
  device_name: string;
  user_agent?: string;
  ip_address?: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  current: boolean;
}

export function listAuthSessions(): Promise<{ sessions: AuthSession[] }> {
  return requestJSON<{ sessions: AuthSession[] }>("/api/auth/sessions");
}

export function revokeAuthSession(id: string): Promise<{ revoked: boolean; current: boolean }> {
  return requestJSON<{ revoked: boolean; current: boolean }>(`/api/auth/sessions/${encodeURIComponent(id)}`, {
    method: "DELETE"
  });
}

export function revokeOtherAuthSessions(): Promise<{ revoked: number }> {
  return requestJSON<{ revoked: number }>("/api/auth/sessions/revoke-others", { method: "POST" });
}

export interface OpenAPIKey {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string;
}

export function listOpenAPIKeys(): Promise<{ keys: OpenAPIKey[] }> {
  return requestJSON<{ keys: OpenAPIKey[] }>("/api/openapi/keys");
}

/** 返回值里的 token 是唯一一次能拿到的密钥明文，之后任何接口都查不到。 */
export function createOpenAPIKey(name: string): Promise<{ key: OpenAPIKey; token: string }> {
  return requestJSON<{ key: OpenAPIKey; token: string }>("/api/openapi/keys", {
    method: "POST",
    body: JSON.stringify({ name })
  });
}

export function revokeOpenAPIKey(id: string): Promise<{ revoked: boolean }> {
  return requestJSON<{ revoked: boolean }>(`/api/openapi/keys/${encodeURIComponent(id)}`, {
    method: "DELETE"
  });
}

export interface OwnerLoginStatus {
  available: boolean;
}

export function getOwnerLoginStatus(): Promise<OwnerLoginStatus> {
  return requestJSON<OwnerLoginStatus>("/api/auth/owner/status");
}

export interface OwnerLoginPairing {
  ok: boolean;
  code: string;
  poll_token: string;
  expires_in_seconds: number;
}

export interface OwnerLoginPairingStatus {
  approved: boolean;
  expired?: boolean;
  expires_in_seconds?: number;
}

export function createOwnerLoginPairing(): Promise<OwnerLoginPairing> {
  return requestJSON<OwnerLoginPairing>("/api/auth/owner/pair", { method: "POST" });
}

export function claimOwnerLoginPairing(code: string): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>("/api/auth/owner/pair/claim", {
    method: "POST",
    body: JSON.stringify({ code })
  });
}

export function pollOwnerLoginPairing(pollToken: string): Promise<OwnerLoginPairingStatus> {
  return requestJSON<OwnerLoginPairingStatus>("/api/auth/owner/pair/status", {
    method: "POST",
    body: JSON.stringify({ poll_token: pollToken })
  });
}

export function getConfig(includeSecrets = false): Promise<LLMConfig> {
  const suffix = includeSecrets ? "?include_secrets=true" : "";
  return requestJSON<LLMConfig>(`/api/llm/config${suffix}`);
}

export function exportConfig(): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config/export");
}

export function saveConfig(config: LLMConfig): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config", {
    method: "POST",
    body: JSON.stringify(config)
  });
}

export function reorderConfigProfiles(ids: string[]): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config/reorder", {
    method: "POST",
    body: JSON.stringify({ ids })
  });
}

export function cloneConfigProfile(id: string): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config/clone", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function deleteConfigProfile(id: string): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function importConfigProfiles(payload: Pick<LLMConfig, "profiles">): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config/import", {
    method: "POST",
    body: JSON.stringify(payload)
  });
}

export function testLLM(message: string, config?: LLMConfig): Promise<GenerateResponse> {
  return requestJSON<GenerateResponse>("/api/llm/test", {
    method: "POST",
    body: JSON.stringify({ ...(config || {}), message })
  });
}

export interface PersonaGenerateResponse {
  persona: string;
  model?: string;
  provider?: string;
}

/** 用当前已配置的模型把一句话需求写成基础人设；带上 current 时是改写而不是重写。 */
export function generatePersona(
  description: string,
  name?: string,
  current?: string,
  options?: { reply_style?: string; response_mode?: string; profile_id?: string; group?: string; model?: string }
): Promise<PersonaGenerateResponse> {
  return requestJSON<PersonaGenerateResponse>("/api/llm/persona", {
    method: "POST",
    body: JSON.stringify({ description, name, current, ...(options ?? {}) })
  });
}

export function testLLMImage(prompt: string, config?: LLMConfig): Promise<ImageGenerateResponse> {
  return requestJSON<ImageGenerateResponse>("/api/llm/test", {
    method: "POST",
    body: JSON.stringify({ ...(config || {}), message: prompt, mode: "image" })
  });
}

export function listLLMModels(config: LLMConfig): Promise<LLMModelsResponse> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), 8_000);
  return requestJSON<LLMModelsResponse>("/api/llm/models", {
    method: "POST",
    body: JSON.stringify(config),
    signal: controller.signal
  }).finally(() => window.clearTimeout(timer));
}

export function getLLMProviderCatalog(): Promise<LLMProviderCatalog> {
  return requestJSON<LLMProviderCatalog>("/api/llm/providers");
}

export function listProviderModels(providerId: string): Promise<LLMModelsResponse> {
  return requestJSON<LLMModelsResponse>("/api/llm/providers/models", {
    method: "POST",
    body: JSON.stringify({ providerId })
  });
}

export function testProviderModel(providerId: string, modelId: string, message: string): Promise<GenerateResponse> {
  return requestJSON<GenerateResponse>("/api/llm/providers/test", {
    method: "POST",
    body: JSON.stringify({ providerId, modelId, message })
  });
}

// includeSecrets 只在配置页显式点「查看」时才带上：常规拉取不需要把 token
// 一起搬到前端，但主人本来就有权改这些凭据，要看时得能看到。
export function getBotProfileConfig(includeSecrets = false): Promise<BotProfileConfig> {
  const suffix = includeSecrets ? "?include_secrets=true" : "";
  return requestJSON<BotProfileConfig>(`/api/assistant/config${suffix}`);
}

export function getBotPlatforms(): Promise<{ platforms: BotPlatform[] }> {
  return requestJSON<{ platforms: BotPlatform[] }>("/api/assistant/platforms");
}

export function saveBotProfileConfig(config: BotProfileConfig): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config", {
    method: "POST",
    body: JSON.stringify(config)
  });
}

export function activateBotProfile(id: string): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/activate", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function cloneBotProfile(id: string): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/clone", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function deleteBotProfile(id: string): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function setBotContextIsolation(enabled: boolean): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/context-isolation", {
    method: "POST",
    body: JSON.stringify({ enabled })
  });
}

export function getBotStatus(): Promise<BotStatus> {
  return requestJSON<BotStatus>("/api/assistant/status");
}

export function startBot(): Promise<BotStatus> {
  return requestJSON<BotStatus>("/api/assistant/start", { method: "POST" });
}

export function stopBot(): Promise<BotStatus> {
  return requestJSON<BotStatus>("/api/assistant/stop", { method: "POST" });
}

export function requestBotBackfill(hours?: number): Promise<{ requested: boolean; window_hours: number }> {
  return requestJSON<{ requested: boolean; window_hours: number }>("/api/assistant/backfill", {
    method: "POST",
    body: JSON.stringify(hours && hours > 0 ? { hours } : {})
  });
}

export function getBotFeatures(): Promise<BotFeatureFlags> {
  return requestJSON<BotFeatureFlags>("/api/assistant/features");
}

export function getOneBotGroupTest(groupID: string): Promise<OneBotGroupTestResponse> {
  const params = new URLSearchParams({ group_id: groupID });
  return requestJSON<OneBotGroupTestResponse>(`/api/assistant/group-test?${params.toString()}`);
}

export function sendOneBotGroupTest(groupID: string, message: string): Promise<OneBotGroupTestResponse> {
  return requestJSON<OneBotGroupTestResponse>("/api/assistant/group-test", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, message })
  });
}

export function listPlugins(): Promise<PluginState[]> {
  return requestJSON<PluginState[]>("/api/assistant/plugins");
}

export function installPlugin(id: string): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/install`, { method: "POST" });
}

export function uninstallPlugin(id: string): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/uninstall`, { method: "POST" });
}

export function setPluginEnabled(id: string, enabled: boolean): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/enabled`, {
    method: "POST",
    body: JSON.stringify({ enabled })
  });
}

export function updatePluginSettings(
  id: string,
  settings: Record<string, unknown>,
  clearSecrets: string[] = []
): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/settings`, {
    method: "POST",
    body: JSON.stringify({ settings, clear_secrets: clearSecrets })
  });
}

export function createRepositoryIssue(input: RepositoryIssueCreateInput): Promise<RepositoryIssueCreateResult> {
  return requestJSON<RepositoryIssueCreateResult>("/api/assistant/plugins/repository-publish/issues", {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function listRepositoryIssueDrafts(status = "all"): Promise<{ drafts: RepositoryIssueDraft[] }> {
  return requestJSON<{ drafts: RepositoryIssueDraft[] }>(`/api/assistant/plugins/repository-publish/drafts?status=${encodeURIComponent(status)}`);
}

export function listPluginDependencies(refresh = false): Promise<PluginDependencyResponse> {
  const suffix = refresh ? "?refresh=1" : "";
  return requestJSON<PluginDependencyResponse>(`/api/assistant/plugins/dependencies${suffix}`);
}

export function installResolverDependency(name: string): Promise<ResolverDependencyInstallResponse> {
  return requestJSON<ResolverDependencyInstallResponse>(
    `/api/assistant/plugins/dependencies/${encodeURIComponent(name)}/install`,
    { method: "POST" }
  );
}

export function requestBotGroupAdminChallenge(groupID: string, userID: string): Promise<BotGroupAdminChallengeResponse> {
  return requestJSON<BotGroupAdminChallengeResponse>("/api/assistant/group-admin/challenge", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID })
  });
}

export function verifyBotGroupAdmin(groupID: string, userID: string, code: string): Promise<BotGroupAdminConfigResponse> {
  return requestJSON<BotGroupAdminConfigResponse>("/api/assistant/group-admin/verify", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID, code })
  });
}

export function getBotGroupAdminConfig(token: string): Promise<BotGroupAdminConfigResponse> {
  return requestJSON<BotGroupAdminConfigResponse>("/api/assistant/group-admin/config", {
    headers: { "X-Diana-Group-Token": token }
  });
}

export function saveBotGroupAdminConfig(token: string, config: BotGroupConfig): Promise<BotGroupAdminConfigResponse> {
  return requestJSON<BotGroupAdminConfigResponse>("/api/assistant/group-admin/config", {
    method: "POST",
    headers: { "X-Diana-Group-Token": token },
    body: JSON.stringify({ config })
  });
}

export function getUpdateStatus(): Promise<UpdateStatus> {
  return requestJSON<UpdateStatus>("/api/system/update");
}

export function pullFromGitHub(force = false): Promise<UpdateResult> {
  return requestJSON<UpdateResult>("/api/system/update", {
    method: "POST",
    body: JSON.stringify(force ? { force: true, confirmation: "force-update" } : { force: false, confirmation: "apply-update" })
  });
}

export function downloadSystemUpdate(force = false): Promise<UpdateResult> {
	return requestJSON<UpdateResult>("/api/system/update/download", {
		method: "POST",
		body: JSON.stringify({ force, confirmation: "download-update" })
	});
}

export function installDownloadedSystemUpdate(): Promise<UpdateResult> {
	return requestJSON<UpdateResult>("/api/system/update/install", {
		method: "POST",
		body: JSON.stringify({ confirmation: "install-restart" })
	});
}

export interface UpdatePolicy {
	auto_download: boolean;
	auto_install: boolean;
	/** 下载加速策略：auto（实测挑线路）、direct（始终直连）或一条具体的镜像地址。 */
	github_mirror?: string;
}

export interface GitHubMirror {
	name: string;
	base_url: string;
}

export interface GitHubMirrorProbe {
	name: string;
	base_url?: string;
	direct?: boolean;
	ok: boolean;
	latency_ms?: number;
	speed_kbps?: number;
	error?: string;
}

export interface GitHubMirrorStatus {
	mode: string;
	mirrors: GitHubMirror[];
	resolved?: string;
	last_probe?: GitHubMirrorProbe[];
}

export function getUpdateMirrors(): Promise<GitHubMirrorStatus> {
	return requestJSON<GitHubMirrorStatus>("/api/system/update/mirrors");
}

export function testUpdateMirrors(): Promise<GitHubMirrorStatus> {
	return requestJSON<GitHubMirrorStatus>("/api/system/update/mirrors/test", { method: "POST" });
}

export function getUpdatePolicy(): Promise<UpdatePolicy> {
	return requestJSON<UpdatePolicy>("/api/system/update/policy");
}

export function saveUpdatePolicy(policy: UpdatePolicy): Promise<UpdatePolicy> {
	return requestJSON<UpdatePolicy>("/api/system/update/policy", {
		method: "PUT",
		body: JSON.stringify(policy)
	});
}

export function restartSystem(): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>("/api/system/restart", {
    method: "POST",
    body: JSON.stringify({ confirmation: "restart-service" })
  });
}

export interface SystemVersion {
  build_version: string;
  build_type?: BuildType;
  version_label: string;
  git_available: boolean;
  deployment_mode: "git" | "release";
  update_supported: boolean;
  update_unsupported_reason?: string;
  head_commit?: string;
  head_subject?: string;
  branch?: string;
  behind?: number;
}

export interface ChangelogEntry {
  sha: string;
  short: string;
  message: string;
  author?: string;
  date?: string;
  url?: string;
}

export interface ReleaseEntry {
  tag: string;
  name?: string;
  notes?: string;
  prerelease?: boolean;
  date?: string;
  url?: string;
  checksum_available: boolean;
  checksum_url?: string;
}

export interface ChangelogResponse {
  repo: string;
  kind: "releases" | "commits";
  entries?: ChangelogEntry[];
  releases?: ReleaseEntry[];
  cached?: boolean;
}

export interface RollbackResponse {
  result: UpdateResult;
}

export interface ConsoleGroupsResponse {
  groups: BotGroupSummary[];
  plugins: PluginState[];
  live_available: boolean;
  warning?: string;
}

export function listBotGroups(refresh = false, profile = ""): Promise<ConsoleGroupsResponse> {
  const params = new URLSearchParams();
  if (refresh) params.set("refresh", "1");
  if (profile) params.set("profile", profile);
  const suffix = params.size > 0 ? `?${params.toString()}` : "";
  return requestJSON<ConsoleGroupsResponse>(`/api/assistant/groups${suffix}`);
}

export function saveBotGroup(config: BotGroupConfig): Promise<{ config: BotGroupConfig }> {
  return requestJSON<{ config: BotGroupConfig }>("/api/assistant/groups", {
    method: "POST",
    body: JSON.stringify({ config })
  });
}

export function rollbackSystem(ref: string): Promise<RollbackResponse> {
  return requestJSON<RollbackResponse>("/api/system/update/rollback", {
    method: "POST",
    body: JSON.stringify({ ref, confirmation: "rollback-version" })
  });
}

// 版本号缓存 60 秒是给频繁读用的；重启之后、升级之后必须拿真值，这两处传
// refresh 绕过缓存（约定见 cacheTTL：带 refresh=1 的请求 TTL 为 0）。
export function getSystemVersion(refresh = false): Promise<SystemVersion> {
  return requestJSON<SystemVersion>(`/api/system/version${refresh ? "?refresh=1" : ""}`);
}

export type BuildType = "release" | "source";

export interface UpdateCheckResponse {
  deployment_mode: "git" | "release";
  current_version: string;
  latest_version?: string;
  latest_published_at?: string;
  checked_at: string;
  update_available: boolean;
  update_supported: boolean;
  /** update_supported 为 false 时说明为什么升不了级。 */
  update_unsupported_reason?: string;
  build_type: BuildType;
  switch_to_release_available: boolean;
  integrity_mode: "git-object-hash" | "sha256";
  checksum_available: boolean;
  checksum_url?: string;
  status?: UpdateStatus;
	policy: UpdatePolicy;
}

export function checkForUpdate(): Promise<UpdateCheckResponse> {
  return requestJSON<UpdateCheckResponse>("/api/system/update/check", { method: "POST" });
}

export function getChangelog(): Promise<ChangelogResponse> {
  return requestJSON<ChangelogResponse>("/api/system/update/changelog");
}

export function listAppLogs(kind?: AppLogKind, limit = 100): Promise<AppLogsResponse> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (kind) {
    params.set("kind", kind);
  }
  return requestJSON<AppLogsResponse>(`/api/logs?${params.toString()}`);
}

export interface StatsHourBucket {
  hour_unix: number;
  total: number;
  handled: number;
  errors: number;
}

export interface StatsBotSummary {
  running: boolean;
  connected: boolean;
  self_id?: string;
  active_workers: number;
  plugins_enabled: number;
  plugins_total: number;
  last_error?: string;
  bridge_enabled: boolean;
  bridge_connected: boolean;
}

export interface StatsServerSummary {
  collected_at: string;
  hostname?: string;
  os: string;
  arch: string;
  process_id: number;
  cpu_model?: string;
  cpu_cores: number;
  cpu_usage_percent?: number;
  process_cpu_percent?: number;
  memory_total_bytes?: number;
  memory_used_bytes?: number;
  memory_usage_percent?: number;
  process_memory_bytes?: number;
  /** Diana 数据目录体积；首次采样跑完前是 0 */
  process_storage_bytes?: number;
  storage_path?: string;
  storage_total_bytes?: number;
  storage_used_bytes?: number;
  storage_available_bytes?: number;
  storage_usage_percent?: number;
  metrics_unavailable_reason?: string;
  process_metrics_unavailable?: string;
  storage_metrics_unavailable?: string;
}

/** 单台机器人的那部分计数，字段名和快照里对应的一致，可以直接覆盖上去。 */
export interface StatsProfileCounters {
  total_events: number;
  handled_events: number;
  error_events: number;
  today_events: number;
  today_handled: number;
  today_errors: number;
  by_kind: Record<string, number>;
  hourly: StatsHourBucket[];
  avg_reply_ms: number;
  last_event_at?: string;
}

export interface StatsSnapshot {
  started_at: string;
  uptime_seconds: number;
  total_events: number;
  handled_events: number;
  error_events: number;
  today_events: number;
  today_handled: number;
  today_errors: number;
  by_kind: Record<string, number>;
  hourly: StatsHourBucket[];
  avg_reply_ms: number;
  last_event_at?: string;
  bot: StatsBotSummary;
  server?: StatsServerSummary;
  /** 每台机器人各自的计数；运行时长、服务器占用这类进程级指标不在里面。 */
  by_profile?: Record<string, StatsProfileCounters>;
}

/**
 * scopeStatsSnapshot 把快照收敛到某台机器人。留空返回原样（全部机器人）；
 * 选中的机器人还没有任何事件时给一份空计数，而不是退回合计——切过去看到别人的
 * 数字比看到 0 更容易让人误判。
 */
export function scopeStatsSnapshot(snapshot: StatsSnapshot | null, profileID: string): StatsSnapshot | null {
  if (!snapshot || !profileID) {
    return snapshot;
  }
  const scoped = snapshot.by_profile?.[profileID];
  if (scoped) {
    return { ...snapshot, ...scoped };
  }
  return {
    ...snapshot,
    total_events: 0,
    handled_events: 0,
    error_events: 0,
    today_events: 0,
    today_handled: 0,
    today_errors: 0,
    by_kind: {},
    hourly: snapshot.hourly.map((bucket) => ({ ...bucket, total: 0, handled: 0, errors: 0 })),
    avg_reply_ms: 0,
    last_event_at: undefined
  };
}

export interface HealthResponse {
  status: string;
  started_at: string;
  uptime_seconds: number;
  version: string;
  repository?: string;
  repository_url?: string;
}

export function getStats(): Promise<StatsSnapshot> {
  return requestJSON<StatsSnapshot>("/api/stats");
}

export type AssistantEventRange = "1h" | "24h" | "7d" | "30d" | "all";
export type AssistantEventResultFilter = "all" | "replied" | "not_replied" | "pending" | "error" | "notice";

export interface AssistantEventImage {
  index: number;
  summary?: string;
  unavailable?: boolean;
}

export interface AssistantEventDetail extends BotEvent {
  id: string;
  sender_name?: string;
  sender_role?: string;
  /** 发言者当时的群等级；回复门槛按等级卡人，排查时要能直接看到。 */
  sender_level?: number;
  sender_level_label?: string;
  sub_type?: string;
  original_time?: string;
  operator_id?: string;
  operator_name?: string;
  operator_role?: string;
  status: string;
  outcome?: string;
  llm_calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
  cached_input_tokens?: number;
  decision: "replied" | "not_replied" | "pending" | "error" | string;
  reason: string;
  delivery_stage?: "generated" | "send_attempted" | "acknowledged" | "echo_persisted" | "failed" | string;
  outbound_message_id?: string;
  reply_generated_at?: string;
  send_attempted_at?: string;
  send_acked_at?: string;
  self_echo_at?: string;
  delivery_error?: string;
  images?: AssistantEventImage[];
  /** 这条消息触发的后台子任务。图片是任务跑完后异步发出去的。 */
  subtasks?: AssistantEventSubtask[];
  /** 这一轮实际发出去的内容概览。reply 只是文本，说不出还发了卡片和媒体。 */
  delivery?: AssistantEventDelivery;
}

export interface AssistantEventDelivery {
  messages?: number;
  images?: number;
  videos?: number;
  audios?: number;
  forward_cards?: number;
  forward_nodes?: number;
}

export interface AssistantEventSubtask {
  task_id: string;
  kind: string;
  name: string;
  phase: string;
  completed?: number;
  total?: number;
  detail?: string;
  error?: string;
  started_at: string;
  updated_at: string;
  finished_at?: string;
}

export interface AssistantEventsResponse {
  range: AssistantEventRange;
  result: AssistantEventResultFilter;
  since?: string;
  events: AssistantEventDetail[];
  total: number;
  filtered_total: number;
  replied: number;
  not_replied: number;
  pending: number;
  errors: number;
  notices: number;
  llm_calls: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cached_input_tokens: number;
  page: number;
  limit: number;
  has_more: boolean;
  group?: string;
  groups: AssistantEventGroup[];
  context_budget?: AssistantContextBudget;
}

export interface AssistantEventGroup {
  group_id: string;
  events: number;
  group_name?: string;
  avatar_url?: string;
}

export interface AssistantContextBudgetLayer {
  key: string;
  label: string;
  share_percent: number;
  ceiling: number;
  tokens: number;
  capped_by_ceiling: boolean;
  configurable: boolean;
}

export interface AssistantContextBudget {
  group_id?: string;
  context_window: number;
  layers: AssistantContextBudgetLayer[];
  allocated: number;
  headroom: number;
}

export function getAssistantEvents(
  range: AssistantEventRange,
  result: AssistantEventResultFilter = "all",
  page = 1,
  limit = 50,
  group = "",
  profile = ""
): Promise<AssistantEventsResponse> {
  const params = new URLSearchParams({ range, result, page: String(page), limit: String(limit) });
  if (group) params.set("group", group);
  if (profile) params.set("profile", profile);
  return requestJSON<AssistantEventsResponse>(`/api/assistant/events?${params.toString()}`);
}

export interface AssistantEventTraceResponse {
  event_id: string;
  message_id?: string;
  steps: AppLogEntry[];
}

export function getAssistantEventTrace(eventID: string): Promise<AssistantEventTraceResponse> {
  return requestJSON<AssistantEventTraceResponse>(`/api/assistant/events/${encodeURIComponent(eventID)}/trace`);
}

export interface UserMemoryItem {
  text: string;
  source?: string;
  group_id?: string;
  message_id?: string;
  at?: string;
}

export interface UserMemoryProfile {
  user_id: string;
  display_name?: string;
  favorability: number;
  message_count: number;
  memories?: UserMemoryItem[];
  /** 列表接口不带记忆正文，只带条数；详情接口带完整 memories。 */
  memory_count?: number;
  last_seen_at?: string;
  updated_at?: string;
}

export interface UserFavorabilityChange {
  id: number;
  user_id: string;
  delta: number;
  before_score: number;
  after_score: number;
  source: string;
  reason?: string;
  operator_id?: string;
  group_id?: string;
  message_id?: string;
  created_at: string;
}

export interface AssistantUsersResponse {
  users: UserMemoryProfile[];
  total: number;
  query?: string;
  limit: number;
  offset: number;
}

export interface AssistantUserDetailResponse {
  profile: UserMemoryProfile;
  favorability_changes: UserFavorabilityChange[];
}

export function listAssistantUsers(query = "", limit = 50, offset = 0, profile = ""): Promise<AssistantUsersResponse> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (query) {
    params.set("q", query);
  }
  if (profile) {
    params.set("profile", profile);
  }
  return requestJSON<AssistantUsersResponse>(`/api/assistant/users?${params.toString()}`);
}

export function getAssistantUser(userID: string, profile = ""): Promise<AssistantUserDetailResponse> {
  const suffix = profile ? `?profile=${encodeURIComponent(profile)}` : "";
  return requestJSON<AssistantUserDetailResponse>(`/api/assistant/users/${encodeURIComponent(userID)}${suffix}`);
}

export interface AssistantUserNamesResponse {
  /** 只包含查到昵称的号；查不到的号不会出现在这里。 */
  names: Record<string, string>;
}

/** 把用户 ID 换成昵称，供各处「填个 QQ 号」的输入框回显。 */
export function fetchAssistantUserNames(userIDs: string[], profile = ""): Promise<AssistantUserNamesResponse> {
  const params = new URLSearchParams({ ids: userIDs.join(",") });
  if (profile) {
    params.set("profile", profile);
  }
  return requestJSON<AssistantUserNamesResponse>(`/api/assistant/user-names?${params.toString()}`);
}

export interface Persona {
  id: string;
  name: string;
  system_prompt?: string;
  reply_style?: "" | "groupmate" | "assistant" | "gentle" | "lively" | "concise" | "catgirl" | "roleplay";
  self_reference?: string;
  sentence_enders?: string;
  updated_at?: string;
}

export interface PersonaListResponse {
  personas: Persona[];
  limit: number;
}

export function listPersonas(): Promise<PersonaListResponse> {
  return requestJSON<PersonaListResponse>("/api/assistant/personas");
}

/** 带 id 是改，不带是新增。返回落库后的那一份和整库。 */
export function savePersona(persona: Persona | Omit<Persona, "id">): Promise<{ persona: Persona; personas: Persona[] }> {
  return requestJSON<{ persona: Persona; personas: Persona[] }>("/api/assistant/personas", {
    method: "POST",
    body: JSON.stringify({ persona })
  });
}

export interface PersonaImportResult {
  personas: Persona[];
  imported: number;
  skipped: number;
  renamed: number;
  dropped: number;
}

/** 导出文件的格式。version 现在不参与判断，只为将来能认出旧文件。 */
export const PERSONA_EXPORT_VERSION = 1;

/** 合并在后端做：一次读改写落一次库，中途失败不会留下「导了一半」的状态。 */
export function importPersonas(personas: Persona[]): Promise<PersonaImportResult> {
  return requestJSON<PersonaImportResult>("/api/assistant/personas/import", {
    method: "POST",
    body: JSON.stringify({ version: PERSONA_EXPORT_VERSION, personas })
  });
}

export function deletePersona(id: string): Promise<{ personas: Persona[] }> {
  return requestJSON<{ personas: Persona[] }>("/api/assistant/personas/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export interface GlossaryRevision {
  version: number;
  meaning?: string;
  example?: string;
  aliases?: string[];
  note?: string;
  editor_user_id?: string;
  editor_name?: string;
  recorded_at: string;
}

export interface GlossaryEntry {
  id: string;
  scope_key: string;
  term: string;
  aliases?: string[];
  meaning: string;
  example?: string;
  note?: string;
  author_user_id?: string;
  author_name?: string;
  editor_user_id?: string;
  editor_name?: string;
  usage_count: number;
  last_used_at?: string;
  version: number;
  status: "active" | "deleted";
  created_at: string;
  updated_at: string;
  /** 只有详情接口带修订记录。 */
  revisions?: GlossaryRevision[];
}

export interface GlossaryScopeSummary {
  scope_key: string;
  active_count: number;
  deleted_count: number;
  updated_at: string;
}

export interface GlossaryListResponse {
  scopes: GlossaryScopeSummary[];
  scope: string;
  entries: GlossaryEntry[];
  query?: string;
}

export interface GlossaryEntryInput {
  scope: string;
  term: string;
  aliases?: string[];
  meaning?: string;
  example?: string;
  note?: string;
}

export function listGlossary(
  scope = "",
  query = "",
  includeDeleted = false,
  botProfileID = ""
): Promise<GlossaryListResponse> {
  const params = new URLSearchParams();
  if (scope) {
    params.set("scope", scope);
  }
  if (query) {
    params.set("q", query);
  }
  if (includeDeleted) {
    params.set("include_deleted", "true");
  }
  if (botProfileID) {
    params.set("profile", botProfileID);
  }
  const search = params.toString();
  return requestJSON<GlossaryListResponse>(`/api/assistant/glossary${search ? `?${search}` : ""}`);
}

export function getGlossaryEntry(scope: string, term: string): Promise<GlossaryEntry> {
  const params = new URLSearchParams({ scope, term });
  return requestJSON<GlossaryEntry>(`/api/assistant/glossary/entry?${params.toString()}`);
}

export function saveGlossaryEntry(input: GlossaryEntryInput): Promise<GlossaryEntry> {
  return requestJSON<GlossaryEntry>("/api/assistant/glossary", {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function deleteGlossaryEntry(scope: string, term: string, note = ""): Promise<GlossaryEntry> {
  return requestJSON<GlossaryEntry>("/api/assistant/glossary/delete", {
    method: "POST",
    body: JSON.stringify({ scope, term, note })
  });
}

export function restoreGlossaryEntry(scope: string, term: string): Promise<GlossaryEntry> {
  return requestJSON<GlossaryEntry>("/api/assistant/glossary/restore", {
    method: "POST",
    body: JSON.stringify({ scope, term })
  });
}

export type AssistantTaskKind = "reminder" | "schedule" | "repository_watch" | "rss_watch";
// 空数组表示「全部种类都要」——后端也是这么存的，别把空当成「一条都不要」。
export type RepositoryWatchPullEvent = "opened" | "updated" | "closed" | "merged";
export type RepositoryWatchIssueEvent = "opened" | "updated" | "closed" | "reopened";
export type AssistantTaskStatus = "active" | "retrying" | "used" | "cancelled";

export interface AssistantTask {
  id: string;
  kind: AssistantTaskKind;
  platform?: string;
  profile_id?: string;
  owner_id: string;
  group_id?: string;
  user_id?: string;
  notification_enabled?: boolean;
  notification_targets?: RepositoryWatchTarget[];
  message: string;
  status: AssistantTaskStatus;
  trigger_at: string;
  interval_seconds?: number;
  last_run_at?: string;
  cancelled_at?: string;
  last_error?: string;
  consecutive_failures?: number;
  pending_delivery?: boolean;
  pending_since?: string;
  repository?: string;
  repository_branch?: string;
  watch_commits?: boolean;
  watch_pull_requests?: boolean;
  watch_pull_request_events?: RepositoryWatchPullEvent[];
  watch_issue_events?: RepositoryWatchIssueEvent[];
  watch_issues?: boolean;
  watch_releases?: boolean;
  watch_stars?: boolean;
  star_notify_mode?: "growth" | "milestone";
  star_notify_threshold?: number;
  star_notify_milestones?: number[];
  last_commit_sha?: string;
  last_pull_request_cursor?: string;
  last_issue_cursor?: string;
  last_release_tag?: string;
  last_star_count?: number;
  last_notified_star_count?: number;
  feed_url?: string;
  feed_source?: "rss" | "twitter";
  feed_handle?: string;
  feed_judge_prompt?: string;
  last_feed_item_id?: string;
  last_feed_published_at?: string;
  created_at: string;
  consumes_quota: boolean;
}

export interface RepositoryWatchTarget {
  destination: "private" | "group";
  group_id?: string;
  user_id?: string;
}

export interface AssistantTasksResponse {
  items: AssistantTask[];
}

export interface RepositoryWatchInput {
  repository: string;
  branch?: string;
  interval_seconds: number;
  watch_commits: boolean;
  watch_pull_requests: boolean;
  watch_pull_request_events?: RepositoryWatchPullEvent[];
  watch_issue_events?: RepositoryWatchIssueEvent[];
  watch_issues: boolean;
  watch_releases: boolean;
  watch_stars: boolean;
  star_notify_mode?: "growth" | "milestone";
  star_notify_threshold?: number;
  star_notify_milestones?: number[];
  profile_id?: string;
  destination?: "private" | "group";
  group_id?: string;
  user_id?: string;
  notification_enabled?: boolean;
  notification_targets?: RepositoryWatchTarget[];
}

export interface RSSWatchInput {
  feed_url?: string;
  twitter_handle?: string;
  judge_prompt: string;
  interval_seconds: number;
  profile_id?: string;
  destination?: "private" | "group";
  group_id?: string;
  user_id?: string;
}

export function getAssistantTasks(): Promise<AssistantTasksResponse> {
  return requestJSON<AssistantTasksResponse>("/api/assistant/tasks");
}

export function createRepositoryWatch(input: RepositoryWatchInput): Promise<AssistantTask> {
  return requestJSON<AssistantTask>("/api/assistant/tasks/repository-watches", {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function updateRepositoryWatch(id: string, input: RepositoryWatchInput): Promise<AssistantTask> {
  return requestJSON<AssistantTask>(`/api/assistant/tasks/repository-watches/${encodeURIComponent(id)}`, {
    method: "PUT",
    body: JSON.stringify(input)
  });
}

export function cancelRepositoryWatch(id: string): Promise<AssistantTask> {
  return requestJSON<AssistantTask>(`/api/assistant/tasks/repository-watches/${encodeURIComponent(id)}/cancel`, { method: "POST" });
}

export function runRepositoryWatch(id: string): Promise<AssistantTask> {
  return requestJSON<AssistantTask>(`/api/assistant/tasks/repository-watches/${encodeURIComponent(id)}/run`, { method: "POST" });
}

export function deleteRepositoryWatch(id: string): Promise<void> {
  return requestJSON<void>(`/api/assistant/tasks/repository-watches/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function createRSSWatch(input: RSSWatchInput): Promise<AssistantTask> {
  return requestJSON<AssistantTask>("/api/assistant/tasks/rss-watches", { method: "POST", body: JSON.stringify(input) });
}

export function updateRSSWatch(id: string, input: Partial<Pick<RSSWatchInput, "feed_url" | "twitter_handle" | "judge_prompt" | "interval_seconds">>): Promise<AssistantTask> {
  return requestJSON<AssistantTask>(`/api/assistant/tasks/rss-watches/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(input) });
}

export function cancelRSSWatch(id: string): Promise<AssistantTask> {
  return requestJSON<AssistantTask>(`/api/assistant/tasks/rss-watches/${encodeURIComponent(id)}/cancel`, { method: "POST" });
}

export function deleteRSSWatch(id: string): Promise<void> {
  return requestJSON<void>(`/api/assistant/tasks/rss-watches/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function getHealth(): Promise<HealthResponse> {
  return requestJSON<HealthResponse>("/api/health");
}

export interface GroupRelationNode {
  user_id: string;
  display_name?: string;
  messages: number;
  favorability: number;
  is_bot?: boolean;
}

export interface GroupRelationEdge {
  source: string;
  target: string;
  weight: number;
}

export interface GroupRelationGraph {
  group_id: string;
  bot_id?: string;
  since?: string;
  messages: number;
  participants: number;
  nodes: GroupRelationNode[];
  edges: GroupRelationEdge[];
  truncated?: boolean;
}

export interface GroupRelationResponse {
  range: AssistantEventRange;
  graph: GroupRelationGraph;
}

export function getGroupRelations(groupID: string, range: AssistantEventRange = "7d"): Promise<GroupRelationResponse> {
  const params = new URLSearchParams({ range });
  return requestJSON<GroupRelationResponse>(`/api/assistant/groups/${encodeURIComponent(groupID)}/relations?${params.toString()}`);
}


// ---- LLM OAuth 登录 -------------------------------------------------------

export interface LLMOAuthProvider {
  key: string;
  label: string;
  authorize_url: string;
  token_url: string;
  client_id?: string;
  /** 读接口里恒为 "***" 或空，明文永远不回传。 */
  client_secret?: string;
  redirect_uri?: string;
  scopes?: string[];
  use_pkce?: boolean;
  token_headers?: Record<string, string>;
  built_in?: boolean;
  notes?: string;
}

export interface LLMOAuthStatus {
  provider: LLMOAuthProvider;
  logged_in: boolean;
  account?: string;
  obtained_at?: string;
  expires_at?: string;
  expired?: boolean;
  refreshable?: boolean;
  scope?: string;
}

export interface LLMOAuthPendingLogin {
  id: string;
  provider_key: string;
  authorize_url: string;
  redirect_uri?: string;
  expires_at: string;
}

export function listOAuthProviders(): Promise<{ providers: LLMOAuthStatus[] }> {
  return requestJSON("/api/llm/oauth/providers");
}

export function saveOAuthProvider(provider: LLMOAuthProvider): Promise<{ providers: LLMOAuthStatus[] }> {
  return requestJSON("/api/llm/oauth/providers", { method: "POST", body: JSON.stringify(provider) });
}

export function deleteOAuthProvider(provider: string): Promise<{ providers: LLMOAuthStatus[] }> {
  return requestJSON("/api/llm/oauth/providers/delete", { method: "POST", body: JSON.stringify({ provider }) });
}

export function startOAuthLogin(provider: string): Promise<{ login: LLMOAuthPendingLogin }> {
  return requestJSON("/api/llm/oauth/login/start", { method: "POST", body: JSON.stringify({ provider }) });
}

export function completeOAuthLogin(provider: string, loginId: string, callback: string): Promise<{ providers: LLMOAuthStatus[] }> {
  return requestJSON("/api/llm/oauth/login/complete", {
    method: "POST",
    body: JSON.stringify({ provider, login_id: loginId, callback })
  });
}

export function cancelOAuthLogin(loginId: string): Promise<{ ok: boolean }> {
  return requestJSON("/api/llm/oauth/login/cancel", { method: "POST", body: JSON.stringify({ login_id: loginId }) });
}

export function logoutOAuthProvider(provider: string): Promise<{ providers: LLMOAuthStatus[] }> {
  return requestJSON("/api/llm/oauth/logout", { method: "POST", body: JSON.stringify({ provider }) });
}
