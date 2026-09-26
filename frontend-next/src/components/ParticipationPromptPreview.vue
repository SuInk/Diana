<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { ChevronDown, ChevronRight } from "@lucide/vue";
import { previewParticipationPrompt, type BotProfileConfig, type ParticipationPromptPreview } from "../api";

// 接话评分发给模型的原样内容。拼法只在后端一处，这里不自己拼：预览请求带上编辑器里
// 眼前这份配置（可能还没保存），改一个字、换一个档位，预览跟着刷新。
const props = defineProps<{ config: BotProfileConfig }>();

const open = ref(false);
const tab = ref<"chat" | "decision">("chat");
const preview = ref<ParticipationPromptPreview | null>(null);
const loading = ref(false);
const error = ref("");
let timer: ReturnType<typeof setTimeout> | undefined;
let requestSeq = 0;

async function refresh(): Promise<void> {
  const seq = ++requestSeq;
  loading.value = true;
  try {
    const result = await previewParticipationPrompt(props.config);
    if (seq !== requestSeq) return;
    preview.value = result;
    error.value = "";
  } catch (err) {
    if (seq !== requestSeq) return;
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

// 打字时不必每个键都请求一次，停下来 400ms 再刷新。
watch(
  () => props.config,
  () => {
    if (!open.value) return;
    clearTimeout(timer);
    timer = setTimeout(refresh, 400);
  },
  { deep: true }
);
watch(open, (value) => {
  if (value) void refresh();
});
onBeforeUnmount(() => clearTimeout(timer));

function runeCount(text: string): number {
  return Array.from(text).length;
}
</script>

<template>
  <div class="prompt-preview">
    <div class="prompt-preview-toolbar">
      <button class="btn small" type="button" :aria-expanded="open" @click="open = !open">
        <component :is="open ? ChevronDown : ChevronRight" :size="14" aria-hidden="true" />
        {{ open ? "收起预览" : "预览发给模型的内容" }}
      </button>
      <div v-if="open" class="segmented" role="group" aria-label="模型类型">
        <button type="button" :class="{ active: tab === 'chat' }" @click="tab = 'chat'">对话模型</button>
        <button type="button" :class="{ active: tab === 'decision' }" @click="tab = 'decision'">判断模型</button>
      </div>
      <span v-if="open && loading" class="prompt-preview-note">刷新中…</span>
    </div>
    <template v-if="open">
      <p v-if="error" class="prompt-preview-warning">预览失败：{{ error }}</p>
      <template v-else-if="preview && tab === 'chat'">
        <p class="prompt-preview-note">绑对话模型时发这两条消息。用户消息开头按配置拼，后面的上下文是一段示例群聊，实际换成当前消息和最近的群聊记录，图片随消息附带。群里填了补充判据时，拼在系统消息末尾的是群里那份。</p>
        <section class="prompt-preview-block">
          <h4>系统消息 <span>{{ runeCount(preview.system) }} 字</span></h4>
          <pre>{{ preview.system }}</pre>
        </section>
        <section class="prompt-preview-block">
          <h4>用户消息 <span>{{ runeCount(preview.user) }} 字</span></h4>
          <pre>{{ preview.user }}</pre>
        </section>
        <section class="prompt-preview-block">
          <h4>解析失败重问时插在最前 <span>{{ runeCount(preview.retry) }} 字</span></h4>
          <pre>{{ preview.retry }}</pre>
        </section>
      </template>
      <template v-else-if="preview">
        <p class="prompt-preview-note">绑只做判断的模型（如 Jev）时，它按题作答：每道题收到下面的说明、判据或分档，后面再附上「对话模型」里那整条系统消息；用户消息摊平成纯文本作为对话内容。改上面的提示词，两种模型一起变。</p>
        <section v-for="question in preview.decision" :key="question.label" class="prompt-preview-block">
          <h4>{{ question.label }}</h4>
          <pre>{{ question.instructions }}</pre>
          <template v-if="question.true_criteria || question.false_criteria">
            <h5>判为是</h5>
            <pre>{{ question.true_criteria }}</pre>
            <h5>判为否</h5>
            <pre>{{ question.false_criteria }}</pre>
          </template>
          <template v-if="question.levels?.length">
            <h5>分档（从低到高）</h5>
            <pre>{{ question.levels.join("\n") }}</pre>
          </template>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.prompt-preview { display: grid; gap: 10px; min-width: 0; }
.prompt-preview-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.prompt-preview-note { margin: 0; color: var(--muted); font-size: 12px; line-height: 1.6; }
.prompt-preview-warning { margin: 0; color: var(--warn); font-size: 12px; line-height: 1.6; }
.prompt-preview-block { display: grid; gap: 6px; min-width: 0; }
.prompt-preview-block h4 { margin: 0; color: var(--text); font-size: 13px; font-weight: 600; }
.prompt-preview-block h4 span { margin-left: 6px; color: var(--muted); font-size: 12px; font-weight: 400; }
.prompt-preview-block h5 { margin: 4px 0 0; color: var(--muted); font-size: 12px; font-weight: 500; }
.prompt-preview-block pre {
  margin: 0;
  max-height: 60vh;
  overflow: auto;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--text);
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.65;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
