<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="qq-chat" :class="{ 'show-list': showList }">
    <aside class="qq-list" aria-label="机器人列表">
      <header class="qq-list-head">
        <h2>消息</h2>
        <span class="qq-list-actions">
          <button class="qq-icon" type="button" aria-label="新建群聊" title="新建群聊" data-new-room @click="roomDialog = 'create'"><UsersRound :size="17" aria-hidden="true" /></button>
          <button class="qq-icon" type="button" aria-label="搜索机器人" :aria-expanded="searching" title="搜索" @click="toggleSearch"><Search :size="17" aria-hidden="true" /></button>
        </span>
      </header>
      <div v-if="searching" class="qq-search"><Search :size="14" aria-hidden="true" /><input ref="searchInput" v-model="search" type="search" placeholder="搜索名称、平台或 ID" aria-label="搜索机器人" @keydown.esc="toggleSearch" /></div>
      <div class="qq-contacts" role="list">
        <div v-if="profilesLoading && !contacts.length" class="qq-loading" role="status" aria-label="正在加载机器人"><SkeletonBlock v-for="i in 3" :key="i" width="100%" height="44px" /></div>
        <p v-else-if="profilesError" class="qq-empty" role="alert">{{ profilesError }}</p>
        <!-- 管理助手置顶：和机器人聊、找管理助手办事都从聊天页进，对话仍各管各的。 -->
        <button
          v-if="!search"
          role="listitem"
          class="qq-contact qq-admin"
          :class="{ selected: adminSelected }"
          :aria-current="adminSelected ? 'true' : undefined"
          type="button"
          data-admin-chat
          @click="openAdmin"
        >
          <span class="qq-admin-avatar" aria-hidden="true"><Wrench :size="20" /></span>
          <span class="qq-contact-text">
            <span class="qq-contact-top"><strong>管理助手</strong></span>
            <small>装扩展、查问题、改配置</small>
          </span>
        </button>
        <p v-if="!profilesLoading && !profilesError && !contacts.length" class="qq-empty">还没有机器人，先到「机器人」页添加一台。</p>
        <!-- 本地群排在机器人前面：群里的人一般比单聊的多，打开得也勤。 -->
        <button
          v-for="room in filteredRooms"
          :key="roomKey(room.id)"
          role="listitem"
          class="qq-contact"
          :class="{ selected: roomKey(room.id) === selected }"
          :aria-current="roomKey(room.id) === selected ? 'true' : undefined"
          type="button"
          data-room
          @click="openRoom(room.id)"
        >
          <span class="qq-room-avatar" aria-hidden="true"><UsersRound :size="20" /></span>
          <span class="qq-contact-text">
            <span class="qq-contact-top"><strong>{{ room.name }}</strong><time v-if="roomLast[room.id]">{{ shortTime(roomLast[room.id]!.at) }}</time></span>
            <small>{{ roomPreview(room) }}</small>
          </span>
        </button>
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
            <small>{{ inFlight[contact.id] ? "正在输入…" : previewOf(contact.id) || contact.platform }}</small>
          </span>
        </button>
        <p v-if="search && contacts.length && !filteredContacts.length" class="qq-empty">没有匹配的机器人</p>
      </div>
    </aside>

    <section class="qq-room" :aria-label="adminSelected ? '管理助手' : currentRoom ? `群聊 ${currentRoom.name}` : current ? `和 ${current.name} 的私聊` : '私聊'">
      <!-- 管理助手和机器人本人是两回事（身份、权限、会话都分开），界面上就是一个普通联系人。 -->
      <template v-if="adminSelected">
        <header class="qq-room-head">
          <button class="qq-icon qq-back" type="button" aria-label="返回列表" @click="showList = true"><ChevronLeft :size="22" aria-hidden="true" /></button>
          <div class="qq-room-title">
            <h2>管理助手</h2>
            <p :class="{ typing: adminBusy }">{{ adminBusy ? adminProgressText : "装扩展、查问题、改配置" }}</p>
          </div>
          <button v-if="adminBusy" class="qq-icon" type="button" aria-label="停止" title="停止" @click="stopAdmin"><CircleStop :size="19" aria-hidden="true" /></button>
          <button class="qq-icon" type="button" aria-label="新会话" title="新会话" :disabled="adminBusy || adminLoading" @click="newAdminSession"><SquarePen :size="18" aria-hidden="true" /></button>
        </header>

        <div v-if="adminError" class="qq-banner err" role="alert"><span>{{ adminError }}</span><button class="btn ghost small" type="button" @click="loadAdmin()">重试</button></div>

        <section ref="transcript" class="qq-transcript" aria-live="polite" :aria-busy="adminLoading || adminBusy">
          <div v-if="adminLoading && !adminMessages.length" class="qq-loading" role="status" aria-label="正在加载管理对话"><SkeletonBlock width="40%" height="36px" /><SkeletonBlock width="55%" height="36px" /></div>
          <p v-else-if="!adminMessages.length" class="qq-hint">想装扩展、查问题、改配置，直接跟管理助手说。这里的对话不会进任何机器人的私聊记录。</p>
          <template v-for="(message, i) in adminMessages" :key="message.id">
            <div v-if="adminDividers[i]" class="qq-time">{{ chatTimeLabel(message.at) }}</div>
            <article class="qq-msg" :class="[message.role, { failed: message.failed }]" :aria-label="message.role === 'user' ? '我说' : '管理助手说'">
              <span v-if="message.role === 'assistant'" class="qq-admin-avatar small" aria-hidden="true"><Wrench :size="16" /></span>
              <div class="qq-bubble-wrap">
                <MarkdownText v-if="message.role === 'assistant'" class="qq-bubble qq-markdown" :class="{ err: message.error }" :source="message.text" />
                <div v-else class="qq-bubble">{{ message.text }}</div>
                <span v-if="message.tools" class="qq-sender">调用了 {{ message.tools }} 次工具</span>
              </div>
              <span v-if="message.role === 'user'" class="qq-self" aria-hidden="true">我</span>
              <span v-if="message.pending" class="qq-state" aria-label="发送中"><LoaderCircle :size="14" class="qq-spin" aria-hidden="true" /></span>
              <span v-else-if="message.failed" class="qq-state err" aria-label="没发出去"><CircleAlert :size="16" aria-hidden="true" /></span>
            </article>
          </template>
          <article v-if="adminBusy" class="qq-msg assistant" aria-label="管理助手正在处理">
            <span class="qq-admin-avatar small" aria-hidden="true"><Wrench :size="16" /></span>
            <div class="qq-bubble-wrap"><div class="qq-bubble qq-typing" aria-hidden="true"><i /><i /><i /></div></div>
          </article>
        </section>

        <form class="qq-composer" @submit.prevent="send">
          <label class="sr-only" for="qq-input">发消息给管理助手</label>
          <textarea id="qq-input" ref="composer" v-model="draft" rows="1" maxlength="4000" placeholder="描述问题，或交给它一个任务…" :disabled="adminLoading" @keydown="onKeydown" />
          <button class="qq-send" type="submit" :disabled="!draft.trim() || adminLoading || adminBusy">发送</button>
        </form>
      </template>
      <template v-else-if="currentRoom">
        <header class="qq-room-head">
          <button class="qq-icon qq-back" type="button" aria-label="返回列表" @click="showList = true"><ChevronLeft :size="22" aria-hidden="true" /></button>
          <div class="qq-room-title">
            <h2>{{ currentRoom.name }}</h2>
            <p :class="{ typing: roomBusy }">{{ roomSubtitle }}</p>
          </div>
          <button v-if="roomBusy" class="qq-icon" type="button" aria-label="停止讨论" title="停止讨论" data-room-stop @click="stopRoom"><CircleStop :size="19" aria-hidden="true" /></button>
          <button class="qq-icon" type="button" aria-label="群设置" title="群设置" @click="roomDialog = 'edit'"><Settings2 :size="19" aria-hidden="true" /></button>
        </header>

        <div v-if="roomError" class="qq-banner err" role="alert"><span>{{ roomError }}</span><button class="btn ghost small" type="button" @click="loadRoom()">重试</button></div>
        <div v-else-if="roomStatus?.paused && !roomBusy && roomMessages.length" class="qq-banner" role="status">机器人们停下来了，等你说话。</div>

        <section ref="transcript" class="qq-transcript" aria-live="polite" :aria-busy="roomLoading || roomBusy">
          <div v-if="roomLoading && !roomMessages.length" class="qq-loading" role="status" aria-label="正在加载群消息"><SkeletonBlock width="40%" height="36px" /><SkeletonBlock width="55%" height="36px" /></div>
          <p v-else-if="!roomMessages.length" class="qq-hint">说点什么，{{ roomMemberNames }}都会接话。群里的话只在 Diana 里，不会发到平台上。</p>
          <template v-for="(message, i) in roomMessages" :key="message.id">
            <div v-if="roomDividers[i]" class="qq-time">{{ chatTimeLabel(message.at) }}</div>
            <article class="qq-msg" :class="[message.sender_id === LOCAL_ROOM_OWNER ? 'user' : 'assistant', { failed: message.failed }]" :aria-label="message.sender_id === LOCAL_ROOM_OWNER ? '我说' : `${message.sender_name} 说`">
              <BotAvatar v-if="message.sender_id !== LOCAL_ROOM_OWNER" :name="message.sender_name" :src="memberAvatar(message.sender_id)" :size="38" />
              <div class="qq-bubble-wrap">
                <span v-if="message.sender_id !== LOCAL_ROOM_OWNER" class="qq-sender">{{ message.sender_name }}</span>
                <template v-if="message.text">
                  <MarkdownText v-if="message.sender_id !== LOCAL_ROOM_OWNER" class="qq-bubble qq-markdown" :source="message.text" />
                  <div v-else class="qq-bubble">{{ message.text }}</div>
                </template>
                <a v-for="image in message.images ?? []" :key="image" class="qq-image" :href="image" target="_blank" rel="noopener noreferrer"><img :src="image" alt="图片" loading="lazy" /></a>
              </div>
              <span v-if="message.sender_id === LOCAL_ROOM_OWNER" class="qq-self" aria-hidden="true">我</span>
              <span v-if="message.pending" class="qq-state" aria-label="发送中"><LoaderCircle :size="14" class="qq-spin" aria-hidden="true" /></span>
              <span v-else-if="message.failed" class="qq-state err" aria-label="没发出去"><CircleAlert :size="16" aria-hidden="true" /></span>
            </article>
          </template>
          <article v-if="roomBusy" class="qq-msg assistant" aria-label="群里有人在想">
            <span class="qq-room-avatar small" aria-hidden="true"><UsersRound :size="16" /></span>
            <div class="qq-bubble-wrap"><div class="qq-bubble qq-typing" aria-hidden="true"><i /><i /><i /></div></div>
          </article>
        </section>

        <form class="qq-composer" @submit.prevent="send">
          <label class="sr-only" for="qq-input">在 {{ currentRoom.name }} 里发言</label>
          <textarea id="qq-input" ref="composer" v-model="draft" rows="1" maxlength="4000" placeholder="在群里说点什么…" :disabled="roomLoading" @keydown="onKeydown" />
          <button class="qq-send" type="submit" :disabled="!draft.trim() || roomLoading">发送</button>
        </form>
      </template>
      <template v-else-if="current">
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
            <div v-if="message.kind === 'call'" class="qq-call-record" role="note"><Phone :size="13" aria-hidden="true" />{{ callRecordLabel(message.text) }}</div>
            <article v-else class="qq-msg" :class="[message.role, { failed: message.failed }]" :aria-label="message.role === 'user' ? '我说' : `${current.name} 说`">
              <BotAvatar v-if="message.role === 'assistant'" :name="current.name" :src="current.avatar" :size="38" />
              <div class="qq-bubble-wrap">
                <template v-if="message.text">
                  <MarkdownText v-if="message.role === 'assistant'" class="qq-bubble qq-markdown" :source="message.text" />
                  <div v-else class="qq-bubble">{{ message.text }}</div>
                </template>
                <a v-for="image in message.images ?? []" :key="image" class="qq-image" :href="image" target="_blank" rel="noopener noreferrer"><img :src="image" alt="图片" loading="lazy" /></a>
              </div>
              <template v-if="message.role === 'user'">
                <BotAvatar v-if="info?.owner_avatar_url" class="qq-self-avatar" name="我" :src="info.owner_avatar_url" :size="38" aria-hidden="true" />
                <span v-else class="qq-self" aria-hidden="true">我</span>
              </template>
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
          <button class="qq-send" type="submit" :disabled="!draft.trim() || !!info?.problem || loading">发送</button>
        </form>
      </template>
      <div v-else class="qq-placeholder"><MessagesSquare :size="40" aria-hidden="true" /><p>选一台机器人开始聊天</p></div>
    </section>

    <LocalRoomDialog
      v-if="roomDialog"
      :room="roomDialog === 'edit' ? currentRoom : undefined"
      :contacts="contacts"
      @close="roomDialog = ''"
      @saved="onRoomSaved"
      @deleted="onRoomDeleted"
    />

    <VoiceCallScreen v-if="callAudio && current" :profile="current.id" :name="current.name" :avatar="current.avatar" :audio="callAudio" @close="endCall" />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { ChevronLeft, CircleAlert, CircleStop, LoaderCircle, MessagesSquare, Phone, Search, Settings2, SquarePen, UsersRound, Wrench } from "@lucide/vue";
