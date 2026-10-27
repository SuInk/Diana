<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="qq-chat" :class="{ 'show-list': showList }">
    <aside class="qq-list" aria-label="机器人列表">
      <header class="qq-list-head">
        <h2>消息</h2>
        <button class="qq-icon" type="button" aria-label="搜索机器人" :aria-expanded="searching" title="搜索" @click="toggleSearch"><Search :size="17" aria-hidden="true" /></button>
      </header>
      <div v-if="searching" class="qq-search"><Search :size="14" aria-hidden="true" /><input ref="searchInput" v-model="search" type="search" placeholder="搜索名称、平台或 ID" aria-label="搜索机器人" @keydown.esc="toggleSearch" /></div>
      <div class="qq-contacts" role="list">
        <div v-if="profilesLoading && !contacts.length" class="qq-loading" role="status" aria-label="正在加载机器人"><SkeletonBlock v-for="i in 3" :key="i" width="100%" height="44px" /></div>
        <p v-else-if="profilesError" class="qq-empty" role="alert">{{ profilesError }}</p>
        <p v-else-if="!contacts.length" class="qq-empty">还没有机器人，先到「机器人」页添加一台。</p>
        <button
          v-for="contact in filteredContacts"
          :key="contact.id"
          role="listitem"
          class="qq-contact"
          :class="{ selected: contact.id === selected }"
          :aria-current="contact.id === selected ? 'true' : undefined"
          type="button"
          @click="openContact(contact.id)"
        >
          <BotAvatar :name="contact.name" :src="contact.avatar" :size="46" />
          <span class="qq-contact-text">
            <span class="qq-contact-top"><strong>{{ contact.name }}</strong><time v-if="lastOf(contact.id)">{{ shortTime(lastOf(contact.id)!.at) }}</time></span>
            <small>{{ typingFor === contact.id ? "正在输入…" : lastOf(contact.id)?.text || contact.platform }}</small>
          </span>
        </button>
        <p v-if="search && contacts.length && !filteredContacts.length" class="qq-empty">没有匹配的机器人</p>
      </div>
    </aside>

    <section class="qq-room" :aria-label="current ? `和 ${current.name} 的私聊` : '私聊'">
      <template v-if="current">
        <header class="qq-room-head">
          <button class="qq-icon qq-back" type="button" aria-label="返回机器人列表" @click="showList = true"><ChevronLeft :size="22" aria-hidden="true" /></button>
          <div class="qq-room-title">
            <h2>{{ current.name }}</h2>
            <p :class="{ typing: sending }">{{ sending ? "对方正在输入…" : current.platform }}</p>
          </div>
          <button class="qq-icon" type="button" :disabled="!!info?.problem" :aria-label="`和 ${current.name} 语音通话`" :title="`语音通话`" @click="startCall"><Phone :size="19" aria-hidden="true" /></button>
        </header>

        <div v-if="info?.problem" class="qq-banner" role="status">{{ info.problem }}</div>
        <div v-else-if="error" class="qq-banner err" role="alert"><span>{{ error }}</span><button class="btn ghost small" type="button" @click="loadHistory">重试</button></div>

        <section ref="transcript" class="qq-transcript" aria-live="polite" :aria-busy="loading || sending">
          <div v-if="loading && !messages.length" class="qq-loading" role="status" aria-label="正在加载聊天记录"><SkeletonBlock width="40%" height="36px" /><SkeletonBlock width="55%" height="36px" /></div>
          <p v-else-if="!messages.length && !info?.problem" class="qq-hint">和 {{ current.name }} 打个招呼吧。这里是主人和她的私聊，平台上聊过的也会出现在这里。</p>
          <template v-for="(message, i) in messages" :key="message.id">
            <div v-if="dividers[i]" class="qq-time">{{ chatTimeLabel(message.at) }}</div>
            <article class="qq-msg" :class="[message.role, { failed: message.failed }]" :aria-label="message.role === 'user' ? '我说' : `${current.name} 说`">
              <BotAvatar v-if="message.role === 'assistant'" :name="current.name" :src="current.avatar" :size="38" />
              <div class="qq-bubble-wrap">
                <div v-if="message.text" class="qq-bubble">{{ message.text }}</div>
                <a v-for="image in message.images ?? []" :key="image" class="qq-image" :href="image" target="_blank" rel="noopener noreferrer"><img :src="image" alt="图片" loading="lazy" /></a>
              </div>
              <span v-if="message.role === 'user'" class="qq-self" aria-hidden="true">我</span>
              <span v-if="message.pending" class="qq-state" aria-label="发送中"><LoaderCircle :size="14" class="qq-spin" aria-hidden="true" /></span>
              <button v-else-if="message.failed" class="qq-state err" type="button" aria-label="重新发送" title="重新发送" @click="resend(message)"><CircleAlert :size="16" aria-hidden="true" /></button>
            </article>
          </template>
          <article v-if="sending" class="qq-msg assistant" aria-label="对方正在输入">
            <BotAvatar :name="current.name" :src="current.avatar" :size="38" />
            <div class="qq-bubble-wrap"><div class="qq-bubble qq-typing" aria-hidden="true"><i /><i /><i /></div></div>
          </article>
        </section>

        <form class="qq-composer" @submit.prevent="send">
          <label class="sr-only" for="qq-input">发消息给 {{ current.name }}</label>
          <textarea id="qq-input" ref="composer" v-model="draft" rows="1" maxlength="4000" placeholder="发消息…" :disabled="!!info?.problem || loading" @keydown="onKeydown" />
          <button class="qq-send" type="submit" :disabled="!draft.trim() || !!info?.problem || loading || sending">发送</button>
        </form>
      </template>
      <div v-else class="qq-placeholder"><MessagesSquare :size="40" aria-hidden="true" /><p>选一台机器人开始聊天</p></div>
    </section>

    <VoiceCallScreen v-if="callAudio && current" :profile="current.id" :name="current.name" :avatar="current.avatar" :audio="callAudio" @close="endCall" />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { ChevronLeft, CircleAlert, LoaderCircle, MessagesSquare, Phone, Search } from "@lucide/vue";
