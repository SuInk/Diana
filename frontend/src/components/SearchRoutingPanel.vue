<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="search-routing-panel">
    <p v-if="loading && !config" class="muted">正在加载搜索来源…</p>
    <div v-if="error" class="operation-error" role="alert">{{ error }}<button class="btn small" type="button" @click="load">重试</button></div>
    <template v-if="config">
      <div class="search-routing-top">
        <AppSwitch :model-value="!draft.disabled" @update:model-value="setEnabled">允许联网搜索</AppSwitch>
        <button class="btn small ghost" type="button" @click="navigate('provider', { section: 'search' })"><Settings2 :size="14" aria-hidden="true" />管理搜索提供商</button>
      </div>
      <p v-if="!modelValue" class="hint">这台机器人还没保存过搜索配置，下面是当前生效的默认值；改动后点「保存配置」即成为它自己的配置。</p>
      <p v-if="draft.disabled" class="hint">已关闭，回复时不会联网搜索。下方来源会保留，重新打开即可沿用。</p>
      <p v-else-if="!config.providers.length" class="hint warn-text">还没有搜索提供商，请先点「管理搜索提供商」新建。</p>
      <div v-if="!draft.disabled || draft.provider_ids.length" class="search-routing-body" :class="{ muted: draft.disabled }">
        <div class="field">
          <label for="search-source-priority">优先使用</label>
          <AppSelect id="search-source-priority" class="search-priority-select" :model-value="draft.source_priority || 'list'" :options="priorityOptions" @update:model-value="patch({ source_priority: $event === 'list' ? undefined : $event as WebSearchAssignment['source_priority'] })" />
        </div>
        <div class="search-route-list" aria-label="搜索来源，按尝试顺序排列">
          <template v-for="(row, position) in orderedRows" :key="row.index">
            <div v-if="row.groupLabel" class="search-route-group">{{ row.groupLabel }}</div>
            <div class="search-route-row" :class="{ 'is-dragging': drag?.from === row.index, 'is-drop-target': drag?.over === row.index && drag.from !== row.index, 'is-warning': providerWarns(row.id) }" :data-search-route-slot="row.index" @dragover="onDragOver(row.index, $event)" @drop="onDrop(row.index, $event)">
              <button class="model-route-handle search-route-label" type="button" draggable="true" :disabled="draft.provider_ids.length < 2" :title="draft.provider_ids.length < 2 ? '只有一个来源' : '拖动或按上下方向键调整顺序'" :aria-label="`第 ${position + 1} 个尝试，按上下方向键调整顺序`" @dragstart="onDragStart(row.index, $event)" @dragend="onDragEnd" @pointerdown="onHandlePointerDown(row.index, $event)" @keydown="onHandleKeydown(position, $event)"><GripVertical :size="14" aria-hidden="true" /><span>{{ position + 1 }}</span></button>
              <AppSelect :id="`search-route-${row.index}`" class="search-route-select" :aria-label="`第 ${position + 1} 个搜索来源`" :model-value="row.id" :options="optionsFor(row.index)" placeholder="选择搜索提供商" @update:model-value="setProvider(row.index, $event)" />
              <span class="badge search-route-kind" :class="{ 'warn-text': providerWarns(row.id) }" :title="providerHint(row.id)">{{ providerKind(row.id) }}</span>
              <button class="btn icon-only small ghost" type="button" :disabled="draft.provider_ids.length < 2 && !draft.disabled" :aria-label="`移除第 ${position + 1} 个搜索来源`" title="移除" @click="remove(row.index)"><X :size="14" aria-hidden="true" /></button>
            </div>
          </template>
        </div>
        <p v-if="warningText" class="hint warn-text">{{ warningText }}</p>
        <div class="search-route-footer">
          <button class="btn small ghost" type="button" :disabled="!unusedProviders.length || draft.provider_ids.length >= 16" @click="add"><Plus :size="14" aria-hidden="true" />添加来源</button>
          <span class="hint">{{ priorityHint }}</span>
        </div>
        <AppDisclosure id="web-search-options" title="搜索选项" class="search-routing-options">
          <div class="form-grid">
            <div class="field"><label for="search-max-results">每次结果上限</label><AppNumberInput id="search-max-results" :model-value="draft.max_results || 5" :min="1" :max="10" label="每次结果上限" @update:model-value="patch({ max_results: $event })" /></div>
            <div class="field"><label for="search-provider-timeout">单来源超时（秒）</label><AppNumberInput id="search-provider-timeout" :model-value="draft.provider_timeout_seconds || 12" :min="2" :max="30" label="单来源超时" @update:model-value="patch({ provider_timeout_seconds: $event })" /></div>
            <div class="field"><label for="search-total-timeout">总搜索超时（秒）</label><AppNumberInput id="search-total-timeout" :model-value="draft.total_timeout_seconds || 35" :min="5" :max="90" label="总搜索超时" @update:model-value="patch({ total_timeout_seconds: $event })" /></div>
            <div class="field"><label for="search-link-policy">回复附带链接</label><AppSelect id="search-link-policy" :model-value="draft.reply_link_policy || 'on_request'" :options="linkPolicies" @update:model-value="patch({ reply_link_policy: $event as WebSearchAssignment['reply_link_policy'] })" /></div>
            <div class="field wide"><AppSwitch :model-value="draft.source_recall !== false" @update:model-value="patch({ source_recall: $event })">保留来源，追问时可给出链接</AppSwitch></div>
          </div>
        </AppDisclosure>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from "vue";
