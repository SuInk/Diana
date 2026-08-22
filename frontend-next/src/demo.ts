// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type {
  AppLogEntry,
  AssistantEventDetail,
  AssistantTask,
  LLMConfig,
  PluginState,
  BotProfileConfig,
  BotGroupSummary,
  BotPlatform,
  BotStatus,
  ResolverDependency,
  StatsSnapshot,
  UpdateStatus,
  UserFavorabilityChange,
  UserMemoryProfile
} from "./api";

export const demoMode = import.meta.env.VITE_DEMO_MODE === "true";

const now = Date.now();
const before = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const after = (minutes: number) => new Date(now + minutes * 60_000).toISOString();

const modelCatalog = [
  { id: "gpt-5.2", input_modalities: ["text", "image"], output_modalities: ["text"] },
  { id: "gpt-5-mini", input_modalities: ["text"], output_modalities: ["text"] },
  { id: "gpt-image-1.5", input_modalities: ["text", "image"], output_modalities: ["image"] }
];

let llmConfig: LLMConfig = {
  provider: "openai_compatible",
  model: "gpt-5.2",
  api_key_configured: true,
  active_profile_id: "llm-chat",
  profiles: [
    { id: "llm-chat", name: "主对话模型", group: "default", description: "群聊、私聊与 Agent 主回复", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-5.2", models: modelCatalog, temperature: 0.7, max_output_tokens: 4096 },
    { id: "llm-vision", name: "视觉理解", group: "vision", description: "图片理解与 OCR", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-5.2", models: modelCatalog },
    { id: "llm-intent", name: "主动回复判断", group: "intent", description: "群聊语义路由和机器人识别", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-5-mini", models: modelCatalog, temperature: 0.2 },
    { id: "llm-image", name: "图片生成", group: "image", description: "独立图片生成测试链路", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-image-1.5", image_model: "gpt-image-1.5", models: modelCatalog }
  ]
};

const oneBotProfile: BotProfileConfig = {
  id: "bot-onebot", name: "Diana OneBot（演示）", platform: "onebot-v11", enabled: true,
  onebot_reverse_ws_endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", onebot_access_token_configured: true,
  bot_account: "100000001", owner_id: "100200001", owner_login_enabled: true, isolate_platform_contexts: true,
  group_triggers: ["Diana", "diana"], disabled_groups: [], system_prompt: "以准确、自然的方式参与对话；遇到时效性事实时先联网检索。",
  debug_mode_enabled: true, bot_reply_loop_detection_enabled: true, prompt_inject_time: false,
  proactive_reply_chance: 1, proactive_reply_threshold: 0.9, recent_context_limit: 40, max_reply_chars: 0,
  long_term_memory_enabled: true, cross_group_memory_enabled: true, dict_segment_enabled: true, agent_enabled: true, agent_max_steps: 12,
  max_bot_concurrency: 4, request_timeout_ms: 60_000,
  model_roles: {
    chat: { profile_id: "llm-chat", model: "gpt-5.2" }, vision: { profile_id: "llm-vision", model: "gpt-5.2" },
    intent: { profile_id: "llm-intent", model: "gpt-5-mini" }, image: { profile_id: "llm-image", model: "gpt-image-1.5" }
  }
};

const telegramProfile: BotProfileConfig = {
  ...oneBotProfile, id: "bot-telegram", name: "Diana Telegram（演示）", platform: "telegram", enabled: true,
  onebot_reverse_ws_endpoint: "", onebot_access_token_configured: false, telegram_bot_token_configured: true,
  telegram_api_base_url: "https://api.telegram.org", bot_account: "", owner_id: "880024"
};

let assistantConfig: BotProfileConfig = { ...oneBotProfile, active_profile_id: "bot-onebot", profiles: [oneBotProfile, telegramProfile] };

let plugins: PluginState[] = [
  { manifest: { id: "official.file-parser", name: "文件解析", version: "0.3.0", description: "解析 PDF、图片和文本附件，把结构化内容交给模型。", official: true, built_in: true, permissions: ["文件解析", "消息读取"] }, installed: true, enabled: true },
  { manifest: { id: "official.nonebot-plugin-resolver-go", name: "链接解析", version: "0.3.0", description: "解析社交媒体链接，支持合并转发图片和限定大小的视频。", official: true, built_in: true, permissions: ["网络请求", "消息发送"] }, installed: true, enabled: true },
  { manifest: { id: "official.onebot-v11", name: "OneBot 协议", version: "0.1.0", description: "提供 OneBot v11 事件、消息发送、群组列表和协议扩展动作。", official: true, built_in: true, permissions: ["OneBot 读取", "OneBot 写入"] }, installed: true, enabled: true },
  {
    manifest: {
      id: "official.repository-publish", name: "Issue 发布", version: "0.4.0", description: "群成员可生成 Issue 草稿，由群内具备仓库权限的授权用户确认后创建。", official: true, built_in: true, permissions: ["network:https", "github:issues:read", "github:issues:write", "audit:write", "llm:tool"],
      settings: [
        { key: "github_auth_mode", label: "GitHub 认证方式", description: "可使用独立 Token 或当前系统的 gh 登录。", type: "select", default: "token", options: [{ value: "token", label: "独立 Token" }, { value: "gh", label: "GitHub CLI (gh)" }, { value: "auto", label: "自动选择" }] },
        { key: "github_token", label: "GitHub Issues Token", description: "在 Token 或自动模式下使用，保存后不回显。", type: "string", default: "", secret: true },
        { key: "allowed_repositories", label: "允许操作的仓库", description: "Issue 的读写操作白名单；精确填写 owner/repo，多个仓库用逗号或换行分隔。", type: "string", default: "" },
        { key: "user_repository_access", label: "用户仓库授权", description: "每行填写：用户ID = owner/repo, owner/repo。", type: "string", default: "" },
        { key: "group_repository_access", label: "群聊仓库授权", description: "每行填写：群ID = owner/repo, owner/repo。群内成员只能操作绑定仓库。", type: "string", default: "" },
        { key: "user_github_tokens", label: "用户 GitHub Token", description: "每个授权用户独立保存。", type: "string", default: "", secret: true },
        { key: "user_github_token_users", label: "已配置 Token 的用户", description: "由授权编辑器维护。", type: "string", default: "" },
        { key: "timeout_seconds", label: "GitHub 请求超时", type: "number", default: 20, min: 5, max: 60, unit: "秒" }
      ]
    },
    installed: true, enabled: true, settings: { allowed_repositories: "SuInk/Diana" }, secrets_configured: { github_token: true }
  },
  {
    manifest: {
      id: "official.repository-watch", name: "仓库订阅", version: "0.2.0", description: "监控公开或私有 GitHub 仓库的 Commit、PR、Release 与 Star，经 LLM 阅读 diff 并总结后通知指定对象。", official: true, built_in: true, permissions: ["网络请求", "任务持久化", "消息发送"],
      settings: [
        { key: "github_token", label: "GitHub Token", description: "用于私有仓库和提高 API 额度。", type: "string", default: "", secret: true },
        { key: "default_interval_seconds", label: "默认检查周期", type: "number", default: 60, min: 30, max: 86400, unit: "秒" }
      ]
    },
    installed: true, enabled: true, settings: { default_interval_seconds: 60 }, secrets_configured: { github_token: true }
  },
  {
    manifest: { id: "official.rss-watch", name: "RSS 订阅", version: "0.1.0", description: "按条件监控 RSS 或社交动态，判断后发送到指定群聊或私聊。", official: true, built_in: true, permissions: ["网络请求", "消息发送"], settings: [{ key: "default_interval_seconds", label: "默认检查周期", type: "number", default: 300, min: 30, max: 86400, unit: "秒" }] },
    installed: true, enabled: true
  },
  { manifest: { id: "official.sandboxed-browser-renderer", name: "网页渲染", version: "0.2.0", description: "在隔离浏览器中执行动态网页并把稳定页面交给模型。", official: true, built_in: true, permissions: ["网页渲染", "无头浏览器"] }, installed: true, enabled: true }
];

const demoGroupAvatar = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(`
  <svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128">
    <rect width="128" height="128" rx="24" fill="#7057d9"/>
    <circle cx="43" cy="54" r="15" fill="#fff" opacity=".95"/>
    <circle cx="85" cy="54" r="15" fill="#fff" opacity=".95"/>
    <path d="M24 101c3-20 18-31 40-31s37 11 40 31" fill="#fff" opacity=".95"/>
  </svg>
`)}`;

const groups: BotGroupSummary[] = [
  { group_id: "100200301", group_name: "产品讨论（演示）", avatar_url: demoGroupAvatar, member_count: 186, max_member_count: 500, enabled: true, configured: true, joined: true, group_triggers: ["Diana", "diana"], system_prompt: "以准确、简洁的方式参与产品和工程讨论。", recent_context_limit: 50, proactive_reply_chance: 1, proactive_reply_threshold: 0.9, reply_gate: { active_hours_enabled: true, active_start: "08:00", active_end: "23:30", timezone: "Asia/Shanghai", blocked_users: ["100200999"], owner_bypass: true }, plugin_overrides: { "official.repository-watch": true }, updated_at: before(12) },
  { group_id: "100200418", group_name: "日常交流（演示）", avatar_url: demoGroupAvatar, member_count: 74, max_member_count: 200, enabled: true, configured: true, joined: true, group_triggers: ["Diana"], system_prompt: "自然参与闲聊，事实不确定时优先搜索。", recent_context_limit: 40, proactive_reply_chance: 1, proactive_reply_threshold: 0.9, plugin_overrides: {}, updated_at: before(28) },
  { group_id: "100200519", group_name: "设计讨论（演示）", avatar_url: demoGroupAvatar, member_count: 52, max_member_count: 200, enabled: true, configured: true, joined: true, group_triggers: ["画一张", "Diana"], system_prompt: "优先理解视觉需求，并在生图前补齐必要约束。", reply_gate: { active_hours_enabled: true, active_start: "09:00", active_end: "22:00", timezone: "Asia/Shanghai", blocked_users: ["100200888", "100200889"] }, plugin_overrides: { "official.sandboxed-browser-renderer": false }, updated_at: before(45) },
  { group_id: "100200627", group_name: "只读观察群（演示）", avatar_url: demoGroupAvatar, member_count: 318, max_member_count: 500, enabled: false, configured: true, joined: true, group_triggers: [], system_prompt: "仅记录事件，不主动回复。", plugin_overrides: {}, updated_at: before(90) }
];

const demoUsers: UserMemoryProfile[] = [
  {
    user_id: "100200711", display_name: "青禾", favorability: 62, message_count: 1843, last_seen_at: before(2), updated_at: before(2),
    memories: [
      { text: "在做 Diana 的发布流程改造，经常问仓库最近的变更", source: "group", group_id: "100200301", at: before(2) },
      { text: "习惯下午提交周报，让机器人 15:30 提醒", source: "group", group_id: "100200301", at: before(1400) },
      { text: "喜欢喝手冲咖啡，不加糖", source: "group", group_id: "100200418", at: before(4300) }
    ]
  },
  {
    user_id: "100200913", display_name: "星野", favorability: 35, message_count: 622, last_seen_at: before(31), updated_at: before(31),
    memories: [
      { text: "经常在设计群让机器人画复古电车和雨夜街景", source: "group", group_id: "100200519", at: before(31) },
      { text: "偏好冷色调和胶片质感的画面", source: "group", group_id: "100200519", at: before(2100) }
    ]
  },
  {
    user_id: "100201014", display_name: "白榆", favorability: 12, message_count: 208, last_seen_at: before(47), updated_at: before(47),
    memories: [{ text: "对产品界面的开发者工具风格很感兴趣", source: "group", group_id: "100200301", at: before(47) }]
  },
  {
    user_id: "100200888", display_name: "路人甲", favorability: -8, message_count: 96, last_seen_at: before(3000), updated_at: before(3000),
    memories: [{ text: "多次刷屏广告链接，已被设计群屏蔽", source: "group", group_id: "100200519", at: before(3000) }]
  }
];

const demoFavorabilityChanges: Record<string, UserFavorabilityChange[]> = {
  "100200711": [
    { id: 3, user_id: "100200711", delta: 2, before_score: 60, after_score: 62, source: "interaction", reason: "耐心帮群友排查了部署问题", group_id: "100200301", created_at: before(300) },
    { id: 2, user_id: "100200711", delta: 10, before_score: 50, after_score: 60, source: "owner_set", reason: "活动奖励", operator_id: "100200001", created_at: before(4300) },
    { id: 1, user_id: "100200711", delta: 1, before_score: 49, after_score: 50, source: "interaction", reason: "明确表达感谢", group_id: "100200418", created_at: before(6000) }
  ],
  "100200888": [
    { id: 4, user_id: "100200888", delta: -3, before_score: -5, after_score: -8, source: "interaction", reason: "重复发送广告内容", group_id: "100200519", created_at: before(3000) }
  ]
};

export const demoEvents: AssistantEventDetail[] = [
  { id: "demo-event-1", at: before(2), kind: "group", platform: "onebot-v11", group_id: "100200301", user_id: "100200711", sender_name: "青禾", message_id: "demo-7319", text: "@Diana 帮我总结一下今天的发布变更", reply: "今天的更新重点是事件原因审计、仓库动态订阅和多通道会话隔离。引用消息同时 @机器人时也会正确进入主 Agent。", handled: true, status: "replied", outcome: "replied", decision: "replied", reason: "检测到显式 @机器人，直接进入主 Agent；问题需要读取仓库近期变更后回答。", duration_ms: 6800, llm_calls: 2, input_tokens: 2470, output_tokens: 376, total_tokens: 2846, delivery_stage: "echo_persisted", outbound_message_id: "demo-out-7319", self_echo_at: before(1) },
  { id: "demo-event-2", at: before(9), kind: "group", platform: "onebot-v11", group_id: "100200418", user_id: "100200812", sender_name: "栖迟", message_id: "demo-7298", text: "[图片]", handled: false, status: "ignored", outcome: "bot_message_ignored", decision: "not_replied", reason: "识别为其他机器人发送的自动消息；“识别机器人后不回复”已启用，因此未启动视觉模型和主 Agent。", duration_ms: 42, llm_calls: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0 },
  { id: "demo-event-3", at: before(18), kind: "private", platform: "telegram", user_id: "880024", sender_name: "Demo User", message_id: "demo-tg-114", text: "Zeabur 最近有什么产品更新？", reply: "我检索了官方更新渠道，并按发布时间整理了近期变化；真实服务会在回答中保留来源链接。", handled: true, status: "replied", outcome: "replied", decision: "replied", reason: "私聊默认响应；问题包含时效性要求，Agent 先调用内置联网搜索再组织答案。", duration_ms: 8200, llm_calls: 2, input_tokens: 2844, output_tokens: 387, total_tokens: 3231, delivery_stage: "acknowledged", outbound_message_id: "demo-tg-out-114" },
  { id: "demo-event-4", at: before(31), kind: "group", platform: "onebot-v11", group_id: "100200519", user_id: "100200913", sender_name: "星野", message_id: "demo-7220", text: "画一张雨夜城市里的复古电车", reply: "图片已生成并发送。事件明细默认显示缩略图，点击后查看原图。", handled: true, status: "replied", outcome: "image_generated", decision: "replied", reason: "命中群触发词并识别为明确的图片生成请求；调用独立生图模型。", duration_ms: 18400, llm_calls: 1, input_tokens: 1410, output_tokens: 262, total_tokens: 1672, delivery_stage: "acknowledged", outbound_message_id: "demo-out-7220" },
  { id: "demo-event-5", at: before(47), kind: "group", platform: "onebot-v11", group_id: "100200301", user_id: "100201014", sender_name: "白榆", message_id: "demo-7166", text: "Zeabur 风味是什么", reply: "如果是在说产品界面，通常指偏开发者工具的克制布局：高信息密度、明确状态和较少装饰。", handled: true, status: "replied", outcome: "proactive_replied", decision: "replied", reason: "短句虽未显式 @机器人，但包含可回答的产品语境问题；主动回复判断认为应该参与。", duration_ms: 4900, llm_calls: 2, input_tokens: 1671, output_tokens: 237, total_tokens: 1908, delivery_stage: "echo_persisted", outbound_message_id: "demo-out-7166" }
];

const trace: AppLogEntry[] = [
  { id: "trace-1", kind: "debug", level: "info", action: "agent_trace", message: "模型请求", created_at: before(2), metadata: { phase: "model_request", purpose: "intent", provider: "openai_compatible", model: "gpt-5-mini", duration_ms: 640, request: { messages: [{ role: "system", content: "判断消息是否明确指向机器人" }, { role: "user", content: "@Diana 帮我总结一下今天的发布变更" }] }, response: { directed_at_bot: true, answerable: true, confidence: 0.99 } } },
  { id: "trace-2", kind: "debug", level: "info", action: "agent_trace", message: "Agent 启动", created_at: before(2), metadata: { phase: "agent_started", model: "gpt-5.2", available_tools: ["repository_history", "web_search", "memory_search", "message_send"] } },
  { id: "trace-3", kind: "debug", level: "info", action: "agent_trace", message: "仓库工具完成", created_at: before(2), metadata: { phase: "agent_tool_completed", tool: "repository_history", duration_ms: 920, tool_input: { repository: "SuInk/Diana", range: "today" }, tool_output: "读取到 6 条提交和 1 个 Release（模拟数据）" } },
  { id: "trace-4", kind: "debug", level: "info", action: "agent_trace", message: "Agent 完成", created_at: before(1), metadata: { phase: "agent_completed", finish_reason: "stop", duration_ms: 6800 } }
];

export const demoStats: StatsSnapshot = {
  started_at: before(3 * 24 * 60 + 8 * 60), uptime_seconds: 288_000, total_events: 12_486, handled_events: 3_218, error_events: 9,
  today_events: 1284, today_handled: 318, today_errors: 0, by_kind: { group: 1108, private: 176 },
  hourly: Array.from({ length: 24 }, (_, index) => ({ hour_unix: Math.floor((now - (23 - index) * 3_600_000) / 1000), total: [14, 22, 18, 35, 29, 48, 41, 60, 45, 72, 54, 82, 63, 77, 66, 39, 52, 44, 61, 34, 49, 57, 78, 64][index], handled: [3, 4, 2, 8, 5, 11, 9, 15, 8, 18, 12, 21, 13, 17, 15, 7, 12, 10, 14, 6, 11, 13, 19, 16][index], errors: 0 })),
  avg_reply_ms: 5820, last_event_at: demoEvents[0].at,
  bot: { running: true, connected: true, self_id: "100000001", active_workers: 2, plugins_enabled: 7, plugins_total: 8, bridge_enabled: false, bridge_connected: false }
};

export const demoStatus: BotStatus = {
  running: true, config: assistantConfig,
  channel: { profile_id: "bot-onebot", platform: "onebot-v11", name: "Diana OneBot（演示）", connected: true, endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", self_id: "100000001", updated_at: before(1) },
  channels: [
    { profile_id: "bot-onebot", platform: "onebot-v11", name: "Diana OneBot（演示）", connected: true, endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", self_id: "100000001", updated_at: before(1) },
    { profile_id: "bot-telegram", platform: "telegram", name: "Diana Telegram（演示）", connected: true, endpoint: "https://api.telegram.org", self_id: "@diana_demo_bot", updated_at: before(1) }
  ],
  nonebot_bridge: { enabled: false, connected: false, updated_at: before(1) }, plugins, recent_events: demoEvents, active_workers: 2, updated_at: before(1)
};

let tasks: AssistantTask[] = [
  { id: "task-reminder-01", kind: "reminder", platform: "onebot-v11", owner_id: "100200301", user_id: "100200301", message: "15:30 提醒提交周报", status: "active", trigger_at: after(70), created_at: before(20), consumes_quota: true },
  { id: "task-schedule-02", kind: "schedule", platform: "telegram", owner_id: "880024", user_id: "880024", message: "每天整理 AI 行业资讯并附来源", status: "active", trigger_at: after(180), interval_seconds: 86400, last_run_at: before(1260), created_at: before(4800), consumes_quota: true },
  { id: "task-repo-03", kind: "repository_watch", platform: "onebot-v11", owner_id: "", group_id: "100200301", message: "Diana 仓库动态", status: "active", trigger_at: after(1), interval_seconds: 60, last_run_at: before(1), repository: "SuInk/Diana", repository_branch: "main", watch_commits: true, watch_pull_requests: true, watch_releases: true, watch_stars: true, last_commit_sha: "26ebc1bed07e9e5b", last_release_tag: "v0.8.6", last_star_count: 128, created_at: before(3800), consumes_quota: true },
  { id: "task-rss-04", kind: "rss_watch", platform: "telegram", owner_id: "", user_id: "880024", message: "Diana Release Feed", status: "active", trigger_at: after(4), interval_seconds: 300, last_run_at: before(4), feed_url: "https://github.com/SuInk/Diana/releases.atom", feed_source: "rss", feed_judge_prompt: "仅在稳定版发布时提醒并总结更新点", last_feed_item_id: "tag:github.com,2008:Repository/", created_at: before(2200), consumes_quota: true }
];

const platforms: BotPlatform[] = [
  { id: "onebot-v11", name: "QQ · OneBot v11", protocol: "onebot-v11-reverse-ws", category: "qq", category_label: "QQ", description: "通过 NapCat、Lagrange 或 go-cqhttp 接入 OneBot v11。" },
  { id: "telegram", name: "Telegram Bot", protocol: "telegram-bot-api", category: "telegram", category_label: "Telegram", description: "通过 Telegram Bot API 长轮询接入。" }
];

const dependencies: ResolverDependency[] = [
  { name: "ffmpeg", purpose: "媒体转码与时长检测", available: true, version: "7.1", path: "/usr/local/bin/ffmpeg", installable: true, installer: "系统包管理器" },
  { name: "yt-dlp", purpose: "视频地址解析", available: true, version: "2026.08.10", path: "/usr/local/bin/yt-dlp", installable: true, installer: "pipx" },
  { name: "node", purpose: "抖音接口签名（a-bogus）", available: true, version: "22.11.0", path: "/usr/local/bin/node", installable: true, installer: "系统包管理器" }
];

// 演示里故意让浏览器缺席：这一格就是要给人看「插件开着但其实跑不起来」长什么样。
const browserDependencies: ResolverDependency[] = [
  {
    name: "chrome",
    purpose: "网页渲染：在一次性沙盒里执行页面 JS 后读取正文",
    available: false,
    detail: "没有找到 Chrome/Chromium",
    installable: true,
    installer: "apt"
  }
];

const updateStatus: UpdateStatus = { root: "/opt/diana", head_commit: "26ebc1bed07e9e5b", head_subject: "真实 WebUI Pages 演示", dirty: false, update_available: true, restart_required: false, download_ready: false, last_fetched_at: before(4) };
let updatePolicy = { auto_download: true, auto_install: false };

const logs: AppLogEntry[] = [
  { id: "log-1", kind: "operation", level: "info", action: "message.reply", message: "群聊消息已回复并收到发送回显", actor: "bot-onebot", target: "group:100200301", created_at: before(2) },
  { id: "log-2", kind: "operation", level: "info", action: "repository.watch", message: "仓库检查完成，未发现新 Commit 或 Release", actor: "scheduler", target: "SuInk/Diana", created_at: before(4) },
  { id: "log-3", kind: "operation", level: "info", action: "memory.compress", message: "已更新群聊压缩摘要与长期事实索引", actor: "memory", target: "group:100200418", created_at: before(16) }
];

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" } });
}

function bodyOf(init?: RequestInit): Record<string, unknown> {
  if (typeof init?.body !== "string") return {};
  try { return JSON.parse(init.body) as Record<string, unknown>; } catch { return {}; }
}

function mutateLLM(action: string, body: Record<string, unknown>): LLMConfig {
  const profiles = [...(llmConfig.profiles ?? [])];
  const id = String(body.id ?? "");
  if (action === "activate") llmConfig.active_profile_id = id;
  if (action === "delete") llmConfig.profiles = profiles.filter((profile) => profile.id !== id);
  if (action === "clone") {
    const source = profiles.find((profile) => profile.id === id);
    if (source) llmConfig.profiles = [...profiles, { ...source, id: `${id}-copy`, name: `${source.name ?? source.model} 副本` }];
  }
  if (action === "reorder" && Array.isArray(body.ids)) {
    const order = new Map((body.ids as string[]).map((value, index) => [value, index]));
    llmConfig.profiles = profiles.sort((a, b) => (order.get(a.id ?? "") ?? 99) - (order.get(b.id ?? "") ?? 99));
  }
  return llmConfig;
}

async function demoFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const url = new URL(raw, window.location.origin);
  if (!url.pathname.startsWith("/api/") && !url.pathname.startsWith("/onebot/")) return window.__dianaOriginalFetch!(input, init);
  await new Promise((resolve) => window.setTimeout(resolve, 60));
  const method = (init?.method ?? "GET").toUpperCase();
  const body = bodyOf(init);
  const path = url.pathname;

  if (path === "/api/auth/status") return json({ auth_required: true, authenticated: true, username: "demo" });
  if (path.startsWith("/api/auth/")) return json({ ok: true, username: "demo" });
  if (path === "/api/health") return json({ status: "ok", started_at: demoStats.started_at, uptime_seconds: demoStats.uptime_seconds, version: "v0.8.6-demo", repository: "SuInk/Diana", repository_url: "https://github.com/SuInk/Diana" });
  if (path === "/api/stats") return json(demoStats);

  if (path === "/api/llm/config/export") return json(llmConfig);
  if (path === "/api/llm/config" && method === "GET") return json(llmConfig);
  if (path === "/api/llm/config" && method === "POST") {
    const incoming = body as unknown as LLMConfig;
    const profiles = [...(llmConfig.profiles ?? [])];
    const saved = { ...incoming, id: incoming.id || `llm-${Date.now()}`, api_key_configured: true, models: incoming.models?.length ? incoming.models : modelCatalog };
    const index = profiles.findIndex((profile) => profile.id === saved.id);
    if (index >= 0) profiles[index] = saved; else profiles.push(saved);
    llmConfig = { ...llmConfig, profiles, active_profile_id: llmConfig.active_profile_id || saved.id };
    return json(llmConfig);
  }
  const llmAction = path.match(/^\/api\/llm\/config\/(activate|clone|delete|reorder)$/)?.[1];
  if (llmAction) return json(mutateLLM(llmAction, body));
  if (path === "/api/llm/config/import") { llmConfig = { ...llmConfig, ...(body as unknown as Partial<LLMConfig>) }; return json(llmConfig); }
  if (path === "/api/llm/models") return json({ models: modelCatalog });
  if (path === "/api/llm/test") {
    if (body.mode === "image") {
      const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024"><rect width="1024" height="1024" fill="#20242a"/><circle cx="512" cy="430" r="230" fill="#c44c7d"/><path d="M330 360 390 170 475 340M549 340 635 170 695 360" fill="#c44c7d"/><circle cx="440" cy="430" r="22" fill="#fff"/><circle cx="584" cy="430" r="22" fill="#fff"/><path d="M430 560 Q512 620 594 560" fill="none" stroke="#fff" stroke-width="18" stroke-linecap="round"/><text x="512" y="850" text-anchor="middle" fill="#fff" font-family="sans-serif" font-size="42">Diana 生图测试 · 模拟结果</text></svg>`;
      return json({ provider: "openai_compatible", model: "gpt-image-1.5", images: [`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`] });
    }
    return json({ provider: "openai_compatible", model: String(body.model ?? "gpt-5.2"), text: "模型测试通过。这是 Pages 演示模式返回的模拟结果，不会消耗真实 Token。", usage: { input_tokens: 36, output_tokens: 24, total_tokens: 60 } });
  }

  if (path === "/api/assistant/platforms") return json({ platforms });
  if (path === "/api/assistant/config" && method === "GET") return json(assistantConfig);
  if (path === "/api/assistant/config" && method === "POST") {
    const incoming = body as unknown as BotProfileConfig;
    const profiles = [...(assistantConfig.profiles ?? [])];
    const saved = { ...incoming, id: incoming.id || `bot-${Date.now()}` };
    const index = profiles.findIndex((profile) => profile.id === saved.id);
    if (index >= 0) profiles[index] = saved; else profiles.push(saved);
    assistantConfig = { ...assistantConfig, profiles, active_profile_id: assistantConfig.active_profile_id || saved.id };
    demoStatus.config = assistantConfig;
    return json(assistantConfig);
  }
  if (path === "/api/assistant/config/context-isolation") { assistantConfig.isolate_platform_contexts = Boolean(body.enabled); return json(assistantConfig); }
  if (path.startsWith("/api/assistant/config/") && method === "POST") return json(assistantConfig);
  if (path === "/api/assistant/status") return json(demoStatus);
  if (path === "/api/assistant/start") { demoStatus.running = true; return json(demoStatus); }
  if (path === "/api/assistant/stop") { demoStatus.running = false; return json(demoStatus); }
  if (path === "/api/assistant/features") return json({ group_test: true });
  if (path === "/api/assistant/group-test") return json({ group_id: String(body.group_id ?? url.searchParams.get("group_id") ?? ""), message: String(body.message ?? "模拟通道测试"), message_id: "demo-group-test", sent: true, send_result: { status: "ok" }, channel: demoStatus.channel, recent_events: demoStatus.recent_events, status: demoStatus });

  if (path === "/api/assistant/plugins/dependencies")
    return json({
      resolver: dependencies,
      plugins: {
        "official.nonebot-plugin-resolver-go": dependencies,
        "official.sandboxed-browser-renderer": browserDependencies
      }
    });
  if (path.startsWith("/api/assistant/plugins/dependencies/") && path.endsWith("/install")) return json({ dependency: dependencies[0], resolver: dependencies });
  if (path === "/api/assistant/plugins") return json(plugins);
  if (path === "/api/assistant/plugins/repository-publish/drafts") {
    const drafts = [{
      id: "draft-demo-01", platform: "onebot-v11", profile_id: "bot-main", group_id: "100200301",
      repository: "SuInk/Diana", requester_id: "100200711", requester_name: "青禾",
      input: { title: "事件图片改为页面内放大", body: "点击事件中的图片时，在当前页面打开查看器，不再跳转到新标签页。", labels: ["enhancement", "webui"] },
      status: "pending", created_at: before(16), updated_at: before(16)
    }];
    const status = url.searchParams.get("status") ?? "all";
    return json({ drafts: status === "all" ? drafts : drafts.filter((draft) => draft.status === status) });
  }
  if (path === "/api/assistant/plugins/repository-publish/issues" && method === "POST") {
    const repository = String(body.repository ?? "SuInk/Diana");
    const title = String(body.title ?? "演示 Issue");
    if (!body.allow_duplicate && title.includes("重复")) {
      return json({
        ok: false, outcome: "duplicate_candidate", repository, failure_code: "duplicate_candidate",
        message: "发现标题相似的现有 Issue，请确认是否仍要新建。", requires_confirmation: true,
        confirmation_token: "demo-confirmation-token",
        candidates: [{ number: 24, title: "修复重复消息处理", state: "open", url: "https://github.com/SuInk/Diana/issues/24" }]
      });
    }
    return json({
      ok: true, outcome: "created", repository, message: "GitHub 已创建 Issue。",
      issue: { number: 49, title, state: "open", url: "https://github.com/SuInk/Diana/issues/49" }
    }, 201);
  }
  const pluginMatch = path.match(/^\/api\/assistant\/plugins\/([^/]+)\/(install|uninstall|enabled|settings)$/);
  if (pluginMatch) {
    const plugin = plugins.find((item) => item.manifest.id === decodeURIComponent(pluginMatch[1]));
    if (!plugin) return json({ error: "演示插件不存在" }, 404);
    if (pluginMatch[2] === "install") plugin.installed = true;
    if (pluginMatch[2] === "uninstall") { plugin.installed = false; plugin.enabled = false; }
    if (pluginMatch[2] === "enabled") plugin.enabled = Boolean(body.enabled);
    if (pluginMatch[2] === "settings") plugin.settings = { ...((body.settings as Record<string, unknown>) ?? {}) };
    plugins = [...plugins]; demoStatus.plugins = plugins; return json(plugin);
  }

  if (path === "/api/assistant/groups" && method === "GET") return json({ groups, plugins, live_available: true });
  if (path === "/api/assistant/groups" && method === "POST") {
    const config = body.config as BotGroupSummary;
    const index = groups.findIndex((group) => group.group_id === config.group_id);
    if (index >= 0) groups[index] = { ...groups[index], ...config, configured: true, joined: true }; else groups.push({ ...config, configured: true, joined: false });
    return json({ config });
  }

  if (path === "/api/assistant/users") {
    const keyword = (url.searchParams.get("q") ?? "").trim();
    const matched = demoUsers.filter((user) => !keyword || user.user_id.includes(keyword) || (user.display_name ?? "").includes(keyword));
    const users = matched.map((user) => ({ ...user, memories: undefined, memory_count: user.memories?.length ?? 0 }));
    return json({ users, total: matched.length, query: keyword || undefined, limit: 50, offset: 0 });
  }
  const userMatch = path.match(/^\/api\/assistant\/users\/([^/]+)$/);
  if (userMatch) {
    const user = demoUsers.find((item) => item.user_id === decodeURIComponent(userMatch[1]));
    if (!user) return json({ error: "人员不存在或还没有画像记录" }, 404);
    return json({ profile: user, favorability_changes: demoFavorabilityChanges[user.user_id] ?? [] });
  }

  if (path === "/api/assistant/events") {
    const result = url.searchParams.get("result") ?? "all";
    const events = demoEvents.filter((event) => result === "all" || (result === "replied" && event.decision === "replied") || (result === "not_replied" && event.decision === "not_replied") || event.decision === result);
    return json({ range: url.searchParams.get("range") ?? "24h", result, since: before(1440), events, total: 652, filtered_total: result === "all" ? 652 : result === "replied" ? 50 : result === "not_replied" ? 602 : 0, replied: 50, not_replied: 602, pending: 0, errors: 0, llm_calls: 49, input_tokens: 232_773, output_tokens: 10_732, total_tokens: 243_505, page: 1, limit: 50, has_more: false });
  }
  const traceMatch = path.match(/^\/api\/assistant\/events\/([^/]+)\/trace$/);
  if (traceMatch) return json({ event_id: decodeURIComponent(traceMatch[1]), steps: decodeURIComponent(traceMatch[1]) === "demo-event-1" ? trace : [] });

  if (path === "/api/assistant/tasks") return json({ items: tasks });
  if ((path.endsWith("/repository-watches") || path.endsWith("/rss-watches")) && method === "POST") {
    const repository = path.endsWith("/repository-watches");
    const task: AssistantTask = { id: `task-${Date.now()}`, kind: repository ? "repository_watch" : "rss_watch", platform: "onebot-v11", owner_id: "", group_id: String(body.group_id ?? ""), user_id: String(body.user_id ?? ""), message: String(body.repository ?? body.feed_url ?? body.twitter_handle ?? "演示订阅"), status: "active", trigger_at: after(1), interval_seconds: Number(body.interval_seconds ?? 60), repository: repository ? String(body.repository ?? "") : undefined, repository_branch: repository ? String(body.branch ?? "main") : undefined, watch_commits: repository ? Boolean(body.watch_commits) : undefined, watch_pull_requests: repository ? Boolean(body.watch_pull_requests) : undefined, watch_releases: repository ? Boolean(body.watch_releases) : undefined, watch_stars: repository ? Boolean(body.watch_stars) : undefined, last_star_count: repository ? 128 : undefined, feed_url: repository ? undefined : String(body.feed_url ?? ""), feed_handle: repository ? undefined : String(body.twitter_handle ?? ""), feed_source: repository ? undefined : body.twitter_handle ? "twitter" : "rss", feed_judge_prompt: repository ? undefined : String(body.judge_prompt ?? ""), created_at: new Date().toISOString(), consumes_quota: true };
    tasks = [task, ...tasks]; return json(task);
  }
  if (path.includes("/repository-watches/") || path.includes("/rss-watches/")) {
    const parts = path.split("/");
    const lastPart = parts[parts.length - 1] ?? "";
    const taskID = decodeURIComponent(lastPart === "cancel" ? parts[parts.length - 2] ?? "" : lastPart);
    const task = tasks.find((item) => item.id === taskID) ?? tasks[0];
    if (method === "DELETE") { tasks = tasks.filter((item) => item.id !== taskID); return json({}); }
    if (path.endsWith("/cancel")) task.status = "cancelled";
    return json(task);
  }

  if (path === "/api/logs") {
    const errorLogs: AppLogEntry[] = [{ id: "log-error-1", kind: "error", level: "error", action: "delivery.retry", message: "一次模拟发送失败，重试后已恢复", detail: "原始错误：temporary network failure（模拟数据）", actor: "bot-telegram", target: "private:880024", created_at: before(240) }];
    return json({ logs: url.searchParams.get("kind") === "error" ? errorLogs : logs });
  }

  if (path === "/api/system/version") return json({ build_version: "v0.8.6-demo", build_type: "release", version_label: "v0.8.6 · Pages 演示", git_available: false, deployment_mode: "release", update_supported: true, head_commit: "26ebc1bed07e9e5b", head_subject: "真实 WebUI Pages 演示" });
  if (path === "/api/system/update" && method === "GET") return json(updateStatus);
  if (path === "/api/system/update/check") return json({ deployment_mode: "release", current_version: "v0.8.6", latest_version: "v0.8.7", latest_published_at: before(30), checked_at: new Date(now).toISOString(), update_available: true, update_supported: true, integrity_mode: "sha256", checksum_available: true, checksum_url: "https://github.com/SuInk/Diana/releases", status: updateStatus, policy: updatePolicy });
  if (path === "/api/system/update/policy" && method === "GET") return json(updatePolicy);
  if (path === "/api/system/update/policy" && method === "PUT") {
    const next = JSON.parse(String(init?.body ?? "{}")) as { auto_download?: boolean; auto_install?: boolean };
    updatePolicy = { auto_download: Boolean(next.auto_download || next.auto_install), auto_install: Boolean(next.auto_install) };
    return json(updatePolicy);
  }
  if (path === "/api/system/update/changelog") return json({ repo: "SuInk/Diana", kind: "releases", cached: true, releases: [{ tag: "v0.8.7", name: "Diana v0.8.7", notes: "真实 WebUI GitHub Pages 演示与可观测性优化。", prerelease: false, date: before(30), url: "https://github.com/SuInk/Diana/releases", checksum_available: true }] });
  if (path.startsWith("/api/system/update") && method === "POST") return json({ status: { ...updateStatus, download_ready: true, downloaded_version: "v0.8.7", downloaded_at: new Date().toISOString() }, fetched: true, updated: false, downloaded: true, output: "演示模式：已模拟完成下载与 SHA-256 校验，未写入任何文件。", at: new Date().toISOString() });

  return json({ error: `演示模式尚未覆盖 ${method} ${path}` }, 404);
}

declare global {
  interface Window { __dianaOriginalFetch?: typeof window.fetch; }
}

export function installDemoMode(): void {
  if (!demoMode || window.__dianaOriginalFetch) return;
  window.__dianaOriginalFetch = window.fetch.bind(window);
  window.fetch = demoFetch;
}