import BotAvatar from "../components/BotAvatar.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import VoiceCallScreen from "../components/VoiceCallScreen.vue";
import { getBotProfileConfig, type BotProfileConfig } from "../api";
import { botScope } from "../lib/bot-scope";
import { subscriptionPlatformLabel } from "../lib/rss-display";
import { openCallAudio, type CallAudio } from "../lib/voice-call";
import { chatTimeLabel, getWebChat, sendWebChat, timeDividers, type WebChatInfo, type WebChatMessage } from "../lib/web-chat";

interface Contact {
  id: string;
  name: string;
  platform: string;
  avatar?: string;
}

const selectedKey = "diana:chat-profile";
const profiles = ref<BotProfileConfig[]>([]);
const profilesLoading = ref(false);
const profilesError = ref("");
const contacts = computed<Contact[]>(() =>
  profiles.value.filter((p) => p.id).map((p) => ({ id: p.id!, name: p.name || p.id!, platform: subscriptionPlatformLabel(p.platform), avatar: p.avatar_url }))
);
const search = ref("");
const searching = ref(false);
const searchInput = ref<HTMLInputElement | null>(null);
const filteredContacts = computed(() => {
  const keyword = search.value.trim().toLowerCase();
  return keyword ? contacts.value.filter((c) => `${c.name} ${c.platform} ${c.id}`.toLowerCase().includes(keyword)) : contacts.value;
});

const selected = ref(readSelected());
const current = computed(() => contacts.value.find((c) => c.id === selected.value));
// 手机上和 QQ 一样先看到列表，点进去才是聊天；宽屏两栏都在。
const showList = ref(true);

const info = ref<WebChatInfo | null>(null);
const messages = ref<WebChatMessage[]>([]);
const dividers = computed(() => timeDividers(messages.value));
// 每台机器人最后一条消息，列表里当预览。
const lastMessages = ref<Record<string, WebChatMessage>>({});
const loading = ref(false);
const sending = ref(false);
const typingFor = ref("");
const error = ref("");
const draft = ref("");
const transcript = ref<HTMLElement | null>(null);
const composer = ref<HTMLTextAreaElement | null>(null);
const callAudio = ref<CallAudio | null>(null);
let loadController: AbortController | null = null;
let sendController: AbortController | null = null;

