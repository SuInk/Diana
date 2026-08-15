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
  api_key?: string;
  api_key_configured?: boolean;
  base_url?: string;
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

export interface LLMModelInfo {
  id: string;
  name?: string;
  object?: string;
  owned_by?: string;
  created?: number;
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
  enabled: boolean;
  onebot_reverse_ws_endpoint: string;
  onebot_access_token?: string;
  onebot_access_token_configured?: boolean;
  nonebot_bridge_enabled?: boolean;
  nonebot_bridge_endpoint?: string;
  nonebot_bridge_token?: string;
  nonebot_bridge_token_configured?: boolean;
  bot_qq?: string;
  owner_id?: string;
  group_triggers?: string[];
  disabled_groups?: string[];
  welcome_enabled?: boolean;
  welcome_message?: string;
  system_prompt?: string;
  max_input_chars?: number;
  max_reply_chars?: number;
  direct_reply_chunk_size?: number;
  forward_reply_threshold?: number;
  recent_context_limit?: number;
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

export interface PluginManifest {
  id: string;
  name: string;
  version: string;
  description: string;
  official: boolean;
  built_in: boolean;
  permissions?: string[];
}

export interface PluginState {
  manifest: PluginManifest;
  installed: boolean;
  enabled: boolean;
}

export interface QQBotGroupConfig {
  group_id: string;
  enabled: boolean;
  enabled_set?: boolean;
  group_triggers?: string[];
  welcome_enabled?: boolean;
  welcome_message?: string;
  recent_context_limit?: number;
  max_reply_chars?: number;
  plugin_overrides?: Record<string, boolean>;
  updated_at?: string;
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
  last_fetched_at?: string;
  last_update_at?: string;
  last_update_text?: string;
}

export interface UpdateResult {
  status: UpdateStatus;
  fetched: boolean;
  updated: boolean;
  output?: string;
  at: string;
}

export type AppLogKind = "operation" | "error";
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

export interface QQBotStatus {
  running: boolean;
  config: QQBotConfig;
  channel: {
    connected: boolean;
    endpoint: string;
    self_id?: string;
    last_error?: string;
    updated_at: string;
  };
  nonebot_bridge: {
    enabled: boolean;
    connected: boolean;
    endpoint?: string;
    last_error?: string;
    updated_at: string;
  };
  plugins: PluginState[];
  recent_events?: Array<{
    at: string;
    kind: string;
    user_id?: string;
    group_id?: string;
    text?: string;
    reply?: string;
    error?: string;
    handled: boolean;
    duration_ms?: number;
  }>;
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

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, {
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {})
    },
    ...init
  });
  const data = (await response.json().catch(() => ({}))) as T & { error?: string };
  if (!response.ok) {
    throw new Error(data.error || `HTTP ${response.status}`);
  }
  return data;
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

export function listLLMModels(config: LLMConfig): Promise<LLMModelsResponse> {
  return requestJSON<LLMModelsResponse>("/api/llm/models", {
    method: "POST",
    body: JSON.stringify(config)
  });
}

export function getQQBotConfig(): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/qqbot/config");
}

export function saveQQBotConfig(config: QQBotConfig): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/qqbot/config", {
    method: "POST",
    body: JSON.stringify(config)
  });
}

export function activateQQBotProfile(id: string): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/qqbot/config/activate", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function cloneQQBotProfile(id: string): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/qqbot/config/clone", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function deleteQQBotProfile(id: string): Promise<QQBotConfig> {
  return requestJSON<QQBotConfig>("/api/qqbot/config/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function getQQBotStatus(): Promise<QQBotStatus> {
  return requestJSON<QQBotStatus>("/api/qqbot/status");
}

export function startQQBot(): Promise<QQBotStatus> {
  return requestJSON<QQBotStatus>("/api/qqbot/start", { method: "POST" });
}

export function stopQQBot(): Promise<QQBotStatus> {
  return requestJSON<QQBotStatus>("/api/qqbot/stop", { method: "POST" });
}

export function getQQBotFeatures(): Promise<QQBotFeatureFlags> {
  return requestJSON<QQBotFeatureFlags>("/api/qqbot/features");
}

export function getQQGroupTest(groupID: string): Promise<QQGroupTestResponse> {
  const params = new URLSearchParams({ group_id: groupID });
  return requestJSON<QQGroupTestResponse>(`/api/qqbot/group-test?${params.toString()}`);
}

export function sendQQGroupTest(groupID: string, message: string): Promise<QQGroupTestResponse> {
  return requestJSON<QQGroupTestResponse>("/api/qqbot/group-test", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, message })
  });
}

export function listPlugins(): Promise<PluginState[]> {
  return requestJSON<PluginState[]>("/api/qqbot/plugins");
}

export function installPlugin(id: string): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/qqbot/plugins/${encodeURIComponent(id)}/install`, { method: "POST" });
}

export function uninstallPlugin(id: string): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/qqbot/plugins/${encodeURIComponent(id)}/uninstall`, { method: "POST" });
}

export function setPluginEnabled(id: string, enabled: boolean): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/qqbot/plugins/${encodeURIComponent(id)}/enabled`, {
    method: "POST",
    body: JSON.stringify({ enabled })
  });
}

export function requestQQBotGroupAdminChallenge(groupID: string, userID: string): Promise<QQBotGroupAdminChallengeResponse> {
  return requestJSON<QQBotGroupAdminChallengeResponse>("/api/qqbot/group-admin/challenge", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID })
  });
}

export function verifyQQBotGroupAdmin(groupID: string, userID: string, code: string): Promise<QQBotGroupAdminConfigResponse> {
  return requestJSON<QQBotGroupAdminConfigResponse>("/api/qqbot/group-admin/verify", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID, code })
  });
}

export function getQQBotGroupAdminConfig(token: string): Promise<QQBotGroupAdminConfigResponse> {
  return requestJSON<QQBotGroupAdminConfigResponse>("/api/qqbot/group-admin/config", {
    headers: { "X-Diana-Group-Token": token }
  });
}

export function saveQQBotGroupAdminConfig(token: string, config: QQBotGroupConfig): Promise<QQBotGroupAdminConfigResponse> {
  return requestJSON<QQBotGroupAdminConfigResponse>("/api/qqbot/group-admin/config", {
    method: "POST",
    headers: { "X-Diana-Group-Token": token },
    body: JSON.stringify({ config })
  });
}

export function getUpdateStatus(): Promise<UpdateStatus> {
  return requestJSON<UpdateStatus>("/api/system/update");
}

export function pullFromGitHub(): Promise<UpdateResult> {
  return requestJSON<UpdateResult>("/api/system/update", { method: "POST" });
}

export function listAppLogs(kind?: AppLogKind, limit = 100): Promise<AppLogsResponse> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (kind) {
    params.set("kind", kind);
  }
  return requestJSON<AppLogsResponse>(`/api/logs?${params.toString()}`);
}