import { GripVertical, Plus, Settings2, X } from "@lucide/vue";
import { navigate } from "../router";
import { getSearchProviders, type SearchConfiguration, type WebSearchAssignment } from "../api";
import { useConfigurationRefresh } from "../lib/configuration-sync";
import { searchProviderKeyStatus, searchProviderTypeLabel } from "../lib/search-providers";
import AppDisclosure from "./AppDisclosure.vue";
import AppNumberInput from "./AppNumberInput.vue";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";

const props = defineProps<{ modelValue?: WebSearchAssignment }>();
const emit = defineEmits<{ 'update:modelValue': [WebSearchAssignment] }>();
const config = ref<SearchConfiguration | null>(null);
const loading = ref(false);
const error = ref("");
let loadVersion = 0;
const draft = computed<WebSearchAssignment>(() => {
  const defaults = config.value?.default_assignment;
  const value = props.modelValue;
  return {
    ...defaults, ...value,
    disabled: value ? !!value.disabled : defaults?.disabled,
    provider_ids: value?.provider_ids ?? defaults?.provider_ids ?? [],
    max_results: value?.max_results || defaults?.max_results,
    provider_timeout_seconds: value?.provider_timeout_seconds || defaults?.provider_timeout_seconds,
    total_timeout_seconds: value?.total_timeout_seconds || defaults?.total_timeout_seconds,
    reply_link_policy: value?.reply_link_policy || defaults?.reply_link_policy,
  };
});
const unusedProviders = computed(() => (config.value?.providers ?? []).filter(provider => !provider.disabled && !draft.value.provider_ids.includes(provider.id)));
const priorityOptions = [{ value: "list", label: "按列表顺序", hint: "完全按下方顺序尝试" }, { value: "api", label: "API 优先", hint: "API 来源排在浏览器来源之前" }, { value: "browser", label: "浏览器优先", hint: "浏览器来源排在 API 来源之前" }];
const priorityHint = computed(() => draft.value.source_priority ? "同类来源内拖动调整顺序；失败时尝试下一个；等待超过 4 秒会同时尝试后备，先返回有效结果的来源优先。" : "拖动调整顺序；失败时尝试下一个；等待超过 4 秒会同时尝试后备，先返回有效结果的来源优先。");
const isBrowser = (id: string) => config.value?.providers.find(provider => provider.id === id)?.type === "browser";
// 按运行时真正的尝试顺序展示：选了 API / 浏览器优先时，同类来源排在一起并标出分组。
const orderedRows = computed(() => {
  const priority = draft.value.source_priority;
  const rows = draft.value.provider_ids.map((id, index) => ({ id, index, browser: isBrowser(id), groupLabel: "" }));
  if (!priority) return rows;
  const browserFirst = priority === "browser";
  rows.sort((a, b) => Number(a.browser !== browserFirst) - Number(b.browser !== browserFirst));
  rows.forEach((row, position) => { if (position === 0 || rows[position - 1].browser !== row.browser) row.groupLabel = row.browser ? "浏览器来源" : "API 来源"; });
  return rows;
});
function providerKind(id: string): string {
  const provider = config.value?.providers.find(provider => provider.id === id);
  if (!provider) return "已删除";
  if (provider.disabled) return "已停用";
  if (searchProviderKeyStatus(provider) === "待配置密钥") return "缺密钥";
  return provider.type === "browser" ? "浏览器" : "API";
}
const warningText = computed(() => {
  const bad = draft.value.provider_ids.filter(providerWarns);
  return bad.length ? `有 ${bad.length} 个来源不可用（已删除、已停用或缺密钥），搜索时会被跳过。` : "";
});
const linkPolicies = [{ value: "on_request", label: "跟随人设，追问再给" }, { value: "always", label: "随回复附带来源" }, { value: "never", label: "不附带链接" }];