function readSelected(): string {
  try {
    return localStorage.getItem(selectedKey) || botScope.value || "";
  } catch {
    return botScope.value || "";
  }
}

function lastOf(id: string): WebChatMessage | undefined {
  return lastMessages.value[id];
}

function shortTime(at: number): string {
  const label = chatTimeLabel(at);
  return label.includes(" ") ? label.split(" ")[0] : label;
}

async function loadProfiles(): Promise<void> {
  profilesLoading.value = true;
  try {
    const config = await getBotProfileConfig();
    profiles.value = config.profiles?.length ? config.profiles : [config];
    profilesError.value = "";
    if (!contacts.value.some((c) => c.id === selected.value)) selected.value = contacts.value[0]?.id ?? "";
    if (selected.value) void loadHistory();
  } catch (err) {
    profilesError.value = err instanceof Error ? err.message : String(err);
  } finally {
    profilesLoading.value = false;
  }
}

async function loadHistory(): Promise<void> {
  const profile = selected.value;
  if (!profile) return;
  loadController?.abort();
  const controller = new AbortController();
  loadController = controller;
  loading.value = true;
  error.value = "";
  try {
    const result = await getWebChat(profile, controller.signal);
    if (controller.signal.aborted || profile !== selected.value) return;
    info.value = result.info;
    messages.value = result.messages;
    rememberLast(profile);
    await scrollToBottom();
  } catch (err) {
    if (controller.signal.aborted) return;
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (loadController === controller) loading.value = false;
  }
}

function rememberLast(profile: string): void {
  const sent = messages.value.filter((m) => !m.failed);
  const last = sent[sent.length - 1];
  if (last) lastMessages.value = { ...lastMessages.value, [profile]: last };
}

function openContact(id: string): void {
  showList.value = false;
  if (id === selected.value && messages.value.length) return;
  selected.value = id;
  try {
    localStorage.setItem(selectedKey, id);
  } catch {
    // 记不住也不影响聊天。
  }
  info.value = null;
  messages.value = [];
  draft.value = "";
  void loadHistory();
}

async function send(): Promise<void> {
  const text = draft.value.trim();
  if (!text || sending.value || !current.value) return;
  draft.value = "";
  const local: WebChatMessage = { id: `local-${Date.now()}`, role: "user", text, at: Math.floor(Date.now() / 1000), pending: true };
  messages.value = [...messages.value, local];
  await deliver(local);
}

async function deliver(local: WebChatMessage): Promise<void> {
  const profile = selected.value;
  sending.value = true;
  typingFor.value = profile;
  await scrollToBottom();
  sendController = new AbortController();
  try {
    await sendWebChat(profile, local.text, sendController.signal, (reply) => {
      if (profile !== selected.value) return;
      markSent(local);
      messages.value = [...messages.value, reply];
      void scrollToBottom();
    });
    markSent(local);
  } catch (err) {
    if (profile === selected.value) {
      messages.value = messages.value.map((m) => (m.id === local.id ? { ...m, pending: false, failed: true } : m));
      if (!sendController?.signal.aborted) error.value = err instanceof Error ? err.message : String(err);
    }
  } finally {
    sending.value = false;
    typingFor.value = "";
    sendController = null;
    rememberLast(profile);
  }
}

function markSent(local: WebChatMessage): void {
  if (!local.pending) return;
  local.pending = false;
  messages.value = messages.value.map((m) => (m.id === local.id ? { ...m, pending: false } : m));
}

function resend(message: WebChatMessage): void {
  if (sending.value) return;
  error.value = "";
  const retry = { ...message, id: `local-${Date.now()}`, pending: true, failed: false, at: Math.floor(Date.now() / 1000) };
  messages.value = [...messages.value.filter((m) => m.id !== message.id), retry];
  void deliver(retry);
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    void send();
  }
}

async function scrollToBottom(): Promise<void> {
  await nextTick();
  const el = transcript.value;
  if (el) el.scrollTop = el.scrollHeight;
}

async function toggleSearch(): Promise<void> {
  searching.value = !searching.value;
  if (!searching.value) {
    search.value = "";
    return;
  }
  await nextTick();
  searchInput.value?.focus();
}

