<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="card search-provider-manager">
    <div class="card-header">
      <div><h2>搜索提供商</h2><span class="card-sub">统一管理接入地址和凭据，机器人按用途选择首选与后备来源。</span></div>
      <button class="btn small primary" type="button" @click="openEditor()"><Plus :size="14" aria-hidden="true" />新建搜索提供商</button>
    </div>
    <div class="card-body stack">
      <p v-if="loading" class="muted">正在加载搜索提供商…</p>
      <div v-if="error" class="operation-error" role="alert">{{ error }}<button class="btn small" type="button" @click="load">重试</button></div>
      <div v-if="providers.length" class="row-list">
        <div v-for="provider in providers" :key="provider.id" class="row-item search-provider-row">
          <span class="search-provider-icon"><Globe :size="18" aria-hidden="true" /></span>
          <div class="row-main">
            <div class="row-title">{{ provider.name }} <span v-if="provider.disabled" class="badge">已停用</span></div>
            <div class="row-sub">{{ searchProviderTypeLabel(provider.type) }} · {{ searchProviderAddress(provider.url) }}<template v-if="provider.type !== 'browser'"> · {{ provider.api_key_configured ? '已配置密钥' : provider.type === 'tavily' ? '待配置密钥' : '未配置密钥' }}</template></div>
          </div>
          <div class="row-actions">
            <button class="btn icon-only small ghost" type="button" :aria-label="`编辑搜索提供商 ${provider.name}`" @click="openEditor(provider)"><Pencil :size="15" aria-hidden="true" /></button>
            <button class="btn icon-only small ghost danger" type="button" :aria-label="`删除搜索提供商 ${provider.name}`" :disabled="busy" @click="remove(provider)"><Trash2 :size="15" aria-hidden="true" /></button>
          </div>
        </div>
      </div>
      <p v-else-if="!loading && !error" class="muted">还没有搜索提供商。新建后可在机器人「模型与搜索」中分配。</p>
      <p class="hint">已有 Exa、Tavily 的地址和密钥自动沿用。自定义来源支持兼容 API、搜索 MCP 和浏览器搜索，无需在插件里重复配置。</p>
    </div>
  </section>

  <Modal v-if="editor" :title="editor.id ? '编辑搜索提供商' : '新建搜索提供商'" initial-focus="#search-provider-name" @close="closeEditor">
    <div class="stack search-provider-editor">
      <div class="field"><label for="search-provider-name">名称</label><input id="search-provider-name" v-model="editor.name" class="input" placeholder="例如 自建搜索 · 主力" maxlength="100" /></div>
      <div class="field"><label for="search-provider-type">接入协议</label><AppSelect id="search-provider-type" :model-value="editor.type" :options="searchProviderTypes" @update:model-value="changeType" /></div>
      <div class="field"><label for="search-provider-url">{{ editor.type === 'browser' ? '搜索页面地址' : '接入地址' }}</label><input id="search-provider-url" v-model="editor.url" class="input mono" :placeholder="editor.type === 'browser' ? 'https://www.google.com/search' : 'https://search.example.com/mcp'" /><span class="hint">{{ editor.type === 'browser' ? '填写搜索页地址，可使用 {query} 占位符；否则搜索词作为下方查询参数传入。使用服务器上的隔离 Chrome / Chromium。' : '支持 HTTPS 服务，或本机 localhost HTTP 服务。' }}</span></div>
      <div v-if="editor.type === 'exa_mcp' || editor.type === 'search_mcp'" class="field"><label for="search-provider-tool">MCP 搜索工具名</label><input id="search-provider-tool" v-model="editor.tool" class="input mono" placeholder="search" /></div>
      <div v-if="editor.type === 'search_mcp' || editor.type === 'browser'" class="form-grid">
        <div class="field"><label for="search-provider-query">搜索词参数名</label><input id="search-provider-query" v-model="editor.query_param" class="input mono" :placeholder="editor.type === 'browser' ? 'q' : 'query'" /></div>
        <div v-if="editor.type === 'search_mcp'" class="field"><label for="search-provider-results">结果数量参数名</label><input id="search-provider-results" v-model="editor.results_param" class="input mono" placeholder="留空不传数量参数" /></div>
      </div>
      <template v-if="editor.type !== 'browser'">
        <SecretField id="search-provider-key" v-model="editor.api_key" label="API Key" :configured="editor.api_key_configured && !editor.clear_api_key" placeholder="填写搜索服务的密钥" :revealed="keyRevealed" hint="已存密钥不回显，留空沿用；眼睛按钮只显示本次填写的值。" @toggle-reveal="keyRevealed = !keyRevealed" />
        <AppSwitch v-if="editor.api_key_configured" v-model="editor.clear_api_key">保存时清除已存密钥</AppSwitch>
      </template>
      <AppSwitch :model-value="!editor.disabled" @update:model-value="editor.disabled = !$event">启用提供商</AppSwitch>
      <p v-if="editorError" class="warn-text" role="alert">{{ editorError }}</p>
    </div>
    <template #footer><button class="btn" type="button" :disabled="busy" @click="closeEditor">取消</button><button class="btn primary" type="button" :disabled="busy" @click="save"><Save :size="14" aria-hidden="true" />{{ busy ? '保存中…' : '保存提供商' }}</button></template>
  </Modal>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { Globe, Pencil, Plus, Save, Trash2 } from "@lucide/vue";
