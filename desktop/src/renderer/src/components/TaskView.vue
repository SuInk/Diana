<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
// 任务：交给 Diana 的 Agent 去做的一件事。对话流里是你说的话、它的回复，回复上面挂着
// 它这一轮调了哪些工具；正在跑的时候步骤实时冒出来，随时能停。
// 新任务（还没有 id）先显示居中的输入框，发第一句时才在服务端建会话。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowUp, Square } from '@lucide/vue'
import type { ApiResult, ModelOption, TaskMessage, TaskSession, TaskStep, TaskStreamFrame } from '../../../shared/types.ts'
import StepList from './StepList.vue'
import RichText from './RichText.vue'

// 任务不绑某个机器人：用默认的模型分配和权限。
const profile = ''

const props = defineProps<{ sessionId: string; models: ModelOption[] }>()
const emit = defineEmits<{ created: [string]; changed: []; expired: []; title: [string] }>()

interface LocalMessage extends TaskMessage {
  pending?: boolean
}

const currentId = ref(props.sessionId)
const session = ref<TaskSession | null>(null)
const messages = ref<LocalMessage[]>([])
const loading = ref(false)
const error = ref('')
const requestId = ref('')
const liveSteps = ref<TaskStep[]>([])
const currentStep = ref<TaskStep | null>(null)
const stopping = ref(false)
const draft = ref('')
const input = ref<HTMLTextAreaElement | null>(null)
const scroller = ref<HTMLElement | null>(null)

const running = computed(() => Boolean(requestId.value) || Boolean(session.value?.running))
const empty = computed(() => !loading.value && !messages.value.length && !requestId.value)

// 模型选择记在本机，留空跟随默认的模型分配。
const modelKey = 'diana-desktop:task-model'
function readModel(): string {
  try {
    return localStorage.getItem(modelKey) ?? ''
  } catch {
    return ''
  }
}
const modelValue = ref(readModel())
const selectedModel = computed(() => props.models.find((m) => `${m.provider}::${m.model}` === modelValue.value))
watch(modelValue, (value) => {
  try {
    localStorage.setItem(modelKey, value)
  } catch {
    // 存不了就只在这次打开里生效。
  }
})

const permissions = computed(() => {
  const s = session.value
  if (!s) return []
  return [
    s.agent_mode === 'safe' ? '安全模式' : '标准模式',
    s.command_enabled ? '可执行命令' : '不能执行命令',
    s.file_write_enabled ? '可写文件' : '只读文件',
    s.network_enabled ? '可联网' : '不联网',
  ]
})

function check<T>(result: ApiResult<T>): T | undefined {
  if (result.ok) return result.value
  if (result.unauthorized) emit('expired')
  error.value = result.error
  return undefined
}

async function load(id: string) {
  currentId.value = id
  session.value = null
  messages.value = []
  error.value = ''
  liveSteps.value = []
  currentStep.value = null
  if (!id) {
    // 新任务：拿一份默认会话的权限信息来显示，不建会话。
    const info = await window.diana.task(profile)
    if (info.ok) session.value = { ...info.value, session_id: '', messages: [], running: false }
    return
  }
  loading.value = true
  const result = await window.diana.task(profile, id)
  loading.value = false
  const value = check(result)
  if (!value || currentId.value !== id) return
  session.value = value
  messages.value = value.messages
  emit('title', value.title ?? '')
  await scrollToEnd()
}

async function scrollToEnd() {
  await nextTick()
  if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
}

