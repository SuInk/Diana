<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="admin-chat-view">
    <div class="chat-workspace" :class="{ 'show-sessions': showSessions }">
      <aside id="admin-chat-sessions" class="chat-sidebar" aria-label="管理会话列表">
        <div class="chat-sidebar-heading">
          <h2>会话</h2>
          <div class="chat-room-actions">
            <button class="chat-icon chat-mobile-back" type="button" :disabled="busy || loading || creating" aria-label="新建管理会话" title="新建会话" @click="createConversation"><SquarePen :size="16" aria-hidden="true" /></button>
            <button class="chat-icon" type="button" aria-label="搜索会话" :aria-expanded="showSessionSearch" title="搜索会话" @click="toggleSessionSearch"><Search :size="16" aria-hidden="true" /></button>
            <button class="chat-icon chat-mobile-back" type="button" aria-label="返回对话" @click="showSessions = false"><PanelLeft :size="16" aria-hidden="true" /></button>
          </div>
        </div>
        <div v-if="showSessionSearch" class="chat-session-search"><Search :size="14" aria-hidden="true" /><input ref="sessionSearchInput" v-model="sessionSearch" class="input" type="search" aria-label="搜索管理会话" placeholder="搜索会话" @keydown.esc="hideSessionSearch" /></div>
        <div class="chat-session-list">
          <div v-if="loading && !conversations.length" class="chat-loading" role="status" aria-label="正在加载管理会话"><SkeletonBlock v-for="i in 3" :key="i" :width="`${90 - i * 10}%`" height="12px" /></div>
          <button v-for="item in filteredSessions" :key="item.session_id" class="chat-session-row" :class="{ selected: item.session_id === selectedSessionID }" :aria-current="item.session_id === selectedSessionID ? 'true' : undefined" :disabled="busy || creating" :title="item.preview" @click="selectConversation(item.session_id)">
            <span>{{ item.title }}</span><LoaderCircle v-if="item.running" :size="13" class="chat-spinner" aria-label="执行中" />
          </button>
          <p v-if="sessionSearch && !filteredSessions.length && !loading" class="chat-list-empty muted">没有匹配的会话</p>
        </div>
      </aside>
      <section class="chat-main" aria-label="管理助手">
        <header class="chat-room-header">
          <div class="chat-room-title">
            <button class="chat-icon" type="button" :aria-label="showSessions ? '收起会话列表' : '展开会话列表'" :aria-expanded="showSessions" aria-controls="admin-chat-sessions" :title="showSessions ? '收起会话列表' : '展开会话列表'" @click="showSessions = !showSessions"><PanelLeft :size="17" aria-hidden="true" /></button>
            <h2>{{ session?.title || '新会话' }}</h2>
          </div>
          <div class="chat-room-actions">
            <button class="chat-icon" type="button" :disabled="busy || loading || creating" aria-label="新建管理会话" title="新建会话" @click="createConversation"><LoaderCircle v-if="creating" :size="17" class="chat-spinner" aria-hidden="true" /><SquarePen v-else :size="17" aria-hidden="true" /></button>
            <details ref="moreMenu" class="chat-menu" @toggle="onMenuToggle($event, 'more')" @keydown.esc.prevent.stop="closeMenus(true)">
              <summary class="chat-icon" role="button" :aria-expanded="moreOpen" aria-label="更多操作" title="更多操作"><Ellipsis :size="18" aria-hidden="true" /></summary>
              <div class="chat-menu-items chat-menu-right">
                <button type="button" :disabled="busy || loading || !session" @click="usePrompt(suggestions[1].prompt)"><Activity :size="15" aria-hidden="true" />运行诊断</button>
                <button type="button" :disabled="busy || loading || !session" @click="usePrompt(suggestions[2].prompt)"><Blocks :size="15" aria-hidden="true" />检查 Skills / MCP</button>
                <button type="button" :disabled="busy || loading || !lastUserMessage" @click="usePrompt(lastUserMessage)"><RotateCcw :size="15" aria-hidden="true" />载入上一条</button>
                <button type="button" :disabled="!session" @click="openPanel('permissions')"><ShieldCheck :size="15" aria-hidden="true" />执行权限</button>
                <div class="chat-menu-divider" />
                <button class="chat-menu-danger" type="button" :disabled="busy || loading || !messages.length" @click="closeMenus(); clear()"><Trash2 :size="15" aria-hidden="true" />清空会话</button>
              </div>
            </details>
          </div>
        </header>
        <div v-if="profilesError" class="chat-error chat-banner" role="alert"><span>{{ profilesError }}</span><button class="btn ghost small" @click="loadProfiles">重试</button></div>
        <div v-if="error" class="chat-error chat-banner" role="alert"><TriangleAlert :size="16" aria-hidden="true" /><span>{{ error }}</span><button class="btn ghost small" type="button" :disabled="busy" @click="load">重新加载</button></div>
        <section ref="transcript" class="chat-transcript" aria-label="管理对话记录" aria-live="polite" :aria-busy="busy || loading" @scroll="onTranscriptScroll">
          <div v-if="loading && !session" class="chat-loading" role="status" aria-label="正在加载管理对话"><SkeletonBlock width="58%" height="14px" /><SkeletonBlock width="90%" height="14px" /><SkeletonBlock width="78%" height="14px" /></div>
          <div v-if="!messages.length && session" class="chat-empty">
            <h3>想处理什么？</h3>
            <div class="chat-suggestions">
              <button type="button" :disabled="busy || loading" @click="usePrompt(suggestions[1].prompt)"><Activity :size="15" aria-hidden="true" />排查问题</button>
              <button type="button" :disabled="busy || loading" @click="openPanel('history')"><Search :size="15" aria-hidden="true" />搜索群聊</button>
            </div>
          </div>
          <article v-for="message in messages" :key="message.id" class="chat-message" :class="[message.role, { failed: message.error }]" :aria-label="message.role === 'user' ? '你的消息' : '管理助手回复'">
            <div class="chat-message-content">{{ message.content }}</div>
            <details v-if="message.steps?.length" class="chat-steps"><summary>执行记录 · {{ message.steps.length }} 次工具调用</summary><div v-for="(step, i) in message.steps" :key="i" class="chat-step"><span class="mono">{{ step.tool }}</span><div class="cluster"><span class="badge" :class="step.failed ? 'err' : 'ok'">{{ step.failed ? '未完成' : '已完成' }}</span><span class="muted">{{ step.duration_ms || 0 }} ms</span></div></div></details>
            <div class="chat-message-meta">
              <button v-if="message.role === 'assistant'" class="chat-icon chat-copy" type="button" :aria-label="copiedID === message.id ? '回复已复制' : '复制回复'" :title="copiedID === message.id ? '已复制' : '复制回复'" @click="copyReply(message)"><Check v-if="copiedID === message.id" :size="14" aria-hidden="true" /><Copy v-else :size="14" aria-hidden="true" /></button>
              <time v-if="message.created_at" :datetime="message.created_at">{{ formatTime(message.created_at) }}</time>
            </div>
          </article>
          <div v-if="busy" class="chat-working" role="status"><LoaderCircle :size="15" class="chat-spinner" aria-hidden="true" />{{ progressLabel }}</div>
        </section>
        <div class="chat-composer-area">
          <button v-if="!followTranscript && messages.length" class="chat-latest" type="button" @click="scroll(true)"><ArrowDown :size="14" aria-hidden="true" />回到最新消息</button>
          <form class="chat-composer" @submit.prevent="send">
            <label class="sr-only" for="admin-chat-input">发给管理助手</label>
            <textarea id="admin-chat-input" ref="composer" v-model="draft" rows="2" maxlength="12000" placeholder="描述问题，或交给我一个任务…" :disabled="!session || busy || loading || creating" @keydown="onKeydown" />
            <div class="chat-composer-actions">
              <details ref="addMenu" class="chat-menu" @toggle="onMenuToggle($event, 'add')" @keydown.esc.prevent.stop="closeMenus(true)">
                <summary class="chat-icon" role="button" :aria-expanded="addOpen" aria-label="添加操作" title="添加操作"><Plus :size="18" aria-hidden="true" /></summary>
                <div class="chat-menu-items chat-menu-up">
                  <button type="button" :disabled="busy || loading || !session" @click="openPanel('history')"><Search :size="15" aria-hidden="true" />搜索群聊记录</button>
                  <button type="button" :disabled="busy || loading || !session" @click="usePrompt(suggestions[3].prompt)"><Blocks :size="15" aria-hidden="true" />安装 Sub2API Skill</button>
                </div>
              </details>
              <label class="sr-only" for="admin-chat-profile">管理机器人</label>
              <AppSelect id="admin-chat-profile" class="chat-profile" :model-value="selectedProfile" :options="profileOptions" :disabled="busy || creating || profilesLoading" searchable search-placeholder="搜索机器人名称、平台或 ID" @update:model-value="selectProfile" />
              <label class="sr-only" for="admin-chat-model">管理对话模型</label>
              <AppSelect id="admin-chat-model" class="chat-profile chat-model" :model-value="selectedModel" :options="modelOptions" :disabled="busy || creating" searchable search-placeholder="搜索模型或提供商" @update:model-value="selectModel" />
              <button v-if="busy" class="chat-send" type="button" :disabled="stopping" :aria-label="stopping ? '正在停止' : '停止'" :title="stopping ? '正在停止…' : '停止'" @click="stop"><LoaderCircle v-if="stopping" :size="16" class="chat-spinner" aria-hidden="true" /><Square v-else :size="14" aria-hidden="true" /></button>
              <button v-else class="chat-send" type="submit" :disabled="!session || loading || creating || !draft.trim()" aria-label="发送" title="发送 · Enter"><ArrowUp :size="18" aria-hidden="true" /></button>
            </div>
          </form>
        </div>
      </section>
    </div>
    <Modal v-if="showHistorySearch" title="搜索群聊记录" @close="closePanel">
      <form id="admin-chat-history-form" ref="historyPanel" class="chat-history-search" @submit.prevent="searchHistory">
        <p class="muted">{{ selectedProfileLabel }} · 仅查询 Diana 已保存的记录</p>
        <div class="field"><label for="admin-chat-history-group">群号</label><input id="admin-chat-history-group" v-model="historyGroup" class="input" maxlength="100" placeholder="留空搜索全部群" :disabled="busy" /></div>
        <div class="field"><label for="admin-chat-history-keyword">关键词</label><input id="admin-chat-history-keyword" v-model="historySearch" class="input" maxlength="200" placeholder="留空查看最近消息" :disabled="busy" /></div>
        <div class="field"><label for="admin-chat-history-days">时间范围</label><AppSelect id="admin-chat-history-days" v-model="historyDays" :options="historyDayOptions" :disabled="busy" /></div>
      </form>
      <template #footer><button class="btn ghost" type="button" @click="closePanel">取消</button><button class="btn primary" type="submit" form="admin-chat-history-form" :disabled="busy || loading || !session"><Search :size="15" aria-hidden="true" />查询群聊</button></template>
    </Modal>
    <Modal v-if="showPermissions && session" title="执行权限" @close="closePanel">
      <div ref="permissionsPanel" class="chat-capabilities">
        <p class="muted">{{ selectedProfileLabel }}</p>
        <dl><dt>模式</dt><dd>{{ session.agent_mode === 'safe' ? '安全模式' : '标准模式' }}</dd><dt>文件</dt><dd>{{ session.file_write_enabled ? '可写工作目录' : '只读' }}</dd><dt>命令白名单</dt><dd>{{ session.command_allowlist?.join('、') || '未开放命令执行' }}</dd><dt>沙盒</dt><dd>{{ session.sandbox }}</dd><dt>命令网络</dt><dd>{{ session.network_enabled ? '允许' : '关闭' }}</dd></dl>
        <p class="muted">权限跟随所选机器人。安装与配置变更需确认，凭据在扩展设置中填写。</p>
      </div>
      <template #footer><button class="btn" type="button" @click="closePanel">关闭</button></template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, ref } from 'vue';
