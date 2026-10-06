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
            <div class="row-sub">{{ searchProviderTypeLabel(provider.type) }} · {{ searchProviderAddress(provider.url) }}<template v-if="provider.type !== 'browser'"> · {{ provider.api_key_configured ? '已配置密钥' : provider.type === 'tavily' || provider.type === 'http' && provider.http_config?.auth_type !== 'none' ? '待配置密钥' : '密钥可选' }}</template></div>
            <div v-if="provider.type === 'exa_mcp' || provider.type === 'browser'" class="hint">{{ searchProviderKeyHint(provider) }}</div>
          </div>
          <div class="row-actions">
            <button class="btn icon-only small ghost" type="button" :aria-label="`编辑搜索提供商 ${provider.name}`" @click="openEditor(provider)"><Pencil :size="15" aria-hidden="true" /></button>
            <button class="btn icon-only small ghost danger" type="button" :aria-label="`删除搜索提供商 ${provider.name}`" :disabled="busy" @click="remove(provider)"><Trash2 :size="15" aria-hidden="true" /></button>
          </div>
        </div>
      </div>
      <p v-else-if="!loading && !error" class="muted">还没有搜索提供商。新建后可在机器人「模型与搜索」中分配。</p>
      <p class="hint">已有 Exa、Tavily 的地址和密钥自动沿用。自定义来源支持 HTTP API、搜索 MCP 和浏览器搜索，无需在插件里重复配置。</p>
    </div>
  </section>

  <Modal v-if="editor" :title="editor.id ? '编辑搜索提供商' : '新建搜索提供商'" initial-focus="#search-provider-name" @close="closeEditor">
    <div class="stack search-provider-editor">
      <div class="field"><label for="search-provider-name">名称</label><input id="search-provider-name" v-model="editor.name" class="input" placeholder="例如 自建搜索 · 主力" maxlength="100" /></div>
      <div class="field"><label for="search-provider-type">接入协议</label><AppSelect id="search-provider-type" :model-value="editor.type" :options="searchProviderTypes" @update:model-value="changeType" /></div>
      <div class="field"><label for="search-provider-url">{{ editor.type === 'browser' ? '搜索页面地址' : '接入地址' }}</label><input id="search-provider-url" v-model="editor.url" class="input mono" :placeholder="editor.type === 'browser' ? 'https://www.google.com/search' : editor.type === 'http' ? 'https://search.example.com/search' : 'https://search.example.com/mcp'" /><span class="hint">{{ editor.type === 'browser' ? '填写搜索页地址，可使用 {query} 占位符；否则搜索词作为下方查询参数传入。使用服务器上的隔离 Chrome / Chromium。' : '填写完整接口地址，支持 HTTPS 服务或本机 localhost HTTP 服务。' }}</span></div>
      <div v-if="editor.type === 'http' && editor.http_config" class="form-grid">
        <div class="field"><label for="search-http-method">请求方式</label><AppSelect id="search-http-method" :model-value="editor.http_config.method || 'POST'" :options="httpMethods" @update:model-value="editor.http_config.method = $event as 'GET' | 'POST'" /></div>
        <div class="field"><label for="search-http-auth">认证方式</label><AppSelect id="search-http-auth" :model-value="editor.http_config.auth_type || 'none'" :options="httpAuthTypes" @update:model-value="editor.http_config.auth_type = $event as 'none' | 'bearer' | 'header'" /></div>
        <div v-if="editor.http_config.auth_type === 'header'" class="field"><label for="search-http-auth-header">密钥请求头</label><input id="search-http-auth-header" v-model="editor.http_config.auth_header" class="input mono" placeholder="X-API-Key" /></div>
      </div>
      <div v-if="editor.type === 'exa_mcp' || editor.type === 'search_mcp'" class="field"><label for="search-provider-tool">MCP 搜索工具名</label><input id="search-provider-tool" v-model="editor.tool" class="input mono" placeholder="search" /></div>
      <div v-if="editor.type === 'search_mcp' || editor.type === 'browser'" class="form-grid">
        <div class="field"><label for="search-provider-query">搜索词参数名</label><input id="search-provider-query" v-model="editor.query_param" class="input mono" :placeholder="editor.type === 'browser' ? 'q' : 'query'" /></div>
        <div v-if="editor.type === 'search_mcp'" class="field"><label for="search-provider-results">结果数量参数名</label><input id="search-provider-results" v-model="editor.results_param" class="input mono" placeholder="留空不传数量参数" /></div>
      </div>
      <template v-if="editor.type !== 'browser'">
        <p class="hint">{{ searchProviderKeyHint(editor) }}</p>
        <SecretField id="search-provider-key" v-model="editor.api_key" label="API Key" :configured="editor.api_key_configured && !editor.clear_api_key" placeholder="填写搜索服务的密钥" :revealed="keyRevealed" hint="已存密钥不回显，留空沿用；眼睛按钮只显示本次填写的值。" @toggle-reveal="keyRevealed = !keyRevealed" />
        <AppSwitch v-if="editor.api_key_configured" v-model="editor.clear_api_key">保存时清除已存密钥</AppSwitch>
      </template>
      <AppDisclosure v-if="editor.type === 'http' && editor.http_config" id="search-http-advanced" title="高级设置与字段映射">
        <div class="stack">
          <p class="hint">默认发送 query / count；响应可为结果数组或含 results 数组的对象。结果自动识别 url / link 和 snippet / content / description。</p>
          <div class="form-grid">
            <div class="field"><label for="search-http-query">搜索词参数名</label><input id="search-http-query" v-model="editor.query_param" class="input mono" placeholder="query" /></div>
            <div class="field"><label for="search-http-count">结果数量参数名</label><input id="search-http-count" v-model="editor.results_param" class="input mono" placeholder="留空不传数量参数" /></div>
          </div>
          <div class="field"><label for="search-http-params">固定请求参数（JSON）</label><textarea id="search-http-params" v-model="editor.params_text" class="input mono" rows="3" placeholder='{"language": "zh"}' /><span class="hint">GET 放入查询参数，POST 放入 JSON 请求体。用 {api_key} 引用上面填写的密钥；搜索词和数量由运行时填写。</span></div>
          <div class="form-grid">
            <div class="field"><label for="search-http-results-path">结果列表路径</label><input id="search-http-results-path" v-model="editor.http_config.results_path" class="input mono" placeholder="自动识别，或 data.items" /></div>
            <div class="field"><label for="search-http-url-path">链接字段</label><input id="search-http-url-path" v-model="editor.http_config.url_path" class="input mono" placeholder="自动识别 url / link" /></div>
            <div class="field"><label for="search-http-title-path">标题字段</label><input id="search-http-title-path" v-model="editor.http_config.title_path" class="input mono" placeholder="title" /></div>
            <div class="field"><label for="search-http-snippet-path">摘要字段</label><input id="search-http-snippet-path" v-model="editor.http_config.snippet_path" class="input mono" placeholder="自动识别 snippet / content" /></div>
          </div>
          <p class="hint">列表路径从响应根部读取，其余字段从每条结果读取。支持点分隔的字段和数组下标，例如 data.items、page.url、summaries.0。</p>
        </div>
      </AppDisclosure>
      <AppSwitch :model-value="!editor.disabled" @update:model-value="editor.disabled = !$event">启用提供商</AppSwitch>
      <div class="search-provider-test stack">
        <div class="field"><label for="search-test-query">测试搜索词</label><div class="search-provider-test-input"><input id="search-test-query" v-model="testQuery" class="input" maxlength="512" placeholder="输入用于验证配置的搜索词" /><button class="btn" type="button" :disabled="busy || testing || !testQuery.trim()" @click="test"><FlaskConical :size="14" aria-hidden="true" />{{ testing ? '测试中…' : '测试搜索' }}</button></div><span class="hint">使用当前草稿发送一次搜索请求，测试不会保存配置。</span></div>
        <div v-if="testResult" class="search-provider-test-result" aria-live="polite">
          <div :class="testResult.error ? 'warn-text' : ''">{{ testResult.error ? '测试失败' : `搜索成功 · ${testResult.result_count} 个结果` }} · {{ testResult.duration_ms }} ms<template v-if="testResult.http_status"> · HTTP {{ testResult.http_status }}</template></div>
          <p v-if="testResult.error" class="warn-text">{{ testResult.error }}</p>
          <pre v-else-if="testResult.content" class="mono">{{ testResult.content }}</pre>
        </div>
      </div>
      <p v-if="editorError" class="warn-text" role="alert">{{ editorError }}</p>
    </div>
    <template #footer><button class="btn" type="button" :disabled="busy" @click="closeEditor">取消</button><button class="btn primary" type="button" :disabled="busy || testing" @click="save"><Save :size="14" aria-hidden="true" />{{ busy ? '保存中…' : '保存提供商' }}</button></template>
  </Modal>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { FlaskConical, Globe, Pencil, Plus, Save, Trash2 } from "@lucide/vue";
