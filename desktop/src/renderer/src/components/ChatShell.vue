<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { LogOut, RefreshCw, TriangleAlert } from '@lucide/vue'
import type { ApiResult, Bot, ChatFrame, ChatInfo, ChatMessage, SessionState } from '../../../shared/types.ts'
import BotAvatar from './BotAvatar.vue'
import MessageList from './MessageList.vue'
import Composer from './Composer.vue'

const props = defineProps<{ session: SessionState }>()
const emit = defineEmits<{ logout: []; expired: [] }>()

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
const serverLabel = computed(() => props.session.server.replace(/^https?:\/\//, ''))

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
onMounted(() => {
  unsubscribe = window.diana.onFrame(onFrame)
  void loadBots()
})
onBeforeUnmount(() => unsubscribe())
</script>

<template>
  <div class="shell">
    <aside class="sidebar" aria-label="机器人">
      <div class="sidebar-heading">
        <h2>消息</h2>
        <button class="icon-button" type="button" title="刷新机器人列表" aria-label="刷新机器人列表" :disabled="botsLoading" @click="loadBots"><RefreshCw :size="15" :class="{ spin: botsLoading }" aria-hidden="true" /></button>
      </div>
      <nav class="bot-list">
        <p v-if="botsError" class="list-note error">{{ botsError }}</p>
        <p v-else-if="!botsLoading && !bots.length" class="list-note muted">这台服务器上还没有机器人</p>
        <button v-for="bot in bots" :key="bot.id" class="bot-row" :class="{ selected: bot.id === selectedID }" :aria-current="bot.id === selectedID ? 'true' : undefined" type="button" @click="select(bot.id)">
          <BotAvatar :name="bot.name" :src="bot.avatar" :size="38" />
          <span class="bot-row-text">
            <span class="bot-name">{{ bot.name }}</span>
            <span class="bot-meta muted">{{ conversations.get(bot.id)?.requestId ? '正在输入…' : bot.platform || '私聊' }}</span>
          </span>
        </button>
      </nav>
      <footer class="sidebar-footer">
        <span class="server-label muted" :title="session.server">{{ session.username }} · {{ serverLabel }}</span>
        <button class="icon-button" type="button" title="退出登录" aria-label="退出登录" @click="emit('logout')"><LogOut :size="15" aria-hidden="true" /></button>
      </footer>
    </aside>

    <section v-if="selected && conversation" class="chat" :aria-label="`和 ${selected.name} 的私聊`">
      <header class="chat-header">
        <div class="chat-title">
          <h1>{{ conversation.info?.bot_name || selected.name }}</h1>
          <span class="muted">{{ sending ? '对方正在输入…' : selected.platform }}</span>
        </div>
      </header>
      <div v-if="conversation.info?.problem" class="banner warn" role="status"><TriangleAlert :size="15" aria-hidden="true" /> {{ conversation.info.problem }}</div>
      <MessageList :messages="conversation.messages" :loading="conversation.loading" :bot="selected" :owner-avatar="conversation.info?.owner_avatar_url" :owner-name="session.username" :typing="sending" />
      <p v-if="conversation.error" class="chat-error" role="alert">{{ conversation.error }}</p>
      <Composer :disabled="Boolean(conversation.info?.problem) || conversation.loading" :busy="sending" :placeholder="`发给 ${selected.name}`" @send="send" />
    </section>
    <section v-else class="chat chat-empty">
      <p class="muted">{{ botsLoading ? '正在加载…' : '选一个机器人开始聊天' }}</p>
    </section>
  </div>
</template>
