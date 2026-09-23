// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { trackScopeRequest } from "./scope-transition";
import { configurationKindForMutation, notifyConfigurationChanged } from "./configuration-sync";

export type Provider = "openai_compatible" | "gemini" | "anthropic" | "typesafe";

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

/** 拒答话术：决定机器人不正面回答时说什么。见后端 RefusalStrategy。 */
export type RefusalStrategy = "smart" | "rewrite" | "explain" | "vague";

export type MessageRelayKind = "group" | "private";

/** 互通链路的一端：某台机器人上的某个群聊或某个人。 */
export interface MessageRelayEndpoint {
  profile_id: string;
  platform?: string;
  kind: MessageRelayKind;
  target_id: string;
}

/** 一条互通链路恒为两端，两端之间双向转发。 */
export interface MessageRelayPair {
  id: string;
  name?: string;
  enabled: boolean;
  endpoints: MessageRelayEndpoint[];
}

export interface BotProfileConfig {
  connection_profile_id?: string;
  persona_id?: string;
  custom_persona?: Persona;
  marked_bot_ids?: string[];
  participation?: import("./participation").ParticipationPreferences;
  id?: string;
  name?: string;
  platform?: string;
  avatar_url?: string;
  profiles?: BotProfileConfig[];
  /** 跨机器人的消息互通链路，一条链路连两个会话。 */
  message_relays?: MessageRelayPair[];
  enabled: boolean;
  onebot_transport?: "reverse_ws" | "forward_ws" | "http";
  onebot_ws_endpoint?: string;
  onebot_http_url?: string;
  onebot_http_secret?: string;
  onebot_http_secret_configured?: boolean;
  onebot_reverse_ws_endpoint: string;
  onebot_access_token?: string;
  onebot_access_token_configured?: boolean;
  /** 已保存 token 的掩码预览（前几位…后几位），仅用于展示。 */
  onebot_access_token_preview?: string;
  /** Telegram 走官方 Bot API 长轮询，凭据与 OneBot 完全不同。 */
  telegram_bot_token?: string;
  telegram_bot_token_configured?: boolean;
  telegram_api_base_url?: string;
  telegram_proxy_url?: string;
  /** 默认抑制其他 Bot 的群消息；语义判断提到本机器人时放行。 */
  telegram_suppress_bot_messages?: boolean;
  /** OneBot 私聊准备回复时显示「对方正在输入」，默认开启。 */
  qq_typing_enabled?: boolean;
  /** QQ 开放平台机器人，出站 WebSocket 网关。 */
  qq_app_id?: string;
  qq_app_secret?: string;
  qq_app_secret_configured?: boolean;
  qq_sandbox?: boolean;
  /** 钉钉 Stream 模式，出站长连接。 */
  dingtalk_client_id?: string;
  dingtalk_client_secret?: string;
  dingtalk_client_secret_configured?: boolean;
  dingtalk_robot_code?: string;
  /** 飞书事件订阅，需要公网回调地址。 */
  feishu_app_id?: string;
  feishu_app_secret?: string;
  feishu_app_secret_configured?: boolean;
  feishu_verification_token?: string;
  feishu_verification_token_configured?: boolean;
  feishu_encrypt_key?: string;
  feishu_encrypt_key_configured?: boolean;
  feishu_api_base_url?: string;
  /** 企业微信应用回调，需要公网回调地址。 */
  wecom_corp_id?: string;
  wecom_agent_id?: string;
  wecom_secret?: string;
  wecom_secret_configured?: boolean;
  wecom_token?: string;
  wecom_token_configured?: boolean;
  wecom_encoding_aes_key?: string;
  wecom_encoding_aes_key_configured?: boolean;
  /** 回调型平台要填到对方后台的路径，只读。 */
  callback_path?: string;
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
  /** 拒答话术；不设等同 smart（由模型按语境判断说不说原因）。 */
  refusal_strategy?: RefusalStrategy;
  /** 语气跟随一天的时间变化（深夜话少、清早迷糊、晚上松弛）；不设等同关闭。 */
  daypart_tone_enabled?: boolean;
  /** 流式调用模型，用于统计首 token 时间；回复仍是攒齐了再发。不设等同关闭。 */
  llm_streaming_enabled?: boolean;
  disabled_groups?: string[];
  /** 新加入的群默认工不工作；逐群开关在群管理里，一个群一份。 */
  group_admission?: GroupAdmission;
  /** 私聊准入；不设等同 all，所有用户的私聊都会响应。 */
  private_admission?: PrivateAdmission;
  /** 全局回复门槛（等级/时段/用户名单）；不设表示无门槛。 */
  reply_gate?: ReplyGate | null;
  welcome_enabled?: boolean;
  welcome_message?: string;
  /** 欢迎词模式：fixed 固定文本 / template 模板池随机 / llm 按人设实时生成；不设等同 fixed。 */
  welcome_mode?: "fixed" | "template" | "llm";
  /** 口吻模板池，每条一行，可用 {user_id} 占位；template/llm 回落时使用。 */
  welcome_templates?: string[];
  /** LLM 欢迎词每群冷却秒数；不设用默认值 300。 */
  welcome_llm_cooldown_seconds?: number;
  system_prompt?: string;
  /** 品格层：身份、价值、硬边界。排在系统提示词最前面，分群覆盖动不了它。 */
  soul?: PersonaSoul;
  /**
   * 人设正文和界面控件谁说了算。
   *
   * fill（默认）＝填空题：正文只写角色，自称、句尾语气词、动作描写、答多长这些由
   * 控件和运行时负责，正文里的段头不生效。own＝接管：正文用段头声明哪几段自己写，
   * 运行时对那几段让位，界面上对应的控件停用。不填按 fill 处理。
   */
  persona_mode?: "fill" | "own";
  response_mode?: "quiet" | "assistant" | "standard" | "active" | "super_active" | "custom";
  action_description_enabled?: boolean;
  /** 机器人怎么称呼自己；留空跟随人设。 */
  self_reference?: string;
  /** 句尾语气词候选，逗号分隔。填多个由模型按当下语气挑，留空跟随人设。 */
  sentence_enders?: string;
  /** 记录完整模型上下文、工具参数和调用结果；默认关闭。 */
  debug_mode_enabled?: boolean;
  /** 回复行为个性化：on 每条都带、off 从不带、auto 交给模型自己判断；缺省等价于 on。 */
  reply_reference_mode?: "on" | "off" | "auto";
  /** 谁能问出机器人所用的模型：owner 仅主人（默认）、everyone 所有人。主人始终能看和改。 */
  model_disclosure?: "owner" | "everyone";
  /** 谁能问出项目开源地址：owner 仅主人（默认）、everyone 所有人。 */
  repository_disclosure?: "owner" | "everyone";
  mention_user_mode?: "on" | "off" | "auto";
  markdown_to_plain?: boolean;
  error_notify_enabled?: boolean;
  error_reply_prefix?: string;
  send_retry_attempts?: number;
  /** 周期订阅（RSS、定时查询、仓库订阅）连续失败几次才报一次警。留空按 5 次，0 表示出错不通知。 */
  recurring_failure_alert_threshold?: number;
  send_chunk_interval_ms?: number;
  private_closing_grace?: number;
  inbound_group_concurrency?: number;
  inbound_private_concurrency?: number;
  /** 按用途分配模型：chat/vision/intent/image → 渠道（或渠道分组）+模型。 */
  auto_image_description?: boolean;
  auto_video_preprocess?: boolean;
  model_roles?: Record<string, {
	 follow_chat?: boolean;
    profile_id?: string;
    group?: string;
    model: string;
    provider_id?: string;
    model_id?: string;
    fallbacks?: Array<{ profile_id?: string; group?: string; model: string; provider_id?: string; model_id?: string }>;
  }>;
  /** 用模型识别其他机器人的自动回复并阻断机器人互聊；缺省等价于开启。 */
  bot_reply_loop_detection_enabled?: boolean;
  /** 机器人级账号安全审核总开关；开启后主动和直接回复都审核，关闭后都不审核。 */
  reply_account_safety_audit_master_enabled?: boolean;
  /** 自定义账号风险范围；留空使用内置规则。 */
  reply_account_safety_audit_prompt?: string;
  /** 笔记本是否跨群共用一本；默认按会话隔离。 */
  notebook_shared_scope_enabled?: boolean;
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
  /** @deprecated 旧的整段路由提示词，已被接话评分契约取代，后端不再读取。 */
  proactive_reply_router_prompt?: string;
  /** 接话评分的补充判据：本群的称呼、黑话和禁区，拼在内置评分提示词尾部，最多 1000 字。 */
  proactive_reply_extra_criteria?: string;
  /** 主动回复路由放行后，注入最终回复模型的生成约束。 */
  proactive_reply_prompt?: string;
  /** 主动回复路由放行后的确定性采样率，范围 0~1。 */
  proactive_reply_chance?: number;
  /** 主动回复最低置信度，范围 0~1，默认 0.9。 */
  proactive_reply_threshold?: number;
  /** 闲聊插话总开关。 */
  chat_in_enabled?: boolean;
  /** 回复欲望档位。 */
  chat_in_level?: "off" | "low" | "medium" | "high" | "max";
  chat_in_threshold?: number;
  chat_in_chance?: number;
  chat_in_cooldown_seconds?: number;
  /** @deprecated 仅兼容历史配置，读取时迁移为极高回复欲望。 */
  natural_interjection_enabled?: boolean;
  max_input_chars?: number;
  max_reply_chars?: number;
  /** 自然分条：模型用 [diana-msg] 开始下一条、[diana-line] 在当前消息内换行；真实换行不参与布局。 */
  natural_reply_split_enabled?: boolean;
  /** 连续消息合并置信度百分比，1–100；未设置时默认 75。 */
  reply_merge_confidence_percent?: number;
  reply_preserve_line_breaks?: boolean;
  social_reply_enabled?: boolean;
  /** @deprecated 仅兼容历史配置，不再限制聊天分条。 */
  reply_max_bubbles?: number;
  /** @deprecated 仅兼容历史配置，不再限制聊天长度。 */
  direct_reply_chunk_size?: number;
  /** 正文超过多少字改用合并转发卡片；未设置或 0 表示无上限。 */
  forward_reply_threshold?: number;
  /** 切出超过多少块改用合并转发卡片；未设置或 0 表示无上限。 */
  forward_reply_chunk_threshold?: number;
  recall_reply_auto_delete_enabled?: boolean;
  recall_reply_auto_delete_delay_seconds?: number;
  max_context_tokens?: number;
  recent_history_token_budget?: number;
  /** 这个群在滚动 5 小时窗口里能用掉的 token 上限；留空或 0 表示不限。 */
  model_token_quota?: number;
  /** 同一窗口里的模型调用次数上限；留空或 0 表示不限。和 token 上限先到先得。 */
  model_call_quota?: number;
  recent_context_limit?: number;
  /** 断线或重启后，每个会话最多补处理最近多少条消息；默认 3，最大 100。 */
  history_backfill_message_limit?: number;
  /** 持久化提取稳定事实、偏好和会话摘要；缺省等价于开启。 */
  long_term_memory_enabled?: boolean;
  /** 允许在同一机器人下检索其他群的非敏感记忆和聊天历史；缺省关闭。 */
  cross_group_memory_enabled?: boolean;
  cross_platform_memory_enabled?: boolean;
  /** 这台机器人要不要带上世界书（世界观设定库）；缺省开启，树为空时开着也不注入。 */
  world_book_enabled?: boolean;
  /** 允许机器人自己写自述（自我认知），只进提示词尾部、改不动人设和权限；缺省关闭。 */
  self_note_enabled?: boolean;
  /** 人机恋（恋爱模式）总开关；缺省关闭。 */
  romance_enabled?: boolean;
  /** 后台空闲时定期探测模型收不收强制指定工具；探测是会计费的真实调用，缺省关闭。 */
  llm_capability_probe_enabled?: boolean;
  /** 情绪系统：随相处涨落、随时间回落的心情，只影响语气；缺省关闭。 */
  mood_enabled?: boolean;
  /** 被戳一戳时回一句（OneBot）；缺省关闭。 */
  poke_reply_enabled?: boolean;
  /** 表达学习：按群收集高频短表达当风格参考；缺省关闭。 */
  expression_learning_enabled?: boolean;
  dict_segment_enabled?: boolean;
  semantic_search_enabled?: boolean;
  max_bot_concurrency?: number;
  request_timeout_ms?: number;
  agent_enabled?: boolean;
  agent_max_steps?: number;
  agent_command_allowlist?: string[];
  agent_command_timeout_ms?: number;
  /** auto / require / off；留空即 auto。 */
  agent_command_sandbox?: string;
  agent_command_sandbox_allow_network?: boolean;
  /** 打开 write_file / edit_file。新建配置默认打开。 */
  agent_file_write_enabled?: boolean;
  agent_browser_cdp_url?: string;
  agent_browser_timeout_ms?: number;
  /** 允许这台机器人使用浏览器控制扩展（browser_ext_*）。默认关闭。 */
  agent_browser_control_enabled?: boolean;
  agent_browser_box_disabled?: boolean;
}