import { deleteSearchProvider, getSearchProviders, saveSearchProvider, testSearchProvider, type SearchProvider, type SearchProviderTestResult } from "../api";
import { changeSearchProviderType, parseSearchParams, searchProviderAddress, searchProviderKeyHint, searchProviderPreset, searchProviderTypeLabel, searchProviderTypes } from "../search-providers";
import { useConfigurationRefresh } from "../configuration-sync";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";
import Modal from "./Modal.vue";
import SecretField from "./SecretField.vue";
import AppDisclosure from "./AppDisclosure.vue";

const providers = ref<SearchProvider[]>([]);
const loading = ref(true);
const error = ref("");
const busy = ref(false);
type SearchDraft = SearchProvider & { api_key: string; clear_api_key: boolean; params_text: string };
const editor = ref<SearchDraft | null>(null);
const editorError = ref("");
const keyRevealed = ref(false);
const testQuery = ref("Diana");
const testing = ref(false);
const testResult = ref<SearchProviderTestResult | null>(null);
let testGeneration = 0;
const httpMethods = [{ value: "POST", label: "POST · JSON 请求体" }, { value: "GET", label: "GET · 查询参数" }];
const httpAuthTypes = [{ value: "none", label: "无需认证" }, { value: "bearer", label: "Bearer Token" }, { value: "header", label: "自定义密钥请求头" }];