import { Activity, ArrowDown, ArrowUp, Blocks, Check, Copy, Ellipsis, LoaderCircle, PanelLeft, Plus, RotateCcw, Search, ShieldCheck, Square, SquarePen, Trash2, TriangleAlert } from '@lucide/vue';
import { botScope } from '../bot-scope';
import AppSelect from '../components/AppSelect.vue';
import Modal from '../components/Modal.vue';
import SkeletonBlock from '../components/SkeletonBlock.vue';
import { getBotProfileConfig, getConfig, type BotProfileConfig, type LLMConfig } from '../api';
import { askConfirm } from '../confirm';
import { useConfigurationRefresh } from '../configuration-sync';
import { clearAdminChat, createAdminChatSession, filterAdminChatSessions, getAdminChat, listAdminChatSessions, sendAdminChat, stopAdminChat, type AdminChatMessage, type AdminChatProgress, type AdminChatSession, type AdminChatSummary } from '../admin-chat';
const session = ref<AdminChatSession | null>(null), conversations = ref<AdminChatSummary[]>([]);
const profileStorageKey = 'diana:admin-chat-profile';
function initialProfile(): string {
    try {
        return sessionStorage.getItem(profileStorageKey) ?? botScope.value;
    } catch {
        return botScope.value;
    }
}
function rememberedSession(profile: string): string {
    try {
        return sessionStorage.getItem(`diana:admin-chat-session:${profile}`) || '';
    } catch {
        return '';
    }
}
const selectedProfile = ref(initialProfile()), selectedSessionID = ref(rememberedSession(selectedProfile.value));
const profiles = ref<BotProfileConfig[]>([]), profilesLoading = ref(false), profilesError = ref('');
const profileOptions = computed(() => [{ value: '', label: '全部机器人', hint: '跨机器人查询群聊；使用默认模型和权限' }, ...profiles.value.filter(p => p.id).map(p => ({ value: p.id!, label: p.name || p.id!, hint: `${p.platform || '未知平台'} · ${p.id}`, avatar: p.avatar_url }))]);
const selectedProfileLabel = computed(() => profileOptions.value.find(p => p.value === selectedProfile.value)?.label || selectedProfile.value);
const modelStorageKey = 'diana:admin-chat-model', modelPairSep = '::';
const llmChannels = ref<LLMConfig[]>([]), selectedModel = ref(readStored(modelStorageKey));
// 只列能出文字的模型；目录没标模态的照样列出，自建网关的模型常常不带这些字段。
const modelOptions = computed(() => [{ value: '', label: '跟随机器人', hint: '使用机器人模型分配里的设置' }, ...llmChannels.value.filter(c => c.id).flatMap(channel => {
  const ids = new Map((channel.models ?? []).filter(m => m.id && (!m.output_modalities?.length || m.output_modalities.includes('text'))).map(m => [m.id, m.name && m.name !== m.id ? `${m.name} (${m.id})` : m.id]));
  if (channel.model && channel.group !== 'image' && !ids.has(channel.model)) ids.set(channel.model, channel.model);
  return [...ids].map(([id, label]) => ({ value: `${channel.id}${modelPairSep}${id}`, label, hint: channel.name || channel.provider }));
})]);
const sessionSearch = ref(''), filteredSessions = computed(() => filterAdminChatSessions(conversations.value, sessionSearch.value));
const messages = ref<AdminChatMessage[]>([]), draft = ref(''), error = ref(''), copiedID = ref('');
const loading = ref(false), creating = ref(false), sending = ref(false), stopping = ref(false), remoteRunning = ref(false);
const showSessions = ref(window.matchMedia('(min-width: 721px)').matches), showSessionSearch = ref(false), showHistorySearch = ref(false), showPermissions = ref(false), followTranscript = ref(true);
const sessionSearchInput = ref<HTMLInputElement | null>(null), moreMenu = ref<HTMLDetailsElement | null>(null), addMenu = ref<HTMLDetailsElement | null>(null);
const moreOpen = ref(false), addOpen = ref(false);
const historyPanel = ref<HTMLElement | null>(null), permissionsPanel = ref<HTMLElement | null>(null);
let panelReturnFocus: HTMLElement | null = null;
const historyGroup = ref(''), historySearch = ref(''), historyDays = ref('7');
const historyDayOptions = [{ value: '1', label: '最近 24 小时' }, { value: '7', label: '最近 7 天' }, { value: '30', label: '最近 30 天' }];
const progress = ref<AdminChatProgress | null>(null), transcript = ref<HTMLElement | null>(null), composer = ref<HTMLTextAreaElement | null>(null);
const busy = computed(() => sending.value || remoteRunning.value);
const progressLabel = computed(() => progress.value?.phase === 'tool_started' ? `正在执行 ${progress.value.tool}…` : '管理助手正在处理…');
const lastUserMessage = computed(() => [...messages.value].reverse().find(m => m.role === 'user')?.content || '');
const suggestions = [
    { label: '搜索群聊记录', prompt: '搜索最近 7 天群聊中提到「MCP」的消息，列出群号和时间' },
    { label: '运行诊断', prompt: '检查当前运行状态和最近的错误' },
    { label: '检查 Skills / MCP', prompt: '列出已安装的 Skills 和 MCP，检查哪些不可用' },
    { label: '安装 Sub2API Skill', prompt: '安装 Sub2API 官方管理 Skill，保留它的脚本和资源' }
];
let controller: AbortController | null = null, loadController: AbortController | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;
let active = false;
function formatTime(value?: string): string {
    if (!value) return '';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '' : new Intl.DateTimeFormat(undefined, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(date);
}
function onMenuToggle(event: Event, menu: 'more' | 'add'): void {
    const open = (event.target as HTMLDetailsElement).open;
    (menu === 'more' ? moreOpen : addOpen).value = open;
    const other = menu === 'more' ? addMenu.value : moreMenu.value;
    if (open && other) other.open = false;
}
function closeMenus(restoreFocus = false): void {
    for (const menu of [moreMenu.value, addMenu.value]) {
        if (!menu?.open) continue;
        menu.open = false;
        if (restoreFocus) menu.querySelector('summary')?.focus();
    }
}
function onOutsideClick(event: PointerEvent): void {
    if (!(event.target instanceof Node)) return;
    for (const menu of [moreMenu.value, addMenu.value]) {
        if (menu && !menu.contains(event.target)) menu.open = false;
    }
}
async function toggleSessionSearch(): Promise<void> {
    showSessionSearch.value = !showSessionSearch.value;
    if (!showSessionSearch.value) sessionSearch.value = '';
    await nextTick();
    sessionSearchInput.value?.focus();
}
function hideSessionSearch(): void {
    showSessionSearch.value = false;
    sessionSearch.value = '';
}
async function usePrompt(prompt: string): Promise<void> {
    closeMenus();
    draft.value = prompt;
    await nextTick();
    composer.value?.focus();
}
async function openPanel(panel: 'history' | 'permissions'): Promise<void> {
    const menu = panel === 'history' ? addMenu.value : moreMenu.value;
    panelReturnFocus = menu?.open ? menu.querySelector('summary') : document.activeElement as HTMLElement;
    closeMenus();
    showHistorySearch.value = panel === 'history';
    showPermissions.value = panel === 'permissions';
    await nextTick();
    const content = historyPanel.value || permissionsPanel.value;
    const firstInput = content?.querySelector<HTMLElement>('input');
    (firstInput || content?.closest('[role="dialog"]')?.querySelector<HTMLElement>('button'))?.focus();
}
async function closePanel(): Promise<void> {
    showHistorySearch.value = false;
    showPermissions.value = false;
    await nextTick();
    if (panelReturnFocus?.isConnected) panelReturnFocus.focus();
    else composer.value?.focus();
    panelReturnFocus = null;
}
function onPanelKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Tab' || (!showHistorySearch.value && !showPermissions.value)) return;
    const dialog = (historyPanel.value || permissionsPanel.value)?.closest('[role="dialog"]');
    if (!dialog?.contains(event.target as Node)) return;
    const controls = Array.from(dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), [tabindex="0"]'));
    const first = controls[0], last = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === first && last) {
        event.preventDefault(); last.focus();
    } else if (!event.shiftKey && document.activeElement === last && first) {
        event.preventDefault(); first.focus();
    }
}
function hideMobileSessions(): void {
    if (window.matchMedia('(max-width: 720px)').matches) showSessions.value = false;
}
function rememberSession(): void {
    try {
        sessionStorage.setItem(`diana:admin-chat-session:${selectedProfile.value}`, selectedSessionID.value);
    } catch { /* Browser storage is optional. */ }
}
function splitModel(value: string): [string, string?] {
  const at = value.indexOf(modelPairSep);
  return at < 0 ? [''] : [value.slice(0, at), value.slice(at + modelPairSep.length)];
}
function readStored(key: string): string {
  try { return localStorage.getItem(key) || ''; } catch { return ''; }
}
function selectModel(value: string): void {
  selectedModel.value = value;
  try { if (value) localStorage.setItem(modelStorageKey, value); else localStorage.removeItem(modelStorageKey); } catch { /* 仅本机记忆，失败不影响发送 */ }
}
async function loadModels(): Promise<void> {
  try {
    llmChannels.value = (await getConfig()).profiles ?? [];
    // 选中的模型被删了就回到跟随机器人，免得发出去才报错。
    if (selectedModel.value && !modelOptions.value.some(o => o.value === selectedModel.value)) selectModel('');
  } catch { llmChannels.value = []; }
}
async function loadProfiles(): Promise<void> {
    if (profilesLoading.value)
        return;
    profilesLoading.value = true;
    try {
        const config = await getBotProfileConfig();
        profiles.value = config.profiles?.length ? config.profiles : [config];
        profilesError.value = '';
        if (selectedProfile.value && !profiles.value.some(p => p.id === selectedProfile.value) && !busy.value)
            selectProfile('');
    }
    catch (e) {
        profilesError.value = e instanceof Error ? e.message : String(e);
    }
    finally {
        profilesLoading.value = false;
    }
}
function resetConversation(): void {
    clearTimeout(timer);
    session.value = null;
    messages.value = [];
    progress.value = null;
    error.value = '';
    draft.value = '';
    copiedID.value = '';
    followTranscript.value = true;
    closeMenus();
    showHistorySearch.value = false;
    showPermissions.value = false;
}
function selectProfile(profile: string): void {
    if (busy.value || creating.value || profile === selectedProfile.value)
        return;
    selectedProfile.value = profile;
    selectedSessionID.value = rememberedSession(profile);
    conversations.value = [];
    sessionSearch.value = '';
    historyGroup.value = '';
    try {
        sessionStorage.setItem(profileStorageKey, profile);
    }
    catch { /* Selection still works without browser storage. */ }
    resetConversation();
    void load();
}
function selectConversation(id: string): void {
    if (busy.value || creating.value) return;
    hideMobileSessions();
    if (id === selectedSessionID.value) return;
    selectedSessionID.value = id;
    rememberSession();
    resetConversation();
    void load();
}
async function createConversation(): Promise<void> {
    if (busy.value || loading.value || creating.value)
        return;
    creating.value = true;
    error.value = '';
    try {
        const created = await createAdminChatSession(selectedProfile.value);
        selectedSessionID.value = created.session_id;
        rememberSession();
        sessionSearch.value = '';
        resetConversation();
        hideMobileSessions();
        await load();
    }
    catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
    }
    finally {
        creating.value = false;
        await nextTick();
        if (active)
            composer.value?.focus();
    }
}
function onTranscriptScroll(): void {
    const area = transcript.value;
    if (area) followTranscript.value = area.scrollHeight - area.scrollTop - area.clientHeight < 64;
}
async function scroll(force = false): Promise<void> {
    if (force) followTranscript.value = true;
    await nextTick();
    if (followTranscript.value) transcript.value?.scrollTo({ top: transcript.value.scrollHeight, behavior: 'auto' });
}
async function load(): Promise<void> {
    if (sending.value || !active)
        return;
    loadController?.abort();
    const run = new AbortController();
    loadController = run;
    const profile = selectedProfile.value, requested = selectedSessionID.value;
    loading.value = true;
    const current = () => active && !run.signal.aborted && loadController === run && selectedProfile.value === profile && selectedSessionID.value === requested;
    try {
        const list = await listAdminChatSessions(profile, run.signal);
        if (!current())
            return;
        const id = list.some(s => s.session_id === requested) ? requested : list[0]?.session_id || '';
        const snapshot = await getAdminChat(profile, run.signal, id);
        if (!current())
            return;
        const freshList = await listAdminChatSessions(profile, run.signal);
        if (!current())
            return;
        session.value = snapshot;
        selectedSessionID.value = snapshot.session_id;
        rememberSession();
        conversations.value = freshList;
        messages.value = snapshot.messages || [];
        remoteRunning.value = snapshot.running;
        error.value = '';
        await scroll();
    }
    catch (e) {
        if (active && !run.signal.aborted && loadController === run)
            error.value = e instanceof Error ? e.message : String(e);
    }
    finally {
        if (loadController === run) {
            loadController = null;
            loading.value = false;
            schedulePoll();
        }
    }
}
function schedulePoll(): void {
    clearTimeout(timer);
    if (active && remoteRunning.value && !sending.value) timer = setTimeout(() => void load(), 1500);
}
async function send(): Promise<void> {
    if (!session.value || busy.value || loading.value || session.value.profile_id !== selectedProfile.value || !draft.value.trim())
        return;
    const text = draft.value.trim(), run = new AbortController();
    controller = run;
    sending.value = true;
    error.value = '';
    progress.value = null;
    followTranscript.value = true;
    let accepted = false, failure = '';
    try {
        await sendAdminChat(session.value.session_id, text, splitModel(selectedModel.value), run.signal, frame => {
            if (frame.type === 'user') {
                accepted = true;
                draft.value = '';
                messages.value.push(frame.message);
            }
            if (frame.type === 'message') messages.value.push(frame.message);
            if (frame.type === 'progress') progress.value = frame.progress;
            void scroll();
        });
    }
    catch (e) {
        if (!run.signal.aborted)
            failure = e instanceof Error ? e.message : String(e);
        if (!accepted)
            draft.value = text;
    }
    finally {
        if (controller === run)
            controller = null;
        sending.value = false;
        remoteRunning.value = false;
        stopping.value = false;
        if (active)
            await load();
        if (failure && active)
            error.value = failure;
    }
}
async function searchHistory(): Promise<void> {
    if (busy.value || loading.value || !session.value) return;
    const group = historyGroup.value.trim(), keyword = historySearch.value.trim();
    draft.value = `搜索${group ? `群 ${group}` : '当前机器人范围的群聊'} 最近 ${historyDays.value} 天${keyword ? `提到「${keyword}」的消息` : '的消息'}，列出发送者、群号、消息编号和时间；有更多结果时继续分页查询。`;
    await closePanel();
    await send();
}
async function copyReply(message: AdminChatMessage): Promise<void> {
    try {
        await navigator.clipboard.writeText(message.content);
        copiedID.value = message.id;
    } catch {
        error.value = '复制失败，请选中回复文字后复制';
    }
}
async function stop(): Promise<void> {
    if (!session.value) return;
    stopping.value = true;
    try {
        await stopAdminChat(session.value.session_id);
        if (!sending.value) await load();
    } catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
    } finally {
        stopping.value = false;
    }
}
async function clear(): Promise<void> {
    if (!session.value || busy.value)
        return;
    const id = session.value.session_id;
    if (!await askConfirm({ title: '清空这个管理会话？', message: '会删除这个会话的消息和上下文，其他会话及已安装的扩展保留。', confirmLabel: '清空会话', danger: true }))
        return;
    if (busy.value || session.value?.session_id !== id)
        return;
    try {
        await clearAdminChat(id);
        await load();
    }
    catch (e) {
        error.value = e instanceof Error ? e.message : String(e);
    }
}
function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        void send();
    }
}
function deactivate(): void {
    active = false;
    document.removeEventListener('pointerdown', onOutsideClick);
    document.removeEventListener('keydown', onPanelKeydown);
    closeMenus();
    showHistorySearch.value = false;
    showPermissions.value = false;
    clearTimeout(timer);
    controller?.abort();
    loadController?.abort();
}
onActivated(() => { active = true; document.addEventListener('pointerdown', onOutsideClick); document.addEventListener('keydown', onPanelKeydown); void loadProfiles(); void loadModels(); void load(); });
useConfigurationRefresh(['bot'], loadProfiles);
useConfigurationRefresh(['llm'], loadModels);
onDeactivated(deactivate);
onBeforeUnmount(deactivate);
</script>