import BotAvatar from "../components/BotAvatar.vue";
import MarkdownText from "../components/MarkdownText.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import VoiceCallScreen from "../components/VoiceCallScreen.vue";
import LocalRoomDialog from "../components/LocalRoomDialog.vue";
import { getBotProfileConfig, type BotProfileConfig } from "../api";
import { botScope } from "../lib/bot-scope";
import { CHAT_ADMIN, chatOpenTarget, chatSelection } from "../lib/chat-target";
import { createAdminChatSession, getAdminChat, listAdminChatSessions, sendAdminChat, stopAdminChat, type AdminChatMessage } from "../lib/admin-chat";
import { subscriptionPlatformLabel } from "../lib/rss-display";
import { openCallAudio, type CallAudio } from "../lib/voice-call";
import { markdownToPlain } from "../lib/markdown";
import { chatTimeLabel, getWebChat, sendWebChat, timeDividers, type WebChatInfo, type WebChatMessage } from "../lib/web-chat";
import { getLocalRoom, listLocalRooms, LOCAL_ROOM_OWNER, sendLocalRoom, stopLocalRoom, type LocalRoom, type LocalRoomMessage, type LocalRoomStatus } from "../lib/local-rooms";

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
// 和 QQ 一样她回复前也能接着发：每台机器人还有几句在等回复。
const inFlight = ref<Record<string, number>>({});
const sending = computed(() => (inFlight.value[selected.value] ?? 0) > 0);
const error = ref("");
const draft = ref("");
const transcript = ref<HTMLElement | null>(null);
const composer = ref<HTMLTextAreaElement | null>(null);
const callAudio = ref<CallAudio | null>(null);
let loadController: AbortController | null = null;
const sendControllers = new Set<AbortController>();
let localSeq = 0;

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

