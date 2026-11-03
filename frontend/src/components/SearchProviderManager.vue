<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="card search-provider-manager">
    <div class="card-header">
      <div><h2>搜索提供商</h2><span class="card-sub">统一管理接入地址和凭据；每台机器人在机器人页「联网搜索」中选择来源和优先级。</span></div>
    </div>
    <div class="card-body stack">
      <p v-if="loading" class="muted">正在加载搜索提供商…</p>
      <div v-if="error" class="operation-error" role="alert">{{ error }}<button class="btn small" type="button" @click="load">重试</button></div>
      <div v-for="section in providerSections" :key="section.key" class="stack search-provider-section">
        <div class="group-header"><span>{{ section.label }}</span><span class="muted" style="font-size: 12px">{{ section.items.length }} 个 · {{ section.hint }}</span></div>
      <div class="row-list">
        <div v-for="provider in section.items" :key="provider.id" class="row-item search-provider-row" :class="{ disabled: provider.disabled }">
          <span class="search-provider-icon"><Globe :size="18" aria-hidden="true" /></span>
          <div class="row-main">
            <div class="row-title">{{ provider.name }} <span v-if="provider.disabled" class="badge" title="旧版停用的来源，编辑保存后恢复启用">已停用</span></div>
            <div class="row-sub">{{ searchProviderTypeLabel(provider.type) }} · {{ searchProviderAddress(provider.url) }}<template v-if="searchProviderKeyStatus(provider)"> · <span :class="{ 'warn-text': searchProviderKeyStatus(provider) === '待配置密钥' }">{{ searchProviderKeyStatus(provider) }}</span></template></div>
          </div>
          <div class="row-actions">
            <span class="cluster" style="gap: 2px" title="调整显示顺序；机器人实际的搜索先后在机器人页「联网搜索」里排">
              <button class="btn icon-only small ghost" type="button" :aria-label="`上移搜索提供商 ${provider.name}`" :disabled="busy || !canMove(section.items, provider, -1)" @click="move(section.items, provider, -1)"><ChevronUp :size="15" aria-hidden="true" /></button>
              <button class="btn icon-only small ghost" type="button" :aria-label="`下移搜索提供商 ${provider.name}`" :disabled="busy || !canMove(section.items, provider, 1)" @click="move(section.items, provider, 1)"><ChevronDown :size="15" aria-hidden="true" /></button>
            </span>
            <button class="btn icon-only small ghost" type="button" :aria-label="`测试搜索提供商 ${provider.name}`" title="测试这个搜索提供商" @click="openRowTest(provider)"><Send :size="15" aria-hidden="true" /></button>
            <button class="btn icon-only small ghost" type="button" :aria-label="`编辑搜索提供商 ${provider.name}`" @click="openEditor(provider)"><Pencil :size="15" aria-hidden="true" /></button>
            <button class="btn icon-only small ghost" type="button" :aria-label="`克隆搜索提供商 ${provider.name}`" title="克隆（不复制密钥）" @click="cloneProvider(provider)"><Copy :size="15" aria-hidden="true" /></button>
            <button class="btn icon-only small ghost danger" type="button" :aria-label="`删除搜索提供商 ${provider.name}`" :disabled="busy" @click="remove(provider)"><Trash2 :size="15" aria-hidden="true" /></button>
          </div>
        </div>
      </div>
      </div>
      <p v-if="!providers.length && !loading && !error" class="muted">还没有搜索提供商。点右上角「新建配置」添加，之后在机器人页「联网搜索」中选用。</p>
      <p class="hint">用不用某个来源在机器人页「联网搜索」里选，测试按钮用已保存的配置搜索一次。已有 Exa、Tavily 的地址和密钥自动沿用；自定义来源支持 HTTP API、搜索 MCP 和浏览器搜索。</p>
    </div>
  </section>

  <Modal v-if="picking" title="选择搜索服务" @close="picking = false">
    <div class="stack">
    <template v-for="group in templateGroups" :key="group.label">
    <div class="group-header"><span>{{ group.label }}</span></div>
    <div class="search-template-grid">
      <button v-for="template in group.items" :key="template.id" class="search-template" type="button" @click="startFromTemplate(template)">
        <strong>{{ template.label }}</strong><span class="hint">{{ template.hint }}</span>
      </button>
    </div>
    </template>
    <p class="hint">地址和参数会按所选服务自动填写，之后仍可修改。</p>
    </div>
  </Modal>

  <Modal v-if="rowTest" :title="`测试 · ${rowTest.provider.name}`" @close="rowTest = null">
    <div class="stack" style="gap: 10px">
      <p class="muted" style="margin: 0; font-size: 12.5px">{{ searchProviderTypeLabel(rowTest.provider.type) }} · {{ searchProviderAddress(rowTest.provider.url) }}</p>
      <input v-model="rowTest.query" class="input" maxlength="512" placeholder="输入测试搜索词…" aria-label="测试搜索词" @keydown.enter="runRowTest" />
      <button class="btn primary" type="button" :disabled="rowTest.running || !rowTest.query.trim()" @click="runRowTest"><Send :size="15" aria-hidden="true" />{{ rowTest.running ? '测试中…' : '测试搜索' }}</button>
      <span class="hint">用已保存的配置和密钥搜索一次，不会改动配置。</span>
      <div v-if="rowTest.result" class="search-provider-test-result" aria-live="polite">
        <div :class="rowTest.result.error ? 'warn-text' : ''">{{ rowTest.result.error ? '测试失败' : `搜索成功 · ${rowTest.result.result_count} 个结果` }} · {{ rowTest.result.duration_ms }} ms<template v-if="rowTest.result.http_status"> · HTTP {{ rowTest.result.http_status }}</template></div>
        <p v-if="rowTest.result.error" class="warn-text">{{ rowTest.result.error }}</p>
        <pre v-else-if="rowTest.result.content" class="mono">{{ rowTest.result.content }}</pre>
      </div>
    </div>
  </Modal>
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
import { computed, onMounted, ref, watch } from "vue";
import { ChevronDown, ChevronUp, Copy, FlaskConical, Globe, Pencil, Save, Send, Trash2 } from "@lucide/vue";
import { deleteSearchProvider, getSearchProviders, reorderSearchProviders, saveSearchProvider, testSearchProvider, type SearchProvider, type SearchProviderTestResult, type WebSearchAssignment } from "../api";
import { changeSearchProviderType, parseSearchParams, searchProviderAddress, searchProviderFromTemplate, searchProviderKeyHint, searchProviderKeyStatus, searchProviderTemplates, searchProviderTypeLabel, searchProviderTypes, type SearchProviderTemplate } from "../lib/search-providers";
import { useConfigurationRefresh } from "../lib/configuration-sync";
import { askConfirm } from "../lib/confirm";
import { toastError, toastSuccess } from "../lib/toast";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";
import Modal from "./Modal.vue";
import SecretField from "./SecretField.vue";
import AppDisclosure from "./AppDisclosure.vue";