function resize() {
  const el = input.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 220)}px`
}

let sequence = 0
async function send() {
  const text = draft.value.trim()
  if (!text || running.value) return
  error.value = ''
  let id = currentId.value
  if (!id) {
    const created = check(await window.diana.createTask(profile))
    if (!created) return
    id = created
    currentId.value = id
    emit('created', id)
  }
  draft.value = ''
  await nextTick()
  resize()
  const rid = `${Date.now()}-${++sequence}`
  requestId.value = rid
  liveSteps.value = []
  currentStep.value = null
  messages.value.push({ id: `local-${rid}`, role: 'user', content: text, pending: true })
  await scrollToEnd()
  const result = await window.diana.runTask(rid, id, text, selectedModel.value ? { ...selectedModel.value } : undefined)
  if (requestId.value === rid) requestId.value = ''
  stopping.value = false
  currentStep.value = null
  if (!result.ok) {
    error.value = result.error
    if (result.unauthorized) emit('expired')
  }
  emit('changed')
}

function onFrame(frame: TaskStreamFrame) {
  if (frame.requestId !== requestId.value) return
  if (frame.type === 'user') {
    const local = messages.value.find((m) => m.pending)
    if (local) Object.assign(local, frame.message, { pending: false })
  } else if (frame.type === 'progress') {
    const step = frame.progress
    if (step.phase === 'tool_started') currentStep.value = step
    if (step.phase === 'tool_completed') {
      liveSteps.value.push(step)
      currentStep.value = null
    }
  } else if (frame.type === 'message') {
    messages.value.push(frame.message)
    liveSteps.value = []
  }
  void scrollToEnd()
}

async function stop() {
  if (!currentId.value || stopping.value) return
  stopping.value = true
  check(await window.diana.stopTask(currentId.value))
  if (!requestId.value) {
    // 在别处发起、这边只是看着的任务：停完重新读一遍。
    stopping.value = false
    await load(currentId.value)
  }
}

// 回车发送，Shift+回车换行；输入法选词时的回车不算。Esc 停止。
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && running.value) {
    event.preventDefault()
    void stop()
    return
  }
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  void send()
}

watch(
  () => props.sessionId,
  (id) => {
    if (id !== currentId.value) void load(id)
  },
)

let unsubscribe = () => {}
onMounted(() => {
  unsubscribe = window.diana.onTaskFrame(onFrame)
  void load(props.sessionId)
  input.value?.focus()
})
onBeforeUnmount(() => unsubscribe())

defineExpose({ focus: () => input.value?.focus(), reload: () => {
    if (!requestId.value) void load(currentId.value)
  } })
</script>

<template>
  <section class="task" :class="{ 'task-empty': empty }" aria-label="任务">
    <div v-if="!empty" ref="scroller" class="task-thread" role="log" aria-live="polite">
      <p v-if="loading" class="list-note muted">正在加载…</p>
      <template v-for="message in messages" :key="message.id">
        <div v-if="message.role === 'user'" class="turn-user"><div class="turn-user-bubble">{{ message.content }}</div></div>
        <div v-else class="turn-agent" :class="{ error: message.error }">
          <StepList v-if="message.steps?.length" :steps="message.steps" />
          <RichText class="turn-agent-text" :text="message.content" />
        </div>
      </template>
      <div v-if="requestId" class="turn-agent">
        <StepList :steps="liveSteps" :current="currentStep" live />
        <p class="thinking muted"><span class="dots"><i /><i /><i /></span> {{ stopping ? '正在停止…' : currentStep ? '正在执行…' : '正在思考…' }}</p>
      </div>
      <p v-else-if="session?.running" class="thinking muted">这个任务正在别处执行，完成后刷新查看。</p>
    </div>

    <div class="task-bottom">
      <h2 v-if="empty" class="task-greeting">今天要做什么？</h2>
      <p v-if="error" class="chat-error" role="alert">{{ error }}</p>
      <form class="task-composer" @submit.prevent="send">
        <textarea
          ref="input"
          v-model="draft"
          class="composer-input"
          rows="1"
          maxlength="12000"
          :placeholder="running ? '等这一轮做完再发，Esc 停止' : empty ? '描述一件事，交给 Diana 去做…' : '继续说…'"
          aria-label="输入任务"
          @input="resize"
          @keydown="onKeydown"
        />
        <div class="task-composer-bar">
          <label class="model-select">
            <span class="sr-only">模型</span>
            <select v-model="modelValue" :disabled="running">
              <option value="">默认模型</option>
              <option v-for="m in models" :key="`${m.provider}::${m.model}`" :value="`${m.provider}::${m.model}`">{{ m.label }} · {{ m.hint }}</option>
            </select>
          </label>
          <span class="task-permissions muted" :title="permissions.join(' · ')">{{ permissions.join(' · ') }}</span>
          <button v-if="running" class="send-button stop" type="button" title="停止（Esc）" aria-label="停止" :disabled="stopping" @click="stop"><Square :size="13" fill="currentColor" aria-hidden="true" /></button>
          <button v-else class="send-button" type="submit" title="发送（回车）" aria-label="发送" :disabled="!draft.trim()"><ArrowUp :size="17" aria-hidden="true" /></button>
        </div>
      </form>
    </div>
  </section>
</template>
