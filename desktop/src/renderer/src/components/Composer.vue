<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { SendHorizontal } from '@lucide/vue'

// 服务端单条上限 4000 字（webui/web_chat.go 的 webChatMaxText）。
const maxChars = 4000

const props = defineProps<{ disabled: boolean; busy: boolean; placeholder: string }>()
const emit = defineEmits<{ send: [string] }>()

const text = ref('')
const input = ref<HTMLTextAreaElement | null>(null)
const length = computed(() => Array.from(text.value).length)
const canSend = computed(() => !props.disabled && !props.busy && text.value.trim() !== '' && length.value <= maxChars)

function resize() {
  const el = input.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 180)}px`
}

async function submit() {
  if (!canSend.value) return
  emit('send', text.value.trim())
  text.value = ''
  await nextTick()
  resize()
  input.value?.focus()
}

// 回车发送，Shift+回车换行；输入法选词时的回车不算。
function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  void submit()
}
</script>

<template>
  <form class="composer" @submit.prevent="submit">
    <textarea
      ref="input"
      v-model="text"
      class="composer-input"
      rows="1"
      :placeholder="busy ? '等她回完再发…' : placeholder"
      :disabled="disabled"
      aria-label="输入消息"
      @input="resize"
      @keydown="onKeydown"
    />
    <span v-if="length > maxChars - 200" class="composer-count" :class="{ error: length > maxChars }">{{ length }}/{{ maxChars }}</span>
    <span v-else-if="!text && !disabled" class="composer-hint" aria-hidden="true">Enter 发送 · Shift+Enter 换行</span>
    <button class="send-button" type="submit" :disabled="!canSend" title="发送（回车）" aria-label="发送"><SendHorizontal :size="17" aria-hidden="true" /></button>
  </form>
</template>