export interface PluginSettingOption {
  value: string;
  label: string;
}

export interface PluginSettingSpec {
  key: string;
  label: string;
  description?: string;
  type: "bool" | "number" | "string" | "select" | "multi_select" | "text" | "size" | "platform_level_rules" | "coding_agents";
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
  platforms?: string[];
  platform_notes?: Record<string, string>;
  settings?: PluginSettingSpec[];
}

export interface PluginState {
  shared_config_source?: string;
  profile_enabled?: Record<string, boolean>;
  manifest: PluginManifest;
  installed: boolean;
  enabled: boolean;
  /** 用户显式覆盖的设置值，默认值以 manifest.settings 声明为准。 */
  settings?: Record<string, unknown>;
  /** 凭据是否已配置；明文永远不会下发。 */
  secrets_configured?: Record<string, boolean>;
  /** 第三方仓库插件的安装来源；有它才显示更新入口。 */
  repo_source?: RepoPluginSource;
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
  /** 待审批草稿的失效时刻，过期后群里的确认码不再可用，可在后台还原。 */
  expires_at?: string;
  /** 当前确认码；后台还原过期草稿会换成新的。历史草稿没有这个字段，按草稿 ID 前六位取。 */
  confirmation_code?: string;
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
  marked_bot_ids?: string[];
  participation?: import("./participation").ParticipationPreferences;
  bot_profile_id?: string;
  group_id: string;
  enabled: boolean;
  enabled_set?: boolean;
  group_triggers?: string[];
  /** 本群触发称呼的匹配松紧；空串或不设表示沿用全局配置。 */
  group_trigger_mode?: AliasTriggerMode | "";
  /** 群专属人设；留空沿用全局系统提示词。 */
  system_prompt?: string;
  /** 兼容旧版回复模式；新界面统一映射为回复欲望。 */
  response_mode?: "" | "quiet" | "assistant" | "standard" | "active" | "super_active" | "custom";
  /** 本群是否穿插括号动作；不设表示跟随机器人。 */
  action_description_enabled?: boolean;
  /** 留空时跟随机器人全局设置。 */
  self_reference?: string;
  sentence_enders?: string;
  welcome_enabled?: boolean;
  welcome_message?: string;
  /** 欢迎词模式：fixed 固定文本 / template 模板池随机 / llm 按人设实时生成；不设等同 fixed。 */
  welcome_mode?: "" | "fixed" | "template" | "llm";
  /** 口吻模板池，每条一行，可用 {user_id} 占位；留空跟随机器人。 */
  welcome_templates?: string[];
  /** LLM 欢迎词每群冷却秒数；不设跟随机器人。 */
  welcome_llm_cooldown_seconds?: number;
  max_context_tokens?: number;
  recent_history_token_budget?: number;
  /** 这个群在滚动 5 小时窗口里能用掉的 token 上限；留空或 0 表示不限。 */
  model_token_quota?: number;
  /** 同一窗口里的模型调用次数上限；留空或 0 表示不限。和 token 上限先到先得。 */
  model_call_quota?: number;
  recent_context_limit?: number;
  max_reply_chars?: number;
  /** 本群的自然分条开关；不设表示跟随机器人。 */
  natural_reply_split_enabled?: boolean;
  /** 本群连续消息合并置信度百分比；未设置时跟随机器人。 */
  reply_merge_confidence_percent?: number;
  reply_preserve_line_breaks?: boolean;
  /** @deprecated 仅兼容历史配置，不再限制聊天分条。 */
  reply_max_bubbles?: number;
  /** @deprecated 仅兼容历史配置，不再限制聊天长度。 */
  direct_reply_chunk_size?: number;
  /** 本群正文超过多少字改用合并转发卡片；未设置或 0 表示无上限。 */
  forward_reply_threshold?: number;
  /** 本群切出超过多少块改用合并转发卡片；未设置或 0 表示无上限。 */
  forward_reply_chunk_threshold?: number;
  proactive_reply_chance?: number;
  proactive_reply_threshold?: number;
  /** 本群闲聊插话总开关；不设表示跟随机器人。 */
  chat_in_enabled?: boolean;
  /** 本群回复欲望；不设表示跟随机器人。 */
  chat_in_level?: "off" | "low" | "medium" | "high" | "max";
  chat_in_threshold?: number;
  chat_in_chance?: number;
  chat_in_cooldown_seconds?: number;
  /** @deprecated 仅兼容历史配置，读取时迁移为极高回复欲望。 */
  natural_interjection_enabled?: boolean;
  /** 本群是否开启社交性回应；不设表示跟随机器人。 */
  social_reply_enabled?: boolean;
  minimum_reply_member_level?: number;
  /** 查看撤回消息后的回复是否自动撤回。 */
  recall_reply_auto_delete_enabled?: boolean;
  /** 自动撤回前的保留时间，单位为秒。 */
  recall_reply_auto_delete_delay_seconds?: number;
  /** 本群账号安全审核；不设表示跟随机器人，false 会同时关闭主动与直接回复审核。 */
  reply_account_safety_audit_enabled?: boolean;
  /** 本群自定义账号安全规则；留空跟随机器人。 */
  reply_account_safety_audit_prompt?: string;
  /** 本群接话评分的补充判据；留空跟随机器人，最多 1000 字。 */
  proactive_reply_extra_criteria?: string;
  /** 本群对 MCP / Skill 的覆盖：档位（off/owner/admins/members，留空跟随机器人）加白名单、黑名单。
   *  判定顺序是停用 > 黑名单 > 白名单 > 档位。 */
  extension_access?: Record<string, { tier?: string; allow?: string[]; deny?: string[] }>;
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
  /** 复用同一条连接、在这个群也开着的其它机器人：这个群会收到多份回复。 */
  shared_with?: BotGroupSharedBot[];
  /** 额度窗口内已用的 token 和调用次数，以及算过继承后真正生效的两档上限。 */
  quota_tokens_used?: number;
  quota_calls_used?: number;
  quota_token_limit?: number;
  quota_call_limit?: number;
}

export interface BotGroupSharedBot {
  bot_profile_id: string;
  name?: string;
}

/**
 * 新群默认：blacklist 表示新加入的群默认工作，whitelist 表示默认不工作。
 * 逐群开关在群管理里，一个群一份，见 saveBotGroupSwitches。
 */
export type GroupAdmissionMode = "blacklist" | "whitelist";

export interface GroupAdmission {
  mode?: GroupAdmissionMode;
  /** @deprecated 已迁进群配置的逐群开关，后端不再写这份名单。 */
  allowed_groups?: string[];
}