// 通话记录在聊天里只显示「通话 3 分 12 秒」，前面的电话图标代替「[语音通话]」。
function callRecordLabel(text: string): string {
  return text.replace(/^\[语音通话\]\s*/, "") || "语音通话";
}

// 列表里一行预览，机器人回复里的 Markdown 标记去掉再显示。
function previewOf(id: string): string {
  const last = lastOf(id);
  if (!last?.text) return "";
  return last.role === "assistant" ? markdownToPlain(last.text) : last.text;
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
    if (isRoomKey(selected.value) || selected.value === CHAT_ADMIN) return;
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
  if (adminSelected.value) return sendAdmin();
  if (currentRoom.value) return sendRoom();
  const text = draft.value.trim();
  if (!text || !current.value) return;
  draft.value = "";
  const local: WebChatMessage = { id: `local-${++localSeq}`, role: "user", text, at: Math.floor(Date.now() / 1000), pending: true };
  messages.value = [...messages.value, local];
  await deliver(local);
}

async function deliver(local: WebChatMessage): Promise<void> {
  const profile = selected.value;
  inFlight.value = { ...inFlight.value, [profile]: (inFlight.value[profile] ?? 0) + 1 };
  await scrollToBottom();
  const controller = new AbortController();
  sendControllers.add(controller);
  try {
    await sendWebChat(profile, local.text, controller.signal, (reply) => {
      if (profile !== selected.value) return;
      markSent(local);
      messages.value = [...messages.value, reply];
      void scrollToBottom();
    });
    markSent(local);
  } catch (err) {
    if (profile === selected.value) {
      messages.value = messages.value.map((m) => (m.id === local.id ? { ...m, pending: false, failed: true } : m));
      if (!controller.signal.aborted) error.value = err instanceof Error ? err.message : String(err);
    }
  } finally {
    inFlight.value = { ...inFlight.value, [profile]: Math.max(0, (inFlight.value[profile] ?? 1) - 1) };
    sendControllers.delete(controller);
    rememberLast(profile);
  }
}

