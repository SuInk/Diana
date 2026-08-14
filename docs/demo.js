const navigation = [
  ["dashboard", "▦", "总览", "运行状态"],
  ["events", "◎", "事件明细", "原因与调用链"],
  ["tasks", "◷", "提醒与订阅", "周期和一次性"],
  ["llm", "◇", "模型配置", "同步与职责"],
  ["bot", "▣", "机器人", "多通道策略"],
  ["plugins", "⊞", "插件", "能力与状态"],
  ["groups", "♢", "群管理", "群级回复策略"],
  ["settings", "⚙", "设置", "系统与更新"],
];

const mock = {
  events: [
    {
      id: "evt-1001", time: "14:28:16", date: "08/14", platform: "QQ", kind: "群聊", target: "演示群 · 产品讨论",
      user: "用户_青禾 · 100200301", text: "@Diana 帮我总结一下今天的发布变更", status: "replied", duration: "6.8s", tokens: 2846,
      reason: "检测到显式 @机器人，绕过主动回复语义阈值；问题需要读取仓库近期变更后回答。",
      reply: "今天的更新重点是事件原因审计、仓库动态订阅和多通道会话隔离。修复了引用消息同时 @机器人时可能被误判为被动回复的问题。",
      trace: [["消息接入", "OneBot v11 事件已规范化，to_me=true"], ["路由判断", "显式 @机器人，直接进入主 Agent"], ["工具调用", "读取模拟仓库最近提交与 Release"], ["模型回复", "生成摘要并保留更新边界"], ["消息发送", "OneBot 返回 message_id=demo_7319"]],
    },
    {
      id: "evt-1002", time: "14:21:03", date: "08/14", platform: "QQ", kind: "群聊", target: "演示群 · 日常交流",
      user: "用户_栖迟 · 100200418", text: "[图片]", status: "ignored", duration: "42ms", tokens: 0,
      reason: "识别为其他机器人发送的自动消息；“识别机器人后不回复”已启用，因此未启动视觉模型和主 Agent。",
      reply: "未生成回复。",
      trace: [["消息接入", "图片消息已记录，媒体仅使用模拟占位数据"], ["机器人识别", "命中自动消息特征，置信度 98%"], ["路由结束", "按群策略忽略，未产生模型调用"]],
    },
    {
      id: "evt-1003", time: "14:08:42", date: "08/14", platform: "Telegram", kind: "私聊", target: "演示私聊",
      user: "Demo User · 880024", text: "Zeabur 最近有什么产品更新？", status: "replied", duration: "8.2s", tokens: 3231,
      reason: "私聊默认响应；问题包含时效性要求，Agent 选择先调用内置联网搜索再组织答案。",
      reply: "我检索了官方更新渠道，并按发布时间整理了近期变化。演示页面不会真正联网，真实服务会在回答中保留来源链接。",
      trace: [["消息接入", "Telegram 私聊直接进入 Agent"], ["意图判断", "识别为近期产品动态查询"], ["工具调用", "内置联网搜索（演示结果）"], ["模型回复", "整合信息并生成带来源的答案"], ["消息发送", "Telegram API 返回成功"]],
    },
    {
      id: "evt-1004", time: "13:55:19", date: "08/14", platform: "QQ", kind: "群聊", target: "演示群 · 设计讨论",
      user: "用户_星野 · 100200519", text: "画一张雨夜城市里的复古电车", status: "replied", duration: "18.4s", tokens: 1672,
      reason: "命中群触发词并识别为明确的图片生成请求；调用独立生图模型，随后发送缩略图与原图入口。",
      reply: "图片已生成并发送。事件明细默认展示缩略图，点击后才加载原图。",
      trace: [["消息接入", "群触发词命中"], ["意图判断", "分类 image_generation，置信度 99%"], ["工具调用", "生图模型测试链路（模拟）"], ["消息发送", "缩略图和原图地址发送成功"]],
    },
    {
      id: "evt-1005", time: "13:42:27", date: "08/14", platform: "QQ", kind: "群聊", target: "演示群 · 产品讨论",
      user: "用户_白榆 · 100200627", text: "Zeabur 风味是什么", status: "replied", duration: "4.9s", tokens: 1908,
      reason: "短句虽未显式 @机器人，但包含可回答的产品语境问题；主动回复阈值与被动触发保持一致，判断应当参与。",
      reply: "如果是在说产品界面，“Zeabur 风味”通常指偏开发者工具的克制布局：高信息密度、明确状态和较少装饰。这里是基于当前群聊上下文的理解。",
      trace: [["主动回复候选", "进入 60 秒聚合窗口"], ["语义判断", "question=true，answerable=true，confidence=94%"], ["主 Agent", "结合近期群聊上下文生成回答"], ["消息发送", "回复成功，未执行截断"]],
    },
  ],
  tasks: [
    { id: 1, icon: "◷", name: "15:30 提醒提交周报", type: "一次性提醒", schedule: "今天 15:30", target: "QQ 私聊 · 100200301", status: "running" },
    { id: 2, icon: "↻", name: "每日 AI 行业资讯摘要", type: "周期任务", schedule: "每天 09:00", target: "Telegram · 演示频道", status: "running" },
    { id: 3, icon: "⌁", name: "Diana 仓库动态", type: "仓库订阅", schedule: "每 30 秒检查", target: "QQ 群 · 产品讨论", status: "running" },
    { id: 4, icon: "✓", name: "Release v0.8.5 发布提醒", type: "一次性提醒", schedule: "08/12 21:06", target: "QQ 私聊 · 100200418", status: "done" },
  ],
  plugins: [
    { id: "rag", icon: "▤", color: "green", name: "Diana 能力知识库 RAG", version: "v0.1.0", desc: "索引核心能力和实时插件清单，为 Agent 提供与问题相关的能力说明。", tags: ["智能体工具", "知识读取"], enabled: true },
    { id: "file", icon: "▧", color: "", name: "文件解析", version: "v0.3.0", desc: "解析 PDF、图片和文本附件，并把可验证的结构化内容交给模型。", tags: ["文件解析", "消息读取"], enabled: true },
    { id: "llm", icon: "◇", color: "pink", name: "LLM 配置技能", version: "v0.1.0", desc: "仅通过受控工具修改 Provider、默认模型和各职责模型绑定。", tags: ["配置写入", "权限控制"], enabled: true },
    { id: "history", icon: "▱", color: "", name: "消息历史", version: "v0.2.0", desc: "检索近期、压缩摘要、长期事实和跨群历史，支持会话隔离策略。", tags: ["消息读取", "长期记忆"], enabled: true },
    { id: "link", icon: "⌁", color: "", name: "链接解析", version: "v0.3.0", desc: "解析社交媒体链接，支持合并转发与限定大小的视频下载。", tags: ["网络请求", "消息发送"], enabled: true },
    { id: "onebot", icon: "▣", color: "green", name: "OneBot v11 协议", version: "v0.1.0", desc: "提供标准事件读取、消息发送和群组列表等 QQ 通道能力。", tags: ["OneBot", "群组管理"], enabled: true },
    { id: "repo", icon: "⇧", color: "green", name: "仓库更新订阅", version: "v0.1.0", desc: "监控公开或私有仓库的 Commit 与 Release，经 LLM 总结后通知指定对象。", tags: ["任务持久化", "消息发送"], enabled: true, needs: true },
    { id: "browser", icon: "◎", color: "pink", name: "沙盒网页渲染", version: "v0.2.0", desc: "在隔离浏览器中渲染动态网页，把稳定后的页面内容交给 Agent。", tags: ["网页渲染", "隔离环境"], enabled: false },
    { id: "tts", icon: "≋", color: "green", name: "自定义语音合成", version: "v0.1.0", desc: "统一调用语音合成服务，按通道发送生成的音频消息。", tags: ["语音合成", "文件写入"], enabled: true },
  ],
  groups: [
    { id: "100200301", name: "产品讨论（演示）", members: 186, enabled: true, window: "08:00 - 23:30", triggers: "Diana, 嘉然", blocked: "100200999", prompt: "以准确、简洁的方式参与产品和工程讨论。" },
    { id: "100200418", name: "日常交流（演示）", members: 74, enabled: true, window: "全天", triggers: "Diana", blocked: "", prompt: "自然参与闲聊，事实不确定时优先搜索。" },
    { id: "100200519", name: "设计讨论（演示）", members: 52, enabled: true, window: "09:00 - 22:00", triggers: "画一张, Diana", blocked: "100200888, 100200889", prompt: "优先理解视觉需求，并在生图前补齐必要约束。" },
    { id: "100200627", name: "只读观察群（演示）", members: 318, enabled: false, window: "全天", triggers: "", blocked: "", prompt: "仅记录事件，不主动回复。" },
  ],
};