/** 私聊准入模式：all 为默认不限制，owner_only 只响应主人，whitelist 只响应主人与白名单。 */
export type PrivateAdmissionMode = "all" | "owner_only" | "whitelist";

export interface PrivateAdmission {
  mode?: PrivateAdmissionMode;
  /** 仅 whitelist 模式生效；主人任何模式下都放行。 */
  allowed_users?: string[];
}

export interface ReplyGate {
  /** 群等级门槛，0 表示不限。 */
  min_group_level?: number;
  /** 等级拿不到时的策略，默认 allow（放行）。 */
  level_unknown_policy?: "allow" | "deny";
  exempt_users?: string[];
  blocked_users?: string[];
  /** 人员准入模式；whitelist 表示只回 allowed_users 里的人，缺省等同 blacklist。 */
  user_admission?: "blacklist" | "whitelist";
  /** 仅 whitelist 模式生效。 */
  allowed_users?: string[];
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
  profile_id?: string;
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
  /** actor 对应的昵称；形如 qq:123456 的 actor 才查得到，查不到时缺省。 */
  actor_name?: string;
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
  group_name?: string;
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
  channel: BotChannelStatus;
  channels?: BotChannelStatus[];
  /** 各机器人自己的 NoneBot 桥接状态，按机器人 ID 索引；没开桥接的不出现。 */
  nonebot_bridges?: Record<string, {
    enabled: boolean;
    connected: boolean;
    endpoint?: string;
    last_error?: string;
    updated_at: string;
  }>;
  plugins: PluginState[];
  recent_events?: BotEvent[];
  active_workers: number;
  /** 正在飞的模型调用。和 active_workers 不是一个量级：一个 worker 一轮会打好几次模型。 */
  llm_concurrency?: LLMConcurrency;
  /** 这些调用花掉的 token。两个桶都只从本次启动算起，重启清零。 */
  llm_usage?: LLMUsageTotals;
  /** 正在跑的后台子任务（生成图片、文档 OCR 等）。 */
  subagent_tasks?: SubagentTask[];
  active_subagent_tasks?: number;
  /** 入站队列积压。排查「机器人怎么不理我」最直接的指标。 */
  pending_events?: number;
  last_error?: string;
  updated_at: string;
}

/** 模型调用并发：此刻有多少次请求发出去还没回来。 */
export interface LLMConcurrency {
  active: number;
  /** 本次运行以来的最高并发。瞬时值落回低谷时用它判断峰值有多高。 */
  peak: number;
  models?: LLMConcurrencyModel[];
}

/** 单个模型上的在飞调用。 */
export interface LLMConcurrencyModel {
  provider?: string;
  model: string;
  active: number;
  /** 这一组里最早发出、还没回来的那次调用的起点。 */
  started_at: string;
}

/** 模型调用的 token 用量。today 跨日清零，session 从本次启动算起。 */
export interface LLMUsageTotals {
  today: LLMUsageCounters;
  session: LLMUsageCounters;
}

export interface LLMUsageCounters {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
  total_tokens: number;
  /** 上游没报用量的调用数；不为 0 时 token 合计只会偏少。 */
  missing_usage_calls: number;
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
  /** 消息入站方式：反连、出站长连接，或平台回调。 */
  inbound?: "reverse_ws" | "outbound" | "callback";
  /** inbound 为 callback 时，要填到对方后台的回调路径。 */
  callback_path?: string;
  /** 出站适配器能不能把 Markdown 渲染出来；决定「Markdown 转纯文本」的默认值。 */
  rich_text?: boolean;
}

const inflightRequests = new Map<string, Promise<unknown>>();
const responseCache = new Map<string, { at: number; data: unknown }>();
let cacheGeneration = 0;

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
      return 30_000;
    case "/api/assistant/plugins/dependencies":
      return 5 * 60_000;
    case "/api/assistant/groups":
      return 20_000;
    // 昵称基本不变，缓存久一点，编辑器里几行私聊对象就不用各打一次请求了。
    case "/api/assistant/user-names":
      return 60_000;
    case "/api/assistant/tasks":
      return 15_000;
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
  cacheGeneration++;
  responseCache.clear();
  inflightRequests.clear();
}

// ApiError 把「后端根本没答话」和「后端答了但不同意」分开。两者混在一起时，
// 后端挂掉会被登录页当成密码错误报出来——用户拿着对的密码被告知密码不对。
export type ApiErrorKind = "offline" | "server" | "auth" | "request";

export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  readonly status: number;
  readonly responseBody: string;

  constructor(message: string, kind: ApiErrorKind, status = 0, responseBody = "") {
    super(message);
    this.name = "ApiError";
    this.kind = kind;
    this.status = status;
    this.responseBody = responseBody;
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

function apiErrorForStatus(status: number, message: string, responseBody = ""): ApiError {
  if (status >= 500) {
    return new ApiError(message || `后端出错（HTTP ${status}）`, "server", status, responseBody);
  }
  if (status === 401 || status === 403) {
    return new ApiError(message || `HTTP ${status}`, "auth", status, responseBody);
  }
  return new ApiError(message || `HTTP ${status}`, "request", status, responseBody);
}

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const finish = trackScopeRequest();
  try {
    return await performRequestJSON<T>(url, init);
  } finally {
    finish();
  }
}

async function performRequestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase();
  const path = requestPath(url);
  const key = requestCacheKey(method, url, init?.body);
  const ttl = cacheTTL(method, url);
  if (isCacheableRead(method, path) && !init?.signal) {
    const cached = responseCache.get(key);
    if (cached && ttl > 0 && Date.now() - cached.at < ttl) {
      return cached.data as T;
    }
    const pending = inflightRequests.get(key);
    if (pending) {
      return pending as Promise<T>;
    }
  }

  const generation = cacheGeneration;
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
    const responseText = await response.text();
    let data = {} as T & { error?: string; message?: string; auth_required?: boolean };
    if (responseText) {
      try {
        data = JSON.parse(responseText) as T & { error?: string; message?: string; auth_required?: boolean };
      } catch {
        // 反向代理常用纯文本或 HTML 返回 5xx。HTML 不直接展示，避免把整页错误模板塞进 toast。
        if (!responseText.trimStart().startsWith("<")) {
          data = { error: responseText.trim() } as T & { error?: string; message?: string; auth_required?: boolean };
        }
      }
    }
    if (!response.ok) {
      // 会话过期或未登录：广播事件让 App 切到登录界面，而不是每个视图各自报错。
      if (response.status === 401 && data.auth_required && !url.startsWith("/api/auth/")) {
        window.dispatchEvent(new CustomEvent("diana:unauthorized"));
      }
      throw apiErrorForStatus(response.status, data.error ?? data.message ?? "", responseText.trim());
    }
    if (isMutatingRequest(method, path)) {
      invalidateAPICache();
      const kind = configurationKindForMutation(path);
      if (kind) notifyConfigurationChanged(kind);
    } else if (isCacheableRead(method, path) && generation !== cacheGeneration) {
      // A save completed while this read was in flight. Join a fresh request
      // instead of returning/caching the pre-save snapshot.
      return performRequestJSON<T>(url, init);
    } else if (ttl > 0) {
      responseCache.set(key, { at: Date.now(), data });
    }
    return data;
  })();

  if (isCacheableRead(method, path) && !init?.signal) {
    inflightRequests.set(key, pending);
  }
  try {
    return (await pending) as T;
  } finally {
    if (inflightRequests.get(key) === pending) inflightRequests.delete(key);
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

export interface BrowserControlPolicy {
  enabled: boolean;
  allowed_origins?: string[];
  allowed_hosts?: string[];
  denied_hosts?: string[];
  write_enabled: boolean;
  command_timeout_ms?: number;
  commands_per_minute?: number;
}

export interface BrowserControlToken {
  id: string;
  name: string;
  prefix: string;
  extension_id?: string;
  created_at: string;
  last_used_at?: string;
}

export interface BrowserControlConnection {
  id: string;
  token_id: string;
  token_name?: string;
  extension_id: string;
  extension_name?: string;
  browser?: string;
  browser_version?: string;
  label?: string;
  connected_at: string;
  last_seen_at: string;
  takeover: boolean;
  takeover_reason?: string;
  allowed_tabs: number;
  commands: number;
}

export interface BrowserControlStatus {
  policy: BrowserControlPolicy;
  tokens: BrowserControlToken[];
  connections: BrowserControlConnection[];
  ready: boolean;
  endpoint: string;
  protocol: number;
  /** 这个部署里带没带扩展源码；带了才显示下载入口。 */
  extension_download?: boolean;
}

export function getBrowserControlStatus(): Promise<BrowserControlStatus> {
  return requestJSON<BrowserControlStatus>("/api/browser-control/status");
}

export function saveBrowserControlPolicy(policy: BrowserControlPolicy): Promise<{ policy: BrowserControlPolicy }> {
  return requestJSON<{ policy: BrowserControlPolicy }>("/api/browser-control/policy", {
    method: "PUT",
    body: JSON.stringify(policy)
  });
}

/** 返回值里的 plaintext 是唯一一次能拿到的令牌明文，之后任何接口都查不到。 */
export function createBrowserControlToken(name: string): Promise<{ token: BrowserControlToken; plaintext: string }> {
  return requestJSON<{ token: BrowserControlToken; plaintext: string }>("/api/browser-control/tokens", {
    method: "POST",
    body: JSON.stringify({ name })
  });
}

export function revokeBrowserControlToken(id: string): Promise<{ token: BrowserControlToken }> {
  return requestJSON<{ token: BrowserControlToken }>(`/api/browser-control/tokens/${encodeURIComponent(id)}`, {
    method: "DELETE"
  });
}

export function setBrowserControlTakeover(id: string, active: boolean, reason = ""): Promise<{ ok: boolean; active: boolean }> {
  return requestJSON<{ ok: boolean; active: boolean }>(
    `/api/browser-control/connections/${encodeURIComponent(id)}/takeover`,
    { method: "POST", body: JSON.stringify({ active, reason }) }
  );
}

export function disconnectBrowserControl(id: string): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>(`/api/browser-control/connections/${encodeURIComponent(id)}`, {
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

/** SillyTavern V2 角色卡的文本字段，和后端 assistant.CharacterCardData 一一对应。 */
export interface CharacterCardData {
  name: string;
  description?: string;
  personality?: string;
  scenario?: string;
  first_mes?: string;
  mes_example?: string;
  system_prompt?: string;
}

/** V2 角色卡封套：spec + data。生成接口按这个形状发回，导出的文件也是它。 */
export interface CharacterCardV2 {
  spec: string;
  spec_version: string;
  data: CharacterCardData;
}

export interface PersonaGenerateResponse {
  persona: string;
  /** 生成正文用的那张卡；人设框里的正文是它拼出来的，导出和下次改写都要用它。 */
  card?: CharacterCardV2;
  model?: string;
  provider?: string;
}

/**
 * 用当前已配置的模型写一张角色卡，并把拼装后的人设正文一起返回；带上 current 时
 * 是改写而不是重写。改写时把上一次那张卡（card）一起带上：正文是卡拼出来的结果，
 * 段头和展开过的宏反推不回字段，没有卡模型只能照正文重写一张。
 */
export function generatePersona(
  description: string,
  name?: string,
  current?: string,
  options?: { response_mode?: string; profile_id?: string; group?: string; model?: string; card?: CharacterCardV2 | null }
): Promise<PersonaGenerateResponse> {
  const { card, ...rest } = options ?? {};
  return requestJSON<PersonaGenerateResponse>("/api/llm/persona", {
    method: "POST",
    body: JSON.stringify({ description, name, current, ...rest, ...(card ? { card } : {}) })
  });
}

/** 人设检查报出的一条。 */
export interface PersonaLintFinding {
  /** sentence-enders | self-reference | action-description | formatting | venue */
  code: string;
  /** 正文里被命中的原话，后端保证能在提交的正文里逐字找到。 */
  match: string;
  message: string;
}

export interface PersonaReviewResponse {
  findings: PersonaLintFinding[];
  model?: string;
  provider?: string;
}

/**
 * 让模型读一遍人设正文，挑出「和界面开关抢同一件事」的地方。
 *
 * 这是人设正文唯一的检查：判断的是意思不是字面，代价是一次模型往返。所以它由用户
 * 点按钮触发，signal 用来让「跳过」当场掐断请求。
 */
export function reviewPersona(
  text: string,
  options?: {
    self_reference?: string;
    sentence_enders?: string;
    action_description_enabled?: boolean;
    profile_id?: string;
    group?: string;
    model?: string;
  },
  signal?: AbortSignal
): Promise<PersonaReviewResponse> {
  return requestJSON<PersonaReviewResponse>("/api/llm/persona/lint", {
    method: "POST",
    body: JSON.stringify({ text, ...(options ?? {}) }),
    signal
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

export function getNewBotProfileDefaults(platform: string): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>(`/api/assistant/config/defaults?platform=${encodeURIComponent(platform)}`);
}

export function createBotProfileConfig(config: BotProfileConfig): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/new", { method: "POST", body: JSON.stringify(config) });
}

export function saveBotProfileConfig(config: BotProfileConfig): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config", {
    method: "POST",
    body: JSON.stringify(config)
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

export function saveMessageRelays(relays: MessageRelayPair[]): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/message-relays", {
    method: "POST",
    body: JSON.stringify({ relays })
  });
}

export function saveProfileEnabled(profileID: string, enabled: boolean): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/profile-enabled", {
    method: "POST",
    body: JSON.stringify({ profile_id: profileID, enabled })
  });
}

