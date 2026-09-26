<script lang="ts">
import { getPromptCatalog, type PromptCatalog } from "../api";

// 一张卡片里同时挂几个框，目录只取一次；取失败了下回再试。
let catalogRequest: Promise<PromptCatalog> | null = null;
function loadCatalog(): Promise<PromptCatalog> {
  catalogRequest ??= getPromptCatalog().catch((error) => {
    catalogRequest = null;
    throw error;
  });
  return catalogRequest;
}
</script>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { ChevronDown, ChevronRight, Download, RotateCcw, Upload } from "@lucide/vue";
import type { PromptSpec } from "../api";
import {
  composePromptSections,
  customizedPromptCount,
  isPromptFormatCustomized,
  missingPromptVars,
  parsePromptSections,
  promptFormatValue,
  promptSectionsMatch,
  withPromptFormat,
  withPromptSections,
  withoutPromptOverrides,
  type PromptSection
} from "../prompt-overrides";

// 几段内置提示词合在一个框里改：每段前面一行【小标题】，输入时按小标题拆回各段存，
// 和「内置提示词」页读写同一份覆盖。小标题坏了就不写回，框下面说哪里不对。
// 带输出格式的段，格式单独一个框放在下面：改坏了程序解析不了，不跟正文混在一起。
// name 用作导出的文件名；titlePrefix 是各段标题里重复的分类前缀，小标题里省掉。
const props = defineProps<{ modelValue?: Record<string, string>; keys: string[]; name: string; titlePrefix?: string }>();
const emit = defineEmits<{ "update:modelValue": [value: Record<string, string> | undefined] }>();

const catalog = ref<PromptCatalog | null>(null);
const loadError = ref("");
const open = ref(false);
const draft = ref("");
const parseError = ref("");
const fileInput = ref<HTMLInputElement | null>(null);

onMounted(async () => {
  try {
    catalog.value = await loadCatalog();
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : String(error);
  }
});

const overrides = computed(() => props.modelValue ?? {});
const sections = computed<PromptSection[]>(() => {
  const prompts = catalog.value?.prompts ?? [];
  return props.keys
    .map((key) => prompts.find((spec) => spec.key === key))
    .filter((spec): spec is PromptSpec => Boolean(spec))
    .map((spec) => ({ spec, title: props.titlePrefix && spec.title.startsWith(props.titlePrefix) ? spec.title.slice(props.titlePrefix.length) : spec.title }));
});
const composed = computed(() => composePromptSections(sections.value, overrides.value));
const customizedCount = computed(() => customizedPromptCount(sections.value.map((section) => section.spec), overrides.value));
const formatSections = computed(() => sections.value.filter((section) => section.spec.format_key));
const missingVars = computed(() =>
  sections.value.map((section) => ({ title: section.title, vars: missingPromptVars(section.spec, overrides.value) })).filter((item) => item.vars.length)
);
const textArea = ref<HTMLTextAreaElement | null>(null);

// 框按内容撑高：几段加起来一屏装得下，框里再套一层滚动条就看不全小标题了。
async function fitHeight(): Promise<void> {
  await nextTick();
  const el = textArea.value;
  if (!el) return;
  el.style.height = "auto";
  el.style.height = `${el.scrollHeight + 2}px`;
}
watch([open, draft], fitHeight);

// 覆盖表变了而框里的文字对不上，说明是别处改的（恢复默认、「内置提示词」页、导入），
// 换成新拼好的；对得上就是自己刚输入的，不动光标。
watch(
  composed,
  (next) => {
    const parsed = parsePromptSections(draft.value, sections.value);
    if (parsed.ok && promptSectionsMatch(parsed.values, sections.value, overrides.value)) return;
    draft.value = next;
    parseError.value = "";
  },
  { immediate: true }
);

function apply(text: string): boolean {
  const parsed = parsePromptSections(text, sections.value, catalog.value?.max_runes ?? 0);
  if (!parsed.ok) {
    parseError.value = parsed.error;
    return false;
  }
  parseError.value = "";
  emit("update:modelValue", withPromptSections(props.modelValue, sections.value, parsed.values));
  return true;
}

function onInput(event: Event): void {
  draft.value = (event.target as HTMLTextAreaElement).value;
  apply(draft.value);
}

function updateFormat(spec: PromptSpec, value: string): void {
  emit("update:modelValue", withPromptFormat(props.modelValue, spec, value));
}

