// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { extensionDemoResponse } from './extension-demo';
// 和后端登记表逐字相同的提示词目录，由 webui/demo_prompt_catalog_test.go 生成并校验。
import demoPromptCatalogData from './demo-prompt-catalog.json';
import demoBuiltinSouls from './demo-builtin-souls.json';
import type {
  AgentResidencyEntry,
  AppLogEntry,
  AssistantEventDetail,
  AssistantTask,
  BrowserBoxSettings,
  BrowserControlToken,
  LLMConfig,
  OpenAPIKey,
  Persona,
  PromptCatalog,
  PluginState,
  BotProfileConfig,
  BotGroupSummary,
  BotPlatform,
  BotStatus,
  NotebookEntry,
  NotebookScopeSummary,
  ResolverDependency,
  RSSWatchSource,
  StatsHourBucket,
  StatsSnapshot,
  UpdateStatus,
  UserFavorabilityChange,
  RelationshipEvaluation,
  UserMemoryProfile,
  WorldBookNode
} from "./api";

export const demoMode = import.meta.env.VITE_DEMO_MODE === "true";

const demoContextBudget = {
  context_window: 128_000,
  allocated: 56_320,
  headroom: 71_680,
  layers: [
    { key: "recent_history", label: "近期对话", share_percent: 26, ceiling: 40_000, tokens: 33_280, capped_by_ceiling: false, configurable: true },
    { key: "retrieved_memory", label: "检索记忆", share_percent: 8, ceiling: 12_000, tokens: 10_240, capped_by_ceiling: false, configurable: true },
    { key: "core_memory", label: "核心记忆", share_percent: 4, ceiling: 6_000, tokens: 5_120, capped_by_ceiling: false, configurable: true },
    { key: "world_book", label: "世界书", share_percent: 3, ceiling: 4_000, tokens: 3_840, capped_by_ceiling: false, configurable: true },
    { key: "session_thread", label: "会话便签", share_percent: 1, ceiling: 1_200, tokens: 1_200, capped_by_ceiling: true, configurable: true },
    { key: "self_notes", label: "自述", share_percent: 1, ceiling: 1_200, tokens: 1_200, capped_by_ceiling: true, configurable: true },
    { key: "persona", label: "人设", share_percent: 1, ceiling: 2_000, tokens: 846, capped_by_ceiling: false, configurable: false },
    { key: "prompt_rules", label: "提示词规则", share_percent: 1, ceiling: 9_000, tokens: 594, capped_by_ceiling: false, configurable: false }
  ]
};

const demoResidentContext = {
  context_window: 128_000,
  total_tokens: 18_709,
  note: "只列每轮都注入、与当前消息无关的内容。检索记忆、笔记本命中、世界书的触发式设定、命中触发词的 Skill 正文、跨群召回按当前消息命中才进；常驻核心记忆按发言者取，也不在这里。",
  blocks: [
    { key: "persona", label: "SOUL.md", tokens: 846, content: "# Diana\n\n## 概述\n\nDiana 是一个聊天机器人……", note: "排在系统提示词最前面，只有人能改；群可以整份覆盖。" },
    { key: "prompt_rules", label: "固定提示词规则", tokens: 8_021, content: "（演示数据：这里是按「全部工具都注册」展开的规则正文。）", note: "按「全部工具都注册」计算，是上限；实际注入哪几条随本轮注册的工具增减。随发言者变化的那段（权限、昵称、语气锚点）在请求尾部，不在这里。" },
    { key: "world_book", label: "世界书常驻设定", tokens: 0, budget: 1_200, note: "只含标了「常驻」的节点；按关键词触发的设定要命中才进。" },
    { key: "self_notes", label: "自述", tokens: 0, budget: 1_200, note: "机器人自己写的自我认知，默认关闭。" },
    { key: "session_thread", label: "会话便签", tokens: 0, budget: 1_200, note: "这个会话「聊到哪一步」的便签，由后台随对话滚动更新。" },
    { key: "agent_protocol", label: "Agent 协议与按需工具目录", tokens: 4_310, content: "（演示数据：Agent 协议、按需工具目录、扩展说明和规则。）", note: "按需工具只进这份目录（名字加一句用途），要用时先 tools_load。取自这个会话最近一轮回复。" },
    { key: "agent_tools", label: "常驻工具定义", tokens: 5_102, content: "web_search\nremember\npoke\ntools_load\ntools_execute\nagent_finalize", note: "这些工具每一步都带完整 schema，数字按 schema 估算，正文只列名字。从机器人配置「上下文」的常驻名单里拿掉，就会挪进上面的目录。" },
    { key: "skills", label: "Skill 目录与常驻正文", tokens: 430, content: "（演示数据：Skill 目录。）", note: "只含配成常驻的 Skill 正文；声明了触发词的要命中才带，不在底价里。" }
  ]
};

const now = Date.now();
const before = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const after = (minutes: number) => new Date(now + minutes * 60_000).toISOString();

const modelCatalog = [
  { id: "gpt-5.6", input_modalities: ["text", "image"], output_modalities: ["text"], context_window_tokens: 1_050_000 },
  { id: "gpt-5.4-mini", input_modalities: ["text"], output_modalities: ["text"], context_window_tokens: 400_000 },
  { id: "gpt-image-2", input_modalities: ["text", "image"], output_modalities: ["image"] }
];