export function saveAllProfilesEnabled(enabled: boolean): Promise<BotProfileConfig> {
  return requestJSON<BotProfileConfig>("/api/assistant/config/profiles-enabled", {
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

export function listPlugins(profile = ""): Promise<PluginState[]> {
  return requestJSON<PluginState[]>(`/api/assistant/plugins?profile=${encodeURIComponent(profile)}`);
}

export interface ManagedExtension { kind: "skill" | "mcp"; id: string; name: string; description?: string; source?: string; managed?: boolean; enabled: boolean; available?: boolean; members_enabled?: boolean; member_audience?: {min_role?: string; users?: string[]; groups?: string[]}; bundled?: boolean; transport?: string; tools?: string[]; resident?: boolean; keywords?: string[]; error?: string }
export function listManagedExtensions(profile = ""): Promise<{items: ManagedExtension[]}> {
  return requestJSON(`/api/assistant/extensions?profile=${encodeURIComponent(profile)}`);
}
/** 内置的 MCP 接入模板：界面照着字段渲染表单，拼配置在服务端做。 */
export interface MCPPresetField { key: string; label: string; placeholder?: string; hint?: string; required?: boolean; secret?: boolean }
export interface MCPPresetTransport { id: string; label: string; hint?: string; fields: MCPPresetField[]; verifiable?: boolean }
export interface MCPPreset { id: string; name: string; title: string; summary: string; docs_url?: string; transports: MCPPresetTransport[] }
export function listMCPPresets(): Promise<{items: {preset: MCPPreset; installed: boolean; hidden?: boolean}[]}> {
  return requestJSON("/api/assistant/extensions", {method: "POST", body: JSON.stringify({operation: "presets", kind: "mcp"})});
}
export function manageExtension<T = {ok: boolean}>(input: Record<string, unknown>): Promise<T> {
  return requestJSON<T>("/api/assistant/extensions", {method:"POST", body:JSON.stringify(input)});
}

/** 交互式浏览器接入：模型通过 CDP 操作一个真实浏览器，用的是那个浏览器已有的登录态。 */
export interface AgentBrowserSettings { profile_id?: string; cdp_url?: string; timeout_ms?: number; tools: string[] }
export function getAgentBrowser(profile = ""): Promise<AgentBrowserSettings> {
  return requestJSON(`/api/assistant/agent-browser?profile=${encodeURIComponent(profile)}`);
}
export function saveAgentBrowser(profile: string, cdpURL: string, timeoutMS: number): Promise<AgentBrowserSettings> {
  return requestJSON("/api/assistant/agent-browser", {method: "POST", body: JSON.stringify({profile_id: profile, cdp_url: cdpURL, timeout_ms: timeoutMS})});
}
export function testAgentBrowser(profile: string, cdpURL: string): Promise<{connected: boolean; browser?: string; error?: string}> {
  return requestJSON("/api/assistant/agent-browser/test", {method: "POST", body: JSON.stringify({profile_id: profile, cdp_url: cdpURL})});
}

/** 常驻档位的一行：一个内置工具，或者一条 MCP 服务。resident 不带表示跟随默认档。 */
export interface AgentResidencyEntry { id: string; kind: "tool" | "mcp" | "plugin"; name: string; description?: string; detail?: string; tools?: string[]; default: boolean; resident?: boolean; resident_tokens?: number; deferred_tokens?: number; parent?: string; stale?: boolean }
export function listAgentResidency(profile = ""): Promise<{items: AgentResidencyEntry[]; listed?: boolean}> {
  return requestJSON(`/api/assistant/agent-residency?profile=${encodeURIComponent(profile)}`);
}
export function setAgentResidency(profile: string, id: string, resident: boolean | null): Promise<{ok: boolean}> {
  const body: Record<string, unknown> = {profile_id: profile, id};
  if (resident !== null) body.resident = resident;
  return requestJSON("/api/assistant/agent-residency", {method: "POST", body: JSON.stringify(body)});
}
/** 整份写下常驻名单；ids 传 null 表示退回内置推荐名单，往后跟着版本走。 */
export function saveAgentResidencyList(profile: string, ids: string[] | null): Promise<{ok: boolean}> {
  const body: Record<string, unknown> = ids === null ? {profile_id: profile, reset: true} : {profile_id: profile, ids};
  return requestJSON("/api/assistant/agent-residency", {method: "POST", body: JSON.stringify(body)});
}

export function installPlugin(id: string): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/install`, { method: "POST" });
}

export function uninstallPlugin(id: string): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/uninstall`, { method: "POST" });
}

export interface RepoPluginPermission {
  id: string;
  label: string;
  /** 高敏感权限在安装确认框里置顶并加醒目标记。 */
  sensitive?: boolean;
}

export interface RepoPluginRisk {
  /** true 表示安装的是默认分支最新提交，内容与权限随时可能变化。 */
  floating_ref?: boolean;
  warnings?: string[];
}

export interface RepoPluginSourceRef {
  owner: string;
  repo: string;
  ref?: string;
}

/** 第三方仓库插件的安装来源；只在从 GitHub 安装的插件上出现。 */
export interface RepoPluginSource {
  id: string;
  owner: string;
  repo: string;
  ref?: string;
  /** 实际安装的提交 SHA；ref 是分支或 tag 时会移动，commit 不会。 */
  commit?: string;
  version: string;
  url: string;
  installed_at?: string;
}

/** 粘贴 GitHub 链接后的安装预览：确认框据此渲染权限、设置与风险。 */
export interface RepoPluginPreview {
  source: RepoPluginSourceRef;
  /** 预览读到的提交；安装时原样带回，仓库在中间有新提交会被拒绝。 */
  commit: string;
  manifest: PluginManifest;
  permissions: RepoPluginPermission[];
  files: string[];
  risk: RepoPluginRisk;
  /** 这个 ID 已经被占用时出现：已装版本、升降级关系、是否内置。 */
  installed?: RepoPluginInstalledVersion;
}

/** 同 ID 已被占用时的情况；内置插件不可替换，第三方覆盖需要显式确认。 */
export interface RepoPluginInstalledVersion {
  version: string;
  /** upgrade / downgrade / same */
  change?: string;
  built_in?: boolean;
}

export function previewRepoPlugin(url: string): Promise<RepoPluginPreview> {
  return requestJSON<RepoPluginPreview>("/api/assistant/plugins/repo/preview", {
    method: "POST",
    body: JSON.stringify({ url })
  });
}

export function installRepoPlugin(
  url: string,
  acceptRisk: boolean,
  commit: string,
  replace = false
): Promise<PluginState> {
  return requestJSON<PluginState>("/api/assistant/plugins/repo/install", {
    method: "POST",
    body: JSON.stringify({ url, accept_risk: acceptRisk, commit, replace })
  });
}

export function updateRepoPlugin(id: string, acceptRisk: boolean, commit = ""): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/repo/update/${encodeURIComponent(id)}`, {
    method: "POST",
    body: JSON.stringify({ url: "", accept_risk: acceptRisk, commit })
  });
}

export function setPluginEnabled(id: string, enabled: boolean, profile = ""): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/enabled?profile=${encodeURIComponent(profile)}`, {
    method: "POST",
    body: JSON.stringify({ enabled })
  });
}