const providers = ref<SearchProvider[]>([]);
const providerSections = computed(() => [
  { key: "api", label: "API 搜索", hint: "调用搜索服务接口，速度快、结果结构化", items: providers.value.filter(provider => provider.type !== "browser") },
  { key: "browser", label: "浏览器搜索", hint: "隔离 Chrome 打开搜索页提取结果，无需密钥", items: providers.value.filter(provider => provider.type === "browser") },
].filter(section => section.items.length));
const picking = ref(false);
const templateGroups = [
  { label: "API 搜索", items: searchProviderTemplates.filter(template => template.provider.type !== "browser") },
  { label: "浏览器搜索", items: searchProviderTemplates.filter(template => template.provider.type === "browser") },
];
const rowTest = ref<{ provider: SearchProvider; query: string; running: boolean; result: SearchProviderTestResult | null } | null>(null);
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
  try { applyConfiguration(await getSearchProviders()); error.value = ""; }
  catch (err) { error.value = err instanceof Error ? err.message : "搜索提供商加载失败"; }
  finally { loading.value = false; }
}
function applyConfiguration(config: { providers: SearchProvider[]; default_assignment: WebSearchAssignment }): void {
  providers.value = config.providers;
}
function startFromTemplate(template: SearchProviderTemplate): void {
  picking.value = false;
  openEditor(searchProviderFromTemplate(template));
}
function cloneProvider(provider: SearchProvider): void {
  openEditor({ ...JSON.parse(JSON.stringify(provider)), id: "", name: `${provider.name} 副本`.slice(0, 100), api_key_configured: false });
}
// 列表上的测试和模型提供商一样弹窗进行，可以改搜索词再测。
function openRowTest(provider: SearchProvider): void { rowTest.value = { provider, query: "Diana", running: false, result: null }; }
async function runRowTest(): Promise<void> {
  const current = rowTest.value;
  if (!current || current.running || !current.query.trim()) return;
  current.running = true;
  current.result = null;
  let result: SearchProviderTestResult;
  try { result = await testSearchProvider({ ...current.provider, api_key: "", clear_api_key: false }, current.query.trim()); }
  catch (err) { result = { duration_ms: 0, result_count: 0, error: err instanceof Error ? err.message : "测试失败" }; }
  if (rowTest.value === current) { current.result = result; current.running = false; }
}
function openEditor(provider: SearchProvider): void {
  const draft: SearchProvider = JSON.parse(JSON.stringify(provider));
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
  // 和模型提供商一样不在这里启停：用不用由机器人页「联网搜索」决定。旧版停用过的保存一次即恢复。
  provider.disabled = false;
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
  try { const saved = editorPayload(); applyConfiguration(await saveSearchProvider(saved)); editor.value = null; toastSuccess("搜索提供商已保存"); }
  catch (err) { editorError.value = err instanceof Error ? err.message : "保存失败"; }
  finally { busy.value = false; }
}
// 在同一分区（API / 浏览器）内上下移动，和模型提供商在组内排序一样。
function canMove(siblings: SearchProvider[], provider: SearchProvider, delta: number): boolean {
  const target = siblings.findIndex(item => item.id === provider.id) + delta;
  return target >= 0 && target < siblings.length && siblings.some(item => item.id === provider.id);
}
async function move(siblings: SearchProvider[], provider: SearchProvider, delta: number): Promise<void> {
  if (!canMove(siblings, provider, delta)) return;
  const other = siblings[siblings.findIndex(item => item.id === provider.id) + delta];
  const ids = providers.value.map(item => item.id);
  const from = ids.indexOf(provider.id), to = ids.indexOf(other.id);
  [ids[from], ids[to]] = [ids[to], ids[from]];
  busy.value = true;
  try { applyConfiguration(await reorderSearchProviders(ids)); toastSuccess("顺序已调整"); }
  catch (err) { toastError(err instanceof Error ? err.message : "调整失败"); await load(); }
  finally { busy.value = false; }
}
async function remove(provider: SearchProvider): Promise<void> {
  if (!await askConfirm({ title: "删除搜索提供商", message: `删除「${provider.name}」及其凭据？仍被机器人使用的来源需要先更换。`, confirmLabel: "删除", danger: true })) return;
  busy.value = true;
  try { applyConfiguration(await deleteSearchProvider(provider.id)); toastSuccess("搜索提供商已删除"); }
  catch (err) { toastError(err instanceof Error ? err.message : "删除失败"); }
  finally { busy.value = false; }
}
// 新建入口在提供商页右上角的「新建配置」，和模型提供商同一个位置。
function startCreate(): void { picking.value = true; }
defineExpose({ startCreate });
onMounted(load);
watch([editor, testQuery], () => { testResult.value = null; }, { deep: true });
useConfigurationRefresh(["search"], load);
</script>