import { deleteSearchProvider, getSearchProviders, saveSearchProvider, type SearchProvider } from "../api";
import { searchProviderAddress, searchProviderPreset, searchProviderTypeLabel, searchProviderTypes } from "../search-providers";
import { useConfigurationRefresh } from "../configuration-sync";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";
import Modal from "./Modal.vue";
import SecretField from "./SecretField.vue";

const providers = ref<SearchProvider[]>([]);
const loading = ref(true);
const error = ref("");
const busy = ref(false);
const editor = ref<(SearchProvider & { api_key: string; clear_api_key: boolean }) | null>(null);
const editorError = ref("");
const keyRevealed = ref(false);

async function load(): Promise<void> {
  loading.value = true;
  try { providers.value = (await getSearchProviders()).providers; error.value = ""; }
  catch (err) { error.value = err instanceof Error ? err.message : "搜索提供商加载失败"; }
  finally { loading.value = false; }
}
function openEditor(provider?: SearchProvider): void {
  editor.value = provider ? { ...provider, api_key: "", clear_api_key: false } : { id: "", name: "", type: "search_mcp", url: "", ...searchProviderPreset("search_mcp"), api_key: "", clear_api_key: false };
  editorError.value = "";
  keyRevealed.value = false;
}
function closeEditor(): void { if (!busy.value) editor.value = null; }
function changeType(value: string): void {
  if (!editor.value) return;
  const type = value as SearchProvider["type"];
  const oldDefault = searchProviderPreset(editor.value.type).url;
  const preset = searchProviderPreset(type);
  const address = !editor.value.url || editor.value.url === oldDefault ? preset.url ?? "" : editor.value.url;
  Object.assign(editor.value, preset, { type, url: address });
}
async function save(): Promise<void> {
  if (!editor.value) return;
  busy.value = true;
  try { providers.value = (await saveSearchProvider(editor.value)).providers; editor.value = null; toastSuccess("搜索提供商已保存"); }
  catch (err) { editorError.value = err instanceof Error ? err.message : "保存失败"; }
  finally { busy.value = false; }
}
async function remove(provider: SearchProvider): Promise<void> {
  if (!await askConfirm({ title: "删除搜索提供商", message: `删除「${provider.name}」及其凭据？仍被机器人使用的来源需要先更换。`, confirmLabel: "删除", danger: true })) return;
  busy.value = true;
  try { providers.value = (await deleteSearchProvider(provider.id)).providers; toastSuccess("搜索提供商已删除"); }
  catch (err) { toastError(err instanceof Error ? err.message : "删除失败"); }
  finally { busy.value = false; }
}
onMounted(load);
useConfigurationRefresh(["search"], load);
</script>