function startCall(): void {
  if (!callAudio.value && current.value && !info.value?.problem) callAudio.value = openCallAudio();
}

// 通话里说的也记进私聊，挂断后刷新一下就能看到。
function endCall(): void {
  callAudio.value = null;
  void loadHistory();
}

// 输入框随内容长高，一行起步。
watch(draft, async () => {
  await nextTick();
  const el = composer.value;
  if (!el) return;
  el.style.height = "auto";
  el.style.height = `${Math.min(el.scrollHeight, 140)}px`;
});

void loadProfiles();

onBeforeUnmount(() => {
  loadController?.abort();
  sendController?.abort();
});
</script>

<style scoped>
.qq-chat {
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  height: calc(100dvh - 148px);
  min-height: 420px;
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--surface);
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
}

.qq-icon {
  display: inline-grid;
  place-items: center;
  flex-shrink: 0;
  width: 34px;
  height: 34px;
  padding: 0;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
}

.qq-icon:hover:not(:disabled) {
  background: var(--surface-2);
  color: var(--text);
}

.qq-icon:disabled {
  opacity: 0.35;
  cursor: default;
}

.qq-icon:focus-visible,
.qq-contact:focus-visible,
.qq-send:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

/* 左栏：好友列表 */
.qq-list {
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-right: 1px solid var(--border);
  background: var(--surface-2);
  background: color-mix(in srgb, var(--surface-2) 60%, var(--surface));
}

.qq-list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 12px 8px 16px;
}

.qq-list-head h2 {
  margin: 0;
  font-size: 16px;
  font-weight: 650;
}

.qq-search {
  position: relative;
  margin: 0 12px 8px;
}

.qq-search svg {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--muted);
}

.qq-search input {
  width: 100%;
  height: 32px;
  padding: 0 10px 0 30px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface);
  color: var(--text);
  font: inherit;
  font-size: 13px;
  outline: none;
}

.qq-contacts {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 0 6px 8px;
}

.qq-contact {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 10px;
  border: 0;
  border-radius: 10px;
  background: transparent;
  color: var(--text);
  text-align: left;
  font: inherit;
  cursor: pointer;
}

.qq-contact:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
}

.qq-contact.selected {
  background: var(--accent);
  color: #fff;
}

.qq-contact-text {
  display: grid;
  gap: 3px;
  flex: 1;
  min-width: 0;
}

.qq-contact-top {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.qq-contact-top strong,
.qq-contact-text small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.qq-contact-top strong {
  font-size: 14px;
  font-weight: 600;
}

.qq-contact-top time,
.qq-contact-text small {
  font-size: 12px;
  color: var(--muted);
}

.qq-contact.selected time,
.qq-contact.selected small {
  color: rgba(255, 255, 255, 0.8);
}

.qq-empty,
.qq-hint {
  margin: 12px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--muted);
}

.qq-hint {
  margin: 24px auto;
  max-width: 360px;
  text-align: center;
}

.qq-loading {
  display: grid;
  gap: 12px;
  padding: 12px;
}

/* 右栏：聊天 */
.qq-room {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

.qq-room-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 56px;
  padding: 8px 12px 8px 18px;
  border-bottom: 1px solid var(--border);
}

.qq-back {
  display: none;
}

.qq-room-title {
  flex: 1;
  min-width: 0;
  line-height: 1.3;
}

.qq-room-title h2 {
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 15px;
  font-weight: 650;
}

.qq-room-title p {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}

.qq-room-title p.typing {
  color: var(--accent);
}

.qq-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 10px 16px 0;
  padding: 9px 12px;
  border-radius: 8px;
  background: var(--warn-soft, color-mix(in srgb, orange 12%, var(--surface)));
  font-size: 13px;
}

.qq-banner.err {
  background: color-mix(in srgb, var(--danger) 10%, var(--surface));
  color: var(--danger);
}

.qq-banner span {
  flex: 1;
}

.qq-transcript {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 12px 18px 20px;
}

.qq-time {
  margin: 14px 0 10px;
  text-align: center;
  font-size: 12px;
  color: var(--muted);
}