const state = {
  eventRange: "24h",
  eventStatus: "all",
  taskFilter: "all",
  pluginFilter: "all",
  pluginSearch: "",
  selectedGroup: mock.groups[0].id,
};

const root = document.documentElement;
const viewRoot = document.querySelector("[data-view]");
const navRoot = document.querySelector("[data-nav]");
const viewTitle = document.querySelector("[data-view-title]");
const sidebar = document.querySelector("[data-sidebar]");
const scrim = document.querySelector("[data-sidebar-scrim]");
const modalBackdrop = document.querySelector("[data-modal-backdrop]");
const modalTitle = document.querySelector("[data-modal-title]");
const modalBody = document.querySelector("[data-modal-body]");
const toast = document.querySelector("[data-toast]");
let toastTimer;

function escapeHTML(value) {
  return String(value).replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[char]);
}

function showToast(message) {
  window.clearTimeout(toastTimer);
  toast.textContent = message;
  toast.hidden = false;
  toastTimer = window.setTimeout(() => { toast.hidden = true; }, 2600);
}

function setSidebar(open) {
  sidebar.classList.toggle("open", open);
  scrim.classList.toggle("open", open);
}

function badge(status) {
  const map = {
    replied: ["ok", "已回复"], ignored: ["warn", "未回复"], error: ["err", "异常"], running: ["ok", "运行中"], done: ["info", "已完成"], disabled: ["", "已停用"],
  };
  const [kind, text] = map[status] || ["", status];
  return `<span class="badge ${kind}">${text}</span>`;
}

function pageHeader(title, subtitle, actions = "") {
  return `<header class="view-header"><div class="view-title"><h1>${title}</h1><p>${subtitle}</p></div>${actions ? `<div class="view-actions">${actions}</div>` : ""}</header>`;
}