async function load(): Promise<void> {
  loading.value = true;
  try { providers.value = (await getSearchProviders()).providers; error.value = ""; }
  catch (err) { error.value = err instanceof Error ? err.message : "搜索提供商加载失败"; }
  finally { loading.value = false; }
}
function openEditor(provider?: SearchProvider): void {
  const draft: SearchProvider = provider ? JSON.parse(JSON.stringify(provider)) : { id: "", name: "", type: "exa_mcp", url: "", ...searchProviderPreset("exa_mcp") };
  if (draft.type === "http") draft.http_config = { method: "POST", auth_type: "none", ...draft.http_config };
  editor.value = { ...draft, api_key: "", clear_api_key: false, params_text: draft.http_config?.params ? JSON.stringify(draft.http_config.params, null, 2) : "" };
  editorError.value = "";
  keyRevealed.value = false;
  testResult.value = null;
}
function closeEditor(): void { if (!busy.value) { editor.value = null; testGeneration++; testing.value = false; } }
function changeType(value: string): void {
  if (!editor.value || value === editor.value.type) return;
  const type = value as SearchProvider["type"];
  Object.assign(editor.value, changeSearchProviderType(editor.value, type), { params_text: "" });
}
function editorPayload(): Omit<SearchDraft, "params_text"> {
  if (!editor.value) throw new Error("搜索草稿不存在");
  const { params_text, ...provider } = editor.value;
  if (provider.type === "http") provider.http_config = { ...provider.http_config, params: parseSearchParams(params_text) };
  return provider;
}
async function test(): Promise<void> {
  if (!editor.value) return;
  const generation = ++testGeneration;
  const snapshot = JSON.stringify(editor.value);
  const query = testQuery.value;
  testing.value = true;
  testResult.value = null;
  editorError.value = "";
  try {
    const result = await testSearchProvider(editorPayload(), query);
    if (generation === testGeneration && snapshot === JSON.stringify(editor.value) && query === testQuery.value) testResult.value = result;
  } catch (err) { if (generation === testGeneration && snapshot === JSON.stringify(editor.value) && query === testQuery.value) editorError.value = err instanceof Error ? err.message : "测试失败"; }
  finally { if (generation === testGeneration) testing.value = false; }
}
async function save(): Promise<void> {
  if (!editor.value) return;
  busy.value = true;
  try { providers.value = (await saveSearchProvider(editorPayload())).providers; editor.value = null; toastSuccess("搜索提供商已保存"); }
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
watch([editor, testQuery], () => { testResult.value = null; }, { deep: true });
useConfigurationRefresh(["search"], load);
</script>
