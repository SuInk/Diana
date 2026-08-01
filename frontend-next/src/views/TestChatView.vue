<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>测试台</h1>
        <p>直接调用当前激活的 LLM 配置，验证连通与回复质量</p>
      </div>
      <div class="view-actions">
        <span v-if="currentModel" class="badge accent">{{ currentModel }}</span>
        <button class="btn" type="button" :disabled="messages.length === 0" @click="clearMessages">
          <RotateCcw :size="15" aria-hidden="true" />
          清空对话
        </button>
      </div>
    </header>

    <section class="card chat-panel">
      <div ref="scrollArea" class="chat-scroll">
        <EmptyState
          v-if="messages.length === 0"
          title="发送一条消息开始测试"
          hint="每条消息独立调用一次 /api/llm/test，不携带上下文"
        >
          <template #icon><MessageCircle :size="20" aria-hidden="true" /></template>
        </EmptyState>
        <template v-for="message in messages" :key="message.id">
          <div class="chat-bubble" :class="message.role">
            {{ message.text }}
            <span v-if="message.meta" class="bubble-meta">{{ message.meta }}</span>
          </div>
        </template>
        <div v-if="sending" class="chat-bubble bot">
          <span class="muted">正在生成…</span>
        </div>
      </div>
      <div class="chat-composer">
        <textarea
          v-model="draft"
          class="textarea"
          rows="1"
          placeholder="输入消息，Enter 发送，Shift+Enter 换行"
          @keydown.enter.exact.prevent="send"
        ></textarea>
        <button class="btn primary" type="button" :disabled="sending || !draft.trim()" @click="send">
          <Send :size="15" aria-hidden="true" />
          发送
        </button>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, ref } from "vue";
import { MessageCircle, RotateCcw, Send } from "@lucide/vue";
import { getConfig, testLLM } from "../api";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";

interface ChatMessage {
  id: number;
  role: "user" | "bot" | "error";
  text: string;
  meta?: string;
}

const messages = ref<ChatMessage[]>([]);
const draft = ref("");
const sending = ref(false);
const currentModel = ref("");
const scrollArea = ref<HTMLElement | null>(null);

let nextID = 1;

function scrollToBottom(): void {
  void nextTick(() => {
    const area = scrollArea.value;
    if (area) {
      area.scrollTop = area.scrollHeight;
    }
  });
}

async function send(): Promise<void> {
  const text = draft.value.trim();
  if (!text || sending.value) {
    return;
  }
  draft.value = "";
  messages.value.push({ id: nextID++, role: "user", text });
  sending.value = true;
  scrollToBottom();
  const startedAt = performance.now();
  try {
    const result = await testLLM(text);
    const elapsed = ((performance.now() - startedAt) / 1000).toFixed(1);
    const usage = result.usage ? ` · ${result.usage.output_tokens ?? 0} tokens` : "";
    messages.value.push({
      id: nextID++,
      role: "bot",
      text: result.text,
      meta: `${result.model ?? ""} · ${elapsed}s${usage}`.replace(/^ · /, "")
    });
  } catch (error) {
    messages.value.push({
      id: nextID++,
      role: "error",
      text: error instanceof Error ? error.message : "请求失败"
    });
  } finally {
    sending.value = false;
    scrollToBottom();
  }
}

function clearMessages(): void {
  messages.value = [];
}

onMounted(async () => {
  try {
    const config = await getConfig();
    currentModel.value = `${config.provider} · ${config.model}`;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "读取当前配置失败");
  }
});
</script>