function eventRows(events, limit) {
  return `<div class="event-list">${events.slice(0, limit || events.length).map((event) => `
    <article class="event-row">
      <div class="event-time">${event.time}<span class="event-date">${event.date}</span></div>
      <div>
        <div class="event-meta"><span class="badge info">${event.platform}</span><span class="badge">${event.kind}</span>${badge(event.status)}<span class="muted">${escapeHTML(event.target)} · ${escapeHTML(event.user)}</span><span class="muted">${event.duration}</span></div>
        <p class="event-message">${escapeHTML(event.text)}</p>
        <div class="event-reason ${event.status === "replied" ? "ok" : "warn"}"><strong>${event.status === "replied" ? "回复原因" : "未回复原因"}</strong> · ${escapeHTML(event.reason)}</div>
        ${event.status === "replied" ? `<p class="event-reply">${escapeHTML(event.reply)}</p>` : ""}
      </div>
      <button class="btn small" type="button" data-event-id="${event.id}">查看详情</button>
    </article>`).join("")}</div>`;
}

function dashboardView() {
  const bars = [18, 32, 22, 48, 38, 61, 45, 72, 51, 84, 66, 93, 58, 78, 69, 42, 54, 47, 65, 39, 56, 71, 88, 62];
  return `${pageHeader("总览", "通道、消息处理、模型用量和服务资源的模拟运行快照", `<button class="btn" type="button" data-action="refresh">↻ 刷新模拟数据</button>`)}
    <div class="stack">
      <section class="stat-grid">
        <article class="stat-card"><span class="stat-label">24h 消息 <span class="text-ok">+12%</span></span><strong class="stat-value">1,284</strong><span class="stat-foot">QQ 1,036 · Telegram 248</span></article>
        <article class="stat-card"><span class="stat-label">已回复</span><strong class="stat-value">318</strong><span class="stat-foot">回复率 24.8%</span></article>
        <article class="stat-card"><span class="stat-label">Token 总量</span><strong class="stat-value">648k</strong><span class="stat-foot">输入 581k · 输出 67k</span></article>
        <article class="stat-card"><span class="stat-label">工具调用</span><strong class="stat-value">146</strong><span class="stat-foot">联网搜索 42 · 记忆 61</span></article>
        <article class="stat-card"><span class="stat-label">处理异常</span><strong class="stat-value text-ok">0</strong><span class="stat-foot">最近 24 小时</span></article>
      </section>
      <div class="grid-main-side">
        <section class="card"><header class="card-head"><div class="card-title"><h2>最近 24 小时消息量</h2><span class="badge">实时模拟</span></div></header><div class="card-body"><div class="chart">${bars.map((height) => `<span class="bar-wrap"><i class="bar" style="height:${height}%"></i></span>`).join("")}</div><div class="chart-axis"><span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>现在</span></div></div></section>
        <section class="card"><header class="card-head"><h2>服务资源</h2><span class="badge ok">运行正常</span></header><div class="card-body resource-list">
          <div class="resource-row"><span>CPU</span><span class="meter"><i style="width:23%"></i></span><strong>23%</strong></div>
          <div class="resource-row"><span>内存</span><span class="meter blue"><i style="width:46%"></i></span><strong>46%</strong></div>
          <div class="resource-row"><span>存储</span><span class="meter amber"><i style="width:61%"></i></span><strong>61%</strong></div>
          <div class="setting-row"><span class="setting-copy"><strong>服务运行时长</strong><small>模拟实例 demo-a83f</small></span><strong>3天 8小时</strong></div>
        </div></section>
      </div>
      <section class="card"><header class="card-head"><h2>在线通道</h2><span class="badge ok">2 / 2 已连接</span></header><div class="card-body channel-list">
        <div class="channel"><span class="channel-icon">Q</span><span class="channel-main"><strong>QQ · OneBot v11</strong><small>演示账号 100000001</small></span><span class="badge ok">在线</span></div>
        <div class="channel telegram"><span class="channel-icon">T</span><span class="channel-main"><strong>Telegram Bot</strong><small>@diana_demo_bot</small></span><span class="badge ok">在线</span></div>
      </div></section>
      <section class="card"><header class="card-head"><div class="card-title"><h2>最近事件</h2><span class="badge ok">实时推送中</span></div><button class="btn small" type="button" data-route="events">全部事件</button></header><div class="card-body">${eventRows(mock.events, 3)}</div></section>
    </div>`;
}

