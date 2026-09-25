<script setup lang="ts">
import { computed, onMounted, ref, useId } from "vue";
import { ChevronDown, ChevronRight, Download, RotateCcw, Upload } from "@lucide/vue";
import { getPromptCatalog, type PromptCatalog, type PromptSpec } from "../api";
import { customizedPromptCount, isPromptCustomized, isPromptFormatCustomized, missingPromptVars, promptFormatValue, promptOverrideValue, withoutPromptCustomization, withPromptFormat, withPromptOverride } from "../prompt-overrides";

// 覆盖表只存改过的正文：输入框里显示的是「当前生效的正文」，和默认值一样就从表里删掉，
// 不把默认值抄进配置——那样以后内置文案更新了，这台机器人还停在旧版上。
// keys 给定时是精简模式：只按这个顺序列这几段，不分组、不带搜索，用来把某一类
// 提示词（比如接话判据）嵌到它所属的设置卡片里就地改。每段收起时只占一行，
// 导入导出和恢复默认在展开后的那一行里。
// titlePrefix：精简模式下各段标题里重复的分类前缀（如「接话评分 · 」），列表里省掉。
const props = defineProps<{ modelValue?: Record<string, string>; keys?: string[]; titlePrefix?: string }>();
const emit = defineEmits<{ "update:modelValue": [value: Record<string, string> | undefined] }>();
const id = useId();

const catalog = ref<PromptCatalog | null>(null);
const loadError = ref("");
const query = ref("");
const onlyCustomized = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);
const importTarget = ref<PromptSpec | null>(null);
const importError = ref("");
const expanded = ref<Set<string>>(new Set());

onMounted(async () => {
  try {
    catalog.value = await getPromptCatalog();
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : String(error);
  }
});

const overrides = computed(() => props.modelValue ?? {});
const scopedPrompts = computed(() => {
  const prompts = catalog.value?.prompts ?? [];
  if (!props.keys) return prompts;
  return props.keys.map((key) => prompts.find((spec) => spec.key === key)).filter((spec): spec is PromptSpec => Boolean(spec));
});
const customizedCount = computed(() => customizedPromptCount(scopedPrompts.value, overrides.value));

const sections = computed(() => {
  const data = catalog.value;
  if (!data) return [];
  if (props.keys) return [{ group: null, prompts: scopedPrompts.value }];
  const needle = query.value.trim().toLowerCase();
  return data.groups
    .map((group) => ({
      group,
      prompts: data.prompts.filter((spec) => {
        if (spec.group !== group.id) return false;
        if (onlyCustomized.value && !isCustomized(spec)) return false;
        if (!needle) return true;
        return [spec.title, spec.usage, spec.key, promptOverrideValue(spec, overrides.value)].some((text) => text.toLowerCase().includes(needle));
      })
    }))
    .filter((section) => section.prompts.length > 0);
});

function isCustomized(spec: PromptSpec): boolean {
  return isPromptCustomized(spec, overrides.value);
}

function expandAll(): void {
  expanded.value = new Set(sections.value.flatMap((section) => section.prompts.map((spec) => spec.key)));
}

function collapseAll(): void {
  expanded.value = new Set();
}

const allExpanded = computed(() => {
  const keys = sections.value.flatMap((section) => section.prompts.map((spec) => spec.key));
  return keys.length > 0 && keys.every((key) => expanded.value.has(key));
});

// 收起时的摘要：按「xxx：」分栏数下面的条目，比截一段正文更看得出写了什么。
// 不分栏的段落没有摘要，标题就够了。
function summaryOf(spec: PromptSpec): string {
  const parts: string[] = [];
  let heading = "";
  let count = 0;
  const flush = () => {
    if (heading) parts.push(`${heading} ${count} 条`);
  };
  for (const raw of promptOverrideValue(spec, overrides.value).split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const match = line.match(/^([^-：:]{1,12})[：:]$/);
    if (match) {
      flush();
      heading = match[1];
      count = 0;
    } else if (heading) {
      count++;
    }
  }
  flush();
  return parts.join(" · ");
}

