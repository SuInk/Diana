<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Phone } from '@lucide/vue'
import type { Bot, ChatMessage } from '../../../shared/types.ts'
import { formatChatTime, needsTimeSeparator } from '../../../shared/time.ts'
import { mediaURL } from '../lib/media.ts'
import BotAvatar from './BotAvatar.vue'
import RichText from './RichText.vue'

const props = defineProps<{
  messages: (ChatMessage & { pending?: boolean; failed?: boolean })[]
  loading: boolean
  bot: Bot
  ownerAvatar?: string
  ownerName: string
  typing: boolean
}>()

const scroller = ref<HTMLElement | null>(null)

const rows = computed(() =>
  props.messages.map((message, index) => ({
    message,
    separator: needsTimeSeparator(props.messages[index - 1]?.at, message.at) ? formatChatTime(message.at) : '',
  })),
)

// 新消息进来时贴底；用户往上翻着看旧消息时不打扰。
let pinned = true
function onScroll() {
  const el = scroller.value
  if (el) pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 80
}
watch(
  () => [props.messages.length, props.typing, props.bot.id],
  async (_next, previous) => {
    if (previous?.[2] !== props.bot.id) pinned = true
    await nextTick()
    if (pinned && scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
  },
  { immediate: true },
)
</script>

<template>
  <div ref="scroller" class="messages" role="log" aria-live="polite" @scroll="onScroll">
    <p v-if="loading && !messages.length" class="list-note muted">正在加载聊天记录…</p>
    <p v-else-if="!messages.length" class="list-note muted">还没聊过，打个招呼吧</p>
    <template v-for="{ message, separator } in rows" :key="message.id">
      <div v-if="separator" class="time-separator">{{ separator }}</div>
      <div v-if="message.kind === 'call'" class="call-record"><Phone :size="13" aria-hidden="true" /> {{ message.text }}</div>
      <div v-else class="message" :class="{ mine: message.role === 'user', pending: message.pending, failed: message.failed }">
        <BotAvatar v-if="message.role === 'user'" :name="ownerName" :src="ownerAvatar" :size="34" />
        <BotAvatar v-else :name="bot.name" :src="bot.avatar" :size="34" />
        <div class="bubble-column">
          <div class="bubble">
            <blockquote v-if="message.quote && (message.quote.text || message.quote.sender_name)" class="quote">
              <strong v-if="message.quote.sender_name">{{ message.quote.sender_name }}：</strong>{{ message.quote.text }}
            </blockquote>
            <RichText v-if="message.text" :text="message.text" />
            <div v-if="message.images?.length" class="bubble-images">
              <img v-for="url in message.images" :key="url" :src="mediaURL(url)" alt="图片" loading="lazy" />
            </div>
          </div>
          <span v-if="message.failed" class="bubble-status error">没发出去</span>
        </div>
      </div>
    </template>
    <div v-if="typing" class="message typing" aria-label="对方正在输入">
      <BotAvatar :name="bot.name" :src="bot.avatar" :size="34" />
      <div class="bubble"><span class="dots"><i /><i /><i /></span></div>
    </div>
  </div>
</template>