<style scoped>
.admin-chat-view { min-width: 0; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.chat-workspace { display: grid; grid-template-columns: minmax(0, 1fr); height: calc(100dvh - 148px); min-height: 400px; }
.chat-workspace.show-sessions { grid-template-columns: 190px minmax(0, 1fr); }
.chat-sidebar { display: none; flex-direction: column; min-width: 0; min-height: 0; padding: 0 16px 0 0; border-right: 1px solid var(--border); }
.show-sessions .chat-sidebar { display: flex; }
.chat-sidebar-heading, .chat-room-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 40px; }
.chat-sidebar-heading { margin-bottom: 12px; }
.chat-sidebar-heading h2 { margin: 0; font-size: 12px; font-weight: 500; color: var(--muted); }
.chat-room-actions, .chat-room-title { display: flex; align-items: center; gap: 6px; min-width: 0; }
.chat-icon { display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 7px; background: transparent; color: var(--muted); cursor: pointer; }
.chat-icon:hover { background: var(--surface-2); color: var(--text); }
.chat-icon:disabled { opacity: .4; cursor: default; }
.chat-icon:focus-visible, .chat-session-row:focus-visible, .chat-menu-items button:focus-visible, .chat-suggestions button:focus-visible, .chat-send:focus-visible, .chat-latest:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
.chat-mobile-back { display: none; }
.chat-session-search { position: relative; margin-bottom: 8px; }
.chat-session-search > svg { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--muted); pointer-events: none; }
.chat-session-search .input { padding-left: 30px; font-size: 12px; }
.chat-session-list { flex: 1; min-height: 0; overflow-y: auto; }
.chat-session-row { display: flex; align-items: center; gap: 6px; width: 100%; min-height: 36px; padding: 8px 10px; border: 0; border-radius: 7px; background: transparent; color: var(--text-secondary); text-align: left; font: inherit; font-size: 12px; cursor: pointer; }
.chat-session-row > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chat-session-row > svg { flex-shrink: 0; margin-left: auto; }
.chat-session-row:hover, .chat-session-row.selected { background: var(--surface-2); color: var(--text); }
.chat-session-row:disabled { cursor: default; opacity: .6; }
.chat-list-empty { padding: 12px 8px; font-size: 12px; }
.chat-loading { display: grid; gap: 20px; padding: 16px 10px; }
.chat-main { display: flex; flex-direction: column; min-height: 0; min-width: 0; padding-left: 20px; }
.chat-workspace:not(.show-sessions) .chat-main { padding-left: 0; }
.chat-room-header { flex-shrink: 0; padding-bottom: 12px; }
.chat-room-title { flex: 1; gap: 10px; }
.chat-room-title h2 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 500; }
.chat-menu { position: relative; }
.chat-menu > summary { list-style: none; }
.chat-menu > summary::-webkit-details-marker { display: none; }
.chat-menu[open] > summary { color: var(--text); background: var(--surface-2); }
.chat-menu-items { position: absolute; z-index: 25; top: calc(100% + 6px); width: 202px; padding: 5px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); box-shadow: var(--shadow-md); }
.chat-menu-right { right: 0; }
.chat-menu-up { top: auto; bottom: calc(100% + 10px); left: 0; }
.chat-menu-items button { display: flex; align-items: center; gap: 10px; width: 100%; padding: 9px 10px; border: 0; border-radius: 6px; background: transparent; color: var(--text); text-align: left; font: inherit; font-size: 12px; cursor: pointer; }
.chat-menu-items button:hover { background: var(--surface-2); }
.chat-menu-items button:disabled { opacity: .4; cursor: default; }
.chat-menu-items .chat-menu-danger { color: var(--danger); }
.chat-menu-divider { height: 1px; margin: 5px; background: var(--border); }
.chat-transcript { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 16px max(8px, calc((100% - 700px) / 2)) 24px; scrollbar-gutter: stable; }
.chat-empty { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 22px; height: 100%; min-height: 160px; padding-bottom: 32px; }
.chat-empty h3 { margin: 0; font-size: 24px; letter-spacing: -.5px; font-weight: 500; }
.chat-suggestions { display: flex; flex-wrap: wrap; justify-content: center; gap: 18px; }
.chat-suggestions button { display: inline-flex; align-items: center; gap: 7px; padding: 4px 0; border: 0; background: transparent; color: var(--muted); font: inherit; font-size: 12px; cursor: pointer; }
.chat-suggestions button:hover { color: var(--text); }
.chat-message { max-width: 700px; margin: 0 auto 24px; }
.chat-message-content { white-space: pre-wrap; overflow-wrap: anywhere; font-size: 14px; line-height: 1.8; }
.chat-message.user { display: flex; flex-direction: column; align-items: flex-end; }
.chat-message.user .chat-message-content { max-width: min(85%, 580px); padding: 10px 15px; border-radius: 16px; background: var(--surface-2); }
.chat-message.failed .chat-message-content { color: var(--danger); }
.chat-message-meta { display: flex; align-items: center; gap: 7px; margin-top: 4px; min-height: 26px; font-size: 10px; color: var(--muted); }
.chat-message-meta time { opacity: 0; transition: opacity .15s; }
.chat-message:hover time, .chat-message:focus-within time { opacity: 1; }
.chat-copy { width: 26px; height: 26px; opacity: .65; }
.chat-steps { margin-top: 14px; font-size: 11px; color: var(--muted); }
.chat-steps summary { cursor: pointer; }
.chat-step { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px; padding: 8px 0; }
.chat-working { display: flex; align-items: center; gap: 8px; max-width: 700px; margin: 0 auto; color: var(--muted); font-size: 12px; }
.chat-composer-area { position: relative; flex-shrink: 0; width: 100%; max-width: 720px; margin: 0 auto; padding: 4px 0; }
.chat-composer { display: block; padding: 12px; border: 1px solid var(--border); border-radius: 18px; background: var(--surface); }
.chat-composer:focus-within { border-color: color-mix(in srgb, var(--text) 25%, var(--border)); }
.chat-composer textarea { display: block; width: 100%; min-height: 58px; max-height: 180px; padding: 2px 4px 8px; border: 0; outline: 0; resize: vertical; background: transparent; color: var(--text); font: inherit; font-size: 13px; line-height: 1.7; }
.chat-composer textarea::placeholder { color: var(--muted); }
.chat-composer textarea:disabled { opacity: .6; }
.chat-composer-actions { display: flex; align-items: center; gap: 6px; }
.chat-profile { flex: 0 1 auto; width: fit-content; min-width: 0; max-width: min(230px, calc(50% - 43px)); }
.chat-profile :deep(.app-select-trigger) { width: auto; min-height: 30px; height: 30px; gap: 6px; padding: 4px 7px; border-color: transparent; background: transparent; box-shadow: none; font-size: 11px; color: var(--muted); }
.chat-profile :deep(.app-select-trigger:hover), .chat-profile :deep(.app-select.open .app-select-trigger) { background: var(--surface-2); color: var(--text); }
.chat-profile :deep(.app-select-value) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chat-profile :deep(.app-select-avatar) { width: 17px; height: 17px; }
.chat-send { display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; width: 30px; height: 30px; margin-left: auto; padding: 0; border: 0; border-radius: 50%; background: var(--text); color: var(--surface); cursor: pointer; }
.chat-send:disabled { opacity: .25; cursor: default; }
.chat-send:hover:not(:disabled) { opacity: .8; }
.chat-latest { position: absolute; left: 50%; bottom: calc(100% + 10px); transform: translateX(-50%); display: flex; align-items: center; gap: 6px; padding: 7px 12px; border: 1px solid var(--border); border-radius: 20px; background: var(--surface); color: var(--text-secondary); box-shadow: var(--shadow-md); font: inherit; font-size: 11px; cursor: pointer; white-space: nowrap; }
.chat-error { color: var(--danger); font-size: 12px; }
.chat-banner { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; padding: 10px 12px; border-radius: 8px; background: color-mix(in srgb, var(--danger) 6%, var(--surface)); }
.chat-banner > span { flex: 1; overflow-wrap: anywhere; }
.chat-history-search { display: grid; gap: 16px; }
.chat-history-search > p, .chat-capabilities > p { margin: 0; font-size: 12px; line-height: 1.7; }
.chat-capabilities { display: grid; gap: 16px; }
.chat-capabilities dl { display: grid; grid-template-columns: 90px minmax(0, 1fr); gap: 12px 20px; margin: 0; font-size: 13px; }
.chat-capabilities dt { color: var(--muted); }
.chat-capabilities dd { margin: 0; overflow-wrap: anywhere; }
.chat-spinner { animation: chat-spin 1s linear infinite; }
@keyframes chat-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .chat-spinner { animation: none; } .chat-message-meta time { transition: none; } }
@media (max-width: 720px) {
  .chat-workspace, .chat-workspace.show-sessions { grid-template-columns: minmax(0, 1fr); height: calc(100dvh - 144px); }
  .chat-main { padding-left: 0; }
  .show-sessions .chat-main { display: none; }
  .show-sessions .chat-sidebar { padding-right: 0; border-right: 0; }
  .chat-mobile-back { display: inline-flex; }
  .chat-room-header { padding-bottom: 8px; }
  .chat-room-title { gap: 5px; }
  .chat-transcript { padding: 12px 2px 20px; }
  .chat-empty h3 { font-size: 22px; }
  .chat-message-content { font-size: 13px; }
  .chat-message.user .chat-message-content { max-width: 92%; }
  .chat-message-meta time { opacity: 1; }
  .chat-composer { padding: 10px; border-radius: 16px; }
  .chat-composer textarea { font-size: 16px; }
  .chat-composer-area { padding: 4px 0; }
}
</style>
