<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="search-routing-panel">
    <div class="model-purpose-heading"><div><h3>联网搜索</h3><span class="badge">主备回退</span></div><button class="btn small" type="button" @click="navigate('provider', { section: 'search' })"><Settings2 :size="14" aria-hidden="true" />管理提供商</button></div>
    <p class="hint search-routing-intro">选择这台机器人使用的搜索来源。地址和密钥在「提供商」页统一管理，插件无需重复配置。</p>
    <p v-if="loading && !config" class="muted">正在加载搜索来源…</p>
    <div v-if="error" class="operation-error" role="alert">{{ error }}<button class="btn small" type="button" @click="load">重试</button></div>
    <template v-if="config">
      <AppSwitch :model-value="!draft.disabled" @update:model-value="setEnabled">允许联网搜索</AppSwitch>
      <p v-if="draft.disabled" class="hint">联网搜索已关闭。已选来源会保留，可在启用前继续调整。</p>
      <template v-if="!draft.disabled || draft.provider_ids.length">
        <div class="search-route-list">
          <div v-for="(id, index) in draft.provider_ids" :key="index" class="search-route-row">
            <span class="search-route-label">{{ index === 0 ? '首选' : `后备 ${index}` }}</span>
            <div class="search-route-choice">
              <AppSelect :id="`search-route-${index}`" :aria-label="index === 0 ? '首选搜索来源' : `后备搜索来源 ${index}`" :model-value="id" :options="optionsFor(index)" placeholder="选择搜索提供商" @update:model-value="setProvider(index, $event)" />
              <span class="hint">{{ providerHint(id) }}</span>
            </div>
            <div class="search-route-actions">
              <button class="btn icon-only small ghost" type="button" :disabled="index === 0" :aria-label="`上移搜索来源 ${index + 1}`" @click="move(index, -1)"><ChevronUp :size="14" aria-hidden="true" /></button>
              <button class="btn icon-only small ghost" type="button" :disabled="index === draft.provider_ids.length - 1" :aria-label="`下移搜索来源 ${index + 1}`" @click="move(index, 1)"><ChevronDown :size="14" aria-hidden="true" /></button>
              <button v-if="index > 0" class="btn icon-only small ghost" type="button" :aria-label="`删除后备搜索来源 ${index}`" @click="remove(index)"><Trash2 :size="14" aria-hidden="true" /></button>
            </div>
          </div>
        </div>
        <button class="btn small ghost search-route-add" type="button" :disabled="!unusedProviders.length || draft.provider_ids.length >= 16" @click="add"><Plus :size="14" aria-hidden="true" />添加后备来源</button>
        <p class="hint">首选来源超时、失败或没有结果时，按顺序尝试后备；候选查询和全部来源共用总超时。</p>
        <AppDisclosure id="web-search-options" title="搜索选项" class="search-routing-options">
          <div class="form-grid">
            <div class="field"><label for="search-max-results">每次结果上限</label><AppNumberInput id="search-max-results" :model-value="draft.max_results || 5" :min="1" :max="10" label="每次结果上限" @update:model-value="patch({ max_results: $event })" /></div>
            <div class="field"><label for="search-provider-timeout">单来源超时（秒）</label><AppNumberInput id="search-provider-timeout" :model-value="draft.provider_timeout_seconds || 12" :min="2" :max="30" label="单来源超时" @update:model-value="patch({ provider_timeout_seconds: $event })" /></div>
            <div class="field"><label for="search-total-timeout">总搜索超时（秒）</label><AppNumberInput id="search-total-timeout" :model-value="draft.total_timeout_seconds || 35" :min="5" :max="90" label="总搜索超时" @update:model-value="patch({ total_timeout_seconds: $event })" /></div>
            <div class="field"><label for="search-link-policy">回复附带链接</label><AppSelect id="search-link-policy" :model-value="draft.reply_link_policy || 'on_request'" :options="linkPolicies" @update:model-value="patch({ reply_link_policy: $event as WebSearchAssignment['reply_link_policy'] })" /></div>
            <div class="field wide"><AppSwitch :model-value="draft.source_recall !== false" @update:model-value="patch({ source_recall: $event })">保留来源，追问时可给出链接</AppSwitch></div>
          </div>
        </AppDisclosure>
      </template>
      <p class="hint search-routing-save-hint">修改后点击页面「保存配置」，仅影响当前机器人。</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ChevronDown, ChevronUp, Plus, Settings2, Trash2 } from "@lucide/vue";
import { getSearchProviders, type SearchConfiguration, type WebSearchAssignment } from "../api";
import { useConfigurationRefresh } from "../configuration-sync";
import { navigate } from "../router";
import { searchProviderTypeLabel } from "../search-providers";
import AppDisclosure from "./AppDisclosure.vue";
import AppNumberInput from "./AppNumberInput.vue";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";

const props = defineProps<{ modelValue?: WebSearchAssignment; profileId?: string }>();
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
const linkPolicies = [{ value: "on_request", label: "跟随人设，追问再给" }, { value: "always", label: "随回复附带来源" }, { value: "never", label: "不附带链接" }];

async function load(): Promise<void> {
  const version = ++loadVersion;
  loading.value = true;
  try { const result = await getSearchProviders(props.profileId); if (version === loadVersion) { config.value = result; error.value = ""; } }
  catch (err) { if (version === loadVersion) error.value = err instanceof Error ? err.message : "搜索来源加载失败"; }
  finally { if (version === loadVersion) loading.value = false; }
}
function patch(value: Partial<WebSearchAssignment>): void { emit('update:modelValue', { ...draft.value, ...value, provider_ids: [...(value.provider_ids ?? draft.value.provider_ids)] }); }
function setEnabled(enabled: boolean): void {
  const ids = draft.value.provider_ids.length ? draft.value.provider_ids : (config.value?.providers ?? []).filter(provider => !provider.disabled).slice(0, 1).map(provider => provider.id);
  patch({ disabled: !enabled, provider_ids: ids });
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
  if (provider.type === 'tavily' && !provider.api_key_configured) return "尚未配置密钥，请到提供商页填写";
  return searchProviderTypeLabel(provider.type);
}
function setProvider(index: number, id: string): void { const ids = [...draft.value.provider_ids]; ids[index] = id; patch({ provider_ids: ids }); }
function add(): void { const provider = unusedProviders.value[0]; if (provider) patch({ provider_ids: [...draft.value.provider_ids, provider.id] }); }
function remove(index: number): void { patch({ provider_ids: draft.value.provider_ids.filter((_, current) => current !== index) }); }
function move(index: number, offset: number): void { const ids = [...draft.value.provider_ids]; const to = index + offset; if (to < 0 || to >= ids.length) return; [ids[index], ids[to]] = [ids[to], ids[index]]; patch({ provider_ids: ids }); }
watch(() => props.profileId, () => { config.value = null; void load(); }, { immediate: true });
useConfigurationRefresh(["search"], load);
</script>
