<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { LayoutDashboard, PanelLeft, RefreshCw, Settings, TriangleAlert } from '@lucide/vue'
import type { ApiResult, AppCommand, Bot, ChatFrame, ChatInfo, ChatMessage, SessionState } from '../../../shared/types.ts'
import BotAvatar from './BotAvatar.vue'
import MessageList from './MessageList.vue'
import Composer from './Composer.vue'
import SettingsView from './SettingsView.vue'

const props = defineProps<{ session: SessionState }>()
const emit = defineEmits<{ logout: []; expired: []; 'use-remote': []; 'use-local': [] }>()

// 主面板显示什么：和某个机器人的私聊、嵌进来的管理后台，或桌面端自己的设置。
type View = 'chat' | 'admin' | 'settings'
const view = ref<View>('chat')

interface LocalMessage extends ChatMessage {
  pending?: boolean
  failed?: boolean
}

interface Conversation {
  info?: ChatInfo
  messages: LocalMessage[]
  loading: boolean
  error: string
  /** 正在等回复的那次发送 */
  requestId: string
}

const bots = ref<Bot[]>([])
const botsLoading = ref(true)
const botsError = ref('')
const selectedID = ref('')
const conversations = reactive(new Map<string, Conversation>())

const selected = computed(() => bots.value.find((bot) => bot.id === selectedID.value))
const conversation = computed(() => conversations.get(selectedID.value))
const sending = computed(() => Boolean(conversation.value?.requestId))
const host = computed(() => props.session.server.replace(/^https?:\/\//, ''))
const statusLabel = computed(() => (props.session.mode === 'local' ? '本机 · 运行中' : `${props.session.username} · ${host.value}`))

// 侧栏开合记在本机；进管理后台时先收起，它自己有一列导航，两列并排太挤，出来再还原。
const sidebarKey = 'diana-desktop:sidebar'
function readSidebar(): boolean {
  try {
    return localStorage.getItem(sidebarKey) !== '0'
  } catch {
    return true
  }
}
const sidebarOpen = ref(readSidebar())
let sidebarBeforeAdmin: boolean | undefined
function setSidebar(open: boolean) {
  sidebarOpen.value = open
  sidebarBeforeAdmin = undefined
  try {
    localStorage.setItem(sidebarKey, open ? '1' : '0')
  } catch {
    // 存不了就只在这次打开里生效。
  }
}

function go(next: View) {
  if (next === view.value) return
  if (next === 'admin') {
    sidebarBeforeAdmin = sidebarOpen.value
    sidebarOpen.value = false
  } else if (view.value === 'admin' && sidebarBeforeAdmin !== undefined) {
    sidebarOpen.value = sidebarBeforeAdmin
    sidebarBeforeAdmin = undefined
  }
  view.value = next
}

function openChat(id: string) {
  go('chat')
  void select(id)
}

// 管理后台是主进程叠在窗口上的一层，界面只负责留出位置、量好告诉它。
const adminHost = ref<HTMLElement | null>(null)
const adminError = ref('')
function hostRect() {
  const rect = adminHost.value!.getBoundingClientRect()
  return { x: rect.x, y: rect.y, width: rect.width, height: rect.height }
}
let observer: ResizeObserver | undefined
watch(view, async (next, previous) => {
  if (previous === 'admin') {
    observer?.disconnect()
    window.diana.hideAdmin()
  }
  if (next !== 'admin') return
  await nextTick()
  if (!adminHost.value) return
  adminError.value = ''
  observer = new ResizeObserver(() => adminHost.value && window.diana.placeAdmin(hostRect()))
  observer.observe(adminHost.value)
  const result = await window.diana.showAdmin(hostRect())
  if (!result.ok) {
    adminError.value = result.error
    if (result.unauthorized) emit('expired')
  }
})

function reload() {
  if (view.value === 'admin') window.diana.reloadAdmin()
  else if (view.value === 'chat') {
    void loadBots()
    if (selectedID.value) void select(selectedID.value)
  }
}

function onCommand(command: AppCommand) {
  if (command === 'sidebar') setSidebar(!sidebarOpen.value)
  else if (command === 'reload') reload()
  else go(command)
}

const title = computed(() => {
  if (view.value === 'admin') return { name: '管理后台', sub: host.value }
  if (view.value === 'settings') return { name: '设置', sub: '' }
  const bot = selected.value
  if (!bot) return { name: '消息', sub: '' }
  return { name: conversation.value?.info?.bot_name || bot.name, sub: sending.value ? '对方正在输入…' : bot.platform }
})

function check<T>(result: ApiResult<T>): T | undefined {
  if (result.ok) return result.value
  if (result.unauthorized) emit('expired')
  return undefined
}

async function loadBots() {
  botsLoading.value = true
  botsError.value = ''
  const result = await window.diana.bots()
  botsLoading.value = false
  const list = check(result)
  if (!list) {
    if (!result.ok) botsError.value = result.error
    return
  }
  bots.value = list
  if (!list.some((bot) => bot.id === selectedID.value) && list.length) await select(list[0].id)
}

async function select(id: string) {
  selectedID.value = id
  if (!conversations.has(id)) conversations.set(id, { messages: [], loading: false, error: '', requestId: '' })
  const current = conversations.get(id)!
  if (current.requestId) return
  current.loading = true
  current.error = ''
  const result = await window.diana.history(id)
  current.loading = false
  const history = check(result)
  if (!history) {
    if (!result.ok) current.error = result.error
    return
  }
  current.info = history.info
  current.messages = history.messages
}

let sequence = 0

async function send(text: string) {
  const id = selectedID.value
  const current = conversations.get(id)
  if (!current || current.requestId) return
  const requestId = `${Date.now()}-${++sequence}`
  const local: LocalMessage = { id: `local-${requestId}`, role: 'user', text, at: Math.floor(Date.now() / 1000), pending: true }
  current.messages.push(local)
  current.requestId = requestId
  current.error = ''
  const result = await window.diana.send(requestId, id, text)
  const mine = current.messages.find((message) => message.id === local.id)
  if (mine) mine.pending = false
  current.requestId = ''
  if (!result.ok) {
    if (mine) mine.failed = true
    current.error = result.error
    if (result.unauthorized) emit('expired')
  }
}

function onFrame(frame: ChatFrame) {
  if (frame.type !== 'message') return
  for (const current of conversations.values()) {
    if (current.requestId !== frame.requestId) continue
    if (!current.messages.some((message) => message.id === frame.message.id)) current.messages.push(frame.message)
  }
}

let unsubscribe = () => {}
let unsubscribeCommand = () => {}
onMounted(() => {
  unsubscribe = window.diana.onFrame(onFrame)
  unsubscribeCommand = window.diana.onCommand(onCommand)
  void loadBots()
})
onBeforeUnmount(() => {
  unsubscribe()
  unsubscribeCommand()
  observer?.disconnect()
  window.diana.hideAdmin()
})
</script>

<template>
  <div class="shell" :class="{ 'sidebar-hidden': !sidebarOpen }">
    <aside v-show="sidebarOpen" class="sidebar" aria-label="导航">
      <div class="sidebar-top" aria-hidden="true" />
      <div class="identity">
        <span class="identity-logo" aria-hidden="true">D</span>
        <span class="identity-text">
          <strong>Diana</strong>
          <span class="identity-status muted" :title="session.server"><i class="status-dot" aria-hidden="true" />{{ statusLabel }}</span>
        </span>
      </div>

      <nav class="sidebar-nav">
        <div class="nav-heading">
          <span>对话</span>
          <button class="icon-button small" type="button" title="刷新机器人列表" aria-label="刷新机器人列表" :disabled="botsLoading" @click="loadBots"><RefreshCw :size="13" :class="{ spin: botsLoading }" aria-hidden="true" /></button>
        </div>
        <p v-if="botsError" class="list-note error">{{ botsError }}</p>
        <p v-else-if="!botsLoading && !bots.length" class="list-note muted">这台服务器上还没有机器人</p>
        <button
          v-for="bot in bots"
          :key="bot.id"
          class="nav-item bot-row"
          :class="{ selected: view === 'chat' && bot.id === selectedID }"
          :aria-current="view === 'chat' && bot.id === selectedID ? 'page' : undefined"
          type="button"
          @click="openChat(bot.id)"
        >
          <BotAvatar :name="bot.name" :src="bot.avatar" :size="26" />
          <span class="bot-row-text">
            <span class="bot-name">{{ bot.name }}</span>
            <span class="bot-meta muted">{{ conversations.get(bot.id)?.requestId ? '正在输入…' : bot.platform || '私聊' }}</span>
          </span>
        </button>

        <div class="nav-heading"><span>管理</span></div>
        <button class="nav-item" :class="{ selected: view === 'admin' }" :aria-current="view === 'admin' ? 'page' : undefined" type="button" @click="go('admin')">
          <LayoutDashboard :size="17" aria-hidden="true" /> 管理后台
        </button>
      </nav>

      <footer class="sidebar-footer">
        <button class="nav-item" :class="{ selected: view === 'settings' }" :aria-current="view === 'settings' ? 'page' : undefined" type="button" @click="go('settings')">
          <Settings :size="17" aria-hidden="true" /> 设置
        </button>
      </footer>
    </aside>

    <main class="panel">
      <header class="toolbar">
        <button
          class="icon-button toolbar-button"
          type="button"
          :title="sidebarOpen ? '隐藏侧栏' : '显示侧栏'"
          :aria-label="sidebarOpen ? '隐藏侧栏' : '显示侧栏'"
          :aria-expanded="sidebarOpen"
          @click="setSidebar(!sidebarOpen)"
        >
          <PanelLeft :size="17" aria-hidden="true" />
        </button>
        <div class="toolbar-title">
          <h1>{{ title.name }}</h1>
          <span v-if="title.sub" class="muted">{{ title.sub }}</span>
        </div>
        <span class="toolbar-spacer" />
        <button v-if="view !== 'settings'" class="icon-button toolbar-button" type="button" title="刷新" aria-label="刷新" @click="reload"><RefreshCw :size="16" aria-hidden="true" /></button>
      </header>

      <template v-if="view === 'chat'">
        <section v-if="selected && conversation" class="chat" :aria-label="`和 ${selected.name} 的私聊`">
          <div v-if="conversation.info?.problem" class="banner warn" role="status">
            <TriangleAlert :size="15" aria-hidden="true" />
            <span class="banner-text">{{ conversation.info.problem }}</span>
            <button class="button banner-action" type="button" @click="go('admin')">去管理后台设置</button>
          </div>
          <MessageList :messages="conversation.messages" :loading="conversation.loading" :bot="selected" :owner-avatar="conversation.info?.owner_avatar_url" :owner-name="session.username || '我'" :typing="sending" />
          <p v-if="conversation.error" class="chat-error" role="alert">{{ conversation.error }}</p>
          <Composer :disabled="Boolean(conversation.info?.problem) || conversation.loading" :busy="sending" :placeholder="`和 ${selected.name} 说点什么…`" @send="send" />
        </section>
        <section v-else class="chat chat-empty">
          <p class="muted">{{ botsLoading ? '正在加载…' : '选一个机器人开始聊天' }}</p>
        </section>
      </template>
      <div v-else-if="view === 'admin'" ref="adminHost" class="admin-host">
        <p v-if="adminError" class="form-error admin-error" role="alert">{{ adminError }}</p>
      </div>
      <SettingsView v-else :session="session" @use-remote="emit('use-remote')" @use-local="emit('use-local')" @logout="emit('logout')" />
    </main>
  </div>
</template>