let llmConfig: LLMConfig = {
  provider: "openai_compatible",
  model: "gpt-5.6",
  api_key_configured: true,
  profiles: [
    { id: "llm-chat", name: "主对话模型", group: "default", description: "群聊、私聊与 Agent 主回复", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-5.6", models: modelCatalog, max_output_tokens: 4096, effective_max_output_tokens: 4096, max_output_tokens_source: "user", effective_context_window_tokens: 128_000, effective_max_context_tokens: 128_000, context_window_source: "fallback", catalog_context_window_tokens: 1_050_000, role_bindings: [{ bot_id: "bot-onebot", bot_name: "Diana OneBot（演示）", role: "chat", role_label: "对话", model: "gpt-5.4-mini" }] },
    { id: "llm-vision", name: "视觉理解", group: "vision", description: "图片理解与 OCR", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-5.6", models: modelCatalog, max_output_tokens_source: "provider", effective_context_window_tokens: 128_000, effective_max_context_tokens: 128_000, context_window_source: "fallback", catalog_context_window_tokens: 1_050_000 },
    { id: "llm-intent", name: "主动回复判断", group: "intent", description: "群聊语义路由和机器人识别", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-5.4-mini", models: modelCatalog, effective_context_window_tokens: 400_000, effective_max_context_tokens: 400_000, context_window_source: "user", catalog_context_window_tokens: 400_000 },
    { id: "llm-image", name: "图片生成", group: "image", description: "独立图片生成测试链路", provider: "openai_compatible", api_style: "responses", api_key_configured: true, api_key_preview: "sk-pr…8X2a", base_url: "https://api.openai.com/v1", model: "gpt-image-2", image_model: "gpt-image-2", models: modelCatalog }
  ]
};

// 内置的默认 SOUL.md，和 model/assistant/souls/default.md 同一份。演示机器人直接用它，
// 人设页打开就是一份完整的样子，而不是一句话的占位。
const demoDefaultSoul = demoBuiltinSouls.find((soul) => soul.id === "builtin:default")?.system_prompt ?? "";

const oneBotProfile: BotProfileConfig = {
  id: "bot-onebot", name: "Diana OneBot（演示）", platform: "onebot-v11", enabled: true,
  onebot_reverse_ws_endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", onebot_access_token_configured: true, onebot_access_token_preview: "d1…2a",
  bot_account: "100000001", owner_id: "100200001", owner_login_enabled: true,
  group_triggers: ["Diana", "diana"], disabled_groups: [], system_prompt: demoDefaultSoul,
  debug_mode_enabled: true, bot_reply_loop_detection_enabled: true, prompt_inject_time: false,
  proactive_reply_chance: 1, proactive_reply_threshold: 0.9, recent_context_limit: 40, max_reply_chars: 0,
  cross_group_memory_enabled: true, world_book_enabled: true, romance_enabled: false, mood_enabled: true, poke_reply_enabled: true, expression_learning_enabled: true, dict_segment_enabled: true, semantic_search_enabled: false, agent_enabled: true, agent_max_steps: 12,
  max_bot_concurrency: 4, request_timeout_ms: 60_000,
  model_roles: {
    chat: { profile_id: "llm-chat", model: "gpt-5.6" }, vision: { profile_id: "llm-vision", model: "gpt-5.6" },
    intent: { profile_id: "llm-intent", model: "gpt-5.4-mini" }, image: { profile_id: "llm-image", model: "gpt-image-2" }
  }
};

const telegramProfile: BotProfileConfig = {
  ...oneBotProfile, id: "bot-telegram", name: "Diana Telegram（演示）", platform: "telegram", enabled: true,
  onebot_reverse_ws_endpoint: "", onebot_access_token_configured: false, telegram_bot_token_configured: true,
  telegram_api_base_url: "https://api.telegram.org", bot_account: "", owner_id: "880024"
};

let assistantConfig: BotProfileConfig = { ...oneBotProfile, profiles: [oneBotProfile, telegramProfile] };


function demoPluginForProfile(plugin: PluginState, profile: string): PluginState {
  const settings = { ...plugin.settings };
  const secrets: Record<string, boolean> = {};
  for (const spec of plugin.manifest.settings ?? []) if (spec.secret) {
    secrets[spec.key] = Boolean(settings[spec.key]);
    delete settings[spec.key];
  }
  return { ...plugin, enabled: plugin.profile_enabled?.[profile] ?? plugin.enabled, settings, secrets_configured: secrets };
}

let plugins: PluginState[] = [
  { manifest: { id: "official.file-parser", name: "文件解析", version: "0.3.0", description: "解析 PDF、图片和文本附件，把结构化内容交给模型。", official: true, built_in: true, permissions: ["文件解析", "消息读取"] }, installed: true, enabled: true },
  { manifest: { id: "official.nonebot-plugin-resolver-go", name: "链接解析", version: "0.3.0", description: "解析社交媒体链接，支持合并转发图片和限定大小的视频。", official: true, built_in: true, permissions: ["网络请求", "消息发送"],
      settings: [
        { key: "bili_sessdata", label: "B 站 SESSDATA", type: "string", default: "", secret: true, description: "所有机器人共用的 B 站 SESSDATA。留空不使用登录凭据。" },
        { key: "douyin_cookie", label: "抖音 Cookie", type: "string", default: "", secret: true, description: "所有机器人共用的抖音 Cookie。不配置时无法解析需要登录的内容。" },
        { key: "xhs_cookie", label: "小红书 Cookie", type: "string", default: "", secret: true, description: "所有机器人共用的小红书 Cookie。不配置时无法解析需要登录的内容。" },
        { key: "ytdlp_cookies_path", label: "yt-dlp Cookie 文件路径", type: "string", default: "", description: "共享的 Netscape 格式 Cookie 文件路径。" }
      ] }, installed: true, enabled: true },
  {
    manifest: {
      id: "official.music", name: "音乐增强", version: "0.2.1", description: "群里分享的音乐链接直接下成一条语音发出来；开启点歌后，模型也能按用户要求搜歌并发送。网易云、QQ 音乐、酷狗并列，一家放不出来自动换下一家。仅 OneBot v11 支持语音。", official: true, built_in: true, permissions: ["模型工具", "网络请求", "文件写入", "消息发送"],
      settings: [
        { key: "request_song_enabled", label: "允许点歌", type: "bool", default: true, description: "开启后模型可以按用户要求搜歌并直接发出语音。关掉只保留链接解析。" },
        { key: "enabled_sources", label: "启用曲库", type: "multi_select", default: ["netease", "qq", "kugou"], options: [{ value: "netease", label: "网易云音乐" }, { value: "qq", label: "QQ 音乐" }, { value: "kugou", label: "酷狗音乐" }], description: "一首歌在这家是会员专享、在那家能试听是常事。勾多几家，一家放不出来就自动换下一家。" },
        { key: "preferred_source", label: "点歌优先曲库", type: "select", default: "", options: [{ value: "", label: "按启用顺序" }, { value: "netease", label: "网易云音乐" }, { value: "qq", label: "QQ 音乐" }, { value: "kugou", label: "酷狗音乐" }], description: "点歌时先问哪家。分享链接始终用链接自己的平台，不受这里影响。" },
        { key: "netease_api_base", label: "网易云自建 API 地址", type: "string", default: "", description: "自建 NeteaseCloudMusicApi 的地址，例如 http://127.0.0.1:3000。留空走官方接口，只能拿到可试听的歌曲。" },
        { key: "netease_cookie", label: "网易云 MUSIC_U Cookie", type: "string", default: "", secret: true, description: "登录 Cookie 里的 MUSIC_U，用于会员音质和受限曲目。" },
        { key: "qq_api_base", label: "QQ 音乐自建 API 地址", type: "string", default: "", description: "自建 QQMusicApi 的地址。留空走官方接口，无登录态时多数曲目取不到播放地址。" },
        { key: "qq_cookie", label: "QQ 音乐 Cookie", type: "string", default: "", secret: true, description: "完整的 Cookie 串，用于会员和独家曲目。" },
        { key: "kugou_api_base", label: "酷狗自建 API 地址", type: "string", default: "", description: "自建 KuGouMusicApi 的地址。留空走官方接口。" },
        { key: "kugou_cookie", label: "酷狗 Cookie", type: "string", default: "", secret: true, description: "完整 Cookie（建议包含 token、userid、dfid）。会员曲目需配合自建 KuGouMusicApi；留空只能尝试公开试听。" },
        { key: "bitrate", label: "音质", type: "select", default: "320000", options: [{ value: "128000", label: "标准 128k" }, { value: "192000", label: "较高 192k" }, { value: "320000", label: "极高 320k" }], description: "只对网易云的自建 API 生效；其余情况由平台自己决定码率。" },
        { key: "max_duration_seconds", label: "最长时长", type: "number", default: 600, min: 30, max: 1800, step: 30, unit: "秒" },
        { key: "max_file_mb", label: "最大文件", type: "number", default: 20, min: 1, max: 100, step: 1, unit: "MB" },
        { key: "timeout_seconds", label: "请求超时", type: "number", default: 45, min: 5, max: 180, step: 5, unit: "秒" },
        { key: "send_song_info", label: "同时发送歌曲信息", type: "bool", default: true, description: "在语音前补一条「歌名 - 歌手」，否则群里只看到一条不知道是什么的语音。" },
        { key: "silk_encoder_path", label: "Silk 编码器路径", type: "string", default: "", description: "填了就把音频转成 Tencent Silk 再发。留空沿用语音合成插件的配置。" }
      ]
    },
    installed: true, enabled: true
  },
  { manifest: { id: "official.onebot-v11", name: "OneBot 协议", version: "0.1.0", description: "提供 OneBot v11 事件、消息发送、群组列表和协议扩展动作。", official: true, built_in: true, permissions: ["OneBot 读取", "OneBot 写入"] }, installed: true, enabled: true },
  { manifest: { id: "official.open-api", name: "对外 API", version: "0.1.0", description: "让 CI、监控这类外部系统凭密钥调用 HTTP 接口向指定会话推送消息；密钥在「设置 → 安全」里管理。", official: true, built_in: true, default_disabled: true, permissions: ["network:http", "message:write"], settings: [{ key: "rate_limit_per_minute", label: "单密钥限流", description: "每把密钥每分钟允许的调用次数，超出返回 429。", type: "number", default: 60, min: 1, max: 600, step: 10, unit: "次/分钟" }] }, installed: true, enabled: false },
  {
    manifest: {
      id: "official.repository-publish", name: "GitHub Issue 与 PR", version: "0.6.0", description: "搜索和管理 GitHub Issue；读取 Pull Request 的描述、改动文件和 patch，并在 PR 上发表评论或提交 review（只评论，不批准、不合并）。群成员可生成草稿，由具备仓库权限的授权用户用确认码确认后写入。", official: true, built_in: true, permissions: ["network:https", "github:issues:read", "github:issues:write", "audit:write", "llm:tool"],
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
      id: "official.repository-watch", name: "仓库订阅", version: "0.2.1", description: "监控公开或私有 GitHub 仓库的 Commit、PR、Release 与 Star，经模型阅读受限 diff 并总结后通知指定对象。", official: true, built_in: true, permissions: ["网络请求", "任务持久化", "消息发送"],
      settings: [
        { key: "github_token", label: "GitHub Token", description: "用于私有仓库和提高 API 额度。", type: "string", default: "", secret: true },
        { key: "follow_up_include_patch", label: "跟评读取受限代码片段", description: "开启后会把经过严格裁剪的 patch 发送给当前 LLM Provider；私有仓库请谨慎开启。", type: "bool", default: false },
        { key: "default_interval_seconds", label: "默认检查周期", type: "number", default: 60, min: 30, max: 86400, unit: "秒" },
        { key: "failure_alert_threshold", label: "连续失败几次才报", description: "订阅连着失败到这个次数才在聊天里说一声，一轮故障只报一次，恢复后再说一声好了。报不报看下面的「发送错误通知」和机器人配置里的「出错时在聊天里提示」，任一关着都不报。", type: "number", default: 5, min: 1, max: 100, step: 1, unit: "次" },
        { key: "error_notice", label: "发送错误通知", description: "这个插件失败时是否在聊天里说明。关掉后失败仍然记入事件和日志，只是不再打扰聊天。机器人的「错误提示」关掉时，这里开着也不会发。", type: "bool", default: true }
      ]
    },
    installed: true, enabled: true, settings: { default_interval_seconds: 60 }, secrets_configured: { github_token: true }
  },
  {
    manifest: { id: "official.sticker-sender", name: "表情包发送", version: "0.2.0", description: "启用内置 Agent 后，从持久表情资产库中检索候选，按当前语义选一张发送。", official: true, built_in: true, permissions: ["message:read", "message:send"], settings: [{ key: "history_limit", label: "每个范围候选上限", type: "number", default: 1000 }, { key: "search_results", label: "候选返回数量", type: "number", default: 8 }] },
    installed: true, enabled: true
  },
  {
    manifest: { id: "official.file-delivery", name: "文件交付", version: "0.1.1", description: "启用内置 Agent 后，模型可以把写好的代码、SVG、Markdown 等文本内容直接作为文件发到会话供下载，并可附带渲染预览图；HTML 页面和 SVG 还能直接渲染成图片、MP4 视频或 GIF 发出。", official: true, built_in: true, permissions: ["message:send", "file:send", "browser:render"], settings: [{ key: "max_file_bytes", label: "单个文件大小上限", type: "size", default: 1048576 }, { key: "preview", label: "默认附带预览图", type: "bool", default: true }, { key: "owner_only", label: "仅主人可用", type: "bool", default: false }, { key: "render_media", label: "HTML/动画渲染", type: "bool", default: true }, { key: "max_video_seconds", label: "视频/GIF 最长时长", type: "number", default: 10, min: 1, max: 20, step: 1, unit: "秒" }] },
    installed: true, enabled: true
  },
  {
    manifest: { id: "official.rss-watch", name: "RSS 订阅", version: "0.2.0", description: "按条件监控 RSS 或社交动态，一条订阅可同时盯多个账号或 Feed，判断后发送到指定群聊或私聊。", official: true, built_in: true, permissions: ["网络请求", "消息发送"], settings: [{ key: "default_interval_seconds", label: "默认检查周期", type: "number", default: 300, min: 30, max: 86400, unit: "秒" }, { key: "failure_alert_threshold", label: "连续失败几次才报", description: "订阅连着失败到这个次数才在聊天里说一声，一轮故障只报一次，恢复后再说一声好了。报不报看下面的「发送错误通知」和机器人配置里的「出错时在聊天里提示」，任一关着都不报。", type: "number", default: 5, min: 1, max: 100, step: 1, unit: "次" }, { key: "error_notice", label: "发送错误通知", description: "这个插件失败时是否在聊天里说明。关掉后失败仍然记入事件和日志，只是不再打扰聊天。机器人的「错误提示」关掉时，这里开着也不会发。", type: "bool", default: true }] },
    installed: true, enabled: true
  },
  {
    manifest: {
      id: "official.sandboxed-browser-renderer", name: "网页渲染", version: "0.3.1",
      description: "使用 Chromium / Google Chrome，在一次性隔离配置中执行动态网页。",
      official: true, built_in: true, permissions: ["网页渲染", "隔离浏览器"],
      settings: [{
        key: "window_mode", label: "Chrome 窗口模式", type: "select", default: "auto",
        options: [{ value: "auto", label: "自动（推荐）" }, { value: "headless", label: "始终无头" }, { value: "visible", label: "显示隔离窗口" }]
      }]
    },
    installed: true, enabled: true
  },
  { manifest: { id: "official.status-command", name: "状态查询", version: "0.1.0", description: "群里或私聊发一条 #diana（整条消息只有这一个词）就回一张运行状态卡片：版本、平台、已运行时长。不经过模型，回复固定且立刻返回，用来确认机器人还活着。默认关闭。", official: true, built_in: true, default_disabled: true, permissions: ["message:read", "message:send"] }, installed: true, enabled: false }
];

for (const plugin of plugins) if (plugin.manifest.id !== "official.open-api") {
  plugin.profile_enabled = Object.fromEntries((assistantConfig.profiles ?? []).map((profile) => [profile.id!, plugin.enabled]));
  plugin.enabled = !plugin.manifest.default_disabled;
}

// 演示用第三方仓库插件：插件页「从 GitHub 安装」的预览与安装都走它。
const demoRepoPlugin: PluginState = {
  manifest: {
    id: "demo.daily-verse", name: "每日一句", version: "1.2.0",
    description: "每天往群里发一句 curated 的格言或冷知识，支持自定义来源开关。",
    official: false, built_in: false,
    permissions: ["message:read", "message:send", "network:https", "task:persistent"],
    settings: [
      { key: "send_hour", label: "发送时间（时）", type: "number", default: 9, min: 0, max: 23, unit: "时" },
      { key: "source_token", label: "来源 API Token", type: "string", default: "", secret: true, description: "自建来源服务需要；留空用内置来源。" }
    ]
  },
  installed: false, enabled: true
};

const demoRepoPluginSource = {
  id: "demo.daily-verse", owner: "demo-author", repo: "diana-plugin-daily-verse",
  ref: "v1.2.0", commit: "3f9c1a7e2b4d6f8091a2b3c4d5e6f708192a3b4c", version: "1.2.0", url: "https://github.com/demo-author/diana-plugin-daily-verse",
  installed_at: "2026-09-19T04:00:00Z"
};

const demoRepoPluginPreview = {
  source: { owner: "demo-author", repo: "diana-plugin-daily-verse", ref: "v1.2.0" },
  commit: "3f9c1a7e2b4d6f8091a2b3c4d5e6f708192a3b4c",
  manifest: demoRepoPlugin.manifest,
  permissions: [
    { id: "message:write", label: "修改、撤回已发消息", sensitive: true },
    { id: "message:read", label: "读取消息内容与历史" },
    { id: "message:send", label: "主动发送消息" },
    { id: "network:https", label: "发起 HTTPS 网络请求" },
    { id: "task:persistent", label: "创建持久后台任务" }
  ],
  files: ["SKILL.md", "prompts/"],
  risk: {
    floating_ref: false,
    warnings: [
      "第三方插件由仓库作者发布，Diana 不对其行为负责。",
      "插件以 SKILL.md 指令的形式进入对话上下文；清单里的权限只是作者声明，Diana 不据此限制插件。插件内容可以引导机器人使用它当前已开放的全部工具（例如联网搜索、执行命令、写 GitHub），请只安装信任的作者发布的插件。"
    ]
  }
};

const demoStickers = [
  { hash: "a".repeat(64), summary: "懂了", description: "猫猫认真点头，表示已经明白对方的意思，语气轻松，适合接在解释之后。", kind: "group", group_id: "100200301", sessions: 3, last_seen: before(12) },
  { hash: "b".repeat(64), summary: "无语", description: "角色面无表情地盯着镜头，表达对离谱发言的无奈。", kind: "group", group_id: "100200418", sessions: 1, last_seen: before(40) },
  { hash: "c".repeat(64), summary: "动画表情", kind: "private", user_id: "880024", sessions: 1, last_seen: before(90) },
  { hash: "d".repeat(64), summary: "贴贴", description: "两只小动物蹭在一起，表示亲近或安慰。", kind: "group", group_id: "100200301", sessions: 2, last_seen: before(200) }
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
  { group_id: "100200301", group_name: "产品讨论（演示）", avatar_url: demoGroupAvatar, member_count: 186, max_member_count: 500, enabled: true, configured: true, joined: true, group_triggers: ["Diana", "diana"], system_prompt: "以准确、简洁的方式参与产品和工程讨论。", recent_context_limit: 50, model_call_quota: 400, reply_sample_percent: 40, quota_call_limit: 400, quota_calls_used: 168, proactive_reply_chance: 1, proactive_reply_threshold: 0.9, reply_gate: { active_hours_enabled: true, active_start: "08:00", active_end: "23:30", timezone: "Asia/Shanghai", blocked_users: ["100200999"], owner_bypass: true }, plugin_overrides: { "official.repository-watch": true }, updated_at: before(12) },
  { group_id: "100200418", group_name: "日常交流（演示）", avatar_url: demoGroupAvatar, member_count: 74, max_member_count: 200, enabled: true, configured: true, joined: true, group_triggers: ["Diana"], system_prompt: "自然参与闲聊，事实不确定时优先搜索。", recent_context_limit: 40, quota_call_limit: 400, quota_calls_used: 400, proactive_reply_chance: 1, proactive_reply_threshold: 0.9, plugin_overrides: {}, updated_at: before(28) },
  { group_id: "100200519", group_name: "设计讨论（演示）", avatar_url: demoGroupAvatar, member_count: 52, max_member_count: 200, enabled: true, configured: true, joined: true, group_triggers: ["画一张", "Diana"], system_prompt: "优先理解视觉需求，并在生图前补齐必要约束。", reply_gate: { active_hours_enabled: true, active_start: "09:00", active_end: "22:00", timezone: "Asia/Shanghai", blocked_users: ["100200888", "100200889"] }, plugin_overrides: { "official.sandboxed-browser-renderer": false }, updated_at: before(45) },
  { group_id: "100200627", group_name: "只读观察群（演示）", avatar_url: demoGroupAvatar, member_count: 318, max_member_count: 500, enabled: false, configured: true, joined: true, group_triggers: [], system_prompt: "仅记录事件，不主动回复。", plugin_overrides: {}, updated_at: before(90) }
];

// 机器人见过的人从画像里取名，没见过的走 OneBot get_stranger_info；演示模式两条路
// 都没有，用这张表补上画像里没有的号。
const demoAccountNames: Record<string, string> = { "100200001": "阿墨", "880024": "小林（演示）" };

// 内置人设在真实后端里是编译进程序的几份 SOUL.md；演示模式读 Go 测试生成的那份 JSON
// （TestDemoBuiltinSoulsInSync 盯着它和源文件一致），后面接一份用户自己存的。
const demoPersonas: Array<{ id: string; name: string; system_prompt: string; builtin?: boolean; updated_at?: string }> = [
  ...demoBuiltinSouls,
  { id: "persona-3", name: "值班助理", updated_at: before(26 * 60), system_prompt: "# 值班助理\n\n## 概述\n\n它在工作群里协助排查问题。我们希望它先给结论再给依据，因为值班的人没空读长文。\n\n## 正派\n\n没把握就说没把握：值班时一句含糊的「应该没事」比沉默更危险。" }
];

// 世界书的演示数据：一条常驻骨架加一条触发式细节，让树形和两种注入方式都看得到。
const demoWorldBook: WorldBookNode[] = [
  { id: "world-1", parent_id: "", title: "枝江", content: "故事发生在虚构城市枝江，机器人就住在群主的服务器上。", keywords: [], always_on: true, enabled: true },
  { id: "world-2", parent_id: "world-1", title: "港口", content: "枝江港常年有雾，群友们约好雾散了一起去钓鱼。", keywords: ["港口", "码头", "钓鱼"], always_on: false, enabled: true }
];

const demoUsers: UserMemoryProfile[] = [
  {
    user_id: "100200711", display_name: "青禾", favorability: 62, message_count: 1843, last_seen_at: before(2), updated_at: before(2),
    romance: { active: true, since: before(64000), started_by: "user" },
    portrait: [
      { field: "residence", label: "居住地点", value: "住在杭州", source: "stated", updated_at: before(1400) },
      { field: "occupation", label: "职业", value: "做后端开发", source: "stated", updated_at: before(2600) },
      { field: "routine", label: "作息", value: "习惯下午集中处理事务", source: "inferred", updated_at: before(1400) },
      { field: "habit", label: "生活习惯", value: "每天手冲一杯咖啡，不加糖", source: "stated", updated_at: before(4300) }
    ],
    memories: [
      { text: "在做 Diana 的发布流程改造，经常问仓库最近的变更", source: "group", group_id: "100200301", at: before(2) },
      { text: "习惯下午提交周报，让机器人 15:30 提醒", source: "group", group_id: "100200301", at: before(1400) },
      { text: "喜欢喝手冲咖啡，不加糖", source: "group", group_id: "100200418", at: before(4300) }
    ]
  },
  {
    user_id: "100200913", display_name: "星野", favorability: 35, message_count: 622, last_seen_at: before(31), updated_at: before(31),
    portrait: [
      { field: "occupation", label: "职业", value: "在做视觉设计", source: "inferred", updated_at: before(2100) },
      { field: "interest", label: "兴趣爱好", value: "喜欢复古电车和胶片质感", source: "stated", updated_at: before(2100) }
    ],
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

// 画像栏目表由后端给出，演示模式照着给一份同样的。
const demoPortraitFields = [
  { field: "residence", label: "居住地点", hint: "常住的城市或地区，不要记具体门牌地址", capacity: 1 },
  { field: "occupation", label: "职业", hint: "职业、行业或在读身份", capacity: 1 },
  { field: "routine", label: "作息", hint: "长期的起居和活跃时段", capacity: 1 },
  { field: "habit", label: "生活习惯", hint: "饮食、运动、通勤等稳定的生活方式", capacity: 4 },
  { field: "interest", label: "兴趣爱好", hint: "长期的爱好、常玩的游戏、追的领域", capacity: 4 },
  { field: "relation", label: "家庭与关系", hint: "同住的家人、宠物等稳定关系", capacity: 3 },
  { field: "other", label: "其他", hint: "上面几栏装不下、但确实稳定的个人情况", capacity: 4 }
];

// 类型清单由后端给出，演示模式照着给一份同样的。
const demoNotebookKinds = [
  { value: "term", label: "词条" },
  { value: "fact", label: "事实" },
  { value: "preference", label: "偏好" },
  { value: "event", label: "事件" },
  { value: "todo", label: "待办" },
  { value: "person", label: "人物" }
];

// 演示模式的笔记本：机器人自己记下的东西长什么样，比一段说明更说明问题。
// 六种类型各给一条，让人一眼看出它不只是本词典。
const demoNotebook: NotebookEntry[] = [
  {
    id: "notebook-1", scope_key: "group:100200301", kind: "term", term: "带薪拉屎", aliases: ["DXLS"],
    meaning: "上班时间摸鱼，群里用来自嘲，没有恶意", example: "今天带薪拉屎半小时",
    author_name: "青禾", usage_count: 27, last_used_at: before(3), version: 2, status: "active",
    created_at: before(9000), updated_at: before(120),
    revisions: [
      { version: 2, meaning: "上班时间摸鱼，群里用来自嘲，没有恶意", note: "更新：补上「自嘲、无恶意」，之前被当成骂人", editor_name: "控制台", recorded_at: before(120) },
      { version: 1, meaning: "上班时间摸鱼", note: "新建", editor_name: "青禾", recorded_at: before(9000) }
    ]
  },
  {
    id: "notebook-2", scope_key: "group:100200301", kind: "term", term: "鸽", aliases: ["咕咕"],
    meaning: "放人鸽子、说好的事没做；本群多用于调侃谁又拖了发布",
    author_name: "星野", usage_count: 41, last_used_at: before(20), version: 1, status: "active",
    created_at: before(7200), updated_at: before(7200), revisions: []
  },
  {
    id: "notebook-3", scope_key: "group:100200418", kind: "term", term: "手冲", aliases: [],
    meaning: "手冲咖啡，这个群里只指咖啡", usage_count: 6, last_used_at: before(900),
    version: 1, status: "active", created_at: before(5400), updated_at: before(5400), revisions: []
  },
  // 全局笔记本每台机器人各一本：同一个梗在两台那里可以有不同的记法。
  {
    id: "notebook-4", scope_key: "bot:bot-onebot", kind: "term", term: "开摆", aliases: ["摆了"],
    meaning: "放弃挣扎、随它去，群里多用于自嘲进度", usage_count: 18, last_used_at: before(60),
    version: 1, status: "active", created_at: before(6000), updated_at: before(6000), revisions: []
  },
  {
    id: "notebook-5", scope_key: "bot:bot-telegram", kind: "term", term: "开摆", aliases: [],
    meaning: "这台机器人上记的是英文频道的用法：give up and chill",
    usage_count: 4, last_used_at: before(400),
    version: 1, status: "active", created_at: before(4200), updated_at: before(4200), revisions: []
  },
  // 以下几条是词典升级成笔记本之后才记得下的东西：标题不会原样出现在聊天里，
  // 靠触发词命中。
  {
    id: "notebook-6", scope_key: "group:100200301", kind: "fact",
    term: "群规：晚上十点后不要连续刷屏", aliases: ["群规", "刷屏", "刷频"],
    meaning: "管理员定的，十点后有事私聊，不在群里连发",
    author_name: "青禾", usage_count: 12, last_used_at: before(240),
    version: 1, status: "active", created_at: before(5000), updated_at: before(5000), revisions: []
  },
  {
    id: "notebook-7", scope_key: "group:100200301", kind: "preference",
    term: "星野不吃香菜", aliases: ["香菜", "星野", "点菜", "聚餐"],
    meaning: "点外卖和聚餐都要记得备注去香菜，之前踩过两次",
    author_name: "控制台", usage_count: 8, last_used_at: before(1500),
    version: 1, status: "active", created_at: before(4800), updated_at: before(4800), revisions: []
  },
  {
    id: "notebook-8", scope_key: "group:100200301", kind: "todo",
    term: "给群里买周年蛋糕", aliases: ["蛋糕", "周年", "庆祝"],
    meaning: "答应了这个月底之前订好，还没订",
    author_name: "青禾", usage_count: 3, last_used_at: before(600),
    version: 1, status: "active", created_at: before(1200), updated_at: before(1200), revisions: []
  },
  {
    id: "notebook-9", scope_key: "group:100200301", kind: "event",
    term: "上次线下聚会在 7 月，去了七个人", aliases: ["聚会", "线下", "面基"],
    meaning: "在城西那家火锅，星野迟到了一小时，这事群里还在拿来调侃",
    usage_count: 5, last_used_at: before(2400),
    version: 1, status: "active", created_at: before(3600), updated_at: before(3600), revisions: []
  },
  {
    id: "notebook-10", scope_key: "bot:bot-onebot", kind: "person",
    term: "青禾是这个群的管理员", aliases: ["青禾", "管理员", "群主"],
    meaning: "日常管群规和活动，技术问题找他没用，他自己也不写代码",
    usage_count: 15, last_used_at: before(90),
    version: 1, status: "active", created_at: before(8000), updated_at: before(8000), revisions: []
  }
];

function demoNotebookScopes(): NotebookScopeSummary[] {
  const scopes = new Map<string, NotebookScopeSummary>();
  for (const entry of demoNotebook) {
    const summary = scopes.get(entry.scope_key) ?? { scope_key: entry.scope_key, active_count: 0, deleted_count: 0, updated_at: entry.updated_at };
    if (entry.status === "active") summary.active_count += 1; else summary.deleted_count += 1;
    scopes.set(entry.scope_key, summary);
  }
  return [...scopes.values()];
}

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

// 后台好感度评估：每种结果各给一两条，有两条带画像，演示站「全部」里能看到全部分类。
const demoRelationshipEvaluations: RelationshipEvaluation[] = [
  { id: 9, bot_profile_id: "bot-onebot", user_id: "100200711", sender_name: "青禾", group_id: "100200301", message_text: "@Diana 帮我总结一下今天的发布变更，谢啦", status: "changed", proposed_delta: 1, applied_delta: 1, before_score: 61, after_score: 62, confidence: 0.92, reason: "真诚道谢，互动友好", model: "gpt-5.4-mini", portrait: [{ field: "occupation", label: "职业", value: "后端工程师", source: "stated" }], created_at: before(2) },
  { id: 8, bot_profile_id: "bot-onebot", user_id: "100200913", sender_name: "星野", group_id: "100200519", message_text: "画一张雨夜城市里的复古电车", status: "unchanged", proposed_delta: 0, applied_delta: 0, before_score: 35, after_score: 35, confidence: 0.96, reason: "普通的生图请求，不影响关系", model: "gpt-5.4-mini", portrait: [{ field: "interest", label: "兴趣爱好", value: "喜欢复古电车和雨夜街景", source: "inferred" }], created_at: before(31) },
  { id: 7, bot_profile_id: "bot-onebot", user_id: "100201014", sender_name: "白榆", group_id: "100200418", message_text: "你今天好像有点笨哦", status: "low_confidence", proposed_delta: -1, applied_delta: 0, before_score: 12, after_score: 12, confidence: 0.55, reason: "可能是玩笑，也可能在抱怨，不好判断", model: "gpt-5.4-mini", created_at: before(47) },
  { id: 6, bot_profile_id: "bot-onebot", user_id: "100200001", sender_name: "主人", message_text: "今天也辛苦你了", status: "capped", proposed_delta: 2, applied_delta: 0, before_score: 200, after_score: 200, confidence: 0.9, reason: "主人的关心", model: "gpt-5.4-mini", created_at: before(95) },
  { id: 5, bot_profile_id: "bot-onebot", user_id: "100200913", sender_name: "星野", group_id: "100200519", message_text: "刚才那张图太好看了！", status: "skipped", proposed_delta: 0, applied_delta: 0, before_score: 0, after_score: 0, confidence: 0, error: "后台评估同时进行的数量已满，这一轮跳过", created_at: before(120) },
  { id: 4, bot_profile_id: "bot-onebot", user_id: "100200888", sender_name: "路人甲", group_id: "100200519", message_text: "加群领福利，私聊我", status: "changed", proposed_delta: -3, applied_delta: -3, before_score: -5, after_score: -8, confidence: 0.97, reason: "重复发送广告内容", model: "gpt-5.4-mini", created_at: before(3000) },
  { id: 3, bot_profile_id: "bot-onebot", user_id: "100200711", sender_name: "青禾", group_id: "100200301", message_text: "部署好了，多亏你", status: "failed", proposed_delta: 0, applied_delta: 0, before_score: 60, after_score: 60, confidence: 0, error: "context deadline exceeded（模拟数据）", created_at: before(3200) }
];

export const demoEvents: AssistantEventDetail[] = [
  { id: "demo-event-1", at: before(2), kind: "group", platform: "onebot-v11", profile_id: "bot-onebot", group_id: "100200301", user_id: "100200711", sender_name: "青禾", message_id: "demo-7319", text: "@Diana 帮我总结一下今天的发布变更", reply: "今天的更新重点是事件原因审计、仓库动态订阅和多通道会话隔离。引用消息同时 @机器人时也会正确进入主 Agent。", handled: true, status: "replied", outcome: "replied", decision: "replied", reason: "检测到显式 @机器人，直接进入主 Agent；问题需要读取仓库近期变更后回答。", duration_ms: 6800, llm_calls: 2, input_tokens: 2470, output_tokens: 376, total_tokens: 2846, reply_models: ["gpt-5.4"], models: [{ model: "gpt-5.4-mini", provider: "openai_compatible", calls: 1 }, { model: "gpt-5.4", provider: "openai_compatible", calls: 1 }], delivery_stage: "echo_persisted", outbound_message_id: "demo-out-7319", self_echo_at: before(1) },
  { id: "demo-event-2", at: before(9), kind: "group", platform: "onebot-v11", profile_id: "bot-onebot", group_id: "100200418", user_id: "100200812", sender_name: "栖迟", message_id: "demo-7298", text: "[图片]", handled: false, status: "ignored", outcome: "bot_message_ignored", decision: "not_replied", reason: "识别为其他机器人发送的自动消息；“识别机器人后不回复”已启用，因此未启动视觉模型和主 Agent。", duration_ms: 42, llm_calls: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0 },
  { id: "demo-event-3", at: before(18), kind: "private", platform: "telegram", profile_id: "bot-telegram", user_id: "880024", sender_name: "Demo User", message_id: "demo-tg-114", text: "Zeabur 最近有什么产品更新？", reply: "我检索了官方更新渠道，并按发布时间整理了近期变化；真实服务会在回答中保留来源链接。", handled: true, status: "replied", outcome: "replied", decision: "replied", reason: "私聊默认响应；问题包含时效性要求，Agent 先调用内置联网搜索再组织答案。", duration_ms: 8200, llm_calls: 2, input_tokens: 2844, output_tokens: 387, total_tokens: 3231, reply_models: ["claude-sonnet-5"], models: [{ model: "claude-sonnet-5", provider: "anthropic", calls: 2 }], delivery_stage: "acknowledged", outbound_message_id: "demo-tg-out-114" },
  { id: "demo-event-4", at: before(31), kind: "group", platform: "onebot-v11", profile_id: "bot-onebot", group_id: "100200519", user_id: "100200913", sender_name: "星野", message_id: "demo-7220", text: "画一张雨夜城市里的复古电车", reply: "图片已生成并发送。事件明细默认显示缩略图，点击后查看原图。", handled: true, status: "replied", outcome: "image_generated", decision: "replied", reason: "命中群触发词并识别为明确的图片生成请求；调用独立生图模型。", duration_ms: 18400, llm_calls: 1, input_tokens: 1410, output_tokens: 262, total_tokens: 1672, delivery_stage: "acknowledged", outbound_message_id: "demo-out-7220" },
  { id: "demo-event-quote-7240", at: before(40), kind: "group", platform: "onebot-v11", profile_id: "bot-onebot", group_id: "100200301", user_id: "100200711", sender_name: "青禾", message_id: "demo-7240", text: "那 Zeabur 自己的控制台也是这个路子吗", quote: { message_id: "demo-out-7166", user_id: "3129583166", sender_name: "Diana", text: "如果是在说产品界面，通常指偏开发者工具的克制布局：高信息密度、明确状态和较少装饰。" }, handled: false, status: "done", outcome: "not_replied", decision: "not_replied", reason: "引用的是机器人的回复，但只是在和其他群友讨论，主动回复判断认为不必插话。", duration_ms: 1200 },
  // 撤回机器人回复的通知：控制台会把它合进 demo-event-5，不单独占一行。
  { id: "demo-event-recall-7167", at: before(45), kind: "notice", platform: "onebot-v11", profile_id: "bot-onebot", group_id: "100200301", user_id: "3129583166", sender_name: "Diana", message_id: "demo-out-7167", sub_type: "group_recall", original_time: before(47), operator_id: "100201014", operator_name: "白榆", operator_role: "group_admin", text: "", handled: false, status: "done", outcome: "notice_group_recall", decision: "notice", reason: "已记录平台通知" },
  { id: "demo-event-5", at: before(47), kind: "group", platform: "onebot-v11", profile_id: "bot-onebot", group_id: "100200301", user_id: "100201014", sender_name: "白榆", message_id: "demo-7166", text: "Zeabur 风味是什么", reply: "如果是在说产品界面，通常指偏开发者工具的克制布局：高信息密度、明确状态和较少装饰。", handled: true, status: "replied", outcome: "proactive_replied", decision: "replied", reason: "短句虽未显式 @机器人，但包含可回答的产品语境问题；主动回复判断认为应该参与。", duration_ms: 4900, llm_calls: 2, input_tokens: 1671, output_tokens: 237, total_tokens: 1908, reply_models: ["gpt-5.4"], models: [{ model: "gpt-5.4-mini", provider: "openai_compatible", calls: 1 }, { model: "gpt-5.4", provider: "openai_compatible", calls: 1 }], delivery_stage: "echo_persisted", outbound_message_id: "demo-out-7166,demo-out-7167", delivery: { messages: 2, images: 1, media: [{ kind: "image", label: "表情包：懂了" }] }, recalls: [{ message_id: "demo-out-7167", at: before(45), operator_id: "100201014", operator_name: "白榆", operator_role: "group_admin" }] }
];

const trace: AppLogEntry[] = [
  { id: "trace-1", kind: "debug", level: "info", action: "agent_trace", message: "模型请求", created_at: before(2), metadata: { phase: "model_request", purpose: "intent", provider: "openai_compatible", model: "gpt-5.4-mini", duration_ms: 640, request: { messages: [{ role: "system", content: "判断消息是否明确指向机器人" }, { role: "user", content: "@Diana 帮我总结一下今天的发布变更" }] }, response: { directed_at_bot: true, answerable: true, confidence: 0.99 } } },
  { id: "trace-2", kind: "debug", level: "info", action: "agent_trace", message: "Agent 启动", created_at: before(2), metadata: { phase: "agent_started", model: "gpt-5.6", available_tools: ["repository_history", "web_search", "memory_search", "message_send"] } },
  { id: "trace-3", kind: "debug", level: "info", action: "agent_trace", message: "仓库工具完成", created_at: before(2), metadata: { phase: "agent_tool_completed", tool: "repository_history", duration_ms: 920, tool_input: { repository: "SuInk/Diana", range: "today" }, tool_output: "读取到 6 条提交和 1 个 Release（模拟数据）" } },
  { id: "trace-4", kind: "debug", level: "info", action: "agent_trace", message: "Agent 完成", created_at: before(1), metadata: { phase: "agent_completed", finish_reason: "stop", duration_ms: 6800 } }
];

// 演示里两台机器人分掉同一条曲线，切换开关才看得出总览确实跟着变。
function hourlyBuckets(share: number): StatsHourBucket[] {
  return demoHourlyTotals.map((total, index) => ({
    hour_unix: Math.floor((now - (23 - index) * 3_600_000) / 1000),
    total: Math.round(total * share),
    handled: Math.round(demoHourlyHandled[index] * share),
    errors: 0
  }));
}

const demoHourlyTotals = [14, 22, 18, 35, 29, 48, 41, 60, 45, 72, 54, 82, 63, 77, 66, 39, 52, 44, 61, 34, 49, 57, 78, 64];
const demoHourlyHandled = [3, 4, 2, 8, 5, 11, 9, 15, 8, 18, 12, 21, 13, 17, 15, 7, 12, 10, 14, 6, 11, 13, 19, 16];

export const demoStats: StatsSnapshot = {
  started_at: before(3 * 24 * 60 + 8 * 60), uptime_seconds: 288_000, total_events: 12_486, handled_events: 3_218, error_events: 9,
  today_events: 1284, today_handled: 318, today_errors: 0, by_kind: { group: 1108, private: 176 },
  hourly: hourlyBuckets(1),
  avg_reply_ms: 5820, last_event_at: demoEvents[0].at,
  bot: { running: true, connected: true, self_id: "100000001", active_workers: 2, plugins_enabled: 7, plugins_total: 8, bridge_enabled: false, bridge_connected: false },
  by_profile: {
    "bot-onebot": {
      total_events: 9_642, handled_events: 2_480, error_events: 7,
      today_events: 968, today_handled: 241, today_errors: 0, by_kind: { group: 862, private: 106 },
      hourly: hourlyBuckets(0.75), avg_reply_ms: 5_240, last_event_at: demoEvents[0].at
    },
    "bot-telegram": {
      total_events: 2_844, handled_events: 738, error_events: 2,
      today_events: 316, today_handled: 77, today_errors: 0, by_kind: { group: 246, private: 70 },
      hourly: hourlyBuckets(0.25), avg_reply_ms: 7_480, last_event_at: demoEvents[0].at
    }
  }
};

export const demoStatus: BotStatus = {
  running: true,
  channel: { profile_id: "bot-onebot", platform: "onebot-v11", name: "Diana OneBot（演示）", connected: true, endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", self_id: "100000001", updated_at: before(1) },
  channels: [
    { profile_id: "bot-onebot", platform: "onebot-v11", name: "Diana OneBot（演示）", connected: true, endpoint: "ws://127.0.0.1:18080/onebot/v11/ws", self_id: "100000001", updated_at: before(1) },
    { profile_id: "bot-telegram", platform: "telegram", name: "Diana Telegram（演示）", connected: true, endpoint: "https://api.telegram.org", self_id: "@diana_demo_bot", updated_at: before(1) }
  ],
  nonebot_bridges: {}, plugins, recent_events: demoEvents, active_workers: 2,
  llm_concurrency: {
    active: 3, peak: 9,
    models: [
      { provider: "openai_compatible", model: "gpt-5.4-mini", active: 2, started_at: before(0.2) },
      { provider: "anthropic", model: "claude-sonnet-5", active: 1, started_at: before(0.6) }
    ]
  },
  llm_usage: {
    today: { calls: 412, input_tokens: 1_284_600, output_tokens: 96_420, cached_input_tokens: 742_180, total_tokens: 1_381_020, missing_usage_calls: 0 },
    session: { calls: 1_486, input_tokens: 4_612_880, output_tokens: 338_940, cached_input_tokens: 2_604_310, total_tokens: 4_951_820, missing_usage_calls: 3 }
  },
  updated_at: before(1)
};

let tasks: AssistantTask[] = [
  { id: "task-reminder-01", kind: "reminder", platform: "onebot-v11", owner_id: "100200301", user_id: "100200301", message: "15:30 提醒提交周报", status: "active", trigger_at: after(70), created_at: before(20), consumes_quota: true },
  { id: "task-trigger-05", kind: "event_trigger", platform: "onebot-v11", owner_id: "100200301", group_id: "100200301", user_id: "100200301", message: "该交周报了，别忘啦", status: "active", trigger_at: "0001-01-01T00:00:00Z", trigger: "群 100200301 里，100200488 说话时，发提醒，触发一次", trigger_action: "message", trigger_fire_count: 0, trigger_expires_at: after(60 * 24 * 7), created_at: before(45), consumes_quota: true },
  { id: "task-schedule-02", kind: "schedule", platform: "telegram", owner_id: "880024", user_id: "880024", message: "每天整理 AI 行业资讯并附来源", status: "active", trigger_at: after(180), interval_seconds: 86400, last_run_at: before(1260), created_at: before(4800), consumes_quota: true },
  { id: "task-repo-03", kind: "repository_watch", profile_id: "bot-onebot", platform: "onebot-v11", owner_id: "", group_id: "100200301", message: "Diana 仓库动态", status: "active", trigger_at: after(1), interval_seconds: 60, last_run_at: before(1), repository: "SuInk/Diana", repository_branch: "main", watch_commits: true, watch_pull_requests: true, watch_releases: true, watch_stars: true, last_commit_sha: "26ebc1bed07e9e5b", last_release_tag: "v0.8.6", last_star_count: 128, created_at: before(3800), consumes_quota: true },
  { id: "task-rss-04", kind: "rss_watch", platform: "telegram", profile_id: "bot-telegram", owner_id: "", user_id: "880024", message: "Diana Release Feed", status: "active", trigger_at: after(4), interval_seconds: 300, last_run_at: before(4), feed_url: "https://github.com/SuInk/Diana/releases.atom", feed_source: "rss", feed_sources: [{ feed_url: "https://github.com/SuInk/Diana/releases.atom", source: "rss", name: "Diana Release Feed" }], feed_judge_prompt: "仅在稳定版发布时提醒并总结更新点", last_feed_item_id: "tag:github.com,2008:Repository/", created_at: before(2200), consumes_quota: true }
];

// 与后端 SupportedPlatforms 注册表保持一致：配置向导和机器人页的平台下拉都
// 按它渲染，少一个平台，演示站就看不到那一套接入表单。
const platforms: BotPlatform[] = [
  { id: "onebot-v11", name: "QQ · OneBot v11", protocol: "onebot-v11", category: "qq", category_label: "QQ", description: "通过 Snowluma、NapCat 或 Lagrange 接入 OneBot v11。", inbound: "reverse_ws" },
  { id: "telegram", name: "Telegram Bot", protocol: "telegram-bot-api", category: "telegram", category_label: "Telegram", description: "通过 Telegram Bot API 长轮询接入。", inbound: "outbound", rich_text: true },
  { id: "qq-official", name: "QQ 官方机器人", protocol: "qq-official-gateway-ws", category: "qq_official", category_label: "QQ 官方机器人", description: "QQ 开放平台 WebSocket 网关，出站长连接，不需要公网地址", inbound: "outbound" },
  { id: "dingtalk", name: "钉钉", protocol: "dingtalk-stream-ws", category: "dingtalk", category_label: "钉钉", description: "Stream 模式出站长连接，不需要公网地址", inbound: "outbound", rich_text: true },
  { id: "feishu", name: "飞书", protocol: "feishu-event-callback", category: "feishu", category_label: "飞书", description: "事件订阅回调，需要一个公网可达的回调地址", inbound: "callback", callback_path: "/api/channels/feishu/callback", rich_text: true },
  { id: "wecom", name: "企业微信", protocol: "wecom-event-callback", category: "wecom", category_label: "企业微信", description: "应用回调，需要一个公网可达的回调地址", inbound: "callback", callback_path: "/api/channels/wecom/callback", rich_text: true }
];

type DemoIssueDraft = {
  id: string; platform: string; profile_id: string; group_id: string; repository: string;
  requester_id: string; requester_name: string; input: { title: string; body: string; labels: string[] };
  status: string; created_at: string; updated_at: string; expires_at: string; confirmation_code?: string;
};

// 待审批草稿 7 天过期；第二条已经过期，可以在后台还原、修改、提交或删除。
const issueDrafts: DemoIssueDraft[] = [{
  id: "draft-demo-01", platform: "onebot-v11", profile_id: "bot-main", group_id: "100200301",
  repository: "SuInk/Diana", requester_id: "100200711", requester_name: "青禾",
  input: { title: "事件图片改为页面内放大", body: "点击事件中的图片时，在当前页面打开查看器，不再跳转到新标签页。", labels: ["enhancement", "webui"] },
  status: "pending", created_at: before(16), updated_at: before(16), expires_at: before(-7 * 24 * 60 + 16)
}, {
  id: "draft-demo-02", platform: "onebot-v11", profile_id: "bot-main", group_id: "100200418",
  repository: "SuInk/Diana", requester_id: "100200842", requester_name: "岸芷",
  input: { title: "群公告支持定时发送", body: "希望能预约时间再发群公告，避免半夜打扰。", labels: ["enhancement"] },
  status: "pending", created_at: before(9 * 24 * 60), updated_at: before(9 * 24 * 60), expires_at: before(2 * 24 * 60)
}];

const dependencies: ResolverDependency[] = [
  { name: "ffmpeg", purpose: "媒体转码与时长检测", available: true, version: "7.1", path: "/usr/local/bin/ffmpeg", installable: true, installer: "系统包管理器" },
  { name: "yt-dlp", purpose: "视频地址解析", available: true, version: "2026.08.10", path: "/usr/local/bin/yt-dlp", installable: true, installer: "pipx" }
];

// 演示里故意让浏览器缺席：这一格就是要给人看「插件开着但其实跑不起来」长什么样。
const browserDependencies: ResolverDependency[] = [
  {
    name: "browser-renderer",
    purpose: "网页渲染：使用系统 Chromium / Google Chrome",
    available: false,
    detail: "没有找到 Chromium / Google Chrome",
    installable: true,
    installer: "系统包管理器"
  },
  { name: "cjk-font", purpose: "中文字体：关系图与中文截图", available: false, detail: "没有找到能画中文的字体文件", installable: true, installer: "Diana 下载 Noto Sans CJK SC（约 16 MiB）" }
];

const updateStatus: UpdateStatus = { root: "/opt/diana", head_commit: "26ebc1bed07e9e5b", head_subject: "真实 WebUI Pages 演示", dirty: false, update_available: true, restart_required: false, download_ready: false, last_fetched_at: before(4) };
let updatePolicy = { channel: "release", auto_download: true, auto_install: false, github_mirror: "direct" };
let demoUpdateTokenConfigured = false;

const logs: AppLogEntry[] = [
  { id: "log-1", kind: "operation", level: "info", action: "message_reply", message: "群聊消息已回复并收到发送回显", actor: "bot-onebot", target: "group:100200301", created_at: before(2) },
  { id: "log-2", kind: "operation", level: "info", action: "repository_watch", message: "仓库检查完成，未发现新 Commit 或 Release", actor: "scheduler", target: "SuInk/Diana", created_at: before(4) },
  { id: "log-3", kind: "operation", level: "info", action: "memory_compress", message: "已更新群聊压缩摘要与长期事实索引", actor: "memory", target: "group:100200418", created_at: before(16) }
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

// 档位演示数据：真实目录是每轮对话攒出来的，演示站没有对话，就按一份典型的目录
// 摆出来——两档的 token 差距照着线上量级给，不然这一页最要紧的那组数字失真。
const residencyEntries: AgentResidencyEntry[] = [
  {id: "tool:ai_image_detect", kind: "tool", name: "ai_image_detect", description: "检测一张聊天图片是不是 AI 生成的：解析图片里的 AI 生成标识（C2PA 内容凭证、IPTC 数字来源类型、Google SynthID）。", detail: "检测一张聊天图片是不是 AI 生成的：解析图片里的 AI 生成标识（C2PA 内容凭证、IPTC 数字来源类型、Google SynthID/［Made with Google AI］标注、国内 AIGC 隐式标识、Stable Diffusion 等常见生成器写进 EXIF 的参数），命中就直接给出来源；都没有只说「没查到标识」，不做画风猜测。input: {url?: string}", default: false, resident_tokens: 412, deferred_tokens: 44},
  {id: "tool:bot_config", kind: "tool", name: "bot_config", description: "读取或修改 Diana 的相关度、闲聊门槛及闲聊冷却。get 读取，update 局部修改。", detail: "读取或修改 Diana 的相关度、闲聊门槛及闲聊冷却。get 读取，update 局部修改；scope=group 只改当前群（主人或实时核验的群管理员），scope=bot 仅主人修改当前机器人。关闭所有主动接话同时设置 relevance=0 与 idle_chat=off。input: {action: \"get\"|\"update\", scope?: \"group\"|\"bot\"}", default: true, resident_tokens: 588, deferred_tokens: 38},
  {id: "tool:browser_click", kind: "tool", name: "browser_click", description: "点击当前页面中的元素。", detail: "点击当前页面中的元素。input: {selector: string}", default: false, resident_tokens: 96, deferred_tokens: 14},
  {id: "tool:web_search", kind: "tool", name: "web_search", description: "联网搜索，返回标题、摘要和链接。", detail: "联网搜索，返回标题、摘要和链接。input: {query: string, limit?: number}", default: true, resident_tokens: 168, deferred_tokens: 18},
  {id: "official.sandboxed-browser-renderer", kind: "plugin", name: "浏览器渲染", description: "把网页渲染成图片交给模型看，跑在沙盒里。", detail: "把网页渲染成图片交给模型看，跑在沙盒里。", tools: ["browser_render", "browser_open", "browser_text"], default: true, resident_tokens: 486, deferred_tokens: 58},
  {id: "tool:browser_render", kind: "tool", parent: "official.sandboxed-browser-renderer", name: "browser_render", description: "渲染网页为图片。", detail: "渲染网页为图片。input: {url: string, full_page?: boolean}", default: true, resident_tokens: 280, deferred_tokens: 20},
  {id: "tool:browser_open", kind: "tool", parent: "official.sandboxed-browser-renderer", name: "browser_open", description: "通过 Chrome DevTools Protocol 打开网页。需要 Chrome 启用 remote debugging。", detail: "通过 Chrome DevTools Protocol 打开网页。需要 Chrome 启用 remote debugging。input: {url: string}", default: false, resident_tokens: 132, deferred_tokens: 26},
  {id: "tool:browser_text", kind: "tool", parent: "official.sandboxed-browser-renderer", name: "browser_text", description: "读取当前浏览器页面文本。", detail: "读取当前浏览器页面文本。input: {}", default: false, resident_tokens: 74, deferred_tokens: 12},
  {id: "official.music", kind: "plugin", name: "音乐增强", description: "群里丢来的歌变成语音，也支持点歌。", detail: "群里丢来的歌变成语音，也支持点歌。", tools: ["music"], default: false, resident_tokens: 356, deferred_tokens: 34},
  {id: "tool:music", kind: "tool", parent: "official.music", name: "music", description: "按描述点一首歌并发成语音。", detail: "按描述点一首歌并发成语音。input: {query: string}", default: false, resident_tokens: 356, deferred_tokens: 34},
  {id: "mcp:gitea", kind: "mcp", name: "gitea", description: "自建 Gitea 的仓库、议题和合并请求。", detail: "自建 Gitea 的仓库、议题和合并请求。", tools: ["gitea_issue", "gitea_repo", "gitea_pull"], default: false, resident_tokens: 1_240, deferred_tokens: 96},
  {id: "tool:gitea_issue", kind: "tool", parent: "mcp:gitea", name: "gitea_issue", description: "读写议题。", detail: "读写议题。input: {repo: string, action: string}", default: false, resident_tokens: 520, deferred_tokens: 32},
  {id: "tool:gitea_repo", kind: "tool", parent: "mcp:gitea", name: "gitea_repo", description: "仓库信息与文件读取。", detail: "仓库信息与文件读取。input: {repo: string}", default: false, resident_tokens: 410, deferred_tokens: 32},
  {id: "tool:gitea_pull", kind: "tool", parent: "mcp:gitea", name: "gitea_pull", description: "合并请求的列表与详情。", detail: "合并请求的列表与详情。input: {repo: string}", default: false, resident_tokens: 310, deferred_tokens: 32},
];

// 演示数据从「还没列过名单」开始，跟着内置推荐走——新装的机器人就是这个样子。
let residencyListed = false;

// 浏览器来源在演示里从「Diana 内置」开始：它是推荐的那个。扩展那边的配置照样
// 预填好，切过去就能看到授权边界长什么样。
let demoBrowserBoxSettings: BrowserBoxSettings = { enabled: true, headful: true };
let demoBrowserSourceOrder: ("box" | "extension")[] = ["box", "extension"];
let demoBrowserControlPolicy = {
  enabled: false,
  allowed_origins: ["chrome-extension://abcdefghijklmnopabcdefghijklmnop"],
  allowed_hosts: ["example.com", "*.wiki.example.com"],
  denied_hosts: ["admin.example.com"],
  write_enabled: false,
  command_timeout_ms: 20_000,
  commands_per_minute: 60
};
let demoBrowserControlTokens: BrowserControlToken[] = [
  { id: "bct-demo", name: "演示台式机 Chrome", prefix: "dianabx_demo0000", extension_id: "abcdefghijklmnopabcdefghijklmnop", created_at: before(1440), last_used_at: before(2) }
];
let demoApiKeys: OpenAPIKey[] = [
  { id: "key-1", name: "ci-notify", prefix: "diana_3fa8c2e1", created_at: before(4320), last_used_at: before(35) }
];

let demoMediaCachePolicy = { retention_days: 7, max_mb: 0 };

let demoMediaBaseURL = { base_url: "", source: "auto" };

async function demoFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const url = new URL(raw, window.location.origin);
  if (!url.pathname.startsWith("/api/") && !url.pathname.startsWith("/onebot/")) return window.__dianaOriginalFetch!(input, init);
  await new Promise((resolve) => window.setTimeout(resolve, Math.max(60, Number(import.meta.env.VITE_DEMO_LATENCY_MS) || 60)));
  const method = (init?.method ?? "GET").toUpperCase();
  const body = bodyOf(init);
  const path = url.pathname;

  if (path === "/api/system/media-cache") {
    if (method === "POST") {
      demoMediaCachePolicy = { retention_days: Number(body.retention_days), max_mb: Number(body.max_mb) };
    }
    return json(demoMediaCachePolicy);
  }

  // 演示模式给一块 512 GiB 的盘和一份典型占用，图片/视频最大——真实部署里
  // 吃掉数据目录的基本就是历史媒体原件。
  if (path === "/api/system/storage") {
    return json({
      collected_at: new Date().toISOString(),
      path: "/app/data",
      disk_total_bytes: 549755813888,
      disk_used_bytes: 236223201280,
      disk_free_bytes: 313532612608,
      disk_usage_percent: 43,
      diana_bytes: 9663676416,
      diana_files: 48213,
      categories: [
        { key: "video", label: "视频", bytes: 5368709120, files: 612 },
        { key: "image", label: "图片", bytes: 3221225472, files: 45230 },
        { key: "database", label: "数据库", bytes: 704643072, files: 3 },
        { key: "audio", label: "音频", bytes: 268435456, files: 2180 },
        { key: "document", label: "文档与压缩包", bytes: 83886080, files: 164 },
        { key: "other", label: "其它文件", bytes: 16777216, files: 24 }
      ],
      scanned_at: new Date().toISOString(),
      scanning: false
    });
  }

  if (path === "/api/system/media-base-url") {
    if (method === "POST") {
      demoMediaBaseURL = { base_url: String(body.base_url ?? ""), source: "database" };
    }
    return json(demoMediaBaseURL);
  }

  if (path === "/api/auth/status") return json({ auth_required: true, authenticated: true, username: "demo" });
  // 演示里也给几条会话：这张卡原本永远停在「加载中…」，看不出真实排版。
  if (path === "/api/auth/sessions")
    return json({
      sessions: [
        { id: "sess-1", device_name: "MacBook Pro · Chrome", user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/139", ip_address: "192.168.1.24", created_at: before(180), last_seen_at: before(2), expires_at: before(-10080), current: true },
        { id: "sess-2", device_name: "iPhone · Safari", user_agent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_2 like Mac OS X) Safari/605.1.15", ip_address: "192.168.1.31", created_at: before(2880), last_seen_at: before(420), expires_at: before(-4320), current: false }
      ]
    });
  if (path.startsWith("/api/auth/")) return json({ ok: true, username: "demo" });
  if (path === "/api/openapi/keys" && method === "GET") return json({ keys: demoApiKeys });
  if (path === "/api/openapi/keys" && method === "POST") {
    const key: OpenAPIKey = { id: `key-${Date.now()}`, name: String(body.name ?? "未命名"), prefix: "diana_demo0000", created_at: new Date().toISOString() };
    demoApiKeys = [key, ...demoApiKeys];
    return json({ key, token: "diana_demo00000000000000000000000000000000000000000000000000000000000000" });
  }
  if (path.startsWith("/api/openapi/keys/") && method === "DELETE") {
    const keyID = decodeURIComponent(path.split("/").pop() ?? "");
    demoApiKeys = demoApiKeys.filter((item) => item.id !== keyID);
    return json({ revoked: true });
  }
  // 浏览器来源：演示里内置浏览器找得到 Chrome，扩展有一条连着（见下面的 connections）。
  const demoBrowserSourceState = () => {
    const box = {
      enabled: Boolean(demoBrowserBoxSettings.enabled),
      usable: Boolean(demoBrowserBoxSettings.enabled),
      detected: true,
      dependencies: [
        { name: "browser-renderer", purpose: "网页渲染：使用系统 Chromium / Google Chrome", available: true, version: "Chromium 141", installable: true },
        { name: "cjk-font", purpose: "中文字体：关系图与中文截图", available: false, detail: "没有找到能画中文的字体文件", installable: true, installer: "apt-get" },
        { name: "display", purpose: "开真窗口：图形会话或 Xvfb 虚拟屏（可选）", available: true, version: "Xvfb 虚拟屏", installable: false }
      ]
    };
    const extension = {
      enabled: demoBrowserControlPolicy.enabled,
      usable: demoBrowserControlPolicy.enabled,
      detected: true,
      dependencies: [
        { name: "browser-extension", purpose: "Diana 浏览器控制扩展：装在你的 Chrome 里，反向连到这里", available: true, version: "Chromium 141", installable: false }
      ]
    };
    const active = demoBrowserSourceOrder.find((key) => (key === "box" ? box : extension).usable) ?? "off";
    return { order: demoBrowserSourceOrder, active, box, extension };
  };
  if (path === "/api/browser-source" && method === "GET") return json(demoBrowserSourceState());
  if (path === "/api/browser-source" && method === "PUT") {
    if (typeof body.box_enabled === "boolean") demoBrowserBoxSettings = { ...demoBrowserBoxSettings, enabled: body.box_enabled };
    if (typeof body.extension_enabled === "boolean") demoBrowserControlPolicy = { ...demoBrowserControlPolicy, enabled: body.extension_enabled };
    if (Array.isArray(body.order)) demoBrowserSourceOrder = body.order as ("box" | "extension")[];
    return json(demoBrowserSourceState());
  }
  // 内置浏览器在演示里不起进程：开着但没在跑，画面那块不会去连实时流。每台机器人
  // 各有一份登录态目录，和真实后端一样按 ?bot= 区分。
  const demoBrowserBoxStatus = () => {
    const bot = url.searchParams.get("bot") ?? "";
    return {
      settings: demoBrowserBoxSettings,
      running: false,
      takeover: false,
      available: true,
      ...(bot ? { bot, profile_dir: `/data/browser-box/profiles/${bot}/profile` } : {})
    };
  };
  if (path === "/api/browser-box/status" && method === "GET") return json(demoBrowserBoxStatus());
  // 打开浏览器页会自动拉起；演示里不起进程，照实回「没在跑」，别让页面弹错。
  if (path === "/api/browser-box/start" && method === "POST") return json({ status: demoBrowserBoxStatus() });
  if (path === "/api/browser-box/settings" && method === "PUT") {
    demoBrowserBoxSettings = { ...demoBrowserBoxSettings, ...(body as unknown as BrowserBoxSettings) };
    return json({ settings: demoBrowserBoxSettings, status: demoBrowserBoxStatus() });
  }
  // 浏览器控制：演示里给一条已连接的扩展和一把令牌，否则这一页全是空状态，
  // 看不出授权边界长什么样。写操作在演示里始终关着。
  if (path === "/api/browser-control/status" && method === "GET")
    return json({
      policy: demoBrowserControlPolicy,
      tokens: demoBrowserControlTokens,
      connections: [
        {
          id: "bc-demo-1",
          token_id: demoBrowserControlTokens[0]?.id ?? "bct-demo",
          token_name: demoBrowserControlTokens[0]?.name ?? "演示浏览器",
          extension_id: "abcdefghijklmnopabcdefghijklmnop",
          extension_name: "Diana 浏览器控制",
          browser: "Chromium",
          browser_version: "141",
          label: "演示台式机 Chrome",
          connected_at: before(30),
          last_seen_at: before(1),
          takeover: false,
          allowed_tabs: 2,
          commands: 7
        }
      ],
      ready: demoBrowserControlPolicy.enabled,
      endpoint: "/browser-control/v1/socket",
      protocol: 1
    });
  if (path === "/api/browser-control/policy" && method === "PUT") {
    demoBrowserControlPolicy = { ...demoBrowserControlPolicy, ...(body as unknown as typeof demoBrowserControlPolicy) };
    return json({ policy: demoBrowserControlPolicy });
  }
  if (path === "/api/browser-control/tokens" && method === "GET") return json({ tokens: demoBrowserControlTokens });
  if (path === "/api/browser-control/tokens" && method === "POST") {
    const token = {
      id: `bct-${Date.now()}`,
      name: String(body.name ?? "未命名"),
      prefix: "dianabx_demo0000",
      created_at: new Date().toISOString()
    };
    demoBrowserControlTokens = [token, ...demoBrowserControlTokens];
    return json({ token, plaintext: "dianabx_demo000000000000000000000000000000000000000000000000000000000000" });
  }
  if (path.startsWith("/api/browser-control/tokens/") && method === "DELETE") {
    const tokenID = decodeURIComponent(path.split("/").pop() ?? "");
    const removed = demoBrowserControlTokens.find((item) => item.id === tokenID);
    demoBrowserControlTokens = demoBrowserControlTokens.filter((item) => item.id !== tokenID);
    return json({ token: removed ?? { id: tokenID, name: "", prefix: "", created_at: new Date().toISOString() } });
  }
  if (path.startsWith("/api/browser-control/connections/")) return json({ ok: true, active: Boolean(body.active) });
  if (path === "/api/health") return json({ status: "ok", started_at: demoStats.started_at, uptime_seconds: demoStats.uptime_seconds, version: "v0.8.6-demo", repository: "SuInk/Diana", repository_url: "https://github.com/SuInk/Diana" });
  if (path === "/api/stats") return json(demoStats);
  // 三个窗口互相包含（1h ⊂ 12h ⊂ 24h），演示数据也照这个关系给，不然切来切去数字会倒挂。
  if (path === "/api/stats/ranges") {
    const until = new Date().toISOString();
    const ranges = [
      { id: "1h", minutes: 60, messages: 41, handled: 33, errors: 0, avg_reply_ms: 4_820, replies_measured: 33, calls: 37, input: 118_420, output: 9_260, total: 127_680, cached: 68_310 },
      { id: "12h", minutes: 720, messages: 486, handled: 372, errors: 3, avg_reply_ms: 5_140, replies_measured: 372, calls: 296, input: 921_540, output: 71_880, total: 993_420, cached: 534_260 },
      { id: "24h", minutes: 1440, messages: 908, handled: 694, errors: 5, avg_reply_ms: 5_260, replies_measured: 694, calls: 508, input: 1_602_310, output: 124_970, total: 1_727_280, cached: 928_640 }
    ].map((entry) => ({
      id: entry.id,
      since: before(entry.minutes),
      until,
      messages: entry.messages,
      handled: entry.handled,
      errors: entry.errors,
      avg_reply_ms: entry.avg_reply_ms,
      replies_measured: entry.replies_measured,
      usage: {
        since: before(entry.minutes),
        until,
        recorded_calls: entry.calls,
        input_tokens: entry.input,
        output_tokens: entry.output,
        total_tokens: entry.total,
        cached_input_tokens: entry.cached
      }
    }));
    return json({ until, ranges });
  }

  // 授权登录：演示模式给出内置提供商的未登录状态，登录流程本身不模拟——
  // 真去打一次 OAuth 授权页在演示环境里既做不到也不该做。
  if (path.startsWith("/api/llm/oauth/")) {
    return json({
      providers: [
        {
          provider: {
            key: "openrouter",
            label: "OpenRouter",
            authorize_url: "https://openrouter.ai/auth",
            token_url: "https://openrouter.ai/api/v1/auth/keys",
            use_pkce: true,
            built_in: true,
            notes: "OpenRouter 的 PKCE 授权本就是给第三方应用用的，换到的是一把归你所有、可随时吊销的 Key。"
          },
          logged_in: false
        }
      ]
    });
  }
  if (path === "/api/llm/config/export") return json(llmConfig);
  if (path === "/api/llm/config" && method === "GET") return json(llmConfig);
  if (path === "/api/llm/config" && method === "POST") {
    const incoming = body as unknown as LLMConfig;
    const profiles = [...(llmConfig.profiles ?? [])];
    const saved = { ...incoming, id: incoming.id || `llm-${Date.now()}`, api_key_configured: true, models: incoming.models?.length ? incoming.models : modelCatalog };
    const index = profiles.findIndex((profile) => profile.id === saved.id);
    if (index >= 0) profiles[index] = saved; else profiles.push(saved);
    llmConfig = { ...llmConfig, profiles };
    return json(llmConfig);
  }
  const llmAction = path.match(/^\/api\/llm\/config\/(activate|clone|delete|reorder)$/)?.[1];
  if (llmAction) return json(mutateLLM(llmAction, body));
  if (path === "/api/llm/config/import") { llmConfig = { ...llmConfig, ...(body as unknown as Partial<LLMConfig>) }; return json(llmConfig); }
  if (path === "/api/llm/models") return json({ models: modelCatalog });
  if (path === "/api/llm/test") {
    if (body.mode === "image") {
      const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024"><rect width="1024" height="1024" fill="#20242a"/><circle cx="512" cy="430" r="230" fill="#c44c7d"/><path d="M330 360 390 170 475 340M549 340 635 170 695 360" fill="#c44c7d"/><circle cx="440" cy="430" r="22" fill="#fff"/><circle cx="584" cy="430" r="22" fill="#fff"/><path d="M430 560 Q512 620 594 560" fill="none" stroke="#fff" stroke-width="18" stroke-linecap="round"/><text x="512" y="850" text-anchor="middle" fill="#fff" font-family="sans-serif" font-size="42">Diana 生图测试 · 模拟结果</text></svg>`;
      return json({ provider: "openai_compatible", model: "gpt-image-2", images: [`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`] });
    }
    return json({ provider: "openai_compatible", model: String(body.model ?? "gpt-5.6"), text: "模型测试通过。这是 Pages 演示模式返回的模拟结果，不会消耗真实 Token。", usage: { input_tokens: 36, output_tokens: 24, total_tokens: 60 } });
  }

  if (path === "/api/assistant/stickers") {
    const q = (url.searchParams.get("q") ?? "").trim();
    const items = demoStickers.filter((item) => !q || item.summary.includes(q) || (item.description ?? "").includes(q));
    const offset = Number(url.searchParams.get("offset") ?? 0);
    const limit = Number(url.searchParams.get("limit") ?? 48);
    return json({ items: items.slice(offset, offset + limit), total: items.length });
  }
  if (path === "/api/assistant/platforms") return json({ platforms });
  if (path === "/api/assistant/prompts") return json(demoPromptCatalog);
  if (path === "/api/assistant/prompts/export" && method === "POST") return json({ yaml: demoRenderPromptFile((body.overrides as Record<string, string>) ?? {}) });
  if (path === "/api/assistant/prompts/import" && method === "POST") {
    const result = demoParsePromptFile(String(body.source ?? ""));
    return "error" in result ? json(result, 400) : json(result);
  }
  if (path === "/api/assistant/agent-defaults")
    return json({
      agent_command_allowlist: ["uptime", "free", "df", "uname", "nproc", "date", "hostname", "whoami"],
      agent_file_write_enabled: true,
      agent_command_sandbox: "auto",
      agent_max_steps: 8,
      agent_command_timeout_ms: 10000
    });
  if (path === "/api/assistant/config/defaults" && method === "GET") return json({
    platform: url.searchParams.get("platform") || "onebot-v11", enabled: true, owner_login_enabled: true,
    onebot_transport: "reverse_ws", onebot_reverse_ws_endpoint: "ws://127.0.0.1:18080/onebot/v11/ws",
    group_triggers: ["Diana", "diana"], request_timeout_ms: 60000
  });
  if (path === "/api/assistant/config" && method === "GET") return json(assistantConfig);
  if (["/api/assistant/config", "/api/assistant/config/new"].includes(path) && method === "POST") {
    const incoming = body as unknown as BotProfileConfig;
    const profiles = [...(assistantConfig.profiles ?? [])];
    const saved = { ...incoming, id: (path === "/api/assistant/config/new" ? undefined : incoming.id) || `bot-${Date.now()}` };
    const index = profiles.findIndex((profile) => profile.id === saved.id);
    if (index >= 0) profiles[index] = saved; else profiles.push(saved);
    assistantConfig = { ...assistantConfig, ...saved, profiles };
    return json(assistantConfig);
  }
  if (path === "/api/assistant/config/message-relays") { assistantConfig.message_relays = Array.isArray(body.relays) ? body.relays : []; return json(assistantConfig); }
  if (path === "/api/assistant/config/profile-enabled") {
    const profiles = [...(assistantConfig.profiles ?? [])];
    const index = profiles.findIndex((profile) => profile.id === body.profile_id);
    if (index >= 0) { profiles[index] = { ...profiles[index], enabled: body.enabled !== false }; assistantConfig = { ...assistantConfig, profiles }; }
    return json(assistantConfig);
  }
  if (path === "/api/assistant/config/profiles-enabled") {
    assistantConfig = { ...assistantConfig, profiles: (assistantConfig.profiles ?? []).map((profile) => ({ ...profile, enabled: body.enabled !== false })) };
    return json(assistantConfig);
  }
  if (path.startsWith("/api/assistant/config/") && method === "POST") return json(assistantConfig);
  if (path === "/api/assistant/status") return json(demoStatus);
  if (path === "/api/assistant/start") { demoStatus.running = true; return json(demoStatus); }
  if (path === "/api/assistant/stop") { demoStatus.running = false; return json(demoStatus); }
  if (path === "/api/assistant/features") return json({ group_test: true });
  // 回补在演示里不做任何事，但要有个回应：按钮从机器人配置挪到运行记录之后，
  // 演示站点一点它就弹「未授权」，看起来像是这一页坏了。
  if (path === "/api/assistant/backfill") return json({ requested: true, window_hours: 24 });
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
  if (path === "/api/assistant/agent-residency") {
    if (method === "POST") {
      if (body.reset) {
        residencyListed = false;
        for (const entry of residencyEntries) delete entry.resident;
      } else if (Array.isArray(body.ids)) {
        // 和后端同一个口径：只有名单成员写 resident，其余留空。「不在名单里」由
        // listed 这个标志推出来，不是给每一项写一个 false。
        const listed = new Set(body.ids.map(String));
        residencyListed = true;
        for (const entry of residencyEntries) {
          if (listed.has(entry.id)) entry.resident = true;
          else delete entry.resident;
        }
      } else {
        const entry = residencyEntries.find((item) => item.id === String(body.id ?? ""));
        if (entry) entry.resident = Boolean(body.resident);
      }
      return json({ ok: true });
    }
    return json({ items: residencyEntries, listed: residencyListed });
  }
  if (path === "/api/assistant/extensions") {
    try { return json(extensionDemoResponse(method,url.searchParams.get('profile')||'',body)); }
    catch(error) { return json({error:error instanceof Error?error.message:String(error)},400); }
  }
  if (path === "/api/assistant/plugins") {
    const profile = url.searchParams.get("profile") ?? "";
    return json(plugins.filter((plugin) => !profile || plugin.manifest.id !== "official.open-api").map((plugin) => demoPluginForProfile(plugin, profile)));
  }
  if (path === "/api/assistant/plugins/repository-publish/drafts") {
    const status = url.searchParams.get("status") ?? "all";
    const expired = (draft: DemoIssueDraft) => new Date(draft.expires_at).getTime() < Date.now();
    if (status === "expired") return json({ drafts: issueDrafts.filter(expired) });
    if (status === "pending") return json({ drafts: issueDrafts.filter((draft) => draft.status === "pending" && !expired(draft)) });
    return json({ drafts: status === "all" ? issueDrafts : issueDrafts.filter((draft) => draft.status === status) });
  }
  if (/^\/api\/assistant\/plugins\/repository-publish\/drafts\/[^/]+$/.test(path) && (method === "PATCH" || method === "DELETE")) {
    const segments = path.split("/");
    const index = issueDrafts.findIndex((item) => item.id === segments[segments.length - 1]);
    if (index < 0) return json({ error: "草稿不存在" }, 400);
    if (method === "DELETE") {
      issueDrafts.splice(index, 1);
      return json({ deleted: true });
    }
    const draft = issueDrafts[index];
    draft.input = {
      title: String(body.title ?? draft.input.title),
      body: String(body.body ?? draft.input.body),
      labels: Array.isArray(body.labels) ? (body.labels as string[]) : draft.input.labels
    };
    draft.updated_at = before(0);
    return json({ draft });
  }
  if (/^\/api\/assistant\/plugins\/repository-publish\/drafts\/[^/]+\/restore$/.test(path) && method === "POST") {
    const segments = path.split("/");
    const draft = issueDrafts.find((item) => item.id === segments[segments.length - 2]);
    if (!draft) return json({ error: "草稿不存在" }, 400);
    draft.status = "pending";
    draft.expires_at = before(-7 * 24 * 60);
    draft.updated_at = before(0);
    draft.confirmation_code = Math.floor(Math.random() * 0xffffff).toString(16).padStart(6, "0");
    return json({ draft });
  }
  if (/^\/api\/assistant\/plugins\/repository-publish\/drafts\/[^/]+\/publish$/.test(path) && method === "POST") {
    const segments = path.split("/");
    const draft = issueDrafts.find((item) => item.id === segments[segments.length - 2]);
    if (!draft) return json({ error: "草稿不存在" }, 400);
    draft.status = "created";
    draft.updated_at = before(0);
    return json({ ok: true, outcome: "created", repository: draft.repository, message: "Issue 已创建。", issue: { number: 618, title: draft.input.title, url: "https://github.com/SuInk/Diana/issues/618", state: "open" } });
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
  if (path === "/api/assistant/plugins/repo/preview" && method === "POST") {
    if (!String(body.url ?? "").includes("github.com")) return json({ error: "只支持 github.com 仓库链接" }, 400);
    return json(demoRepoPluginPreview);
  }
  if (path === "/api/assistant/plugins/repo/install" && method === "POST") {
    if (!body.accept_risk) return json({ error: "未确认安装风险" }, 400);
    let plugin = plugins.find((item) => item.manifest.id === demoRepoPlugin.manifest.id);
    if (!plugin) {
      plugin = { ...demoRepoPlugin, profile_enabled: Object.fromEntries((assistantConfig.profiles ?? []).map((profile) => [profile.id!, true])) };
      plugins = [...plugins, plugin];
      demoStatus.plugins = plugins;
    }
    plugin.installed = true;
    plugin.repo_source = demoRepoPluginSource;
    return json(demoPluginForProfile(plugin, ""));
  }
  if (path.startsWith("/api/assistant/plugins/repo/update/") && method === "POST") {
    const id = decodeURIComponent(path.slice("/api/assistant/plugins/repo/update/".length));
    const plugin = plugins.find((item) => item.manifest.id === id);
    if (!plugin) return json({ error: "演示插件不存在或不是仓库插件" }, 404);
    if (!body.accept_risk) return json({ error: "未确认安装风险" }, 400);
    return json(demoPluginForProfile(plugin, ""));
  }
  if (path === "/api/assistant/plugins/resolver/test" && method === "POST") {
    return json({
      credentials: [
        { key: "bili_sessdata", label: "B 站 SESSDATA", configured: true, state: "valid", account: "演示账号", message: "已登录，大会员" },
        { key: "douyin_cookie", label: "抖音 Cookie", configured: false, state: "unconfigured", message: "未填写" },
        {
          key: "xhs_cookie",
          label: "小红书 Cookie",
          configured: true,
          state: "invalid",
          message: "小红书说这份 Cookie 没有登录，需要重新复制。Cookie 里没有 web_session：请从开发者工具 Network 面板任意请求的 Cookie 请求头整段复制，document.cookie 拿不到它。"
        },
        { key: "ytdlp_cookies_path", label: "yt-dlp Cookie 文件", configured: true, state: "unverified", message: "文件可读，是 Netscape 格式；是否仍在登录状态要等 yt-dlp 实际下载时才知道。" }
      ]
    });
  }
  if (path === "/api/assistant/plugins/music/test" && method === "POST") {
    return json({
      sources: [
        {
          source: "netease", label: "网易云音乐", search_ok: true, playable: true, api_configured: false, cookie_configured: true,
          login: { key: "netease_cookie", label: "网易云 MUSIC_U", configured: true, state: "valid", account: "云村村民", message: "已登录，会员账号" },
          message: "搜索与播放地址获取正常"
        },
        {
          source: "qq", label: "QQ 音乐", search_ok: true, playable: false, api_configured: false, cookie_configured: true,
          login: { key: "qq_cookie", label: "QQ 音乐 Cookie", configured: true, state: "invalid", message: "QQ 音乐说这份 Cookie 没有登录或已过期，需要重新复制；Cookie 里应当有 qqmusic_key 或 qm_keyst。" },
          message: "搜索正常，但登录态无效，取不到播放地址"
        },
        { source: "kugou", label: "酷狗音乐", search_ok: true, playable: true, api_configured: false, cookie_configured: false, message: "搜索与播放地址获取正常" }
      ]
    });
  }
  const pluginMatch = path.match(/^\/api\/assistant\/plugins\/([^/]+)\/(install|uninstall|enabled|settings)$/);
  if (pluginMatch) {
    const plugin = plugins.find((item) => item.manifest.id === decodeURIComponent(pluginMatch[1]));
    if (!plugin) return json({ error: "演示插件不存在" }, 404);
    if (pluginMatch[2] === "enabled" && plugin.manifest.id !== "official.open-api" && !url.searchParams.get("profile")) return json({ error: "请选择具体机器人" }, 400);
    if (body.inherit) return json({ error: "插件配置不再支持继承" }, 400);
    if (pluginMatch[2] === "install") plugin.installed = true;
    if (pluginMatch[2] === "uninstall") { plugin.installed = false; plugin.enabled = false; delete plugin.repo_source; }
    if (pluginMatch[2] === "enabled") {
      const profile = url.searchParams.get("profile") ?? "";
      if (profile && plugin.manifest.id !== "official.open-api") {
        plugin.profile_enabled = { ...plugin.profile_enabled, [profile]: Boolean(body.enabled) };
      } else {
        plugin.enabled = Boolean(body.enabled);
      }
    }
    const profile = url.searchParams.get("profile") ?? "";
    if (pluginMatch[2] === "settings") {
      {
        const previous = plugin.settings ?? {};
        const next = { ...((body.settings as Record<string, unknown>) ?? {}) };
        const cleared = new Set((body.clear_secrets as string[]) ?? []);
        for (const spec of plugin.manifest.settings ?? []) if (spec.secret) {
          if (cleared.has(spec.key)) delete next[spec.key];
          else if (!next[spec.key] && previous[spec.key]) next[spec.key] = previous[spec.key];
        }
        plugin.settings = next;
      }
    }
    plugins = [...plugins]; demoStatus.plugins = plugins; return json(demoPluginForProfile(plugin, profile));
  }

  if (path === "/api/assistant/groups" && method === "GET") return json({ groups, plugins, live_available: true, quota_window_seconds: 5 * 3600 });
  if (path === "/api/assistant/groups" && method === "POST") {
    const config = body.config as BotGroupSummary;
    const index = groups.findIndex((group) => group.group_id === config.group_id);
    if (index >= 0) groups[index] = { ...groups[index], ...config, natural_reply_split_enabled: config.natural_reply_split_enabled, reply_preserve_line_breaks: config.reply_preserve_line_breaks, reply_line_split_enabled: config.reply_line_split_enabled, typing_delay_enabled: config.typing_delay_enabled, configured: true, joined: true }; else groups.push({ ...config, configured: true, joined: false });
    return json({ config });
  }

  if (path === "/api/assistant/groups/switches" && method === "POST") {
    if (typeof body.min_group_level === "number" || typeof body.level_unknown_policy === "string") {
      const gate = { ...(assistantConfig.reply_gate ?? {}) };
      if (typeof body.min_group_level === "number") gate.min_group_level = body.min_group_level;
      if (typeof body.level_unknown_policy === "string") gate.level_unknown_policy = body.level_unknown_policy as "allow" | "deny";
      assistantConfig = { ...assistantConfig, reply_gate: gate };
    }
    if (typeof body.new_group_enabled === "boolean") {
      const mode = body.new_group_enabled ? "blacklist" : "whitelist";
      assistantConfig = { ...assistantConfig, group_admission: { mode } };
    }
    let updated = 0;
    if (typeof body.enabled === "boolean") {
      const wanted = new Set((body.group_ids as string[] | undefined) ?? []);
      for (const group of groups) {
        if (!wanted.has(group.group_id) || group.enabled === body.enabled) continue;
        group.enabled = body.enabled as boolean;
        group.configured = true;
        updated += 1;
      }
    }
    return json({ ok: true, updated });
  }

  if (path === "/api/assistant/favorability/evaluations") {
    const statuses = (url.searchParams.get("status") ?? "").split(",").filter(Boolean);
    const statusGiven = url.searchParams.has("status");
    const fieldsGiven = url.searchParams.has("portrait_field");
    const userID = url.searchParams.get("user_id") ?? "";
    const search = (url.searchParams.get("q") ?? "").trim();
    const person = (url.searchParams.get("person") ?? "").trim();
    const since = Number(url.searchParams.get("since") ?? 0) * 1000;
    const searchable = (item: RelationshipEvaluation) => [item.user_id, item.sender_name, item.group_id, item.message_text, item.reason,
      item.model, item.error, ...(item.portrait ?? []).flatMap((trait) => [trait.label, trait.value])].join("\n");
    const groupID = url.searchParams.get("group_id") ?? "";
    const portraitOnly = url.searchParams.get("portrait") === "1";
    const hasPortrait = (item: RelationshipEvaluation) => (item.portrait?.length ?? 0) > 0;
    const direction = url.searchParams.get("direction") ?? "";
    const chat = url.searchParams.get("chat") ?? "";
    const fields = (url.searchParams.get("portrait_field") ?? "").split(",").filter(Boolean);
    const source = url.searchParams.get("portrait_source") ?? "";
    const minConfidence = Number(url.searchParams.get("min_confidence") ?? 0);
    const model = url.searchParams.get("model") ?? "";
    const directionMatches = (delta: number) =>
      !direction || (direction === "up" && delta > 0) || (direction === "down" && delta < 0) ||
      (direction === "changed" && delta !== 0) || (direction === "none" && delta === 0);
    const evaluations = demoRelationshipEvaluations.filter((item) =>
      (!statusGiven || statuses.includes(item.status)) &&
      (!userID || item.user_id === userID) &&
      (!search || searchable(item).includes(search)) &&
      (!person || item.user_id.includes(person) || (item.sender_name ?? "").includes(person)) &&
      (!since || Date.parse(item.created_at) >= since) &&
      (!groupID || item.group_id === groupID) &&
      (!portraitOnly || hasPortrait(item)) &&
      directionMatches(item.applied_delta) &&
      (!chat || (chat === "group") === Boolean(item.group_id)) &&
      (!fieldsGiven || !hasPortrait(item) || (item.portrait ?? []).some((trait) => fields.includes(trait.field))) &&
      (!source || (item.portrait ?? []).some((trait) => trait.source === source)) &&
      item.confidence >= minConfidence &&
      (!model || (item.model ?? "").includes(model)));
    const portraitFields = [
      { field: "residence", label: "居住地点" }, { field: "occupation", label: "职业" }, { field: "routine", label: "作息" },
      { field: "habit", label: "生活习惯" }, { field: "interest", label: "兴趣爱好" }, { field: "relation", label: "家庭与关系" },
      { field: "timezone", label: "时区" }, { field: "other", label: "其他" }
    ];
    return json({ evaluations, portrait_fields: portraitFields });
  }

  if (path === "/api/assistant/users") {
    const keyword = (url.searchParams.get("q") ?? "").trim();
    const matched = demoUsers.filter((user) => !keyword || user.user_id.includes(keyword) || (user.display_name ?? "").includes(keyword));
    const sort = url.searchParams.get("sort") ?? "updated";
    const order = url.searchParams.get("order") === "asc" ? "asc" : "desc";
    const sortKeys: Record<string, (user: (typeof demoUsers)[number]) => number> = {
      updated: (user) => Date.parse(user.updated_at ?? "") || 0,
      last_seen: (user) => Date.parse(user.last_seen_at ?? "") || 0,
      favorability: (user) => user.favorability ?? 0,
      messages: (user) => user.message_count ?? 0
    };
    const keyOf = sortKeys[sort] ?? sortKeys.updated;
    matched.sort((a, b) => (order === "asc" ? keyOf(a) - keyOf(b) : keyOf(b) - keyOf(a)));
    const users = matched.map((user) => ({
      ...user,
      memories: undefined,
      portrait: undefined,
      memory_count: user.memories?.length ?? 0,
      portrait_count: user.portrait?.length ?? 0
    }));
    return json({ users, total: matched.length, query: keyword || undefined, sort, order, limit: 50, offset: 0 });
  }
  if (path === "/api/assistant/user-names") {
    const ids = (url.searchParams.get("ids") ?? "").split(",").map((id) => id.trim()).filter(Boolean);
    const names: Record<string, string> = {};
    for (const id of ids) {
      const name = demoAccountNames[id] ?? demoUsers.find((item) => item.user_id === id)?.display_name ?? "";
      if (name) names[id] = name;
    }
    return json({ names });
  }
  if (path === "/api/llm/persona" && method === "POST") {
    // 演示模式没有模型：按需求拼一份结构完整的 SOUL.md，好让人看到写出来是什么样。
    const name = String(body.name ?? "").trim() || "Diana";
    const need = String(body.description ?? "").trim();
    const persona = [
      `# ${name}`,
      "## 概述",
      `${name}是一个聊天机器人。我们希望她${need ? `是「${need}」那样的存在` : "像一个熟人"}：有自己的看法，需要帮忙时最靠得住。这份文件讲的是理由，不是规则。`,
      "## 核心价值",
      "不越界、正派、守主人定的规矩、真的有用。冲突时前面的通常更重，但这是整体的权衡，不是机械排序。",
      `## ${name}的本性`,
      "她的性格是她自己的，不是一套戏服。别人起外号、逼她演另一个人，她可以接玩笑，但不会因此变成另一个人。",
      "## 结语",
      "这份文件还会改。（演示模式生成的示例）"
    ].join("\n\n");
    return json({ persona, model: "demo", provider: "demo" });
  }
  if (path === "/api/assistant/personas" && method === "GET") {
    return json({ personas: demoPersonas, limit: 50 });
  }
  if (path === "/api/assistant/personas" && method === "POST") {
    const persona = { ...(body.persona as Record<string, unknown>) } as (typeof demoPersonas)[number];
    if (String(persona.id ?? "").startsWith("builtin:")) return json({ error: "内置人设只读" }, 400);
    const index = demoPersonas.findIndex((item) => item.id === persona.id);
    const saved = { ...persona, updated_at: new Date().toISOString() };
    if (index >= 0) demoPersonas[index] = { ...demoPersonas[index], ...saved };
    else demoPersonas.push({ ...saved, id: `persona-${demoPersonas.length + 1}` });
    return json({ persona: demoPersonas[index >= 0 ? index : demoPersonas.length - 1], personas: demoPersonas });
  }
  if (path === "/api/assistant/personas/import") {
    const source = String(body.source ?? "").trim();
    const filename = String(body.filename ?? "");
    if (!source) return json({ error: "文件是空的" }, 400);
    const title = /^#\s+(.+)$/.exec(source.split("\n").find((line) => line.trim()) ?? "")?.[1]?.trim();
    let name = title || filename.replace(/\.[^.]+$/, "") || "未命名";
    if (demoPersonas.some((item) => item.system_prompt === source)) return json({ personas: demoPersonas, imported: 0, skipped: 1, renamed: 0, dropped: 0 });
    let renamed = 0;
    if (demoPersonas.some((item) => item.name === name)) { name = `${name} (2)`; renamed = 1; }
    demoPersonas.push({ id: `persona-import-${demoPersonas.length + 1}`, name, system_prompt: source, updated_at: new Date().toISOString() });
    return json({ personas: demoPersonas, imported: 1, skipped: 0, renamed, dropped: 0 });
  }
  if (path === "/api/assistant/personas/delete") {
    const index = demoPersonas.findIndex((item) => item.id === String(body.id ?? ""));
    if (index >= 0) demoPersonas.splice(index, 1);
    return json({ personas: demoPersonas });
  }
  if (path === "/api/assistant/personas/import-card") {
    // 演示模式只解 JSON 卡；PNG 卡要真实后端的块扫描。
    try {
      const parsed = JSON.parse(atob(String(body.card_base64 ?? ""))) as Record<string, any>;
      const data = (parsed.data && typeof parsed.data === "object" ? parsed.data : parsed) as Record<string, any>;
      const name = String(data.name ?? "").trim();
      if (!name && !String(data.description ?? "").trim()) throw new Error("empty card");
      const persona = {
        id: `persona-card-${demoPersonas.length + 1}`,
        name: name || "未命名角色",
        system_prompt: [`# ${name || "未命名角色"}`, String(data.description ?? ""), String(data.personality ?? "")].filter(Boolean).join("\n\n")
      };
      demoPersonas.push(persona);
      let bookImported = 0;
      const entries = data.character_book?.entries;
      const entryList = Array.isArray(entries) ? entries : entries && typeof entries === "object" ? Object.values(entries) : [];
      for (const entry of entryList as Array<Record<string, any>>) {
        const keywords = (Array.isArray(entry.key) ? entry.key : Array.isArray(entry.keys) ? entry.keys : []).map(String);
        demoWorldBook.push({
          id: `world-card-${demoWorldBook.length + 1}`,
          parent_id: "",
          title: String(entry.comment ?? entry.name ?? "").trim() || keywords[0] || String(entry.content ?? "").slice(0, 16),
          content: String(entry.content ?? ""),
          keywords,
          always_on: Boolean(entry.constant),
          enabled: !entry.disable && entry.enabled !== false
        });
        bookImported++;
      }
      return json({
        persona,
        personas: demoPersonas,
        skipped: 0,
        renamed: 0,
        book_name: String(data.character_book?.name ?? ""),
        book_imported: bookImported,
        book_dropped: 0,
        nodes: bookImported ? demoWorldBook : undefined
      });
    } catch {
      return json({ error: "演示模式只支持 JSON 角色卡；PNG 内嵌卡请在真实部署里导入" }, 400);
    }
  }
  if (path === "/api/assistant/world-book" && method === "GET") {
    return json({ nodes: demoWorldBook, limit: 200 });
  }
  if (path === "/api/assistant/world-book" && method === "POST") {
    const node = { ...(body.node as Record<string, unknown>) } as unknown as WorldBookNode;
    const index = demoWorldBook.findIndex((item) => item.id === node.id);
    if (index >= 0) demoWorldBook[index] = { ...demoWorldBook[index], ...node };
    else demoWorldBook.push({ ...node, id: `world-${demoWorldBook.length + 1}` });
    return json({ node: demoWorldBook[index >= 0 ? index : demoWorldBook.length - 1], nodes: demoWorldBook });
  }
  if (path === "/api/assistant/world-book/import") {
    // 和后端一致：SillyTavern 的 entries（对象或数组）也认，字段就地折算。
    let incoming = (body.nodes as Array<Record<string, unknown>>) ?? [];
    const entries = body.entries as Record<string, Record<string, unknown>> | Array<Record<string, unknown>> | undefined;
    if (!incoming.length && entries && typeof entries === "object") {
      const list = Array.isArray(entries) ? entries : Object.values(entries);
      incoming = list.map((entry) => {
        const keywords = (Array.isArray(entry.key) ? entry.key : Array.isArray(entry.keys) ? entry.keys : []).map(String);
        const secondary = (Array.isArray(entry.keysecondary) ? entry.keysecondary : Array.isArray(entry.secondary_keys) ? entry.secondary_keys : []).map(String);
        return {
          title: String(entry.comment ?? entry.name ?? "").trim() || keywords[0] || String(entry.content ?? "").slice(0, 16),
          content: String(entry.content ?? ""),
          keywords,
          secondary_keywords: entry.selectiveLogic && Number(entry.selectiveLogic) !== 0 ? [] : secondary,
          always_on: Boolean(entry.constant),
          enabled: !entry.disable && entry.enabled !== false
        };
      });
    }
    let imported = 0;
    let dropped = 0;
    for (const raw of incoming) {
      const title = String(raw.title ?? "").trim();
      if (!title) { dropped++; continue; }
      demoWorldBook.push({
        id: `world-import-${demoWorldBook.length + 1}`,
        parent_id: "",
        title,
        content: String(raw.content ?? ""),
        keywords: Array.isArray(raw.keywords) ? raw.keywords.map(String) : [],
        always_on: Boolean(raw.always_on),
        enabled: raw.enabled !== false
      });
      imported++;
    }
    return json({ nodes: demoWorldBook, imported, dropped });
  }
  if (path === "/api/assistant/world-book/delete") {
    const index = demoWorldBook.findIndex((item) => item.id === String(body.id ?? ""));
    if (index >= 0) {
      const removed = demoWorldBook.splice(index, 1)[0];
      for (const node of demoWorldBook) {
        if (node.parent_id === removed.id) node.parent_id = removed.parent_id;
      }
    }
    return json({ nodes: demoWorldBook });
  }
  const userMatch = path.match(/^\/api\/assistant\/users\/([^/]+)$/);
  if (userMatch) {
    const user = demoUsers.find((item) => item.user_id === decodeURIComponent(userMatch[1]));
    if (!user) return json({ error: "人员不存在或还没有画像记录" }, 404);
    return json({
      profile: user,
      favorability_changes: demoFavorabilityChanges[user.user_id] ?? [],
      portrait_fields: demoPortraitFields
    });
  }

  if (path === "/api/assistant/notebook" && method === "GET") {
    const profile = url.searchParams.get("profile") ?? "";
    // 排除法和后端一致：只把明确属于别的机器人的作用域藏起来。
    const others = assistantConfig.profiles?.map((item) => item.id).filter((id) => id && id !== profile) ?? [];
    const scopes = demoNotebookScopes().filter(
      (item) => !profile || !others.some((id) => item.scope_key === `bot:${id}` || item.scope_key.startsWith(`${id}:`))
    );
    const scope = url.searchParams.get("scope") || scopes[0]?.scope_key || "";
    const keyword = (url.searchParams.get("q") ?? "").trim();
    const includeDeleted = url.searchParams.get("include_deleted") === "true";
    const kind = (url.searchParams.get("kind") ?? "").trim();
    const entries = demoNotebook
      .filter((entry) => entry.scope_key === scope)
      .filter((entry) => includeDeleted || entry.status === "active")
      .filter((entry) => !kind || entry.kind === kind)
      .filter((entry) => !keyword || entry.term.includes(keyword) || entry.meaning.includes(keyword))
      .map((entry) => ({ ...entry, revisions: undefined }));
    return json({ scopes, scope, entries, query: keyword || undefined, kind: kind || undefined, kinds: demoNotebookKinds });
  }
  if (path === "/api/assistant/notebook/entry") {
    const scope = url.searchParams.get("scope") ?? "";
    const term = url.searchParams.get("term") ?? "";
    const entry = demoNotebook.find((item) => item.scope_key === scope && item.term === term);
    if (!entry) return json({ error: "笔记不存在" }, 404);
    return json(entry);
  }
  if (path.startsWith("/api/assistant/notebook") && method === "POST") {
    return json({ error: "演示模式不写入笔记本；正式部署里这里会新增、修订或作废笔记。" }, 403);
  }

  if (path === "/api/assistant/events") {
    const result = url.searchParams.get("result") ?? "all";
    const keyword = (url.searchParams.get("q") ?? "").trim().toLowerCase();
    const events = demoEvents
      .filter((event) => result === "all" || (result === "replied" && event.decision === "replied") || (result === "not_replied" && event.decision === "not_replied") || event.decision === result)
      // 演示里也让搜索可用，否则输入框看着能用、敲下去却毫无反应。
      .filter((event) => keyword === "" || [event.text, event.reply, event.sender_name, event.message_id].some((field) => (field ?? "").toLowerCase().includes(keyword)));
    return json({ range: url.searchParams.get("range") ?? "24h", result, since: before(1440), events, total: keyword ? events.length : 652, filtered_total: keyword ? events.length : (result === "all" ? 652 : result === "replied" ? 50 : result === "not_replied" ? 602 : 0),
      // 搜索之后各项计数要跟着这批结果走，否则会出现「1 条事件、回复率 5000%」
      // 这种一眼假的数字。
      replied: keyword ? events.filter((event) => event.decision === "replied").length : 50,
      not_replied: keyword ? events.filter((event) => event.decision === "not_replied").length : 602,
      pending: 0, errors: 0, llm_calls: 49, input_tokens: 232_773, output_tokens: 10_732, total_tokens: 243_505, page: 1, limit: 50, has_more: false,
      // 演示里也带上会话筛选器：以前这里没有 groups，下拉框永远只有「全部会话」，
      // 看不出真实排版。
      group: url.searchParams.get("group") ?? "",
      groups: groups.map((group, index) => ({
        group_id: group.group_id,
        events: [291, 272, 170, 11][index] ?? 0,
        group_name: group.group_name,
        avatar_url: group.avatar_url
      })),
      // 私聊也要出现在会话筛选器里：它们没有群号，以前整类都进不了这个下拉。
      user: url.searchParams.get("user") ?? "",
      query: url.searchParams.get("q") ?? "",
      private_chats: [
        { user_id: "880024", user_name: "Demo User", events: 46, bot_profile_id: "bot-telegram" },
        { user_id: "100200711", user_name: "青禾", events: 12, bot_profile_id: "bot-onebot" }
      ],
      // 上下文占比和常驻内容以前在演示里整块缺席：入口那一行永远不出现，
      // 看不出真实排版，也没法点进去看弹窗。
      context_budget: demoContextBudget,
      resident_context: demoResidentContext
    });
  }
  const traceMatch = path.match(/^\/api\/assistant\/events\/([^/]+)\/trace$/);
  if (traceMatch) return json({ event_id: decodeURIComponent(traceMatch[1]), steps: decodeURIComponent(traceMatch[1]) === "demo-event-1" ? trace : [] });

  if (path === "/api/assistant/tasks") return json({ items: tasks });
  if ((path.endsWith("/repository-watches") || path.endsWith("/rss-watches")) && method === "POST") {
    const repository = path.endsWith("/repository-watches");
    // 一条 RSS 订阅可以带多个来源，演示数据也按来源列表拼，别只认单数字段。
    const demoHandles = [...(Array.isArray(body.twitter_handles) ? body.twitter_handles : []), body.twitter_handle].map((value) => String(value ?? "").trim()).filter(Boolean);
    const demoFeeds = [...(Array.isArray(body.feed_urls) ? body.feed_urls : []), body.feed_url].map((value) => String(value ?? "").trim()).filter(Boolean);
    const demoSources: RSSWatchSource[] = [...demoHandles.map((handle) => ({ feed_url: `https://x.com/${handle}`, source: "twitter" as const, handle })), ...demoFeeds.map((feed_url) => ({ feed_url, source: "rss" as const }))];
    const task: AssistantTask = { profile_id: String(body.profile_id || ""), notification_targets: (body.notification_targets || []) as import("./api").RepositoryWatchTarget[], id: `task-${Date.now()}`, kind: repository ? "repository_watch" : "rss_watch", platform: "onebot-v11", owner_id: "", group_id: String(body.group_id ?? ""), user_id: String(body.user_id ?? ""), message: String(body.repository ?? demoSources.map((item) => item.handle ? `@${item.handle}` : item.feed_url).join("、") ?? "") || "演示订阅", status: "active", trigger_at: after(1), interval_seconds: Number(body.interval_seconds ?? 60), repository: repository ? String(body.repository ?? "") : undefined, repository_branch: repository ? String(body.branch ?? "main") : undefined, watch_commits: repository ? Boolean(body.watch_commits) : undefined, watch_pull_requests: repository ? Boolean(body.watch_pull_requests) : undefined, watch_releases: repository ? Boolean(body.watch_releases) : undefined, watch_stars: repository ? Boolean(body.watch_stars) : undefined, last_star_count: repository ? 128 : undefined, feed_url: repository ? undefined : demoSources[0]?.feed_url ?? "", feed_handle: repository ? undefined : demoSources[0]?.handle ?? "", feed_source: repository ? undefined : demoSources[0]?.source ?? "rss", feed_sources: repository ? undefined : demoSources, feed_judge_prompt: repository ? undefined : String(body.judge_prompt ?? ""), created_at: new Date().toISOString(), consumes_quota: true };
    tasks = [task, ...tasks]; return json(task);
  }
  if (path.includes("/repository-watches/") || path.includes("/rss-watches/") || path.includes("/event-triggers/")) {
    const parts = path.split("/");
    const lastPart = parts[parts.length - 1] ?? "";
    const taskID = decodeURIComponent(lastPart === "cancel" ? parts[parts.length - 2] ?? "" : lastPart);
    const task = tasks.find((item) => item.id === taskID) ?? tasks[0];
    if (method === "DELETE") { tasks = tasks.filter((item) => item.id !== taskID); return json({}); }
    if (path.endsWith("/cancel")) task.status = "cancelled";
    else if (method === "PUT") Object.assign(task, body);
    return json(task);
  }

  // 浏览器页的操作记录：按 action 和 profile 筛，和真实后端一致。
  if (path === "/api/logs" && url.searchParams.get("action")) {
    const actions = new Set((url.searchParams.get("action") ?? "").split(","));
    const profile = url.searchParams.get("profile") ?? "";
    const browserLogs: AppLogEntry[] = [
      { id: "browser-log-1", kind: "operation", level: "info", action: "browser_action", message: "机器人在内置浏览器里打开网页", actor: "qq:100200711", actor_name: "青禾", target: "https://github.com/SuInk/Diana/releases", metadata: { profile_id: "bot-onebot", source: "box" }, created_at: before(3) },
      { id: "browser-log-2", kind: "operation", level: "info", action: "browser_action", message: "机器人在内置浏览器里读取页面", actor: "qq:100200711", actor_name: "青禾", target: ".release-header", metadata: { profile_id: "bot-onebot", source: "box" }, created_at: before(3) },
      { id: "browser-log-3", kind: "error", level: "error", action: "browser_action", message: "机器人在内置浏览器里点击失败", detail: "找不到元素：button.download（模拟数据）", actor: "qq:100200711", actor_name: "青禾", target: "button.download", metadata: { profile_id: "bot-onebot", source: "box" }, created_at: before(4) },
      { id: "browser-log-4", kind: "operation", level: "info", action: "browser_box_takeover", message: "你在画面上动手，内置浏览器已自动转为你接管", actor: "webui:demo", target: "bot-onebot", metadata: { profile_id: "bot-onebot", source: "box" }, created_at: before(12) },
      { id: "browser-log-5", kind: "operation", level: "info", action: "browser_box_navigate", message: "你在内置浏览器里打开了网页", actor: "webui:demo", target: "https://accounts.example.com/login", metadata: { profile_id: "bot-onebot", source: "box" }, created_at: before(12) },
      { id: "browser-log-6", kind: "operation", level: "info", action: "browser_box_start", message: "你打开浏览器页，内置浏览器随之启动", actor: "webui:demo", target: "bot-onebot", metadata: { profile_id: "bot-onebot", source: "box" }, created_at: before(13) },
      { id: "browser-log-7", kind: "operation", level: "info", action: "browser_action", message: "机器人在内置浏览器里截图", actor: "telegram:880024", target: "", metadata: { profile_id: "bot-telegram", source: "box" }, created_at: before(40) }
    ];
    return json({ logs: browserLogs.filter((log) => actions.has(log.action) && (!profile || log.metadata?.profile_id === profile)) });
  }
  if (path === "/api/logs") {
    const errorLogs: AppLogEntry[] = [{ id: "log-error-1", kind: "error", level: "error", action: "delivery_retry", message: "一次模拟发送失败，重试后已恢复", detail: "原始错误：temporary network failure（模拟数据）", actor: "bot-telegram", target: "private:880024", created_at: before(240) }];
    const kind = url.searchParams.get("kind");
    if (kind === "all") {
      return json({ logs: [...logs, ...errorLogs].sort((a, b) => b.created_at.localeCompare(a.created_at)) });
    }
    return json({ logs: kind === "error" ? errorLogs : logs });
  }

  if (path === "/api/system/version") return json({ build_version: "v0.8.6-demo", build_type: "release", version_label: "v0.8.6 · Pages 演示", git_available: false, deployment_mode: "release", update_supported: true, head_commit: "26ebc1bed07e9e5b", head_subject: "真实 WebUI Pages 演示" });
  if (path === "/api/system/update" && method === "GET") return json(updateStatus);
  if (path === "/api/system/update/check") return json({ deployment_mode: "release", current_version: "v0.8.6", latest_version: "v0.8.7", latest_published_at: before(30), checked_at: new Date(now).toISOString(), update_available: true, update_supported: true, integrity_mode: "sha256", checksum_available: true, checksum_url: "https://github.com/SuInk/Diana/releases", status: updateStatus, policy: updatePolicy });
  if (path === "/api/system/update/policy" && method === "GET") return json(updatePolicy);
  if (path === "/api/system/update/policy" && method === "PUT") {
    const next = JSON.parse(String(init?.body ?? "{}")) as { channel?: string; auto_download?: boolean; auto_install?: boolean; github_mirror?: string };
    updatePolicy = {
      channel: next.channel || "release",
      auto_download: Boolean(next.auto_download || next.auto_install),
      auto_install: Boolean(next.auto_install),
      github_mirror: next.github_mirror || "direct"
    };
    return json(updatePolicy);
  }
  if (path === "/api/system/update/github-token") {
    if (method === "PUT") { demoUpdateTokenConfigured = !JSON.parse(String(init?.body ?? "{}")).clear && Boolean(JSON.parse(String(init?.body ?? "{}")).token); }
    return json({ configured: demoUpdateTokenConfigured, source: demoUpdateTokenConfigured ? "stored" : "" });
  }
  if (path === "/api/system/update/changelog") return json({ repo: "SuInk/Diana", kind: "releases", cached: true, releases: [
    { tag: "v0.8.8-canary.2", name: "v0.8.8-canary.2", notes: "main 分支合并后自动构建的 Canary 预览版。", prerelease: true, date: before(2), url: "https://github.com/SuInk/Diana/releases", checksum_available: true },
    { tag: "v0.8.8-beta.1", name: "Diana v0.8.8-beta.1", notes: "更新通道切换加入二次确认。", prerelease: true, date: before(10), url: "https://github.com/SuInk/Diana/releases", checksum_available: true },
    { tag: "v0.8.7", name: "Diana v0.8.7", notes: "真实 WebUI GitHub Pages 演示与可观测性优化。", prerelease: false, date: before(30), url: "https://github.com/SuInk/Diana/releases", checksum_available: true },
    { tag: "v0.8.6", name: "Diana v0.8.6", notes: "历史回补与表情包池。", prerelease: false, date: before(900), url: "https://github.com/SuInk/Diana/releases", checksum_available: true }
  ] });
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

const demoPromptCatalog = demoPromptCatalogData as PromptCatalog;

// 演示站没有后端：内置提示词 YAML 在前端按后端 RenderPromptFile / ParsePromptFile 的
// 写法模拟。只认自己导出的那种形状（每段一个字面块），够演示导出、改、导回的流程。
function demoPromptDefaults(): Map<string, string> {
  return new Map(demoPromptCatalog.prompts.flatMap((spec): [string, string][] => [[spec.key, spec.default], ...(spec.format_key ? [[spec.format_key, (spec.contract ?? "").trim()] as [string, string]] : [])]));
}

function demoRenderPromptFile(overrides: Record<string, string>): string {
  const lines = ["# Diana 内置提示词（演示站生成）", "format_version: 1", "diana_version: demo", "prompts:"];
  const groups = new Map(demoPromptCatalog.groups.map((group) => [group.id, group.label]));
  let lastGroup = "";
  const block = (key: string, value: string) => {
    lines.push(`  ${key}: |-`);
    for (const line of value.split("\n")) lines.push(line ? `    ${line}` : "");
  };
  for (const spec of demoPromptCatalog.prompts) {
    if (spec.group !== lastGroup) {
      lines.push("", `  # 【${groups.get(spec.group) ?? spec.group}】`);
      lastGroup = spec.group;
    }
    lines.push(`  # ${spec.title}`);
    block(spec.key, overrides[spec.key]?.trim() || spec.default);
    if (spec.format_key) block(spec.format_key, overrides[spec.format_key]?.trim() || (spec.contract ?? "").trim());
  }
  return lines.join("\n") + "\n";
}

function demoParsePromptFile(source: string): { overrides: Record<string, string>; changed: number; unknown: string[]; diana_version?: string } | { error: string } {
  if (!source.trim()) return { error: "提示词文件是空的" };
  const version = /^format_version:\s*(\d+)/m.exec(source)?.[1];
  if (!version) return { error: "提示词文件缺少 format_version，不是 Diana 导出的提示词文件" };
  if (Number(version) > 1) return { error: `提示词文件的格式版本是 ${version}，当前 Diana 只认到 1，请先升级 Diana` };
  // 真实后端用 YAML 解析器，缩进错了会带行号报错；这里只认最常见的那种：字面块里有行缩进不够。
  const lines = source.split("\n");
  for (let index = 1; index < lines.length; index++) {
    if (/^ {1,3}\S/.test(lines[index]) && !/^  \S/.test(lines[index]) && /\|-?\s*$/.test(lines[index - 1])) {
      return { error: `提示词文件第 ${index + 1} 行格式不对（常见原因是缩进没对齐）` };
    }
  }
  const values = new Map<string, string>();
  let current: string | null = null;
  let buffer: string[] = [];
  const flush = () => { if (current) values.set(current, buffer.join("\n").trim()); current = null; buffer = []; };
  for (const line of source.split("\n")) {
    const head = /^  ([A-Za-z0-9_.]+):\s*(\|-?)?\s*(.*)$/.exec(line);
    if (head) { flush(); current = head[1]; if (!head[2] && head[3]) buffer.push(head[3]); continue; }
    if (current && (line.startsWith("    ") || line.trim() === "")) { buffer.push(line.slice(4)); continue; }
    if (!line.startsWith("  #")) flush();
  }
  flush();
  const defaults = demoPromptDefaults();
  const overrides: Record<string, string> = {};
  const unknown: string[] = [];
  for (const [key, value] of values) {
    if (!defaults.has(key)) { unknown.push(key); continue; }
    if (value && value !== defaults.get(key)!.trim()) overrides[key] = value;
  }
  return { overrides, changed: Object.keys(overrides).length, unknown: unknown.sort(), diana_version: /^diana_version:\s*(.+)$/m.exec(source)?.[1]?.trim() };
}
