<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { LayoutDashboard, MessageCircle, PanelLeft, RefreshCw, Settings, SquarePen, Trash2, TriangleAlert } from '@lucide/vue'
import type { ApiResult, AppCommand, Bot, ChatFrame, ChatInfo, ChatMessage, ModelOption, SessionState, TaskSummary } from '../../../shared/types.ts'
import MessageList from './MessageList.vue'
import Composer from './Composer.vue'
import SettingsView from './SettingsView.vue'
import TaskView from './TaskView.vue'

const props = defineProps<{ session: SessionState }>()
const emit = defineEmits<{ logout: []; expired: []; 'use-remote': []; 'use-local': [] }>()

// 主面板显示什么：任务（默认，交给 Agent 去做的事）、和某个机器人的私聊、嵌进来的管理后台，或桌面端自己的设置。
type View = 'task' | 'chat' | 'admin' | 'settings'
const view = ref<View>('task')

// ---------- 任务 ----------
const tasks = ref<TaskSummary[]>([])
const taskId = ref('')
const taskTitle = ref('')
const models = ref<ModelOption[]>([])
const taskView = ref<InstanceType<typeof TaskView> | null>(null)

// 空会话（打开过但一句没说）不进列表，正在看的那条除外。
const visibleTasks = computed(() => tasks.value.filter((t) => t.message_count > 0 || t.session_id === taskId.value))
const taskGroups = computed(() => {
  const startOfToday = new Date()
  startOfToday.setHours(0, 0, 0, 0)
  const day = 86_400_000
  const groups: { label: string; items: TaskSummary[] }[] = [
    { label: '今天', items: [] },
    { label: '昨天', items: [] },
    { label: '最近 7 天', items: [] },
    { label: '更早', items: [] },
  ]
  for (const task of visibleTasks.value) {
    const at = new Date(task.updated_at).getTime()
    const index = at >= startOfToday.getTime() ? 0 : at >= startOfToday.getTime() - day ? 1 : at >= startOfToday.getTime() - 6 * day ? 2 : 3
    groups[index].items.push(task)
  }
  return groups.filter((group) => group.items.length)
})

async function loadTasks() {
  const result = await window.diana.tasks('')
  const list = check(result)
  if (list) tasks.value = list
}

function newTask() {
  go('task')
  taskId.value = ''
  taskTitle.value = ''
  void nextTick(() => taskView.value?.focus())
}

function openTask(id: string) {
  go('task')
  taskId.value = id
}

function onTaskCreated(id: string) {
  taskId.value = id
  void loadTasks()
}

async function deleteTask(task: TaskSummary) {
  if (!window.confirm(`删除「${task.title}」？对话记录会一起删掉，不能恢复。`)) return
  const result = await window.diana.deleteTask(task.session_id)
  if (!result.ok) {
    window.alert(result.error)
    return
  }
  if (taskId.value === task.session_id) newTask()
  await loadTasks()
}

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

function openChat() {
  go('chat')
  if (!selectedID.value && bots.value.length) void select(bots.value[0].id)
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
  } else if (view.value === 'task') {
    void loadTasks()
    taskView.value?.reload()
  }
}

function onCommand(command: AppCommand) {
  if (command === 'sidebar') setSidebar(!sidebarOpen.value)
  else if (command === 'reload') reload()
  else if (command === 'new-task') newTask()
  else if (command === 'chat') openChat()
  else go(command)
}

