<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 整块可点：点空白处也落到输入框，不用非得瞄准那一小段。 -->
  <div class="chip-input" :class="{ focused }" @mousedown="focusInput">
    <span v-for="(id, index) in items" :key="`${id}-${index}`" class="chip" :title="titleOf(id)">
      <span class="chip-id">{{ id }}</span>
      <span v-if="names[id]" class="chip-name">{{ names[id] }}</span>
      <button
        type="button"
        class="chip-remove"
        :aria-label="`移除 ${id}`"
        @mousedown.stop.prevent
        @click="removeAt(index)"
      >
        <X :size="11" aria-hidden="true" />
      </button>
    </span>
    <input
      :id="inputId"
      ref="field"
      v-model="draft"
      class="chip-field"
      :placeholder="items.length > 0 ? '' : placeholder"
      autocomplete="off"
      @keydown.enter.prevent="commitDraft"
      @keydown.tab="commitDraft"
      @keydown.backspace="backspace"
      @paste.prevent="paste"
      @input="splitOnSeparator"
      @focus="focused = true"
      @blur="blur"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { X } from "@lucide/vue";

// 一颗一颗的账号/群号编辑器，替掉「逗号分隔的一长串」。
//
// 逗号串有两个治不好的毛病：删中间一条要手动动逗号；而且整串是一坨文本，
// 没有「一条」这个位置能挂昵称——所以此前昵称回显只做进了单值输入框。
// 拆成 chip 之后每条是独立节点，名字直接显示在里面。
const props = defineProps<{
  modelValue?: string[];
  inputId?: string;
  placeholder?: string;
  /**
   * 批量把 ID 换成显示名。给 undefined 就是不查名字，chip 只显示 ID。
   * 由调用方决定名字从哪来：账号查昵称，群号查群名。
   */
  resolveNames?: (ids: string[]) => Promise<Record<string, string>>;
}>();
const emit = defineEmits<{ "update:modelValue": [string[]] }>();

const SEPARATORS = /[,，\s]+/;
// 服务端单次最多查 20 个（webui/assistant_user_names.go 的 userNameLookupLimit），
// 超了会被截断，所以这边按 20 一批发。
const BATCH_SIZE = 20;
const DEBOUNCE_MS = 400;

const field = ref<HTMLInputElement | null>(null);
const draft = ref("");
const focused = ref(false);
const names = ref<Record<string, string>>({});

const items = computed(() => props.modelValue ?? []);

function titleOf(id: string): string {
  return names.value[id] ? `${id} · ${names.value[id]}` : id;
}

function focusInput(): void {
  field.value?.focus();
}

function commit(next: string[]): void {
  emit("update:modelValue", next);
}

/** 已经有的不重复加：同一个号写两遍没有意义，也会让删除对不上号。 */
function add(candidates: string[]): void {
  const next = [...items.value];
  for (const candidate of candidates) {
    const id = candidate.trim();
    if (id !== "" && !next.includes(id)) next.push(id);
  }
  if (next.length !== items.value.length) commit(next);
}

function removeAt(index: number): void {
  const next = [...items.value];
  next.splice(index, 1);
  commit(next);
  void nextTick(focusInput);
}

function commitDraft(): void {
  if (draft.value.trim() === "") return;
  add(draft.value.split(SEPARATORS));
  draft.value = "";
}

/** 打逗号或空格就当这一条写完了。 */
function splitOnSeparator(): void {
  if (!SEPARATORS.test(draft.value)) return;
  const parts = draft.value.split(SEPARATORS);
  // 最后一段后面没有分隔符时是还没写完的，留在输入框里继续编辑。
  const trailing = SEPARATORS.test(draft.value.slice(-1)) ? "" : (parts.pop() ?? "");
  add(parts);
  draft.value = trailing;
}

/** 输入框空着时退格删掉最后一颗，和邮件收件人栏一致。 */
function backspace(event: KeyboardEvent): void {
  if (draft.value !== "" || items.value.length === 0) return;
  event.preventDefault();
  removeAt(items.value.length - 1);
}

function paste(event: ClipboardEvent): void {
  const text = event.clipboardData?.getData("text") ?? "";
  add(text.split(SEPARATORS));
  draft.value = "";
}

/**
 * 失焦时把没敲回车的那一条也收下。不收的话，填完直接去点「保存」的人
 * 会静悄悄丢掉最后一个号——而且看不出来。
 */
function blur(): void {
  focused.value = false;
  commitDraft();
}

// —— 名字解析：防抖 + 只认最后一次请求，和 AccountNameHint 一个路子。
// 差别是这里一次问一批，而不是每颗 chip 各发一个请求。
let timer: ReturnType<typeof setTimeout> | null = null;
let latest = 0;

function clearTimer(): void {
  if (timer !== null) {
    clearTimeout(timer);
    timer = null;
  }
}

async function resolve(pending: string[], token: number): Promise<void> {
  const resolver = props.resolveNames;
  if (!resolver) return;
  for (let start = 0; start < pending.length; start += BATCH_SIZE) {
    const batch = pending.slice(start, start + BATCH_SIZE);
    try {
      const resolved = await resolver(batch);
      if (token !== latest) return;
      // 逐批合并，前面查到的先显示出来，不用等整串查完。
      names.value = { ...names.value, ...resolved };
    } catch {
      // 查不到名字不该在界面上报错——这只是个锦上添花的回显。
      return;
    }
  }
}

watch(
  [items, () => props.resolveNames],
  () => {
    clearTimer();
    latest += 1;
    if (!props.resolveNames) return;
    // 只查还没有名字的，别让每次增删都把整串重查一遍。
    const pending = items.value.filter((id) => !(id in names.value));
    if (pending.length === 0) return;
    const token = latest;
    timer = setTimeout(() => void resolve(pending, token), DEBOUNCE_MS);
  },
  { immediate: true, deep: true }
);

onBeforeUnmount(() => {
  clearTimer();
  latest += 1;
});
</script>

<style scoped>
/* 视觉上要和 .input 是同一件东西：同样的边框、圆角、底色和聚焦光圈。 */
.chip-input {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
  width: 100%;
  min-height: 36px;
  padding: 5px 7px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-md);
  background: var(--bg-raised);
  cursor: text;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}

.chip-input.focused {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}

.chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  padding: 2px 3px 2px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--surface);
  font-size: 12.5px;
  line-height: 1.6;
}

.chip-id {
  font-variant-numeric: tabular-nums;
}

.chip-name {
  min-width: 0;
  overflow: hidden;
  color: var(--muted);
  font-size: 11.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chip-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 17px;
  height: 17px;
  padding: 0;
  border: none;
  border-radius: 999px;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}

.chip-remove:hover {
  background: var(--accent-soft);
  color: var(--text);
}

.chip-field {
  flex: 1;
  min-width: 120px;
  border: none;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: 13.5px;
}

.chip-field:focus {
  outline: none;
}
</style>
