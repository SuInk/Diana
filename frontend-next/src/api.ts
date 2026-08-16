// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

export type Provider = "openai_compatible" | "gemini" | "anthropic";

export interface LLMConfig {
  id?: string;
  name?: string;
  group?: string;
  description?: string;
  updated_at?: string;
  active_profile_id?: string;
  profiles?: LLMConfig[];
  provider: Provider;
  api_style?: "responses" | "chat_completions";
  api_key?: string;
  api_key_configured?: boolean;
  api_key_preview?: string;
  base_url?: string;
  models?: LLMModelInfo[];
  model: string;
  image_model?: string;
  user_agent?: string;
  headers?: Record<string, string>;
  temperature?: number | null;
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

export interface QQBotConfig {
  id?: string;
  name?: string;
  platform?: string;
  avatar_url?: string;
  active_profile_id?: string;
  profiles?: QQBotConfig[];
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
  bot_qq?: string;
  owner_id?: string;
  owner_login_enabled?: boolean;
  owner_llm_config_enabled?: boolean;
  group_triggers?: string[];
  disabled_groups?: string[];
  /** 群准入模式与白名单；不设等同 blacklist，行为与旧配置一致。 */
  group_admission?: GroupAdmission;
  /** 全局回复门槛（等级/时段/用户名单）；不设表示无门槛。 */
  reply_gate?: ReplyGate | null;
  welcome_enabled?: boolean;
  welcome_message?: string;
  system_prompt?: string;
  response_mode?: "quiet" | "standard" | "active" | "custom";
  reply_style?: "assistant" | "gentle" | "lively" | "concise";
  /** 记录完整模型上下文、工具参数和调用结果；默认关闭。 */
  debug_mode_enabled?: boolean;
  /** 回复行为个性化；后端缺省（字段不存在）等价于开启。 */
  reply_reference_enabled?: boolean;
  mention_user_enabled?: boolean;
  markdown_to_plain?: boolean;
  error_notify_enabled?: boolean;
  error_reply_prefix?: string;
  send_retry_attempts?: number;
  send_chunk_interval_ms?: number;
  /** 按用途分配模型：chat/vision/intent/image → 渠道（或渠道分组）+模型。 */
  model_roles?: Record<string, { profile_id?: string; group?: string; model: string }>;
  /** 用模型识别其他机器人的自动回复并阻断机器人互聊；缺省等价于开启。 */
  bot_reply_loop_detection_enabled?: boolean;
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
  direct_reply_chunk_size?: number;
  forward_reply_threshold?: number;
  recall_reply_auto_delete_enabled?: boolean;
  recall_reply_auto_delete_delay_seconds?: number;
  recent_context_limit?: number;
  /** 持久化提取稳定事实、偏好和会话摘要；缺省等价于开启。 */
  long_term_memory_enabled?: boolean;
  /** 允许在同一机器人下检索其他群的非敏感记忆和聊天历史；缺省关闭。 */
  cross_group_memory_enabled?: boolean;
  max_bot_concurrency?: number;
  request_timeout_ms?: number;
  agent_enabled?: boolean;
  agent_work_dir?: string;
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
  type: "bool" | "number" | "string" | "select" | "multi_select";
  default: unknown;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  options?: PluginSettingOption[];
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
  installable: boolean;
  installer?: string;
}

export interface ResolverDependencyInstallResponse {
  dependency: ResolverDependency;
  resolver: ResolverDependency[];
  installer?: string;
}

export interface QQBotGroupConfig {
  group_id: string;
  enabled: boolean;
  enabled_set?: boolean;
  group_triggers?: string[];
  /** 群专属人设；留空沿用全局系统提示词。 */
  system_prompt?: string;
  /** 留空时跟随机器人全局回复模式。 */
  response_mode?: "" | "quiet" | "standard" | "active" | "custom";
  /** 留空时跟随机器人全局表达风格。 */
  reply_style?: "" | "assistant" | "gentle" | "lively" | "concise";
  welcome_enabled?: boolean;
  welcome_message?: string;
  recent_context_limit?: number;
  max_reply_chars?: number;
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
  /** 本群专属回复时间、屏蔽 QQ 号与准入门槛；不设表示跟随全局。 */
  reply_gate?: ReplyGate | null;
  updated_at?: string;
}

export interface QQBotGroupSummary extends QQBotGroupConfig {
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
  /** QQ 群等级门槛，0 表示不限。 */
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

export interface QQBotGroupAdminChallengeResponse {
  group_id: string;
  user_id: string;
  expires_at: string;
  message: string;
}

export interface QQBotGroupAdminConfigResponse {
  group_id: string;
  user_id?: string;
  token?: string;
  expires_at?: string;
  config: QQBotGroupConfig;
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

export interface QQBotEvent {
  at: string;
  kind: string;
  platform?: string;
  profile_id?: string;
  user_id?: string;
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

export interface QQBotChannelStatus {
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

export interface QQBotStatus {
  running: boolean;
  config: QQBotConfig;
  channel: QQBotChannelStatus;
  channels?: QQBotChannelStatus[];
  nonebot_bridge: {
    enabled: boolean;
    connected: boolean;
    endpoint?: string;
    last_error?: string;
    updated_at: string;
  };
  plugins: PluginState[];
  recent_events?: QQBotEvent[];
  active_workers: number;
  last_error?: string;
  updated_at: string;
}

export interface QQGroupTestResponse {
  group_id: string;
  message?: string;
  message_id?: string;
  sent: boolean;
  send_result?: Record<string, unknown>;
  channel: QQBotStatus["channel"];
  recent_events?: NonNullable<QQBotStatus["recent_events"]>;
  status: QQBotStatus;
}

export interface QQBotFeatureFlags {
  group_test: boolean;
}

export interface QQBotPlatform {
  id: string;
  name: string;
  protocol: string;
  /** 聊天平台本身，用于分组；同一分类下通常只是不同协议实现。 */
  category: string;
  category_label: string;
  description?: string;
}

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, {
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {})
    },
    ...init
  });
  const data = (await response.json().catch(() => ({}))) as T & { error?: string; auth_required?: boolean };
  if (!response.ok) {
    // 会话过期或未登录：广播事件让 App 切到登录界面，而不是每个视图各自报错。
    if (response.status === 401 && data.auth_required && !url.startsWith("/api/auth/")) {
      window.dispatchEvent(new CustomEvent("diana:unauthorized"));
    }
    throw new Error(data.error || `HTTP ${response.status}`);
  }
  return data;
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

export interface OwnerLoginStatus {
  available: boolean;
  code_delivery_available: boolean;
}

export function getOwnerLoginStatus(): Promise<OwnerLoginStatus> {
  return requestJSON<OwnerLoginStatus>("/api/auth/owner/status");
}

export interface OwnerLoginChallenge {
  ok: boolean;
  challenge_token: string;
  expires_in_seconds: number;
  cooldown_seconds: number;
}

export function requestOwnerLoginCode(): Promise<OwnerLoginChallenge> {
  return requestJSON<OwnerLoginChallenge>("/api/auth/owner/challenge", { method: "POST" });
}

export function verifyOwnerLoginCode(challengeToken: string, code: string): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>("/api/auth/owner/verify", {
    method: "POST",
    body: JSON.stringify({ challenge_token: challengeToken, code })
  });
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

export function activateConfigProfile(id: string): Promise<LLMConfig> {
  return requestJSON<LLMConfig>("/api/llm/config/activate", {
    method: "POST",
    body: JSON.stringify({ id })
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

export function importConfigProfiles(payload: Pick<LLMConfig, "active_profile_id" | "profiles">): Promise<LLMConfig> {
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

export function testLLMImage(prompt: string, config?: LLMConfig): Promise<ImageGenerateResponse> {
  return requestJSON<ImageGenerateResponse>("/api/llm/test", {
    method: "POST",
    body: JSON.stringify({ ...(config || {}), message: prompt, mode: "image" })
  });
}

export function listLLMModels(config: LLMConfig): Promise<LLMModelsResponse> {
  return requestJSON<LLMModelsResponse>("/api/llm/models", {
    method: "POST",
    body: JSON.stringify(config)
  });
}

export function getQQBotConfig(): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/assistant/config");
}

export function getQQBotPlatforms(): Promise<{ platforms: QQBotPlatform[] }> {
  return requestJSON<{ platforms: QQBotPlatform[] }>("/api/assistant/platforms");
}

export function saveQQBotConfig(config: QQBotConfig): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/assistant/config", {
    method: "POST",
    body: JSON.stringify(config)
  });
}

export function activateQQBotProfile(id: string): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/assistant/config/activate", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function cloneQQBotProfile(id: string): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/assistant/config/clone", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function deleteQQBotProfile(id: string): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/assistant/config/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function setQQBotContextIsolation(enabled: boolean): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/assistant/config/context-isolation", {
    method: "POST",
    body: JSON.stringify({ enabled })
  });
}