const title = computed(() => {
  if (view.value === 'admin') return { name: '管理后台', sub: host.value }
  if (view.value === 'settings') return { name: '设置', sub: '' }
  if (view.value === 'task') return { name: taskId.value ? taskTitle.value || '任务' : '新任务', sub: '' }
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
  void loadTasks()
  void window.diana.models().then((result) => {
    if (result.ok) models.value = result.value
  })
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
      <button class="nav-item new-task" :class="{ selected: view === 'task' && !taskId }" type="button" title="新任务（⌘N）" @click="newTask">
        <SquarePen :size="17" aria-hidden="true" /> 新任务
      </button>

      <nav class="sidebar-nav" aria-label="任务">
        <template v-for="group in taskGroups" :key="group.label">
          <div class="nav-heading"><span>{{ group.label }}</span></div>
          <div v-for="task in group.items" :key="task.session_id" class="task-row" :class="{ selected: view === 'task' && task.session_id === taskId }">
            <button class="nav-item" type="button" :aria-current="view === 'task' && task.session_id === taskId ? 'page' : undefined" :title="task.title" @click="openTask(task.session_id)">
              <span v-if="task.running" class="task-running" aria-label="执行中" />
              <span class="task-title">{{ task.title }}</span>
            </button>
            <button class="icon-button small task-delete" type="button" :title="`删除「${task.title}」`" :aria-label="`删除「${task.title}」`" :disabled="task.running" @click="deleteTask(task)"><Trash2 :size="13" aria-hidden="true" /></button>
          </div>
        </template>
        <p v-if="!visibleTasks.length" class="list-note muted">还没有任务。在右边说一件事，交给 Diana 去做。</p>
      </nav>

      <footer class="sidebar-footer">
        <button class="nav-item" :class="{ selected: view === 'chat' }" :aria-current="view === 'chat' ? 'page' : undefined" type="button" @click="openChat">
          <MessageCircle :size="17" aria-hidden="true" /> 机器人私聊
        </button>
        <button class="nav-item" :class="{ selected: view === 'admin' }" :aria-current="view === 'admin' ? 'page' : undefined" type="button" @click="go('admin')">
          <LayoutDashboard :size="17" aria-hidden="true" /> 管理后台
        </button>
        <button class="nav-item" :class="{ selected: view === 'settings' }" :aria-current="view === 'settings' ? 'page' : undefined" type="button" @click="go('settings')">
          <Settings :size="17" aria-hidden="true" /> 设置
        </button>
        <p class="identity-status muted" :title="session.server"><i class="status-dot" aria-hidden="true" />{{ statusLabel }}</p>
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
        <button v-if="!sidebarOpen && view === 'task'" class="icon-button toolbar-button" type="button" title="新任务" aria-label="新任务" @click="newTask"><SquarePen :size="16" aria-hidden="true" /></button>
        <div class="toolbar-title">
          <h1>{{ title.name }}</h1>
          <span v-if="title.sub" class="muted">{{ title.sub }}</span>
        </div>
        <span class="toolbar-spacer" />
        <label v-if="view === 'chat' && bots.length > 1" class="model-select toolbar-select">
          <span class="sr-only">机器人</span>
          <select :value="selectedID" @change="select(($event.target as HTMLSelectElement).value)">
            <option v-for="bot in bots" :key="bot.id" :value="bot.id">{{ bot.name }}</option>
          </select>
        </label>
        <button v-if="view !== 'settings'" class="icon-button toolbar-button" type="button" title="刷新" aria-label="刷新" @click="reload"><RefreshCw :size="16" aria-hidden="true" /></button>
      </header>

      <TaskView v-if="view === 'task'" ref="taskView" :session-id="taskId" :models="models" @created="onTaskCreated" @changed="loadTasks" @expired="emit('expired')" @title="taskTitle = $event" />
      <template v-else-if="view === 'chat'">
        <p v-if="botsError" class="chat-error" role="alert">{{ botsError }}</p>
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
          <p class="muted">{{ botsLoading ? '正在加载…' : '这台服务器上还没有机器人' }}</p>
        </section>
      </template>
      <div v-else-if="view === 'admin'" ref="adminHost" class="admin-host">
        <p v-if="adminError" class="form-error admin-error" role="alert">{{ adminError }}</p>
      </div>
      <SettingsView v-else :session="session" @use-remote="emit('use-remote')" @use-local="emit('use-local')" @logout="emit('logout')" />
    </main>
  </div>
</template>
