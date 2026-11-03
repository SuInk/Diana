<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="search-routing-panel">
    <p v-if="loading && !config" class="muted">正在加载搜索来源…</p>
    <div v-if="error" class="operation-error" role="alert">{{ error }}<button class="btn small" type="button" @click="load">重试</button></div>
    <template v-if="config">
      <!-- 开关、优先级和管理入口压成一行；来源列表直接可编辑，不再另画一排一览。 -->
      <div class="search-routing-top">
        <AppSwitch :model-value="!draft.disabled" aria-label="允许联网搜索" @update:model-value="setEnabled">启用</AppSwitch>
        <AppSelect v-if="!draft.disabled && draft.provider_ids.length > 1" id="search-source-priority" class="search-priority-select" aria-label="优先使用" :model-value="draft.source_priority || 'list'" :options="priorityOptions" @update:model-value="patch({ source_priority: $event === 'list' ? undefined : $event as WebSearchAssignment['source_priority'] })" />
        <span class="search-routing-actions">
          <button class="btn small ghost" type="button" @click="navigate('provider', { section: 'search' })"><Settings2 :size="14" aria-hidden="true" />管理提供商</button>
          <button v-if="!draft.disabled && draft.provider_ids.length < 16" class="btn small primary" type="button" @click="openAdd"><Plus :size="14" aria-hidden="true" />添加</button>
        </span>
      </div>
      <p v-if="draft.disabled" class="hint">已关闭，来源保留。</p>
      <p v-else-if="!config.providers.length" class="hint warn-text">还没有搜索提供商，先去「管理提供商」新建。</p>
      <div v-if="!draft.disabled || draft.provider_ids.length" class="search-routing-body" :class="{ muted: draft.disabled }">
        <div v-if="draft.provider_ids.length" class="search-route-list" aria-label="搜索来源，按尝试顺序排列">
          <template v-for="(row, position) in orderedRows" :key="row.index">
            <div v-if="row.groupLabel" class="search-route-group"><strong>{{ row.groupLabel }}</strong><span>{{ row.groupHint }}</span></div>
            <div class="search-route-row" :class="{ 'is-off': sourceOff(row.id), 'is-dragging': drag?.from === row.index, 'is-drop-target': drag?.over === row.index && drag.from !== row.index, 'is-warning': providerWarns(row.id) }" :data-search-route-slot="row.index" @dragover="onDragOver(row.index, $event)" @drop="onDrop(row.index, $event)">
              <button class="model-route-handle search-route-label" type="button" draggable="true" :disabled="draft.provider_ids.length < 2" :title="draft.provider_ids.length < 2 ? '只有一个来源' : '拖动或按上下方向键调整顺序'" :aria-label="`第 ${position + 1} 个尝试，按上下方向键调整顺序`" @dragstart="onDragStart(row.index, $event)" @dragend="onDragEnd" @pointerdown="onHandlePointerDown(row.index, $event)" @keydown="onHandleKeydown(position, $event)"><GripVertical :size="14" aria-hidden="true" /><span>{{ position + 1 }}</span></button>
              <span class="model-health-dot" :class="providerWarns(row.id) ? 'warn' : 'ok'" aria-hidden="true"></span>
              <span class="search-route-name" :title="providerHint(row.id)">{{ providerName(row.id) }}<small v-if="!draft.source_priority && !providerWarns(row.id)">{{ providerKind(row.id) }}</small></span>
              <span v-if="providerWarns(row.id)" class="search-route-warn" :title="providerHint(row.id)">{{ providerKind(row.id) }}</span>
              <AppSwitch :model-value="!sourceOff(row.id)" compact :aria-label="`启用搜索来源 ${providerName(row.id)}`" @update:model-value="setSourceEnabled(row.id, $event)" />
              <span class="search-route-actions">
                <button class="btn icon-only small ghost" type="button" :disabled="!providerExists(row.id)" :aria-label="`测试搜索来源 ${providerName(row.id)}`" title="用已保存的配置搜一次" @click="manager?.testByID(row.id)"><Play :size="14" aria-hidden="true" /></button>
                <button class="btn icon-only small ghost" type="button" :disabled="!providerExists(row.id)" :aria-label="`编辑搜索来源 ${providerName(row.id)}`" title="编辑这个来源（改的是提供商，所有机器人共用）" @click="manager?.editByID(row.id)"><Pencil :size="14" aria-hidden="true" /></button>
                <button class="btn icon-only small ghost danger" type="button" :disabled="draft.provider_ids.length < 2 && !draft.disabled" :aria-label="`移除第 ${position + 1} 个搜索来源`" title="从这台机器人移除" @click="confirmRemove(row.index, row.id)"><Trash2 :size="14" aria-hidden="true" /></button>
              </span>
            </div>
          </template>
        </div>
        <details class="search-routing-options">
          <summary>{{ optionsSummary }}<span>修改</span></summary>
          <div class="form-grid">
            <div class="field"><label for="search-max-results">每次结果上限</label><AppNumberInput id="search-max-results" :model-value="draft.max_results || 5" :min="1" :max="10" label="每次结果上限" @update:model-value="patch({ max_results: $event })" /></div>
            <div class="field"><label for="search-provider-timeout">单来源超时（秒）</label><AppNumberInput id="search-provider-timeout" :model-value="draft.provider_timeout_seconds || 12" :min="2" :max="30" label="单来源超时" @update:model-value="patch({ provider_timeout_seconds: $event })" /></div>
            <div class="field"><label for="search-total-timeout">总搜索超时（秒）</label><AppNumberInput id="search-total-timeout" :model-value="draft.total_timeout_seconds || 35" :min="5" :max="90" label="总搜索超时" @update:model-value="patch({ total_timeout_seconds: $event })" /></div>
            <div class="field"><label for="search-link-policy">回复附带链接</label><AppSelect id="search-link-policy" :model-value="draft.reply_link_policy || 'on_request'" :options="linkPolicies" @update:model-value="patch({ reply_link_policy: $event as WebSearchAssignment['reply_link_policy'] })" /></div>
            <div class="field wide"><AppSwitch :model-value="draft.source_recall !== false" @update:model-value="patch({ source_recall: $event })">保留来源，追问时可给出链接</AppSwitch></div>
          </div>
        </details>
      </div>
    </template>
    <SearchProviderManager ref="manager" dialogs-only />
    <!-- 添加来源：弹窗里一个下拉，按浏览器 / API 分段列出全部提供商，已加的置灰。自定义来源在提供商页新建。 -->
    <Modal v-if="adding" title="添加搜索来源" initial-focus="#search-add-source" @close="adding = false">
      <div class="field">
        <label for="search-add-source">来源</label>
        <AppSelect id="search-add-source" v-model="pendingSource" :options="addOptions" placeholder="选择搜索来源" :searchable="addOptions.length > 8" />
      </div>
      <template #footer>
        <button class="btn" type="button" @click="adding = false">取消</button>
        <button class="btn primary" type="button" :disabled="!pendingSource" @click="confirmAdd">添加</button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from "vue";