export function getQQBotStatus(): Promise<QQBotStatus> {
  return requestJSON<QQBotStatus>("/api/assistant/status");
}

export function startQQBot(): Promise<QQBotStatus> {
  return requestJSON<QQBotStatus>("/api/assistant/start", { method: "POST" });
}

export function stopQQBot(): Promise<QQBotStatus> {
  return requestJSON<QQBotStatus>("/api/assistant/stop", { method: "POST" });
}

export function getQQBotFeatures(): Promise<QQBotFeatureFlags> {
  return requestJSON<QQBotFeatureFlags>("/api/assistant/features");
}

export function getQQGroupTest(groupID: string): Promise<QQGroupTestResponse> {
  const params = new URLSearchParams({ group_id: groupID });
  return requestJSON<QQGroupTestResponse>(`/api/assistant/group-test?${params.toString()}`);
}

export function sendQQGroupTest(groupID: string, message: string): Promise<QQGroupTestResponse> {
  return requestJSON<QQGroupTestResponse>("/api/assistant/group-test", {
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

export function listResolverDependencies(): Promise<{ resolver: ResolverDependency[] }> {
  return requestJSON<{ resolver: ResolverDependency[] }>("/api/assistant/plugins/dependencies");
}

export function installResolverDependency(name: string): Promise<ResolverDependencyInstallResponse> {
  return requestJSON<ResolverDependencyInstallResponse>(
    `/api/assistant/plugins/dependencies/${encodeURIComponent(name)}/install`,
    { method: "POST" }
  );
}

export function requestQQBotGroupAdminChallenge(groupID: string, userID: string): Promise<QQBotGroupAdminChallengeResponse> {
  return requestJSON<QQBotGroupAdminChallengeResponse>("/api/assistant/group-admin/challenge", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID })
  });
}