function eventsView() {
  const events = mock.events.filter((event) => state.eventStatus === "all" || event.status === state.eventStatus);
  const totalTokens = mock.events.reduce((sum, event) => sum + event.tokens, 0).toLocaleString("zh-CN");
  const ranges = [["1h", "最近 1h"], ["24h", "24h"], ["7d", "7d"], ["30d", "30d"], ["long", "更久"]];
  return `${pageHeader("事件明细", "查看每条消息为什么回复、为什么不回复、最终结果与完整调用链", `<button class="btn" type="button" data-action="export-events">⇩ 导出演示记录</button>`)}
    <div class="stack">
      <section class="card"><div class="card-body toolbar"><div class="filters"><span class="muted">时间范围</span><div class="segmented">${ranges.map(([id, label]) => `<button type="button" class="${state.eventRange === id ? "active" : ""}" data-event-range="${id}">${label}</button>`).join("")}</div></div><div class="segmented"><button type="button" class="${state.eventStatus === "all" ? "active" : ""}" data-event-status="all">全部</button><button type="button" class="${state.eventStatus === "replied" ? "active" : ""}" data-event-status="replied">已回复</button><button type="button" class="${state.eventStatus === "ignored" ? "active" : ""}" data-event-status="ignored">未回复</button></div></div></section>
      <section class="stat-grid">
        <article class="stat-card"><span class="stat-label">范围内消息</span><strong class="stat-value">652</strong><span class="stat-foot">${state.eventRange}</span></article>
        <article class="stat-card"><span class="stat-label">已回复</span><strong class="stat-value">50</strong><span class="stat-foot">回复率 8%</span></article>
        <article class="stat-card"><span class="stat-label">未回复</span><strong class="stat-value">602</strong><span class="stat-foot">均已记录原因</span></article>
        <article class="stat-card"><span class="stat-label">处理异常</span><strong class="stat-value text-ok">0</strong><span class="stat-foot">无等待队列</span></article>
        <article class="stat-card"><span class="stat-label">Token 总量</span><strong class="stat-value">${totalTokens}</strong><span class="stat-foot">本页 ${mock.events.filter((e) => e.tokens).length} 次模型调用</span></article>
      </section>
      <section class="card"><header class="card-head"><div class="card-title"><h2>消息处理记录</h2><span class="badge ok">实时更新</span><span class="muted">显示 ${events.length} 条模拟样例</span></div></header><div class="card-body">${eventRows(events)}</div></section>
    </div>`;
}

function tasksView() {
  const tasks = mock.tasks.filter((task) => state.taskFilter === "all" || task.status === state.taskFilter);
  return `${pageHeader("提醒与订阅", "统一查看周期任务、一次性提醒和仓库动态订阅", `<button class="btn primary" type="button" data-action="new-task">＋ 新建模拟任务</button>`)}
    <div class="stack"><section class="card"><div class="card-body toolbar"><div class="segmented"><button type="button" class="${state.taskFilter === "all" ? "active" : ""}" data-task-filter="all">全部 ${mock.tasks.length}</button><button type="button" class="${state.taskFilter === "running" ? "active" : ""}" data-task-filter="running">运行中</button><button type="button" class="${state.taskFilter === "done" ? "active" : ""}" data-task-filter="done">已完成</button></div><span class="muted">检查周期最低 30 秒 · 模拟数据</span></div></section>
      <section class="card"><header class="card-head"><h2>任务列表</h2><span class="badge ok">${tasks.filter((t) => t.status === "running").length} 个运行中</span></header><div class="card-body task-list">${tasks.map((task) => `<article class="task-row"><span class="task-icon">${task.icon}</span><span class="task-main"><strong>${escapeHTML(task.name)}</strong><small>${task.type} · ${task.schedule} · ${escapeHTML(task.target)}</small></span><span class="task-actions">${badge(task.status)}<button class="btn small" type="button" data-task-id="${task.id}">查看</button></span></article>`).join("")}</div></section>
      <div class="grid-two"><section class="card"><header class="card-head"><h2>仓库订阅能力</h2><span class="badge info">公开 / 私有</span></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>监控 Commit 与 Release</strong><small>检测到变化后由 LLM 总结，再发送给指定群聊或私聊</small></span><span class="badge ok">已启用</span></div><div class="setting-row"><span class="setting-copy"><strong>GitHub Token</strong><small>仅用于演示，真实密钥不会显示</small></span><code class="mono">ghp_••••••••</code></div></div></section><section class="card"><header class="card-head"><h2>发送目标</h2></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>无需主人身份</strong><small>创建时直接选择发送通道与对象</small></span><span class="badge ok">按目标配置</span></div><div class="setting-row"><span class="setting-copy"><strong>群聊消息不可自动创建</strong><small>订阅任务只能在 WebUI 中明确创建</small></span><span class="badge">WebUI only</span></div></div></section></div>
    </div>`;
}

function llmView() {
  const roles = [["对话模型", "gpt-5.2", "群聊、私聊和 Agent 主回复"], ["视觉模型", "gpt-5.2", "图片理解与 OCR"], ["路由判断", "gpt-5-mini", "主动回复语义判断"], ["图片生成", "gpt-image-1.5", "真实生图测试链路"]];
  return `${pageHeader("模型配置", "先同步 Provider 模型列表，再分别设置默认模型和职责模型", `<button class="btn" type="button" data-action="sync-models">↻ 同步模型列表</button><button class="btn primary" type="button" data-action="save-models">保存模拟配置</button>`)}
    <div class="stack"><section class="card"><header class="card-head"><div><h2>Provider 与模型列表</h2><span class="muted">最后同步：刚刚 · 共 18 个模拟模型</span></div><span class="badge ok">API 可用</span></header><div class="card-body grid-two"><div class="field"><label>Provider</label><select class="select"><option>OpenAI Compatible（演示）</option><option>Anthropic（演示）</option></select></div><div class="field"><label>已同步模型</label><select class="select"><option>gpt-5.2</option><option>gpt-5-mini</option><option>gpt-image-1.5</option></select></div></div></section>
      <section class="card"><header class="card-head"><h2>默认模型</h2><span class="badge info">手动填写</span></header><div class="card-body form-grid"><div class="field"><label>默认 Provider</label><input class="input" value="OpenAI Compatible（演示）" /></div><div class="field"><label>默认模型 ID</label><input class="input mono" value="gpt-5.2" /></div></div></section>
      <section class="grid-two">${roles.map(([name, model, desc]) => `<article class="card"><header class="card-head"><div><h2>${name}</h2><span class="muted">${desc}</span></div><span class="badge ok">已配置</span></header><div class="card-body"><div class="field"><label>模型</label><input class="input mono" value="${model}" /></div><div class="cluster" style="margin-top:12px"><button class="btn small" type="button" data-action="model-test" data-model-role="${name}">▷ 运行${name === "图片生成" ? "生图" : "模型"}测试</button><span class="muted">不会调用真实服务</span></div></div></article>`).join("")}</section>
      <section class="card"><header class="card-head"><h2>回复策略</h2></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>识别其他机器人后不回复</strong><small>避免机器人之间互相触发，默认启用</small></span><label class="switch"><input type="checkbox" checked data-local-toggle><span></span></label></div><div class="setting-row"><span class="setting-copy"><strong>主动回复不截断</strong><small>与被动触发使用相同语义阈值，不设置额外字数上限</small></span><span class="badge ok">已启用</span></div><div class="setting-row"><span class="setting-copy"><strong>主动回复聚合窗口</strong><small>候选消息最长等待时间</small></span><strong>60 秒</strong></div></div></section>
    </div>`;
}