function markSent(local: WebChatMessage): void {
  if (!local.pending) return;
  local.pending = false;
  messages.value = messages.value.map((m) => (m.id === local.id ? { ...m, pending: false } : m));
}

function resend(message: WebChatMessage): void {
  error.value = "";
  const retry = { ...message, id: `local-${++localSeq}`, pending: true, failed: false, at: Math.floor(Date.now() / 1000) };
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

// —— 本地群 ——
// 选中的是群时 selected 存 "room:<id>"，和机器人 ID 分开。
const roomPrefix = "room:";
const rooms = ref<LocalRoom[]>([]);
const roomDialog = ref<"" | "create" | "edit">("");
const roomMessages = ref<LocalRoomMessage[]>([]);
const roomStatus = ref<LocalRoomStatus | null>(null);
const roomLoading = ref(false);
const roomError = ref("");
// 页面上还没结束的发言流。
const roomSends = ref(0);
const roomLast = ref<Record<string, LocalRoomMessage>>({});
let roomController: AbortController | null = null;
let roomPoll: number | undefined;

function roomKey(id: string): string {
  return roomPrefix + id;
}

function isRoomKey(value: string): boolean {
  return value.startsWith(roomPrefix);
}

const currentRoom = computed(() => (isRoomKey(selected.value) ? rooms.value.find((room) => roomKey(room.id) === selected.value) : undefined));
const filteredRooms = computed(() => {
  const keyword = search.value.trim().toLowerCase();
  return keyword ? rooms.value.filter((room) => room.name.toLowerCase().includes(keyword)) : rooms.value;
});
const roomBusy = computed(() => roomSends.value > 0 || (roomStatus.value?.thinking ?? 0) > 0);
const roomDividers = computed(() => timeDividers(roomMessages.value));
const contactByID = computed(() => new Map(contacts.value.map((contact) => [contact.id, contact])));
const roomMemberNames = computed(() => (currentRoom.value?.members ?? []).map((id) => contactByID.value.get(id)?.name ?? id).join("、"));
const roomSubtitle = computed(() => {
  const room = currentRoom.value;
  if (!room) return "";
  return roomBusy.value ? "群里有人在说话…" : `${room.members.length} 个成员`;
});

function memberAvatar(id: string): string | undefined {
  return contactByID.value.get(id)?.avatar;
}

function roomPreview(room: LocalRoom): string {
  const last = roomLast.value[room.id];
  if (!last) return `${room.members.length} 个成员`;
  return last.sender_id === LOCAL_ROOM_OWNER ? `我：${last.text}` : `${last.sender_name}：${markdownToPlain(last.text)}`;
}

function rememberRoomLast(id: string): void {
  const sent = roomMessages.value.filter((message) => !message.failed);
  const last = sent[sent.length - 1];
  if (last) roomLast.value = { ...roomLast.value, [id]: last };
}

async function loadRooms(): Promise<void> {
  try {
    rooms.value = await listLocalRooms();
  } catch {
    // 群列表读不到不影响私聊。
  }
  if (!isRoomKey(selected.value)) return;
  if (currentRoom.value) void loadRoom();
  else {
    selected.value = contacts.value[0]?.id ?? "";
    if (selected.value) void loadHistory();
  }
}

// quiet 是后台轮询：不显示加载骨架，有新消息才滚到底。
async function loadRoom(quiet = false): Promise<void> {
  const room = currentRoom.value;
  if (!room) return;
  roomController?.abort();
  const controller = new AbortController();
  roomController = controller;
  if (!quiet) roomLoading.value = true;
  roomError.value = "";
  try {
    const result = await getLocalRoom(room.id, controller.signal);
    if (controller.signal.aborted || currentRoom.value?.id !== room.id) return;
    const grew = result.messages.length !== roomMessages.value.length;
    rooms.value = rooms.value.map((item) => (item.id === result.room.id ? result.room : item));
    roomMessages.value = result.messages;
    roomStatus.value = result.status;
    rememberRoomLast(room.id);
    if (!quiet || grew) await scrollToBottom();
    scheduleRoomPoll();
  } catch (err) {
    if (controller.signal.aborted) return;
    roomError.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (roomController === controller) roomLoading.value = false;
  }
}

// 讨论还在进行、但这个页面没开着发言流（比如刚打开，或者流已经断了）时，隔两秒刷一次。
function scheduleRoomPoll(): void {
  window.clearTimeout(roomPoll);
  if ((roomStatus.value?.thinking ?? 0) > 0 && roomSends.value === 0) roomPoll = window.setTimeout(() => void loadRoom(true), 2000);
}

function openRoom(id: string): void {
  showList.value = false;
  if (roomKey(id) === selected.value && roomMessages.value.length) return;
  selected.value = roomKey(id);
  try {
    localStorage.setItem(selectedKey, selected.value);
  } catch {
    // 记不住也不影响聊天。
  }
  roomMessages.value = [];
  roomStatus.value = null;
  draft.value = "";
  void loadRoom();
}

async function sendRoom(): Promise<void> {
  const room = currentRoom.value;
  const text = draft.value.trim();
  if (!room || !text) return;
  draft.value = "";
  const local: LocalRoomMessage = { id: `local-${++localSeq}`, sender_id: LOCAL_ROOM_OWNER, sender_name: "主人", text, at: Math.floor(Date.now() / 1000), pending: true };
  roomMessages.value = [...roomMessages.value, local];
  roomSends.value++;
  window.clearTimeout(roomPoll);
  await scrollToBottom();
  const controller = new AbortController();
  sendControllers.add(controller);
  const settle = (patch: Partial<LocalRoomMessage>) => {
    roomMessages.value = roomMessages.value.map((message) => (message.id === local.id ? { ...message, ...patch } : message));
  };
  try {
    const status = await sendLocalRoom(room.id, text, controller.signal, (message) => {
      if (currentRoom.value?.id !== room.id) return;
      settle({ pending: false });
      roomMessages.value = [...roomMessages.value, message];
      void scrollToBottom();
    });
    if (currentRoom.value?.id === room.id) {
      settle({ pending: false });
      if (status) roomStatus.value = status;
    }
  } catch (err) {
    if (currentRoom.value?.id === room.id) {
      settle({ pending: false, failed: true });
      if (!controller.signal.aborted) roomError.value = err instanceof Error ? err.message : String(err);
    }
  } finally {
    roomSends.value = Math.max(0, roomSends.value - 1);
    sendControllers.delete(controller);
    rememberRoomLast(room.id);
    scheduleRoomPoll();
  }
}

async function stopRoom(): Promise<void> {
  const room = currentRoom.value;
  if (!room) return;
  try {
    roomStatus.value = await stopLocalRoom(room.id);
  } catch (err) {
    roomError.value = err instanceof Error ? err.message : String(err);
  }
}

async function onRoomSaved(room: LocalRoom, created: boolean): Promise<void> {
  roomDialog.value = "";
  if (!created) {
    rooms.value = rooms.value.map((item) => (item.id === room.id ? room : item));
    return;
  }
  rooms.value = [room, ...rooms.value.filter((item) => item.id !== room.id)];
  // 一句话建群会新建机器人，列表里要有它们的名字和头像。
  await loadProfiles();
  openRoom(room.id);
}

function onRoomDeleted(id: string): void {
  roomDialog.value = "";
  rooms.value = rooms.value.filter((room) => room.id !== id);
  selected.value = contacts.value[0]?.id ?? "";
  showList.value = true;
  roomMessages.value = [];
  if (selected.value) void loadHistory();
}

// —— 管理助手 ——
const adminSelected = computed(() => selected.value === CHAT_ADMIN);

function openAdmin(): void {
  showList.value = false;
  const switching = selected.value !== CHAT_ADMIN;
  selected.value = CHAT_ADMIN;
  try {
    localStorage.setItem(selectedKey, selected.value);
  } catch {
    // 记不住也不影响聊天。
  }
  if (switching || !adminMessages.value.length) void loadAdmin();
}

// 管理助手就是一个联系人：一段会话，和机器人私聊一样的气泡。管理对话的会话按
// 「机器人范围」分，这里跟着侧栏选的机器人走；记住最近用的那段会话。
interface AdminBubble {
  id: string;
  role: "user" | "assistant";
  text: string;
  at: number;
  error?: boolean;
  tools?: number;
  pending?: boolean;
  failed?: boolean;
}
const adminSessionKey = "diana:chat-admin-session";
const adminSessionID = ref("");
const adminMessages = ref<AdminBubble[]>([]);
const adminLoading = ref(false);
const adminError = ref("");
const adminBusy = ref(false);
const adminProgressText = ref("正在处理…");
const adminDividers = computed(() => timeDividers(adminMessages.value));
let adminController: AbortController | null = null;

function adminBubble(message: AdminChatMessage): AdminBubble {
  const at = message.created_at ? Math.floor(new Date(message.created_at).getTime() / 1000) : Math.floor(Date.now() / 1000);
  const tools = (message.steps ?? []).filter((step) => step.tool && step.phase !== "started").length;
  return { id: message.id, role: message.role, text: message.content, at, error: message.error, tools: tools || undefined };
}

function adminProfile(): string {
  return botScope.value || "";
}

async function loadAdmin(): Promise<void> {
  adminController?.abort();
  const controller = new AbortController();
  adminController = controller;
  adminLoading.value = true;
  adminError.value = "";
  try {
    const profile = adminProfile();
    let remembered = "";
    try {
      remembered = localStorage.getItem(adminSessionKey) ?? "";
    } catch {
      // 记不住就用最近一段。
    }
    const sessions = await listAdminChatSessions(profile, controller.signal);
    const id = sessions.some((item) => item.session_id === remembered) ? remembered : sessions[0]?.session_id || "";
    const session = await getAdminChat(profile, controller.signal, id);
    if (controller.signal.aborted || !adminSelected.value) return;
    adminSessionID.value = session.session_id;
    rememberAdminSession(session.session_id);
    adminMessages.value = (session.messages ?? []).map(adminBubble);
    adminBusy.value = session.running;
    await scrollToBottom();
  } catch (err) {
    if (controller.signal.aborted) return;
    adminError.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (adminController === controller) adminLoading.value = false;
  }
}

function rememberAdminSession(id: string): void {
  try {
    localStorage.setItem(adminSessionKey, id);
  } catch {
    // 记不住也不影响对话。
  }
}

async function newAdminSession(): Promise<void> {
  try {
    const created = await createAdminChatSession(adminProfile());
    rememberAdminSession(created.session_id);
    adminMessages.value = [];
    await loadAdmin();
  } catch (err) {
    adminError.value = err instanceof Error ? err.message : String(err);
  }
}

async function sendAdmin(): Promise<void> {
  const text = draft.value.trim();
  if (!text || !adminSessionID.value || adminBusy.value) return;
  draft.value = "";
  const local: AdminBubble = { id: `local-${++localSeq}`, role: "user", text, at: Math.floor(Date.now() / 1000), pending: true };
  adminMessages.value = [...adminMessages.value, local];
  adminBusy.value = true;
  adminProgressText.value = "正在处理…";
  await scrollToBottom();
  const controller = new AbortController();
  sendControllers.add(controller);
  try {
    await sendAdminChat(adminSessionID.value, text, ["", ""], controller.signal, (frame) => {
      if (!adminSelected.value) return;
      if (frame.type === "user") {
        adminMessages.value = adminMessages.value.map((message) => (message.id === local.id ? { ...adminBubble(frame.message), pending: false } : message));
      } else if (frame.type === "message") {
        adminMessages.value = [...adminMessages.value, adminBubble(frame.message)];
        void scrollToBottom();
      } else if (frame.type === "progress") {
        adminProgressText.value = frame.progress.tool ? `正在调用 ${frame.progress.tool}…` : "正在处理…";
      }
    });
  } catch (err) {
    adminMessages.value = adminMessages.value.map((message) => (message.id === local.id ? { ...message, pending: false, failed: true } : message));
    if (!controller.signal.aborted) adminError.value = err instanceof Error ? err.message : String(err);
  } finally {
    adminBusy.value = false;
    sendControllers.delete(controller);
  }
}

async function stopAdmin(): Promise<void> {
  if (!adminSessionID.value) return;
  try {
    await stopAdminChat(adminSessionID.value);
  } catch (err) {
    adminError.value = err instanceof Error ? err.message : String(err);
  }
}

// 顶栏按钮、旧的 /admin-chat 地址要求切到管理助手。
watch(
  chatOpenTarget,
  (target) => {
    if (!target) return;
    chatOpenTarget.value = "";
    if (target === CHAT_ADMIN) openAdmin();
  },
  { immediate: true }
);
watch(selected, (value) => (chatSelection.value = value), { immediate: true });

void loadProfiles();
void loadRooms();

onBeforeUnmount(() => {
  loadController?.abort();
  roomController?.abort();
  window.clearTimeout(roomPoll);
  for (const controller of sendControllers) controller.abort();
});
</script>

<style scoped>
.qq-chat {
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  height: calc(100dvh - 86px);
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

/* 管理助手的头像和机器人头像一样大，换成工具图标区分开 */
.qq-admin-avatar {
  display: grid;
  flex: none;
  place-items: center;
  width: 46px;
  height: 46px;
  border-radius: 50%;
  background: var(--surface-2);
  color: var(--accent);
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

/* 选中只淡淡地衬一下底色，像微信、QQ 桌面版，不整行涂成主题色。 */
.qq-contact.selected {
  background: color-mix(in srgb, var(--text) 9%, transparent);
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

/* 通话记录：挂断时记的一条，像 QQ 里的「通话时长」提示一样居中 */
.qq-call-record {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  width: fit-content;
  margin: 4px auto 14px;
  padding: 3px 10px;
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--muted);
  font-size: 12px;
}

.qq-list-actions {
  display: flex;
  gap: 2px;
}

/* 群聊：头像是群图标，机器人的气泡上面写名字 */
.qq-room-avatar {
  display: grid;
  flex: none;
  place-items: center;
  width: 46px;
  height: 46px;
  border-radius: 50%;
  background: var(--surface-2);
  color: var(--accent);
}

.qq-admin-avatar.small {
  width: 38px;
  height: 38px;
}

.qq-markdown.err {
  color: var(--err);
}

.qq-room-avatar.small {
  width: 38px;
  height: 38px;
}

.qq-sender {
  margin: 0 0 3px 2px;
  color: var(--muted);
  font-size: 12px;
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

.qq-markdown {
  white-space: normal;
}

/* 气泡本身是 surface-2，代码块和表头换成更深一档，才分得出层次。 */
.qq-markdown :deep(code),
.qq-markdown :deep(.md-code),
.qq-markdown :deep(th) {
  background: var(--surface);
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
    height: calc(100dvh - 90px);
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
