<script setup lang="ts">
import { computed, onMounted, ref, useId } from "vue";
import { ChevronRight, RotateCcw } from "@lucide/vue";
import { getPromptCatalog, type PromptCatalog, type PromptSpec } from "../api";
import { customizedPromptCount, missingPromptVars, promptOverrideValue, withPromptOverride } from "../prompt-overrides";

// 覆盖表只存改过的正文：输入框里显示的是「当前生效的正文」，和默认值一样就从表里删掉，
// 不把默认值抄进配置——那样以后内置文案更新了，这台机器人还停在旧版上。
const props = defineProps<{ modelValue?: Record<string, string> }>();
const emit = defineEmits<{ "update:modelValue": [value: Record<string, string> | undefined] }>();
const id = useId();

const catalog = ref<PromptCatalog | null>(null);
const loadError = ref("");
const query = ref("");
const onlyCustomized = ref(false);
const expanded = ref<Set<string>>(new Set());

onMounted(async () => {
  try {
    catalog.value = await getPromptCatalog();
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : String(error);
  }
});

const overrides = computed(() => props.modelValue ?? {});
const customizedCount = computed(() => customizedPromptCount(catalog.value?.prompts ?? [], overrides.value));

const sections = computed(() => {
  const data = catalog.value;
  if (!data) return [];
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
  return (overrides.value[spec.key] ?? "").trim() !== "";
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

function reset(spec: PromptSpec): void {
  emit("update:modelValue", withPromptOverride(overrides.value, spec, spec.default));
}

function resetAll(): void {
  emit("update:modelValue", undefined);
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
  <div class="prompt-overrides">
    <div class="prompt-toolbar">
      <input v-model="query" class="input" type="search" placeholder="搜索标题、用途或正文" aria-label="搜索提示词" />
      <label class="prompt-filter">
        <input v-model="onlyCustomized" type="checkbox" />
        只看改过的<template v-if="customizedCount">（{{ customizedCount }}）</template>
      </label>
      <button class="btn small" type="button" :disabled="!customizedCount" @click="resetAll">
        <RotateCcw :size="14" aria-hidden="true" />
        全部恢复默认
      </button>
    </div>

    <p v-if="loadError" class="prompt-empty">提示词列表加载失败：{{ loadError }}</p>
    <p v-else-if="!catalog" class="prompt-empty">正在加载内置提示词…</p>
    <p v-else-if="!sections.length" class="prompt-empty">{{ onlyCustomized ? "还没有改过任何提示词。" : "没有匹配的提示词。" }}</p>

    <section v-for="section in sections" :key="section.group.id" class="prompt-group">
      <header class="prompt-group-head">
        <h3>{{ section.group.label }}</h3>
        <p>{{ section.group.description }}</p>
      </header>
      <article v-for="spec in section.prompts" :key="spec.key" class="prompt-item" :class="{ open: expanded.has(spec.key) }">
        <button class="prompt-summary" type="button" :aria-expanded="expanded.has(spec.key)" :aria-controls="`${id}-${spec.key}`" @click="toggle(spec.key)">
          <ChevronRight :size="16" class="prompt-chevron" aria-hidden="true" />
          <span class="prompt-title">{{ spec.title }}</span>
          <span v-if="isCustomized(spec)" class="badge accent">已修改</span>
          <span v-if="spec.contract" class="badge" title="输出格式已锁定">格式锁定</span>
        </button>
        <p class="prompt-usage">{{ spec.usage }}</p>
        <div v-if="expanded.has(spec.key)" :id="`${id}-${spec.key}`" class="prompt-body">
          <textarea
            class="textarea prompt-text"
            :aria-label="spec.title"
            :value="promptOverrideValue(spec, overrides)"
            :maxlength="catalog?.max_runes"
            rows="8"
            spellcheck="false"
            @input="update(spec, ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
          <div class="prompt-meta">
            <span>{{ runeCount(promptOverrideValue(spec, overrides)) }} 字</span>
            <span v-if="spec.vars?.length" class="prompt-vars">
              占位符：
              <code v-for="variable in spec.vars" :key="variable.name" :title="variable.description">{{ placeholder(variable.name) }}</code>
            </span>
            <button v-if="isCustomized(spec)" class="btn small ghost" type="button" @click="reset(spec)">
              <RotateCcw :size="14" aria-hidden="true" />
              恢复默认
            </button>
          </div>
          <p v-if="missingPromptVars(spec, overrides).length" class="prompt-warning">
            正文里没有 {{ missingPromptVars(spec, overrides).map(placeholder).join("、") }}，这些运行时信息不会再进提示词。
          </p>
          <div v-if="spec.contract" class="prompt-contract">
            <span class="prompt-contract-label">锁定的输出格式（程序要解析，不能改，总是拼在正文后面）</span>
            <pre>{{ spec.contract.trim() }}</pre>
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
.prompt-meta .btn { margin-left: auto; }
.prompt-warning { margin: 0; color: var(--warn); font-size: 12px; line-height: 1.6; }
.prompt-contract { display: grid; gap: 4px; min-width: 0; }
.prompt-contract-label { color: var(--muted); font-size: 12px; }
.prompt-contract pre { margin: 0; padding: 8px 10px; max-height: 220px; overflow: auto; border: 1px dashed var(--border); border-radius: 6px; color: var(--muted); font-size: 12px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  .prompt-usage, .prompt-body { margin-left: 0; }
}
</style>