function botView() {
  return `${pageHeader("机器人", "QQ 与 Telegram 可同时在线，会话和记忆可按需要隔离", `<button class="btn primary" type="button" data-action="save-bot">保存模拟配置</button>`)}
    <div class="stack"><section class="grid-two"><article class="card"><header class="card-head"><div class="card-title"><span class="channel-icon">Q</span><div><h2>QQ · OneBot v11</h2><span class="muted">演示账号 100000001</span></div></div><span class="badge ok">已连接</span></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>接收群聊与私聊</strong><small>连接地址和凭据已隐藏</small></span><label class="switch"><input type="checkbox" checked data-local-toggle><span></span></label></div><button class="btn" type="button" data-action="channel-test">▷ 测试 QQ 通道</button></div></article><article class="card"><header class="card-head"><div class="card-title"><span class="channel-icon">T</span><div><h2>Telegram Bot</h2><span class="muted">@diana_demo_bot</span></div></div><span class="badge ok">已连接</span></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>接收群聊与私聊</strong><small>Bot Token 已隐藏</small></span><label class="switch"><input type="checkbox" checked data-local-toggle><span></span></label></div><button class="btn" type="button" data-action="channel-test">▷ 测试 Telegram 通道</button></div></article></section>
      <section class="card"><header class="card-head"><h2>跨通道内容策略</h2><span class="badge info">可选隔离</span></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>隔离 QQ 与 Telegram 会话</strong><small>开启后，上下文、短期历史和回复链分别存储</small></span><label class="switch"><input type="checkbox" checked data-local-toggle><span></span></label></div><div class="setting-row"><span class="setting-copy"><strong>共享长期记忆</strong><small>只共享经过压缩与权限筛选的长期事实</small></span><label class="switch"><input type="checkbox" data-local-toggle><span></span></label></div></div></section>
      <div class="grid-two"><section class="card"><header class="card-head"><h2>上下文与记忆</h2></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>近期上下文</strong><small>按会话保留高相关原始消息</small></span><strong>40 条</strong></div><div class="setting-row"><span class="setting-copy"><strong>长期记忆检索</strong><small>结构化事实 + 摘要 + 超长历史混合召回</small></span><span class="badge ok">已启用</span></div></div></section><section class="card"><header class="card-head"><h2>管理员登录</h2></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>管理员验证码快速登录</strong><small>验证码一次性使用，过期后自动失效</small></span><span class="badge ok">可用</span></div><button class="btn" type="button" data-action="login-code">生成模拟验证码</button></div></section></div>
    </div>`;
}

function pluginsView() {
  const plugins = mock.plugins.filter((plugin) => {
    const byType = state.pluginFilter === "all" || (state.pluginFilter === "enabled" && plugin.enabled) || (state.pluginFilter === "disabled" && !plugin.enabled) || (state.pluginFilter === "needs" && plugin.needs);
    const query = state.pluginSearch.toLocaleLowerCase();
    return byType && (!query || `${plugin.name} ${plugin.desc} ${plugin.tags.join(" ")}`.toLocaleLowerCase().includes(query));
  });
  const enabled = mock.plugins.filter((plugin) => plugin.enabled).length;
  return `${pageHeader("插件", "查看内置与扩展能力、运行状态和配置入口", `<button class="btn" type="button" data-action="refresh-plugins">↻ 刷新</button>`)}
    <div class="stack"><section class="card"><div class="card-body toolbar"><div class="segmented"><button type="button" class="${state.pluginFilter === "all" ? "active" : ""}" data-plugin-filter="all">全部 ${mock.plugins.length}</button><button type="button" class="${state.pluginFilter === "enabled" ? "active" : ""}" data-plugin-filter="enabled">已启用 ${enabled}</button><button type="button" class="${state.pluginFilter === "disabled" ? "active" : ""}" data-plugin-filter="disabled">已停用</button><button type="button" class="${state.pluginFilter === "needs" ? "active" : ""}" data-plugin-filter="needs">需配置</button></div><input class="input search-input" type="search" placeholder="搜索插件名称或能力" value="${escapeHTML(state.pluginSearch)}" data-plugin-search /></div></section>
      <section class="plugin-grid">${plugins.map((plugin) => `<article class="plugin-card"><div class="plugin-top"><span class="plugin-icon ${plugin.color}">${plugin.icon}</span><div class="plugin-title"><strong>${plugin.name}</strong><small>${plugin.version}</small></div><label class="switch"><input type="checkbox" ${plugin.enabled ? "checked" : ""} data-plugin-toggle="${plugin.id}"><span></span></label></div><p class="plugin-desc">${plugin.desc}</p><div class="tag-list">${plugin.tags.map((tag) => `<span class="badge">${tag}</span>`).join("")}</div><footer class="plugin-foot"><span class="${plugin.enabled ? "text-ok" : "muted"}">${plugin.enabled ? "● 运行正常" : "○ 已停用"}${plugin.needs ? " · 需配置 Token" : ""}</span><button class="btn small" type="button" data-plugin-settings="${plugin.id}">⚙ 设置</button></footer></article>`).join("") || `<div class="card"><div class="card-body muted">没有匹配的模拟插件。</div></div>`}</section></div>`;
}