export function verifyQQBotGroupAdmin(groupID: string, userID: string, code: string): Promise<QQBotGroupAdminConfigResponse> {
  return requestJSON<QQBotGroupAdminConfigResponse>("/api/assistant/group-admin/verify", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID, code })
  });
}

export function getQQBotGroupAdminConfig(token: string): Promise<QQBotGroupAdminConfigResponse> {
  return requestJSON<QQBotGroupAdminConfigResponse>("/api/assistant/group-admin/config", {
    headers: { "X-Diana-Group-Token": token }
  });
}

export function saveQQBotGroupAdminConfig(token: string, config: QQBotGroupConfig): Promise<QQBotGroupAdminConfigResponse> {
  return requestJSON<QQBotGroupAdminConfigResponse>("/api/assistant/group-admin/config", {
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

export interface SystemVersion {
  build_version: string;
  version_label: string;
  git_available: boolean;
  deployment_mode: "git" | "release";
  update_supported: boolean;
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
  groups: QQBotGroupSummary[];
  plugins: PluginState[];
  live_available: boolean;
  warning?: string;
}

export function listQQBotGroups(): Promise<ConsoleGroupsResponse> {
  return requestJSON<ConsoleGroupsResponse>("/api/assistant/groups");
}

export function saveQQBotGroup(config: QQBotGroupConfig): Promise<{ config: QQBotGroupConfig }> {
  return requestJSON<{ config: QQBotGroupConfig }>("/api/assistant/groups", {
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

export function getSystemVersion(): Promise<SystemVersion> {
  return requestJSON<SystemVersion>("/api/system/version");
}

export interface UpdateCheckResponse {
  deployment_mode: "git" | "release";
  current_version: string;
  latest_version?: string;
  latest_published_at?: string;
  checked_at: string;
  update_available: boolean;
  update_supported: boolean;
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
}

export interface HealthResponse {
  status: string;
  started_at: string;
  uptime_seconds: number;
  version: string;
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

export interface AssistantEventDetail extends QQBotEvent {
  id: string;
  sender_name?: string;
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
  page: number;
  limit: number;
  has_more: boolean;
}

export function getAssistantEvents(
  range: AssistantEventRange,
  result: AssistantEventResultFilter = "all",
  page = 1,
  limit = 50
): Promise<AssistantEventsResponse> {
  const params = new URLSearchParams({ range, result, page: String(page), limit: String(limit) });
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

export type AssistantTaskKind = "reminder" | "schedule" | "repository_watch" | "rss_watch";
export type AssistantTaskStatus = "active" | "retrying" | "used" | "cancelled";

export interface AssistantTask {
  id: string;
  kind: AssistantTaskKind;
  platform?: string;
  profile_id?: string;
  owner_id: string;
  group_id?: string;
  user_id?: string;
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
  watch_releases?: boolean;
  watch_stars?: boolean;
  last_commit_sha?: string;
  last_pull_request_cursor?: string;
  last_release_tag?: string;
  last_star_count?: number;
  feed_url?: string;
  feed_source?: "rss" | "twitter";
  feed_handle?: string;
  feed_judge_prompt?: string;
  last_feed_item_id?: string;
  last_feed_published_at?: string;
  created_at: string;
  consumes_quota: boolean;
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
  watch_releases: boolean;
  watch_stars: boolean;
  profile_id?: string;
  destination?: "private" | "group";
  group_id?: string;
  user_id?: string;
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