function exportPrompt(spec: PromptSpec): void {
  const url = URL.createObjectURL(new Blob([promptOverrideValue(spec, overrides.value) + "\n"], { type: "text/plain;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = `${displayTitle(spec)}.txt`;
  link.click();
  URL.revokeObjectURL(url);
}

function pickImport(spec: PromptSpec): void {
  importTarget.value = spec;
  fileInput.value?.click();
}

async function importPrompt(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  const spec = importTarget.value;
  if (!file || !spec) return;
  const text = (await file.text()).replace(/\r\n/g, "\n").trim();
  const limit = catalog.value?.max_runes ?? 0;
  if (!text) {
    importError.value = `${displayTitle(spec)}：文件是空的`;
    return;
  }
  if (limit && runeCount(text) > limit) {
    importError.value = `${displayTitle(spec)}：文件有 ${runeCount(text)} 字，超过上限 ${limit} 字`;
    return;
  }
  importError.value = "";
  update(spec, text);
}

function toggle(key: string): void {
  const next = new Set(expanded.value);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  expanded.value = next;
}

function update(spec: PromptSpec, value: string): void {
  emit("update:modelValue", withPromptOverride(overrides.value, spec, value));
}

function updateFormat(spec: PromptSpec, value: string): void {
  emit("update:modelValue", withPromptFormat(overrides.value, spec, value));
}

function reset(spec: PromptSpec): void {
  emit("update:modelValue", withoutPromptCustomization(overrides.value, spec));
}

function resetAll(): void {
  emit("update:modelValue", undefined);
}

function displayTitle(spec: PromptSpec): string {
  const prefix = props.titlePrefix;
  return prefix && spec.title.startsWith(prefix) ? spec.title.slice(prefix.length) : spec.title;
}

// 模板里直接写 `{${name}}` 会被 Vue 当成插值结束符，拼好再给模板。
function placeholder(name: string): string {
  return "{" + name + "}";
}

function runeCount(text: string): number {
  return Array.from(text).length;
}
</script>

<template>
  <div class="prompt-overrides" :class="{ embedded: keys }">
    <input v-if="keys" ref="fileInput" type="file" accept=".txt,.md,text/plain,text/markdown" hidden @change="importPrompt" />
    <p v-if="importError" class="prompt-warning">{{ importError }}</p>
    <div v-if="!keys" class="prompt-toolbar">
      <input v-model="query" class="input" type="search" placeholder="搜索标题、用途或正文" aria-label="搜索提示词" />
      <label class="prompt-filter">
        <input v-model="onlyCustomized" type="checkbox" />
        只看改过的<template v-if="customizedCount">（{{ customizedCount }}）</template>
      </label>
      <button class="btn small" type="button" :disabled="!sections.length" @click="allExpanded ? collapseAll() : expandAll()">
        <component :is="allExpanded ? ChevronDown : ChevronRight" :size="14" aria-hidden="true" />
        {{ allExpanded ? "全部收起" : "全部展开" }}
      </button>
      <button class="btn small" type="button" :disabled="!customizedCount" @click="resetAll">
        <RotateCcw :size="14" aria-hidden="true" />
        全部恢复默认
      </button>
    </div>

    <p v-if="loadError" class="prompt-empty">提示词列表加载失败：{{ loadError }}</p>
    <p v-else-if="!catalog" class="prompt-empty">正在加载内置提示词…</p>
    <p v-else-if="!sections.length" class="prompt-empty">{{ onlyCustomized ? "还没有改过任何提示词。" : "没有匹配的提示词。" }}</p>

    <section v-for="section in sections" :key="section.group?.id ?? 'scoped'" class="prompt-group">
      <header v-if="section.group" class="prompt-group-head">
        <h3>{{ section.group.label }}</h3>
        <p>{{ section.group.description }}</p>
      </header>
      <article v-for="spec in section.prompts" :key="spec.key" class="prompt-item" :class="{ open: expanded.has(spec.key) }">
        <button class="prompt-summary" type="button" :title="keys ? spec.usage : undefined" :aria-expanded="expanded.has(spec.key)" :aria-controls="`${id}-${spec.key}`" @click="toggle(spec.key)">
          <ChevronRight :size="16" class="prompt-chevron" aria-hidden="true" />
          <span class="prompt-title">{{ displayTitle(spec) }}</span>
          <span v-if="keys && !expanded.has(spec.key) && summaryOf(spec)" class="prompt-digest">{{ summaryOf(spec) }}</span>
          <span v-if="isCustomized(spec)" class="badge accent">已修改</span>
          <span v-if="isPromptFormatCustomized(spec, overrides)" class="badge warn" title="输出格式改过，程序可能解析不了模型的回答">格式已改</span>
          <span v-else-if="spec.contract" class="badge" title="这段带输出格式，程序按它解析模型的回答">含输出格式</span>
        </button>
        <p v-if="!keys" class="prompt-usage">{{ spec.usage }}</p>
        <div v-if="expanded.has(spec.key)" :id="`${id}-${spec.key}`" class="prompt-body">
          <textarea
            class="textarea prompt-text"
            :aria-label="spec.title"
            :value="promptOverrideValue(spec, overrides)"
            :maxlength="catalog?.max_runes"
            :rows="keys ? 10 : 8"
            spellcheck="false"
            @input="update(spec, ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
          <div class="prompt-meta">
            <span>{{ runeCount(promptOverrideValue(spec, overrides)) }} 字</span>
            <span v-if="spec.vars?.length" class="prompt-vars">
              占位符：
              <code v-for="variable in spec.vars" :key="variable.name" :title="variable.description">{{ placeholder(variable.name) }}</code>
            </span>
            <template v-if="keys">
              <button class="btn small ghost" type="button" @click="exportPrompt(spec)">
                <Download :size="14" aria-hidden="true" />
                导出
              </button>
              <button class="btn small ghost" type="button" @click="pickImport(spec)">
                <Upload :size="14" aria-hidden="true" />
                导入
              </button>
            </template>
            <button v-if="isCustomized(spec)" class="btn small ghost prompt-reset" type="button" @click="reset(spec)">
              <RotateCcw :size="14" aria-hidden="true" />
              恢复默认
            </button>
          </div>
          <p v-if="missingPromptVars(spec, overrides).length" class="prompt-warning">
            正文里没有 {{ missingPromptVars(spec, overrides).map(placeholder).join("、") }}，这些运行时信息不会再进提示词。
          </p>
          <div v-if="spec.format_key" class="prompt-contract">
            <span class="prompt-contract-label">输出格式（拼在正文后面，程序按它解析模型的回答。字段名、取值和结构要和程序对得上，改坏了这条链路会沉默或放行）</span>
            <textarea
              class="textarea prompt-text prompt-format"
              :aria-label="`${spec.title} · 输出格式`"
              :value="promptFormatValue(spec, overrides)"
              :maxlength="catalog?.max_runes"
              rows="4"
              spellcheck="false"
              @input="updateFormat(spec, ($event.target as HTMLTextAreaElement).value)"
            ></textarea>
          </div>
        </div>
      </article>
    </section>
  </div>
</template>

<style scoped>
.prompt-overrides { display: grid; gap: 16px; min-width: 0; }
.prompt-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 16px; }
.prompt-toolbar > .input { flex: 1 1 240px; min-width: 0; }
.prompt-filter { display: inline-flex; align-items: center; gap: 6px; color: var(--muted); font-size: 13px; cursor: pointer; }
.prompt-empty { margin: 0; color: var(--muted); font-size: 13px; }
.prompt-group { display: grid; gap: 0; min-width: 0; }
.prompt-group-head { padding-bottom: 8px; border-bottom: 1px solid var(--border); }
.prompt-group-head h3 { margin: 0; font-size: 14px; font-weight: 600; color: var(--text); }
.prompt-group-head p { margin: 2px 0 0; color: var(--muted); font-size: 12px; line-height: 1.6; }
.prompt-item { padding: 10px 0; border-bottom: 1px solid var(--border); min-width: 0; }
/* 嵌在设置卡片里时下面紧跟分区线，最后一段再画一条就成了双线。 */
.embedded .prompt-item:last-child { border-bottom: 0; }
.prompt-summary { display: flex; align-items: center; gap: 8px; width: 100%; padding: 0; border: 0; background: none; color: var(--text); font: inherit; text-align: left; cursor: pointer; }
.prompt-summary:focus-visible { outline: 2px solid var(--accent); outline-offset: 4px; border-radius: 4px; }
.prompt-chevron { flex: none; color: var(--muted); transition: transform 0.15s ease; }
.prompt-item.open .prompt-chevron { transform: rotate(90deg); }
.prompt-title { font-size: 14px; font-weight: 500; min-width: 0; overflow-wrap: anywhere; }
.prompt-usage { margin: 2px 0 0 24px; color: var(--muted); font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.prompt-body { display: grid; gap: 8px; margin: 10px 0 4px 24px; min-width: 0; }
.prompt-text { width: 100%; min-width: 0; resize: vertical; font-size: 13px; line-height: 1.7; }
.prompt-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; color: var(--muted); font-size: 12px; }
.prompt-vars { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.prompt-vars code { padding: 1px 5px; border-radius: 4px; background: var(--surface-2); color: var(--text); font-size: 12px; cursor: help; }
.prompt-meta .prompt-reset { margin-left: auto; }
.prompt-digest { color: var(--muted); font-size: 12px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.prompt-warning { margin: 0; color: var(--warn); font-size: 12px; line-height: 1.6; }
.prompt-contract { display: grid; gap: 4px; min-width: 0; }
.prompt-contract-label { color: var(--muted); font-size: 12px; }
.prompt-format { border-style: dashed; font-size: 12px; }
@media (max-width: 640px) {
  .prompt-usage, .prompt-body { margin-left: 0; }
}
</style>