function groupsView() {
  const group = mock.groups.find((item) => item.id === state.selectedGroup) || mock.groups[0];
  return `${pageHeader("群管理", "自动列出机器人加入的全部群，并为每个群保存独立回复策略", `<button class="btn" type="button" data-action="sync-groups">↻ 同步群列表</button><button class="btn primary" type="button" data-action="save-group">保存模拟配置</button>`)}
    <div class="group-layout"><section class="card"><header class="card-head"><h2>全部群聊</h2><span class="badge ok">${mock.groups.length} 个</span></header><div class="card-body group-list">${mock.groups.map((item) => `<button type="button" class="group-row ${item.id === group.id ? "active" : ""}" data-group-id="${item.id}"><span class="group-avatar">群</span><span class="group-main"><strong>${item.name}</strong><small>${item.id} · ${item.members} 名成员</small></span>${badge(item.enabled ? "running" : "disabled")}</button>`).join("")}</div></section>
      <section class="card"><header class="card-head"><div><h2>${group.name}</h2><span class="muted mono">群号 ${group.id}</span></div><label class="switch"><input type="checkbox" ${group.enabled ? "checked" : ""} data-local-toggle><span></span></label></header><div class="card-body form-grid"><div class="field"><label>允许回复时间</label><input class="input" value="${group.window}" /></div><div class="field"><label>触发关键词</label><input class="input" value="${group.triggers}" placeholder="用逗号分隔" /></div><div class="field wide"><label>屏蔽 QQ 号</label><input class="input mono" value="${group.blocked}" placeholder="多个 QQ 号用逗号分隔" /></div><div class="field wide"><label>群级 Prompt</label><textarea class="textarea" rows="4">${group.prompt}</textarea></div><div class="field"><label>主动回复</label><select class="select"><option>与被动触发使用相同阈值</option><option>仅显式触发</option></select></div><div class="field"><label>可用工具</label><select class="select"><option>联网搜索、记忆、文件解析</option><option>仅联网搜索</option></select></div></div></section></div>`;
}

function settingsView() {
  return `${pageHeader("设置", "系统运行、调试、备份和手动更新策略", `<button class="btn primary" type="button" data-action="save-settings">保存模拟设置</button>`)}
    <div class="stack"><div class="grid-two"><section class="card"><header class="card-head"><h2>运行资源</h2><span class="badge ok">健康</span></header><div class="card-body resource-list"><div class="resource-row"><span>CPU</span><span class="meter"><i style="width:23%"></i></span><strong>23%</strong></div><div class="resource-row"><span>内存</span><span class="meter blue"><i style="width:46%"></i></span><strong>46%</strong></div><div class="resource-row"><span>存储</span><span class="meter amber"><i style="width:61%"></i></span><strong>61%</strong></div></div></section><section class="card"><header class="card-head"><h2>数据库与备份</h2></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>SQLite 数据库</strong><small>最近备份：今天 03:00</small></span><strong>28.6 MB</strong></div><button class="btn" type="button" data-action="backup">⇩ 创建模拟备份</button></div></section></div>
      <section class="card"><header class="card-head"><div><h2>版本更新</h2><span class="muted">当前版本 v0.8.5-demo</span></div><span class="badge warn">● 发现新版本</span></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>仅提示，不自动更新</strong><small>检测到 Release 后只显示小黄点；必须点击并确认才执行下载、校验、备份、切换和健康检查</small></span><span class="badge warn">v0.8.6</span></div><button class="btn" type="button" data-action="update-confirm">查看模拟更新确认</button></div></section>
      <section class="card"><header class="card-head"><h2>调试与隐私</h2></header><div class="card-body setting-list"><div class="setting-row"><span class="setting-copy"><strong>调试模式</strong><small>在事件明细中显示模型上下文、工具参数、耗时与完整请求调用链</small></span><label class="switch"><input type="checkbox" data-local-toggle><span></span></label></div><div class="setting-row"><span class="setting-copy"><strong>错误消息经 LLM 处理</strong><small>保留原始错误供模型理解，再按机器人 Prompt 生成用户可读回复</small></span><label class="switch"><input type="checkbox" checked data-local-toggle><span></span></label></div><div class="setting-row"><span class="setting-copy"><strong>重新加载配置</strong><small>只重新读取配置，不重新计算历史统计和 token</small></span><button class="btn small" type="button" data-action="reload-config">↻ 重新加载</button></div></div></section>
    </div>`;
}