async function load(): Promise<void> {
  const version = ++loadVersion;
  loading.value = true;
  try { const result = await getSearchProviders(); if (version === loadVersion) { config.value = result; error.value = ""; } }
  catch (err) { if (version === loadVersion) error.value = err instanceof Error ? err.message : "搜索来源加载失败"; }
  finally { if (version === loadVersion) loading.value = false; }
}
function patch(value: Partial<WebSearchAssignment>): void { emit('update:modelValue', { ...draft.value, ...value, provider_ids: [...(value.provider_ids ?? draft.value.provider_ids)] }); }
function setEnabled(enabled: boolean): void {
  const ids = draft.value.provider_ids.length ? draft.value.provider_ids : (config.value?.providers ?? []).filter(provider => !provider.disabled).slice(0, 1).map(provider => provider.id);
  patch({ disabled: !enabled, provider_ids: enabled ? ids : draft.value.provider_ids });
}
function optionsFor(index: number) {
  const current = draft.value.provider_ids[index];
  const options = (config.value?.providers ?? []).filter(provider => provider.id === current || !provider.disabled && !draft.value.provider_ids.includes(provider.id)).map(provider => ({ value: provider.id, label: `${provider.name}${provider.disabled ? '（已停用）' : ''}`, hint: searchProviderTypeLabel(provider.type) }));
  if (current && !options.some(option => option.value === current)) options.unshift({ value: current, label: `来源已删除（${current}）`, hint: '请选择其他提供商' });
  return options;
}
function providerHint(id: string): string {
  const provider = config.value?.providers.find(provider => provider.id === id);
  if (!provider) return "请选择可用的搜索提供商";
  if (provider.disabled) return "提供商已停用，请更换来源或到提供商页启用";
  if (provider.type === 'browser') return "使用服务器上的隔离 Chrome / Chromium";
  if (searchProviderKeyStatus(provider) === "待配置密钥") return `${searchProviderTypeLabel(provider.type)} 尚未配置密钥，请到「搜索提供商」填写`;
  return searchProviderTypeLabel(provider.type);
}
function providerWarns(id: string): boolean {
  const provider = config.value?.providers.find(provider => provider.id === id);
  return !provider || !!provider.disabled || searchProviderKeyStatus(provider) === "待配置密钥";
}
function setProvider(index: number, id: string): void { const ids = [...draft.value.provider_ids]; ids[index] = id; patch({ provider_ids: ids }); }
function add(): void { const provider = unusedProviders.value[0]; if (provider) patch({ provider_ids: [...draft.value.provider_ids, provider.id] }); }
function remove(index: number): void { patch({ provider_ids: draft.value.provider_ids.filter((_, current) => current !== index) }); }
function move(from: number, to: number): void {
  const ids = [...draft.value.provider_ids];
  if (from === to || from < 0 || to < 0 || from >= ids.length || to >= ids.length) return;
  const [moved] = ids.splice(from, 1);
  ids.splice(to, 0, moved);
  patch({ provider_ids: ids });
}

// 拖拽与模型路由一致：鼠标走原生拖放，触摸走指针事件，键盘用上下方向键。
const drag = ref<{ from: number; over: number | null; touch?: boolean } | null>(null);
function onDragStart(index: number, event: DragEvent): void {
  if (drag.value?.touch) { event.preventDefault(); return; }
  drag.value = { from: index, over: null };
  if (event.dataTransfer) { event.dataTransfer.effectAllowed = "move"; event.dataTransfer.setData("text/plain", String(index)); }
}
function onDragOver(index: number, event: DragEvent): void {
  if (!drag.value || drag.value.touch) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  drag.value.over = index;
}
function onDrop(index: number, event: DragEvent): void {
  if (!drag.value || drag.value.touch) return;
  event.preventDefault();
  move(drag.value.from, index);
  drag.value = null;
}
function onDragEnd(): void { if (!drag.value?.touch) drag.value = null; }
function onHandlePointerDown(index: number, event: PointerEvent): void {
  if (event.pointerType === "mouse" || !event.isPrimary || draft.value.provider_ids.length < 2) return;
  const handle = event.currentTarget as HTMLElement;
  handle.setPointerCapture(event.pointerId);
  drag.value = { from: index, over: null, touch: true };
  const onMove = (next: PointerEvent) => {
    const slot = document.elementFromPoint(next.clientX, next.clientY)?.closest<HTMLElement>("[data-search-route-slot]")?.dataset.searchRouteSlot;
    if (drag.value?.touch) drag.value.over = slot === undefined ? null : Number(slot);
  };
  const finish = (commit: boolean) => {
    handle.removeEventListener("pointermove", onMove);
    handle.removeEventListener("pointerup", onUp);
    handle.removeEventListener("pointercancel", onCancel);
    handle.removeEventListener("lostpointercapture", onCancel);
    const current = drag.value;
    drag.value = null;
    if (commit && current?.touch && current.over !== null) move(current.from, current.over);
  };
  const onUp = () => finish(true);
  const onCancel = () => finish(false);
  handle.addEventListener("pointermove", onMove);
  handle.addEventListener("pointerup", onUp);
  handle.addEventListener("pointercancel", onCancel);
  handle.addEventListener("lostpointercapture", onCancel);
}
async function onHandleKeydown(position: number, event: KeyboardEvent): Promise<void> {
  const rows = orderedRows.value;
  const target = position + (event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : 0);
  if (target === position || target < 0 || target >= rows.length) return;
  event.preventDefault();
  // 分组排序时跨组移动没有意义，组的先后由「优先使用」决定。
  if (draft.value.source_priority && rows[target].browser !== rows[position].browser) return;
  const moved = rows[position].index;
  move(moved, rows[target].index);
  await nextTick();
  document.querySelector<HTMLElement>(`[data-search-route-slot="${draft.value.provider_ids.indexOf(rows[position].id)}"] .search-route-label`)?.focus();
}
onMounted(load);
useConfigurationRefresh(["search"], load);
</script>