function resetAll(): void {
  const keys = sections.value.flatMap((section) => (section.spec.format_key ? [section.spec.key, section.spec.format_key] : [section.spec.key]));
  emit("update:modelValue", withoutPromptOverrides(props.modelValue, keys));
}

function exportText(): void {
  const url = URL.createObjectURL(new Blob([draft.value + "\n"], { type: "text/plain;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = `${props.name}.txt`;
  link.click();
  URL.revokeObjectURL(url);
}

async function importText(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  const text = (await file.text()).replace(/\r\n/g, "\n").trim();
  open.value = true;
  if (apply(text)) draft.value = text;
}

// 模板里直接写 `{${name}}` 会被 Vue 当成插值结束符，拼好再给模板。
function placeholder(name: string): string {
  return "{" + name + "}";
}
</script>

<template>
  <div class="prompt-sections">
    <p v-if="loadError" class="prompt-sections-note">提示词列表加载失败：{{ loadError }}</p>
    <p v-else-if="!catalog" class="prompt-sections-note">正在加载内置提示词…</p>
    <template v-else>
      <div class="prompt-sections-toolbar">
        <button class="btn small" type="button" :aria-expanded="open" @click="open = !open">
          <component :is="open ? ChevronDown : ChevronRight" :size="14" aria-hidden="true" />
          {{ open ? "收起" : "展开编辑" }}
        </button>
        <button class="btn small ghost" type="button" @click="exportText">
          <Download :size="14" aria-hidden="true" />
          导出
        </button>
        <button class="btn small ghost" type="button" @click="fileInput?.click()">
          <Upload :size="14" aria-hidden="true" />
          导入
        </button>
        <button class="btn small ghost" type="button" :disabled="!customizedCount" @click="resetAll">
          <RotateCcw :size="14" aria-hidden="true" />
          恢复默认
        </button>
        <input ref="fileInput" type="file" accept=".txt,.md,text/plain,text/markdown" hidden @change="importText" />
        <span class="prompt-sections-note">{{ sections.length }} 段 · {{ customizedCount ? `改过 ${customizedCount} 段` : "都是默认" }}</span>
      </div>
      <template v-if="open">
        <textarea class="textarea prompt-sections-text" :aria-label="name" :value="draft" rows="10" spellcheck="false" ref="textArea" @input="onInput"></textarea>
        <p v-if="parseError" class="prompt-sections-warning">没有保存这次改动：{{ parseError }}</p>
        <p v-else class="prompt-sections-note">按【小标题】分段，小标题单独占一行、原样保留；每段分开保存，和「内置提示词」页是同一份。</p>
        <p v-for="item in missingVars" :key="item.title" class="prompt-sections-warning">【{{ item.title }}】里没有 {{ item.vars.map(placeholder).join("、") }}，这些运行时信息不会再进提示词。</p>
        <div v-for="section in formatSections" :key="section.spec.key" class="prompt-sections-format">
          <span class="prompt-sections-note">
            【{{ section.title }}】的输出格式<template v-if="isPromptFormatCustomized(section.spec, overrides)">（已改）</template>：拼在正文后面，程序按它解析模型的回答。字段名、取值和结构要和程序对得上，改坏了这条链路会沉默或放行。
          </span>
          <textarea
            class="textarea prompt-sections-text"
            :aria-label="`${section.title} · 输出格式`"
            :value="promptFormatValue(section.spec, overrides)"
            :maxlength="catalog.max_runes"
            rows="4"
            spellcheck="false"
            @input="updateFormat(section.spec, ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.prompt-sections { display: grid; gap: 8px; min-width: 0; padding-top: 10px; }
.prompt-sections-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.prompt-sections-note { margin: 0; color: var(--muted); font-size: 12px; line-height: 1.6; }
.prompt-sections-warning { margin: 0; color: var(--warn); font-size: 12px; line-height: 1.6; }
.prompt-sections-text { width: 100%; min-width: 0; resize: vertical; font-size: 13px; line-height: 1.7; }
.prompt-sections > .prompt-sections-text { max-height: 70vh; overflow-y: auto; }
.prompt-sections-format { display: grid; gap: 4px; min-width: 0; }
.prompt-sections-format .prompt-sections-text { border-style: dashed; font-size: 12px; }
</style>