const views = { dashboard: dashboardView, events: eventsView, tasks: tasksView, llm: llmView, bot: botView, plugins: pluginsView, groups: groupsView, settings: settingsView };

function currentRoute() {
  const route = window.location.hash.replace(/^#\/?/, "");
  return views[route] ? route : "dashboard";
}

function renderNavigation() {
  const current = currentRoute();
  navRoot.innerHTML = navigation.map(([id, glyph, title, note]) => `<button class="nav-item ${id === current ? "active" : ""}" type="button" data-route="${id}"><span class="nav-glyph">${glyph}</span><span class="nav-copy"><strong>${title}</strong><small>${note}</small></span></button>`).join("");
}

function render() {
  const route = currentRoute();
  const entry = navigation.find(([id]) => id === route);
  viewTitle.textContent = entry[2];
  document.title = `Diana WebUI - ${entry[2]}（模拟演示）`;
  renderNavigation();
  viewRoot.innerHTML = views[route]();
  setSidebar(false);
  window.scrollTo({ top: 0, behavior: "instant" });
}

function openEvent(event) {
  modalTitle.textContent = `${event.platform} ${event.kind} · ${event.status === "replied" ? "已回复" : "未回复"}`;
  modalBody.innerHTML = `<div class="stack"><section><span class="eyebrow">原始消息</span><p class="event-message">${escapeHTML(event.text)}</p><div class="event-meta" style="margin-top:8px">${badge(event.status)}<span class="muted">${escapeHTML(event.target)} · ${event.time} · ${event.duration}</span></div></section><section class="card"><header class="card-head"><h2>${event.status === "replied" ? "为什么回复" : "为什么不回复"}</h2></header><div class="card-body"><p>${escapeHTML(event.reason)}</p></div></section><section class="card"><header class="card-head"><h2>最终结果</h2><span class="badge info">${event.tokens.toLocaleString("zh-CN")} tokens</span></header><div class="card-body"><p>${escapeHTML(event.reply)}</p></div></section><section><span class="eyebrow">Agent 调用链</span><div class="trace" style="margin-top:9px">${event.trace.map(([name, detail], index) => `<div class="trace-step"><span class="trace-index">${index + 1}</span><div class="trace-content"><strong>${name}</strong><p>${escapeHTML(detail)}</p></div></div>`).join("")}</div></section><section><span class="eyebrow">模型上下文（模拟）</span><div class="code-box" style="margin-top:9px">system: 以当前机器人 Prompt 回复，不泄露密钥。\ncontext: 最近相关消息 8 条，长期记忆命中 2 条。\ntools: web_search, memory_search, message_send\nrequest_id: demo-${event.id}</div></section></div>`;
  modalBackdrop.hidden = false;
  document.body.style.overflow = "hidden";
}

function closeModal() {
  modalBackdrop.hidden = true;
  document.body.style.overflow = "";
}

function openSimpleModal(title, content) {
  modalTitle.textContent = title;
  modalBody.innerHTML = content;
  modalBackdrop.hidden = false;
  document.body.style.overflow = "hidden";
}

document.addEventListener("click", (event) => {
  const routeButton = event.target.closest("[data-route]");
  if (routeButton) {
    window.location.hash = `#/${routeButton.dataset.route}`;
    return;
  }
  const eventButton = event.target.closest("[data-event-id]");
  if (eventButton) {
    const item = mock.events.find((entry) => entry.id === eventButton.dataset.eventId);
    if (item) openEvent(item);
    return;
  }
  const rangeButton = event.target.closest("[data-event-range]");
  if (rangeButton) { state.eventRange = rangeButton.dataset.eventRange; render(); return; }
  const statusButton = event.target.closest("[data-event-status]");
  if (statusButton) { state.eventStatus = statusButton.dataset.eventStatus; render(); return; }
  const taskFilter = event.target.closest("[data-task-filter]");
  if (taskFilter) { state.taskFilter = taskFilter.dataset.taskFilter; render(); return; }
  const pluginFilter = event.target.closest("[data-plugin-filter]");
  if (pluginFilter) { state.pluginFilter = pluginFilter.dataset.pluginFilter; render(); return; }
  const groupButton = event.target.closest("[data-group-id]");
  if (groupButton) { state.selectedGroup = groupButton.dataset.groupId; render(); return; }
  const taskButton = event.target.closest("[data-task-id]");
  if (taskButton) {
    const task = mock.tasks.find((entry) => entry.id === Number(taskButton.dataset.taskId));
    openSimpleModal(task.name, `<div class="setting-list"><div class="setting-row"><span class="setting-copy"><strong>任务类型</strong><small>${task.type}</small></span>${badge(task.status)}</div><div class="setting-row"><span class="setting-copy"><strong>执行周期</strong><small>${task.schedule}</small></span><strong>${task.target}</strong></div><p class="muted">这是模拟任务，不会发送真实提醒。</p></div>`);
    return;
  }
  const settingsButton = event.target.closest("[data-plugin-settings]");
  if (settingsButton) {
    const plugin = mock.plugins.find((item) => item.id === settingsButton.dataset.pluginSettings);
    openSimpleModal(`${plugin.name} · 模拟配置`, `<div class="form-grid"><div class="field wide"><label>运行状态</label><input class="input" value="${plugin.enabled ? "运行正常" : "已停用"}" readonly /></div><div class="field wide"><label>配置边界</label><textarea class="textarea" rows="4" readonly>演示页不读取密钥，也不会调用插件后端。真实 WebUI 会按插件声明呈现所需配置。</textarea></div></div>`);
    return;
  }
  const actionButton = event.target.closest("[data-action]");
  if (actionButton) {
    const action = actionButton.dataset.action;
    if (action === "new-task") {
      openSimpleModal("新建模拟任务", `<div class="form-grid"><div class="field"><label>任务名称</label><input class="input" value="演示提醒任务" /></div><div class="field"><label>类型</label><select class="select"><option>一次性提醒</option><option>周期任务</option><option>仓库订阅</option></select></div><div class="field"><label>发送通道</label><select class="select"><option>QQ 群聊</option><option>QQ 私聊</option><option>Telegram</option></select></div><div class="field"><label>发送对象</label><input class="input" value="100200301" /></div><div class="field wide"><button class="btn primary" type="button" data-modal-demo-save>创建到演示列表</button></div></div>`);
    } else if (action === "update-confirm") {
      openSimpleModal("确认模拟更新", `<div class="stack"><div class="callout"><strong>v0.8.6 可用</strong><p>确认后，真实服务才会下载对应平台完整包、校验 SHA-256、备份数据库与旧版本、切换并健康检查。本演示不会下载任何文件。</p></div><button class="btn primary" type="button" data-modal-demo-update>确认演示更新</button></div>`);
    } else if (action === "login-code") {
      openSimpleModal("管理员快速登录", `<div class="stack"><p class="muted">一次性模拟验证码，有效期 5 分钟。</p><div class="code-box" style="font-size:24px;text-align:center;color:var(--text)">482 731</div></div>`);
    } else {
      const labels = { refresh: "总览数据已在本页刷新", "export-events": "演示记录不会导出真实数据", "sync-models": "已同步 18 个模拟模型", "save-models": "模型配置已在本页保存", "model-test": `${actionButton.dataset.modelRole || "模型"}模拟测试通过`, "save-bot": "机器人配置已在本页保存", "channel-test": "模拟通道连接正常", "refresh-plugins": "插件状态已刷新", "sync-groups": "已同步 4 个模拟群聊", "save-group": "群配置已在本页保存", "save-settings": "系统设置已在本页保存", backup: "模拟备份已创建", "reload-config": "配置已重新加载，历史统计未重新计算" };
      showToast(`模拟模式：${labels[action] || "操作只在本页生效"}`);
    }
  }
});

document.addEventListener("input", (event) => {
  if (event.target.matches("[data-plugin-search]")) {
    state.pluginSearch = event.target.value;
    const position = event.target.selectionStart;
    render();
    const next = document.querySelector("[data-plugin-search]");
    next.focus();
    next.setSelectionRange(position, position);
  }
});

document.addEventListener("change", (event) => {
  if (event.target.matches("[data-plugin-toggle]")) {
    const plugin = mock.plugins.find((item) => item.id === event.target.dataset.pluginToggle);
    plugin.enabled = event.target.checked;
    showToast(`模拟模式：${plugin.name}${plugin.enabled ? "已启用" : "已停用"}`);
    render();
  } else if (event.target.matches("[data-local-toggle]")) {
    showToast("模拟模式：开关状态只在本页生效");
  }
});

document.addEventListener("click", (event) => {
  if (event.target.closest("[data-modal-close]") || event.target === modalBackdrop) closeModal();
  if (event.target.closest("[data-modal-demo-save]")) {
    mock.tasks.unshift({ id: Date.now(), icon: "◷", name: "演示提醒任务", type: "一次性提醒", schedule: "今天 18:30", target: "QQ 群 · 100200301", status: "running" });
    closeModal();
    render();
    showToast("模拟模式：任务已添加到本页列表");
  }
  if (event.target.closest("[data-modal-demo-update]")) {
    closeModal();
    showToast("模拟模式：更新流程演示完成，未下载任何文件");
  }
});

document.querySelector("[data-menu]").addEventListener("click", () => setSidebar(true));
scrim.addEventListener("click", () => setSidebar(false));
document.querySelector("[data-modal-close]").addEventListener("click", closeModal);
document.addEventListener("keydown", (event) => { if (event.key === "Escape") { closeModal(); setSidebar(false); } });

const savedTheme = window.localStorage.getItem("diana-demo-theme");
const preferredDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
root.dataset.theme = savedTheme || (preferredDark ? "dark" : "light");
document.querySelector("[data-theme-toggle]").textContent = root.dataset.theme === "dark" ? "☼" : "☾";
document.querySelector("[data-theme-toggle]").addEventListener("click", (event) => {
  root.dataset.theme = root.dataset.theme === "dark" ? "light" : "dark";
  event.currentTarget.textContent = root.dataset.theme === "dark" ? "☼" : "☾";
  window.localStorage.setItem("diana-demo-theme", root.dataset.theme);
});

window.addEventListener("hashchange", render);
if (!window.location.hash) window.location.hash = "#/dashboard";
render();