import { GripVertical, Pencil, Play, Plus, Settings2, Trash2 } from "@lucide/vue";
import { navigate } from "../router";
import { getSearchProviders, type SearchConfiguration, type WebSearchAssignment } from "../api";
import { useConfigurationRefresh } from "../lib/configuration-sync";
import { searchProviderKeyStatus, searchProviderTypeLabel } from "../lib/search-providers";
import AppNumberInput from "./AppNumberInput.vue";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";
import Modal from "./Modal.vue";
import SearchProviderManager from "./SearchProviderManager.vue";
import { askConfirm } from "../lib/confirm";
import { toastError } from "../lib/toast";

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
const isBrowser = (id: string) => config.value?.providers.find(provider => provider.id === id)?.type === "browser";
// 按运行时真正的尝试顺序展示：选了 API / 浏览器优先时，同类来源排在一起并标出分组。
const orderedRows = computed(() => {
  const priority = draft.value.source_priority;
  const rows = draft.value.provider_ids.map((id, index) => ({ id, index, browser: isBrowser(id), groupLabel: "", groupHint: "" }));
  if (!priority) return rows;
  const browserFirst = priority === "browser";
  rows.sort((a, b) => Number(a.browser !== browserFirst) - Number(b.browser !== browserFirst));
  // 组名后一句说清怎么用：前一组先试、组内失败换下一个，后一组等前一组都失败再用。
  rows.forEach((row, position) => {
    if (position === 0 || rows[position - 1].browser !== row.browser) {
      row.groupLabel = row.browser ? "浏览器" : "API";
      row.groupHint = position === 0 ? "先试，失败换下一个" : `${rows[0].browser ? "浏览器" : "API"}都失败再用`;
    }
  });
  return rows;
});
function providerName(id: string): string {
  return config.value?.providers.find(provider => provider.id === id)?.name ?? "已删除的来源";
}
const optionsSummary = computed(() => {
  const link = linkPolicies.find(policy => policy.value === (draft.value.reply_link_policy || "on_request"))?.label ?? "";
  return `每次 ${draft.value.max_results || 5} 条 · 超时 ${draft.value.provider_timeout_seconds || 12} / ${draft.value.total_timeout_seconds || 35} 秒 · ${link}`;
});
function providerKind(id: string): string {
  const provider = config.value?.providers.find(provider => provider.id === id);
  if (!provider) return "已删除";
  if (provider.disabled) return "已停用";
  if (searchProviderKeyStatus(provider) === "待配置密钥") return "缺密钥";
  return provider.type === "browser" ? "浏览器" : "API";
}
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
// 添加来源在弹窗里挑，不再先塞一个再改：行里只显示名字，换来源就删了再加。
// 列出全部提供商并按浏览器 / API 分段，已加的标「已添加」不可选，免得以为下拉里少了来源。
const addOptions = computed(() => {
  const providers = [...(config.value?.providers ?? [])].sort((a, b) => Number(a.type !== "browser") - Number(b.type !== "browser"));
  return providers.map(provider => {
    const added = draft.value.provider_ids.includes(provider.id);
    return {
      value: provider.id,
      label: provider.name,
      hint: added ? "已添加" : provider.disabled ? "已停用" : searchProviderTypeLabel(provider.type),
      disabled: added || !!provider.disabled,
      group: provider.type === "browser" ? "浏览器" : "API"
    };
  });
});
const adding = ref(false);
// 行里的测试、编辑借提供商页的弹窗；编辑改的是提供商本身，所有机器人共用。
const manager = ref<InstanceType<typeof SearchProviderManager> | null>(null);
function providerExists(id: string): boolean { return !!config.value?.providers.some(provider => provider.id === id); }
// 移除只是从这台机器人的来源里拿掉，提供商本身不删；也要先确认，免得误点。
async function confirmRemove(index: number, id: string): Promise<void> {
  if (!await askConfirm({ title: "移除搜索来源", message: `从这台机器人的联网搜索里移除「${providerName(id)}」？提供商本身不会删除，之后可以再添加。`, confirmLabel: "移除", danger: true })) return;
  remove(index);
}
// 单个来源的开关：关掉的留在列表里、搜索时跳过；至少留一个开着，不然等于关了联网搜索。
function sourceOff(id: string): boolean { return !!draft.value.disabled_provider_ids?.includes(id); }
function setSourceEnabled(id: string, enabled: boolean): void {
  const off = new Set(draft.value.disabled_provider_ids ?? []);
  if (enabled) off.delete(id);
  else {
    if (draft.value.provider_ids.every(item => item === id || off.has(item))) { toastError("至少要留一个来源开着"); return; }
    off.add(id);
  }
  patch({ disabled_provider_ids: draft.value.provider_ids.filter(item => off.has(item)) });
}
const pendingSource = ref("");
function openAdd(): void {
  pendingSource.value = unusedProviders.value[0]?.id ?? "";
  adding.value = true;
}
function confirmAdd(): void {
  addProvider(pendingSource.value);
  adding.value = false;
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
function addProvider(id: string): void { if (id && !draft.value.provider_ids.includes(id)) patch({ provider_ids: [...draft.value.provider_ids, id] }); }
function remove(index: number): void {
  const id = draft.value.provider_ids[index];
  patch({ provider_ids: draft.value.provider_ids.filter((_, current) => current !== index), disabled_provider_ids: (draft.value.disabled_provider_ids ?? []).filter(item => item !== id) });
}
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