.qq-msg {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin: 0 0 14px;
}

.qq-msg.user {
  justify-content: flex-end;
}

/* 自己的消息：发送中/失败的标记在气泡左边，头像在最右。 */
.qq-msg.user .qq-state {
  order: -1;
}

.qq-bubble-wrap {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  max-width: min(68%, 560px);
}

.qq-msg.user .qq-bubble-wrap {
  align-items: flex-end;
}

.qq-bubble {
  padding: 9px 13px;
  border-radius: 12px;
  background: var(--surface-2);
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.qq-msg.user .qq-bubble {
  background: var(--accent);
  color: #fff;
}

.qq-msg.failed .qq-bubble {
  opacity: 0.6;
}

.qq-image img {
  display: block;
  max-width: min(240px, 100%);
  max-height: 240px;
  border-radius: 10px;
  object-fit: cover;
}

.qq-self {
  display: inline-grid;
  place-items: center;
  flex-shrink: 0;
  width: 38px;
  height: 38px;
  border-radius: 50%;
  background: color-mix(in srgb, var(--accent) 18%, var(--surface-2));
  color: var(--accent);
  font-size: 14px;
  font-weight: 600;
}

.qq-state {
  align-self: center;
  display: inline-grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: none;
  color: var(--muted);
}

.qq-state.err {
  color: var(--danger);
  cursor: pointer;
}

.qq-typing {
  display: inline-flex;
  gap: 4px;
  padding: 14px 14px;
}

.qq-typing i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--muted);
  animation: qq-typing 1.2s ease-in-out infinite;
}

.qq-typing i:nth-child(2) {
  animation-delay: 0.15s;
}

.qq-typing i:nth-child(3) {
  animation-delay: 0.3s;
}

@keyframes qq-typing {
  0%,
  60%,
  100% {
    opacity: 0.3;
    transform: translateY(0);
  }
  30% {
    opacity: 1;
    transform: translateY(-3px);
  }
}

.qq-spin {
  animation: qq-spin 1s linear infinite;
}

@keyframes qq-spin {
  to {
    transform: rotate(360deg);
  }
}

.qq-composer {
  display: flex;
  align-items: flex-end;
  gap: 10px;
  padding: 10px 16px 14px;
  border-top: 1px solid var(--border);
}

.qq-composer textarea {
  flex: 1;
  min-width: 0;
  height: 40px;
  max-height: 140px;
  padding: 9px 14px;
  border: 1px solid var(--border);
  border-radius: 20px;
  outline: 0;
  resize: none;
  background: var(--surface);
  color: var(--text);
  font: inherit;
  font-size: 14px;
  line-height: 20px;
}

.qq-composer textarea:focus {
  border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
}

.qq-send {
  flex-shrink: 0;
  height: 40px;
  padding: 0 18px;
  border: 0;
  border-radius: 20px;
  background: var(--accent);
  color: #fff;
  font: inherit;
  font-size: 14px;
  cursor: pointer;
}

.qq-send:disabled {
  opacity: 0.4;
  cursor: default;
}

.qq-placeholder {
  display: grid;
  place-content: center;
  justify-items: center;
  gap: 10px;
  flex: 1;
  color: var(--muted);
}

@media (prefers-reduced-motion: reduce) {
  .qq-typing i,
  .qq-spin {
    animation: none;
  }
}

/* 手机：列表和聊天各占一屏，和 QQ 一样点进去、返回出来。 */
@media (max-width: 720px) {
  .qq-chat {
    grid-template-columns: minmax(0, 1fr);
    height: calc(100dvh - 132px);
    border-radius: 12px;
  }

  .qq-chat.show-list .qq-room,
  .qq-chat:not(.show-list) .qq-list {
    display: none;
  }

  .qq-list {
    border-right: 0;
  }

  .qq-back {
    display: inline-grid;
  }

  .qq-room-head {
    padding-left: 6px;
  }

  .qq-transcript {
    padding: 10px 12px 16px;
  }

  .qq-bubble-wrap {
    max-width: 76%;
  }

  .qq-composer {
    padding: 8px 10px 10px;
  }

  .qq-composer textarea {
    font-size: 16px;
  }
}
</style>