export function updatePluginSettings(
  id: string,
  settings: Record<string, unknown>,
  clearSecrets: string[] = [],
  profileID = ""
): Promise<PluginState> {
  return requestJSON<PluginState>(`/api/assistant/plugins/${encodeURIComponent(id)}/settings?profile=${encodeURIComponent(profileID)}`, {
    method: "POST",
    body: JSON.stringify({ settings, clear_secrets: clearSecrets })
  });
}

export interface MusicConnectionStatus {
  source: string;
  label: string;
  search_ok: boolean;
  playable: boolean;
  api_configured: boolean;
  cookie_configured: boolean;
  message: string;
}

export function testMusicConnections(
  settings: Record<string, unknown>,
  clearSecrets: string[] = [],
  profileID = ""
): Promise<{ sources: MusicConnectionStatus[] }> {
  return requestJSON<{ sources: MusicConnectionStatus[] }>(`/api/assistant/plugins/music/test?profile=${encodeURIComponent(profileID)}`, {
    method: "POST",
    body: JSON.stringify({ settings, clear_secrets: clearSecrets })
  });
}

export function createRepositoryIssue(input: RepositoryIssueCreateInput, profileID = ""): Promise<RepositoryIssueCreateResult> {
  return requestJSON<RepositoryIssueCreateResult>(`/api/assistant/plugins/repository-publish/issues?profile=${encodeURIComponent(profileID)}`, {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function listRepositoryIssueDrafts(status = "all"): Promise<{ drafts: RepositoryIssueDraft[] }> {
  return requestJSON<{ drafts: RepositoryIssueDraft[] }>(`/api/assistant/plugins/repository-publish/drafts?status=${encodeURIComponent(status)}`);
}

export function restoreRepositoryIssueDraft(id: string, profileID = ""): Promise<{ draft: RepositoryIssueDraft }> {
  return requestJSON<{ draft: RepositoryIssueDraft }>(
    `/api/assistant/plugins/repository-publish/drafts/${encodeURIComponent(id)}/restore?profile=${encodeURIComponent(profileID)}`,
    { method: "POST" }
  );
}

export function editRepositoryIssueDraft(
  id: string,
  input: { title: string; body: string; labels: string[] },
  profileID = ""
): Promise<{ draft: RepositoryIssueDraft }> {
  return requestJSON<{ draft: RepositoryIssueDraft }>(
    `/api/assistant/plugins/repository-publish/drafts/${encodeURIComponent(id)}?profile=${encodeURIComponent(profileID)}`,
    { method: "PATCH", body: JSON.stringify(input) }
  );
}

export function deleteRepositoryIssueDraft(id: string, profileID = ""): Promise<{ deleted: boolean }> {
  return requestJSON<{ deleted: boolean }>(
    `/api/assistant/plugins/repository-publish/drafts/${encodeURIComponent(id)}?profile=${encodeURIComponent(profileID)}`,
    { method: "DELETE" }
  );
}

export function publishRepositoryIssueDraft(id: string, profileID = ""): Promise<RepositoryIssueCreateResult> {
  return requestJSON<RepositoryIssueCreateResult>(
    `/api/assistant/plugins/repository-publish/drafts/${encodeURIComponent(id)}/publish?profile=${encodeURIComponent(profileID)}`,
    { method: "POST" }
  );
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

export function requestBotGroupAdminChallenge(groupID: string, userID: string, profileID = ""): Promise<BotGroupAdminChallengeResponse> {
  return requestJSON<BotGroupAdminChallengeResponse>("/api/assistant/group-admin/challenge", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID, profile_id: profileID })
  });
}

export function verifyBotGroupAdmin(groupID: string, userID: string, code: string, profileID = ""): Promise<BotGroupAdminConfigResponse> {
  return requestJSON<BotGroupAdminConfigResponse>("/api/assistant/group-admin/verify", {
    method: "POST",
    body: JSON.stringify({ group_id: groupID, user_id: userID, code, profile_id: profileID })
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
	channel?: "release" | "beta" | "canary";
	auto_download: boolean;
	auto_install: boolean;
	/** 下载加速策略：auto（实测挑线路）、direct（始终直连）或一条具体的镜像地址。 */
	github_mirror?: string;
}

export interface UpdateGitHubTokenStatus {
	configured: boolean;
	source?: "stored" | "environment" | "";
}

export function getUpdateGitHubToken(): Promise<UpdateGitHubTokenStatus> {
	return requestJSON<UpdateGitHubTokenStatus>("/api/system/update/github-token");
}

export function saveUpdateGitHubToken(token: string, clear = false): Promise<UpdateGitHubTokenStatus> {
	return requestJSON<UpdateGitHubTokenStatus>("/api/system/update/github-token", {
		method: "PUT",
		body: JSON.stringify({ token, clear })
	});
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

export interface MediaCachePolicy {
  retention_days: number;
  max_mb: number;
}

export function getMediaCachePolicy(): Promise<MediaCachePolicy> {
  return requestJSON<MediaCachePolicy>("/api/system/media-cache");
}

export function saveMediaCachePolicy(policy: MediaCachePolicy): Promise<MediaCachePolicy> {
  return requestJSON<MediaCachePolicy>("/api/system/media-cache", {
    method: "POST",
    body: JSON.stringify(policy)
  });
}

/** 设置页存储卡片的载荷：整块盘的容量 + Diana 数据目录按文件类型的拆分。 */
export interface StorageUsageCategory {
  key: string;
  label: string;
  bytes: number;
  files: number;
}

export interface StorageUsage {
  collected_at: string;
  path: string;
  disk_total_bytes?: number;
  disk_used_bytes?: number;
  disk_free_bytes?: number;
  disk_usage_percent?: number;
  diana_bytes: number;
  diana_files: number;
  categories: StorageUsageCategory[];
  /** 后台遍历完成的时间；从没跑完过时缺省 */
  scanned_at?: string;
  /** 正在后台遍历数据目录，拆分结果还是上一次的（或为空） */
  scanning: boolean;
  disk_unavailable?: string;
}

export function getStorageUsage(): Promise<StorageUsage> {
  return requestJSON<StorageUsage>("/api/system/storage");
}

export interface HistoryMediaPolicy { retention_days: number; max_mb: number; }
export function getHistoryMediaPolicy(): Promise<HistoryMediaPolicy> { return requestJSON<HistoryMediaPolicy>("/api/system/history-media"); }
export function saveHistoryMediaPolicy(policy: HistoryMediaPolicy): Promise<HistoryMediaPolicy> {
  return requestJSON<HistoryMediaPolicy>("/api/system/history-media", { method: "POST", body: JSON.stringify(policy) });
}

export interface MediaBaseURLSetting {
  base_url: string;
  source: "database" | "config" | "auto";
}
export function getMediaBaseURLSetting(): Promise<MediaBaseURLSetting> {
  return requestJSON<MediaBaseURLSetting>("/api/system/media-base-url");
}
export function saveMediaBaseURLSetting(setting: { base_url: string }): Promise<MediaBaseURLSetting> {
  return requestJSON<MediaBaseURLSetting>("/api/system/media-base-url", { method: "POST", body: JSON.stringify(setting) });
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

/** 同一条连接上的另一台机器人及其群归属：路由表散在各台自己的配置里，这是那张全貌。 */
export interface ConnectionPeer {
  bot_profile_id: string;
  name?: string;
  /** 这台机器人本身启不启用。停用的不参与回复。 */
  enabled: boolean;
  /** 新群默认工作：相当于这台收所有群，白名单模式才是划分。 */
  new_group_enabled: boolean;
  /** 明确开着的群号。新群默认为开时，这份名单之外的群它也照收。 */
  enabled_groups?: string[];
}

export interface ConsoleGroupsResponse {
  groups: BotGroupSummary[];
  plugins: PluginState[];
  live_available: boolean;
  warning?: string;
  connection_peers?: ConnectionPeer[];
  /** 额度统计窗口长度，前端据此写「最近 N 小时」，不要自己写死 5。 */
  quota_window_seconds?: number;
}

export function listBotGroups(refresh = false, profile = ""): Promise<ConsoleGroupsResponse> {
  const params = new URLSearchParams();
  if (refresh) params.set("refresh", "1");
  if (profile) params.set("profile", profile);
  const suffix = params.size > 0 ? `?${params.toString()}` : "";
  return requestJSON<ConsoleGroupsResponse>(`/api/assistant/groups${suffix}`);
}

/**
 * saveBotGroupSwitches 是群管理里那排批量操作：一键开关传进来的这些群，
 * 以及「新加入的群默认工作吗」。两件事可以一起提交，也可以只提一件。
 */
export function saveBotGroupSwitches(payload: {
  bot_profile_id: string;
  group_ids?: string[];
  enabled?: boolean;
  new_group_enabled?: boolean;
  min_group_level?: number;
  level_unknown_policy?: "allow" | "deny";
}): Promise<{ ok: boolean; updated: number; warning?: string }> {
  return requestJSON<{ ok: boolean; updated: number; warning?: string }>("/api/assistant/groups/switches", {
    method: "POST",
    body: JSON.stringify(payload)
  });
}

export function saveBotGroup(config: BotGroupConfig): Promise<{ config: BotGroupConfig; warning?: string }> {
  return requestJSON<{ config: BotGroupConfig; warning?: string }>("/api/assistant/groups", {
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

/** 总览页能切的时间窗。today 走实时统计，其余走库里的窗口查询。 */
export type StatsRangeID = "1h" | "12h" | "24h";

/** 一个时间窗内的模型用量。cached_input_tokens 已包含在 input_tokens 里。 */
export interface LLMUsageSummary {
  since: string;
  until: string;
  recorded_calls: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cached_input_tokens: number;
}

/** 一个时间窗内总览页要用的全部数字。 */
export interface StatsRange {
  id: StatsRangeID;
  since: string;
  until: string;
  messages: number;
  handled: number;
  errors: number;
  avg_reply_ms: number;
  /** 平均耗时的样本数；为 0 说明这段时间没有可计时的回复，平均值不该显示成 0。 */
  replies_measured: number;
  usage: LLMUsageSummary;
}

export interface StatsRanges {
  until: string;
  ranges: StatsRange[];
}

/**
 * 按时间窗读总览页统计。数字来自库里的队列事件和用量日志而非进程内累加器，
 * 所以跨重启仍然成立，和「今日」那一档不是同一个口径。
 */
export function getStatsRanges(): Promise<StatsRanges> {
  return requestJSON<StatsRanges>("/api/stats/ranges");
}

export type AssistantEventRange = "1h" | "24h" | "7d" | "30d" | "all";
export type AssistantEventResultFilter = "all" | "replied" | "not_replied" | "pending" | "error" | "notice";

export interface AssistantEventImage {
  index: number;
  summary?: string;
  unavailable?: boolean;
}

export interface AssistantEventMemory {
  id?: string;
  kind?: string;
  topic?: string;
  entity?: string;
  content: string;
  source_type?: string;
  scope_key?: string;
  source_group_id?: string;
  source_message_id?: string;
  visibility?: string;
  sensitive?: boolean;
  confidence?: number;
  importance?: number;
  retrieval_score?: number;
  retrieval_reason?: string;
}

export interface AssistantEventTemporaryMemory {
  id?: string;
  kind: "session_thread" | "private_thread_state" | string;
  scope?: "user" | "session" | string;
  task_kind?: string;
  topic?: string;
  content: unknown;
  version?: number;
  expires_at?: string;
  source_message_id?: string;
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
  /** 这条消息所有模型调用的墙钟耗时之和；和 duration_ms（整条消息的处理耗时）不同。 */
  llm_duration_ms?: number;
  /** 输出 token 速率，由 output_tokens 和 llm_duration_ms 算出。 */
  output_tokens_per_second?: number;
  /** 首 token 时延均值；只有流式跑通的调用才有样本。 */
  avg_ttft_ms?: number;
  ttft_calls?: number;
  /** 主回复实际用到的模型，按首次调用排序；带图的一轮可能先走视觉模型。 */
  /** 上游没报用量的调用数；不为 0 时这条的 token 数偏少。 */
  usage_missing_calls?: number;
  reply_models?: string[];
  /** 这条消息所有模型调用按模型汇总的次数，路由、审核、记忆抽取都算。 */
  models?: AssistantEventModelUsage[];
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
  /** 实际进入本轮模型上下文的长期记忆；仅管理员事件接口返回。 */
  memories?: AssistantEventMemory[];
  /** 实际进入本轮模型上下文的短期会话状态；仅管理员事件接口返回。 */
  temporary_memories?: AssistantEventTemporaryMemory[];
  /** 发送者头像地址；只有 QQ 系给得出，其余平台为空，界面退回首字母占位。 */
  sender_avatar_url?: string;
  /** 这条消息触发的后台子任务。图片是任务跑完后异步发出去的。 */
  subtasks?: AssistantEventSubtask[];
  /** 这一轮实际发出去的内容概览。reply 只是文本，说不出还发了卡片和媒体。 */
  delivery?: AssistantEventDelivery;
}

export interface AssistantEventModelUsage {
  model: string;
  provider?: string;
  calls: number;
}

/** 一轮里发出去的一张图；来源路径不下发，按序号从 outbound-images 接口取。 */
export interface AssistantEventOutboundMedia {
  kind: string;
  label?: string;
  /** 内联图片（data URI）不落库，没法预览。 */
  inline?: boolean;
}

export interface AssistantEventDelivery {
  messages?: number;
  images?: number;
  videos?: number;
  audios?: number;
  forward_cards?: number;
  forward_nodes?: number;
  media?: AssistantEventOutboundMedia[];
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
  llm_duration_ms: number;
  output_tokens_per_second: number;
  avg_ttft_ms: number;
  ttft_calls: number;
  /** 上游没报用量的调用数；不为 0 时 token 合计偏少。 */
  usage_missing_calls?: number;
  page: number;
  limit: number;
  has_more: boolean;
  group?: string;
  groups: AssistantEventGroup[];
  user?: string;
  query?: string;
  private_chats: AssistantEventPrivateChat[];
  context_budget?: AssistantContextBudget;
  resident_context?: AssistantResidentContext;
}

export interface AssistantEventGroup {
  group_id: string;
  events: number;
  group_name?: string;
  avatar_url?: string;
}

export interface AssistantEventPrivateChat {
  user_id: string;
  events: number;
  user_name?: string;
  bot_profile_id?: string;
}

/** 每轮都注入、与当前消息无关的一块上下文。 */
export interface AssistantResidentContextBlock {
  key: string;
  label: string;
  tokens: number;
  /** 这块所在层的 token 配额；0 表示它不单独占一层配额。 */
  budget?: number;
  content?: string;
  note?: string;
}

export interface AssistantResidentContext {
  profile_id?: string;
  group_id?: string;
  context_window: number;
  blocks: AssistantResidentContextBlock[];
  total_tokens: number;
  note?: string;
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
  profile = "",
  user = "",
  query = "",
  mode: "full" | "list" | "summary" = "full",
  signal?: AbortSignal
): Promise<AssistantEventsResponse> {
  const params = new URLSearchParams({ range, result, page: String(page), limit: String(limit) });
  params.set("mode", mode);
  if (group) params.set("group", group);
  if (user) params.set("user", user);
  if (query) params.set("q", query);
  if (profile) params.set("profile", profile);
  return requestJSON<AssistantEventsResponse>(`/api/assistant/events?${params.toString()}`, { signal });
}

export interface AssistantEventTraceResponse {
  memories?: AssistantEventMemory[];
  temporary_memories?: AssistantEventTemporaryMemory[];
  event_id: string;
  message_id?: string;
  steps: AppLogEntry[];
}

/** 表情包池里的一张表情包；同一张图在多个会话出现只列一次，字段取最近那次。 */
export interface StickerLibraryItem {
  hash: string;
  summary: string;
  /** 视觉模型写的简介；还没被搜到过的表情包没有。 */
  description?: string;
  mime?: string;
  kind: string;
  group_id?: string;
  user_id?: string;
  profile_id?: string;
  /** 出现过的会话数。 */
  sessions: number;
  last_seen: string;
}

export interface StickerLibraryPage {
  items: StickerLibraryItem[];
  total: number;
}

export function listStickerLibrary(profile: string, query: string, offset: number, limit: number): Promise<StickerLibraryPage> {
  const params = new URLSearchParams({ offset: String(offset), limit: String(limit) });
  if (profile) params.set("profile", profile);
  if (query.trim()) params.set("q", query.trim());
  return requestJSON<StickerLibraryPage>(`/api/assistant/stickers?${params.toString()}`);
}

export function stickerImageURL(hash: string, profile: string, thumbnail = false): string {
  const params = new URLSearchParams();
  if (profile) params.set("profile", profile);
  if (thumbnail) params.set("thumbnail", "1");
  const query = params.toString();
  return `/api/assistant/stickers/${encodeURIComponent(hash)}/image${query ? `?${query}` : ""}`;
}

export function getAssistantEventTrace(eventID: string): Promise<AssistantEventTraceResponse> {
  return requestJSON<AssistantEventTraceResponse>(`/api/assistant/events/${encodeURIComponent(eventID)}/trace`);
}

/**
 * 一条原始发言缓冲。它不是长期记忆：入库时没有模型参与，只按「至少两个字」过滤，
 * 每人只留最近 20 条。控制台按「最近发言」显示，只给排查用。
 */
export interface UserMemoryItem {
  text: string;
  source?: string;
  group_id?: string;
  message_id?: string;
  at?: string;
}

/** 人员画像里的一条：这个人住在哪、做什么、有什么生活习惯。 */
export interface UserPortraitTrait {
  field: string;
  label: string;
  value: string;
  evidence?: string;
  confidence?: number;
  /** stated 是本人明说的，inferred 是机器人据上下文推断的，manual 是当面记下的。 */
  source?: string;
  updated_at?: string;
}

/** 画像栏目表，由后端给出，前端不再自己抄一份字段到中文的映射。 */
export interface PortraitFieldSpec {
  field: string;
  label: string;
  hint: string;
  capacity: number;
}

/** 与机器人的恋爱关系状态（人机恋）；没谈过就是缺省。 */
export interface UserRomanceState {
  active: boolean;
  /** 确立关系的时间，纪念日从它算。 */
  since?: string;
  started_by?: string;
}

export interface UserMemoryProfile {
	bot_profile_id?: string;
  user_id: string;
  display_name?: string;
  favorability: number;
  message_count: number;
  /** 原始发言缓冲，不是长期记忆；见 UserMemoryItem。 */
  memories?: UserMemoryItem[];
  portrait?: UserPortraitTrait[];
  romance?: UserRomanceState;
  /** 列表接口不带正文，只带条数；详情接口带完整内容。 */
  memory_count?: number;
  /** 门控器写出来的长期记忆条数，和 memory_count 不是一回事。 */
  structured_memory_count?: number;
  portrait_count?: number;
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
  /** 后端收敛后的排序键与方向；传了不认识的值会回落到最近更新倒序。 */
  sort?: AssistantUsersSort;
  order?: AssistantUsersOrder;
  limit: number;
  offset: number;
}

export type AssistantUsersSort = "updated" | "last_seen" | "favorability" | "messages";
export type AssistantUsersOrder = "asc" | "desc";

/**
 * 门控器筛出来的一条长期记忆。和 UserMemoryItem 的区别是它由模型判定值不值得记、
 * 带主题和置信度、按相关性被检索进提示词。
 */
export interface UserStructuredMemory {
  id: string;
  subject_user_id?: string;
  subject_name?: string;
  key: string;
  kind: string;
  topic: string;
  entity?: string;
  content: string;
  evidence?: string;
  source_type: string;
  source_group_id?: string;
  source_message_id?: string;
  source_event_time?: string;
  confidence: number;
  importance: number;
  visibility: string;
  sensitive: boolean;
  expires_at?: string;
  last_verified_at?: string;
  version: number;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface AssistantUserDetailResponse {
  profile: UserMemoryProfile;
  favorability_changes: UserFavorabilityChange[];
  portrait_fields: PortraitFieldSpec[];
  structured_memories: UserStructuredMemory[];
}

export function listAssistantUsers(
  query = "",
  limit = 50,
  offset = 0,
  profile = "",
  sort: AssistantUsersSort = "updated",
  order: AssistantUsersOrder = "desc"
): Promise<AssistantUsersResponse> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset), sort, order });
  if (query) {
    params.set("q", query);
  }
  if (profile) {
    params.set("profile", profile);
  }
  return requestJSON<AssistantUsersResponse>(`/api/assistant/users?${params.toString()}`);
}

export function getAssistantUser(userID: string, profile = ""): Promise<AssistantUserDetailResponse> {
  const suffix = `?profile=${encodeURIComponent(profile)}`;
  return requestJSON<AssistantUserDetailResponse>(`/api/assistant/users/${encodeURIComponent(userID)}${suffix}`);
}

export function saveAssistantUser(profile: UserMemoryProfile, remove = false): Promise<{ ok: boolean }> {
  return requestJSON(`/api/assistant/users/${encodeURIComponent(profile.user_id)}?profile=${encodeURIComponent(profile.bot_profile_id ?? "")}`, {
    method: remove ? "DELETE" : "PUT", body: JSON.stringify({ profile })
  });
}

/** 清空一个人的结构化长期记忆；给 memoryID 就只删那一条。profile 必须显式指定。 */
export function clearAssistantUserMemories(userID: string, profile: string, memoryID = ""): Promise<{ ok: boolean; cleared: number }> {
  const suffix = memoryID ? `/${encodeURIComponent(memoryID)}` : "";
  return requestJSON(
    `/api/assistant/users/${encodeURIComponent(userID)}/memories${suffix}?profile=${encodeURIComponent(profile)}`,
    { method: "DELETE" }
  );
}

export function deleteBotGroup(groupID: string, profile = ""): Promise<{ ok: boolean }> {
  return requestJSON(`/api/assistant/groups/${encodeURIComponent(groupID)}?profile=${encodeURIComponent(profile)}`, { method: "DELETE" });
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

/** 人设的品格层：身份、价值、硬边界。只有人能改，前端只原样搬运，不逐字段编辑。 */
export interface PersonaSoul {
  identity?: string;
  priority?: { order?: string[]; note?: string };
  values?: { value: string; why?: string }[];
  honesty?: string[];
  self_nature?: string;
  relationships?: { owner?: string; admins?: string; members?: string };
  correctable?: string;
  restraint?: string;
  hard_limits?: { limit: string; why?: string }[];
  on_criticism?: string;
  on_mistake?: string;
  open_questions?: string[];
}

export interface Persona {
  soul?: PersonaSoul;
  id: string;
  name: string;
  system_prompt?: string;
  /** 跟着正文走：带段头的接管正文套到填空题档上会和运行时重复。 */
  persona_mode?: "fill" | "own";
  action_description_enabled?: boolean;
  daypart_tone_enabled?: boolean;
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
  /** 旧文件中无法识别的表达风格；忽略该字段并保留人设正文。 */
  unknown_styles?: string[];
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

/** 机器人自己写下的一条自述。写入只有它自己能做，这里只读、删和清空。 */
export interface SelfNote {
  id: string;
  topic: string;
  content: string;
  status: "active" | "superseded" | "deleted";
  version: number;
  source_group_id?: string;
  source_user_id?: string;
  source_user_name?: string;
  created_at: string;
  updated_at: string;
}

export interface SelfNoteListResult {
  notes: SelfNote[];
  /** 这台机器人有没有开自述。关着时列表为空，但「空」和「没开」是两件事。 */
  enabled: boolean;
}

// profile 指这条自述属于哪台机器人。自述按机器人隔离，留空时后端落到当前这台。
function selfNoteQuery(profile: string, includeInactive = false): string {
  const params = new URLSearchParams();
  if (profile) params.set("profile", profile);
  if (includeInactive) params.set("include_inactive", "true");
  return params.size > 0 ? `?${params.toString()}` : "";
}

export function listSelfNotes(profile: string, includeInactive = false): Promise<SelfNoteListResult> {
  return requestJSON<SelfNoteListResult>(`/api/assistant/self-notes${selfNoteQuery(profile, includeInactive)}`);
}

export function deleteSelfNote(profile: string, id: string): Promise<SelfNoteListResult> {
  return requestJSON<SelfNoteListResult>(`/api/assistant/self-notes/delete${selfNoteQuery(profile)}`, {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export function purgeSelfNotes(profile: string): Promise<SelfNoteListResult> {
  return requestJSON<SelfNoteListResult>(`/api/assistant/self-notes/purge${selfNoteQuery(profile)}`, { method: "POST" });
}

/** YAML 只能在后端解析：这里原样把文件内容发过去。JSON 文件走上面那条。 */
export function importPersonaSource(source: string): Promise<PersonaImportResult> {
  return requestJSON<PersonaImportResult>("/api/assistant/personas/import", {
    method: "POST",
    body: JSON.stringify({ version: PERSONA_EXPORT_VERSION, source })
  });
}

export function deletePersona(id: string): Promise<{ personas: Persona[] }> {
  return requestJSON<{ personas: Persona[] }>("/api/assistant/personas/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

/** 世界书的一条世界观设定。树是全局一棵，parent_id 挂父节点，空表示根。 */
export interface WorldBookNode {
  id: string;
  parent_id?: string;
  title: string;
  content?: string;
  /** 触发词：最近对话里出现任意一个就注入本条。 */
  keywords?: string[];
  /** 副触发词（AND 逻辑）：填了之后主词命中还要求任一副词在场才注入。 */
  secondary_keywords?: string[];
  /** 常驻：每轮都注入，不看触发词。 */
  always_on?: boolean;
  /** 关掉后整个子树都不注入；缺省启用。 */
  enabled?: boolean;
  updated_at?: string;
}

export interface WorldBookListResponse {
  nodes: WorldBookNode[];
  limit: number;
}

export function listWorldBook(): Promise<WorldBookListResponse> {
  return requestJSON<WorldBookListResponse>("/api/assistant/world-book");
}

/** 带 id 是改，不带是新增。返回落库后的那一份和整棵树。 */
export function saveWorldBookNode(node: WorldBookNode | Omit<WorldBookNode, "id">): Promise<{ node: WorldBookNode; nodes: WorldBookNode[] }> {
  return requestJSON<{ node: WorldBookNode; nodes: WorldBookNode[] }>("/api/assistant/world-book", {
    method: "POST",
    body: JSON.stringify({ node })
  });
}

/** 删掉一个节点，它的子节点会接到它的父节点上。 */
export function deleteWorldBookNode(id: string): Promise<{ nodes: WorldBookNode[] }> {
  return requestJSON<{ nodes: WorldBookNode[] }>("/api/assistant/world-book/delete", {
    method: "POST",
    body: JSON.stringify({ id })
  });
}

export interface WorldBookImportResult {
  nodes: WorldBookNode[];
  imported: number;
  dropped: number;
}

/** 导出文件的格式。version 现在不参与判断，只为将来能认出旧文件。 */
export const WORLD_BOOK_EXPORT_VERSION = 1;

export function importWorldBook(nodes: WorldBookNode[]): Promise<WorldBookImportResult> {
  return requestJSON<WorldBookImportResult>("/api/assistant/world-book/import", {
    method: "POST",
    body: JSON.stringify({ version: WORLD_BOOK_EXPORT_VERSION, nodes })
  });
}

/** 导入 SillyTavern 世界书/角色卡 character_book 的原始 entries；转换在后端做，规则只维护一份。 */
export function importWorldBookSillyTavern(entries: unknown): Promise<WorldBookImportResult> {
  return requestJSON<WorldBookImportResult>("/api/assistant/world-book/import", {
    method: "POST",
    body: JSON.stringify({ entries })
  });
}

export interface CharacterCardImportResult {
  /** 导入后的那套人设；同名同内容被跳过时为空。 */
  persona?: Persona;
  personas: Persona[];
  skipped: number;
  renamed: number;
  book_name?: string;
  book_imported: number;
  book_dropped: number;
  nodes?: WorldBookNode[];
}

/** 导入 SillyTavern 角色卡（JSON 或 PNG 内嵌卡的原始字节，base64 递交）：人设进人设库，内嵌世界书并进世界书。 */
export function importCharacterCard(cardBase64: string): Promise<CharacterCardImportResult> {
  return requestJSON<CharacterCardImportResult>("/api/assistant/personas/import-card", {
    method: "POST",
    body: JSON.stringify({ card_base64: cardBase64 })
  });
}

/** 笔记类型。后端给出全量清单，前端不自己维护一份。 */
export type NotebookKind = "term" | "fact" | "preference" | "event" | "todo" | "person";

export interface NotebookKindOption {
  value: NotebookKind;
  label: string;
}

export interface NotebookRevision {
  version: number;
  kind?: NotebookKind;
  meaning?: string;
  example?: string;
  aliases?: string[];
  note?: string;
  editor_user_id?: string;
  editor_name?: string;
  recorded_at: string;
}

export interface NotebookEntry {
  id: string;
  scope_key: string;
  kind: NotebookKind;
  /** 标题：词条是那个词本身，其余类型是一句概括。 */
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
  revisions?: NotebookRevision[];
}

export interface NotebookScopeSummary {
  scope_key: string;
  active_count: number;
  deleted_count: number;
  updated_at: string;
}

export interface NotebookListResponse {
  scopes: NotebookScopeSummary[];
  scope: string;
  entries: NotebookEntry[];
  query?: string;
  kinds: NotebookKindOption[];
  kind?: string;
}

export interface NotebookEntryInput {
  scope: string;
  kind?: NotebookKind;
  term: string;
  aliases?: string[];
  meaning?: string;
  example?: string;
  note?: string;
}

export function listNotebook(
  scope = "",
  query = "",
  includeDeleted = false,
  botProfileID = "",
  kind = ""
): Promise<NotebookListResponse> {
  const params = new URLSearchParams();
  if (scope) {
    params.set("scope", scope);
  }
  if (kind) {
    params.set("kind", kind);
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
  return requestJSON<NotebookListResponse>(`/api/assistant/notebook${search ? `?${search}` : ""}`);
}

export function getNotebookEntry(scope: string, term: string): Promise<NotebookEntry> {
  const params = new URLSearchParams({ scope, term });
  return requestJSON<NotebookEntry>(`/api/assistant/notebook/entry?${params.toString()}`);
}

export function saveNotebookEntry(input: NotebookEntryInput): Promise<NotebookEntry> {
  return requestJSON<NotebookEntry>("/api/assistant/notebook", {
    method: "POST",
    body: JSON.stringify(input)
  });
}

export function deleteNotebookEntry(scope: string, term: string, note = ""): Promise<NotebookEntry> {
  return requestJSON<NotebookEntry>("/api/assistant/notebook/delete", {
    method: "POST",
    body: JSON.stringify({ scope, term, note })
  });
}

export function restoreNotebookEntry(scope: string, term: string): Promise<NotebookEntry> {
  return requestJSON<NotebookEntry>("/api/assistant/notebook/restore", {
    method: "POST",
    body: JSON.stringify({ scope, term })
  });
}

export type AssistantTaskKind = "reminder" | "schedule" | "repository_watch" | "rss_watch";
// 空数组表示「全部种类都要」——后端也是这么存的，别把空当成「一条都不要」。
export type RepositoryWatchPullEvent = "opened" | "updated" | "closed" | "merged";
export type RepositoryWatchIssueEvent = "opened" | "updated" | "closed" | "reopened";
export type RepositoryWatchReleaseKind = "stable" | "prerelease";
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
  watch_release_kinds?: RepositoryWatchReleaseKind[];
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
  feed_sources?: RSSWatchSource[];
  feed_judge_prompt?: string;
  last_feed_item_id?: string;
  last_feed_published_at?: string;
  created_at: string;
  consumes_quota: boolean;
}

export interface RepositoryWatchTarget {
  profile_id?: string;
  platform?: string;
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
  watch_release_kinds?: RepositoryWatchReleaseKind[];
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

// 一条订阅可以盯多个来源，它们共用同一套判断规则。
export interface RSSWatchSource {
  feed_url: string;
  source?: "rss" | "twitter";
  handle?: string;
  name?: string;
}

export interface RSSWatchInput {
  notification_targets?: RepositoryWatchTarget[];
  feed_url?: string;
  twitter_handle?: string;
  feed_urls?: string[];
  twitter_handles?: string[];
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

export function updateRSSWatch(id: string, input: Partial<Pick<RSSWatchInput, "feed_url" | "twitter_handle" | "feed_urls" | "twitter_handles" | "judge_prompt" | "interval_seconds">>): Promise<AssistantTask> {
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

/** 新建机器人时用的 Agent 推荐默认值。只读，不改任何东西。 */
export interface AgentRecommendedDefaults {
  agent_command_allowlist: string[];
  agent_file_write_enabled: boolean;
  agent_command_sandbox: string;
  agent_max_steps: number;
  agent_command_timeout_ms: number;
}

export function getAgentDefaults(): Promise<AgentRecommendedDefaults> {
  return requestJSON<AgentRecommendedDefaults>("/api/assistant/agent-defaults");
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
  /** 令牌写进哪个请求头；留空即 Authorization。 */
  token_header?: string;
  /** 令牌前缀；留空时 Authorization 用 Bearer，其它头不加前缀。 */
  token_scheme?: string;
  /** 换令牌时请求体的编码方式；留空即 form（RFC 6749 规定的格式）。 */
  token_request_format?: "form" | "json";
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

export function codingAgentSetup(agent: string, operation: "status" | "install" | "test" | "login-start" | "login-status" | "login-cancel"): Promise<{installed: boolean; key_configured: boolean; installable: boolean; message: string; login_url?: string; device_code?: string; login_state?: string}> {
  return requestJSON("/api/assistant/plugins/coding-agent/setup", {method: "POST", body: JSON.stringify({agent, operation})});
}

// ---------------------------------------------------------------------------
// 内置浏览器：Diana 自己那个常驻浏览器，画面和输入都走 /api/browser-box。
// ---------------------------------------------------------------------------

export interface BrowserBoxSettings {
  enabled: boolean;
  /** 有头窗口。新装时按本机条件自动选：有显示器或能起 Xvfb 就开。 */
  headful?: boolean;
  window_width?: number;
  window_height?: number;
  denied_hosts?: string[];
  executable?: string;
}

export interface BrowserBoxStatus {
  settings: BrowserBoxSettings;
  running: boolean;
  takeover: boolean;
  cdp_url?: string;
  executable?: string;
  profile_dir?: string;
  started_at?: string;
  last_error?: string;
  available: boolean;
}

export interface BrowserBoxTab {
  id: string;
  title?: string;
  url?: string;
}

/** 机器人用哪个浏览器：关闭、Diana 内置、用户自己的 Chrome（扩展）。三者互斥。 */
export type BrowserSource = "off" | "box" | "extension";

export function getBrowserSource(): Promise<{ source: BrowserSource }> {
  return requestJSON<{ source: BrowserSource }>("/api/browser-source");
}

export function saveBrowserSource(source: BrowserSource): Promise<{ source: BrowserSource }> {
  return requestJSON<{ source: BrowserSource }>("/api/browser-source", {
    method: "PUT",
    body: JSON.stringify({ source })
  });
}

// 内置浏览器按机器人各用一份登录态，进程相关的接口都带上 ?bot=。不带时 status
// 只回全局配置和本机能不能找到浏览器。
function browserBoxPath(path: string, botID?: string, extra?: Record<string, string>): string {
  const params = new URLSearchParams();
  if (botID) params.set("bot", botID);
  for (const [key, value] of Object.entries(extra ?? {})) params.set(key, value);
  const query = params.toString();
  return `/api/browser-box/${path}${query ? `?${query}` : ""}`;
}

export function getBrowserBoxStatus(botID?: string): Promise<BrowserBoxStatus> {
  return requestJSON<BrowserBoxStatus>(browserBoxPath("status", botID));
}

export function saveBrowserBoxSettings(
  settings: BrowserBoxSettings,
  botID?: string
): Promise<{ settings: BrowserBoxSettings; status: BrowserBoxStatus }> {
  return requestJSON<{ settings: BrowserBoxSettings; status: BrowserBoxStatus }>(browserBoxPath("settings", botID), {
    method: "PUT",
    body: JSON.stringify(settings)
  });
}

export function startBrowserBox(botID: string): Promise<{ status: BrowserBoxStatus }> {
  return requestJSON<{ status: BrowserBoxStatus }>(browserBoxPath("start", botID), { method: "POST" });
}

export function stopBrowserBox(botID: string): Promise<{ status: BrowserBoxStatus }> {
  return requestJSON<{ status: BrowserBoxStatus }>(browserBoxPath("stop", botID), { method: "POST" });
}

export function setBrowserBoxTakeover(botID: string, active: boolean): Promise<{ ok: boolean; active: boolean }> {
  return requestJSON<{ ok: boolean; active: boolean }>(browserBoxPath("takeover", botID), {
    method: "POST",
    body: JSON.stringify({ active })
  });
}

export function listBrowserBoxTabs(botID: string): Promise<{ tabs: BrowserBoxTab[] }> {
  return requestJSON<{ tabs: BrowserBoxTab[] }>(browserBoxPath("tabs", botID));
}

export function openBrowserBoxTab(botID: string, url: string): Promise<{ tab: BrowserBoxTab }> {
  return requestJSON<{ tab: BrowserBoxTab }>(browserBoxPath("tabs", botID), {
    method: "POST",
    body: JSON.stringify({ url })
  });
}

export function closeBrowserBoxTab(botID: string, id: string): Promise<{ ok: boolean }> {
  return requestJSON<{ ok: boolean }>(browserBoxPath(`tabs/${encodeURIComponent(id)}`, botID), { method: "DELETE" });
}

/** 实时画面的 WebSocket 地址。页面是 https 时自动用 wss。 */
export function browserBoxLiveURL(botID: string, tabID?: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}${browserBoxPath("live", botID, tabID ? { tab: tabID } : undefined)}`;
}
