<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="card">
    <header class="card-header">
      <div class="view-title">
        <h2>浏览器权限与外接 CDP</h2>
        <p>{{ botScope ? '为主人和指定用户配置浏览器权限，个人登录态相互隔离' : '选择机器人后配置' }}</p>
      </div>
      <div class="view-actions"><button class="btn" :disabled="loading" @click="load"><RefreshCw :size="15" />刷新</button></div>
    </header>
    <div class="card-body">
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取…</p>
    <template v-else-if="!botScope"><p class="hint">先在顶部选一个机器人。</p></template>
    <template v-else-if="!loadError">
      <p class="hint">主人使用配置的内置浏览器或 CDP。指定用户使用各自独立的浏览器，只能操作白名单网站，不会连接主人的浏览器。公开网页由「网页渲染」插件使用临时浏览器访问。</p>
      <div class="browser-form">
        <label class="field">
          主人的 CDP 地址
          <input v-model.trim="cdpURL" class="input" placeholder="http://127.0.0.1:9222" />
          <span class="hint">Chrome 带 <code>--remote-debugging-port</code> 启动后监听的地址。留空用默认的 http://127.0.0.1:9222。</span>
        </label>
        <label class="field">
          超时（毫秒）
          <input v-model.number="timeoutMS" class="input" inputmode="numeric" />
          <span class="hint">单次页面操作等多久，上限 60000。</span>
        </label>
        <div class="field">
          <label for="browser-operation-access">浏览器操作权限</label>
          <AppSelect id="browser-operation-access" v-model="operationAccess.mode" :options="screenshotAccessOptions" />
          <span class="hint">默认仅主人。指定用户模式始终包含主人；普通用户只在私聊操作自己的独立浏览器。</span>
        </div>
        <div v-if="operationAccess.mode === 'whitelist'" class="field">
          <label for="browser-operation-users">允许操作的用户</label>
          <IdChipInput input-id="browser-operation-users" v-model="operationAccess.allowed_users" placeholder="填用户 ID 后回车" />
          <span class="hint">按本机器人所在平台的用户 ID 精确匹配，登录态按机器人、平台和用户分别保存。</span>
        </div>
        <div v-if="operationAccess.mode === 'whitelist'" class="field wide">
          <label for="browser-operation-hosts">允许操作的网站</label>
          <IdChipInput input-id="browser-operation-hosts" v-model="operationAccess.allowed_hosts" placeholder="例如 example.com，回车添加" />
          <span class="hint">必填精确域名，可带端口，不含子域名。业务网站、登录跳转和资源域名都需加入；不支持协议、路径或通配符，禁止内网地址。</span>
        </div>
        <div class="field">
          <label for="browser-screenshot-access">登录浏览器截图权限</label>
          <AppSelect id="browser-screenshot-access" v-model="screenshotAccess.mode" :options="screenshotAccessOptions" />
          <span class="hint">默认仅主人。停用对主人也生效；指定用户截图还需开启上方的个人操作权限，只能截取自己的页面。</span>
        </div>
        <div v-if="screenshotAccess.mode === 'whitelist'" class="field">
          <label for="browser-screenshot-users">允许截图的用户</label>
          <IdChipInput input-id="browser-screenshot-users" v-model="screenshotAccess.allowed_users" placeholder="填用户 ID 后回车" />
          <span class="hint">用户和网站都填好才会开放，普通用户仅限私聊；扫码登录也需授权截图。</span>
        </div>
        <div v-if="screenshotAccess.mode === 'whitelist'" class="field wide">
          <label for="browser-screenshot-hosts">允许截图的网站</label>
          <IdChipInput input-id="browser-screenshot-hosts" v-model="screenshotAccess.allowed_hosts" placeholder="例如 example.com，回车添加" />
          <span class="hint">必填。精确匹配域名（可带端口），不包含子域名；不填协议、路径或通配符。嵌入页面也必须在授权范围内。</span>
        </div>
      </div>
      <p class="hint">公共网页截图通过「网页渲染」插件提供，使用独立临时浏览器，不带登录态，不受此处登录浏览器截图权限影响。</p>
      <p v-if="screenshotAccess.mode === 'whitelist'" class="hint">截图权限与操作权限分别设置。普通用户可查看和向当前私聊发送自己的登录页面；仅授权截图不能接入主人的浏览器，也不授予本地文件工具。</p>
      <p v-if="testResult" :class="testResult.connected ? 'hint' : 'error-text'" role="status">{{ testResult.connected ? `连上了${testResult.browser ? '：' + testResult.browser : ''}` : `连不上：${testResult.error}` }}</p>
      <div class="view-actions browser-actions">
        <button class="btn" :disabled="busy" @click="test"><PlugZap :size="15" />测试连接</button>
        <button class="btn primary" :disabled="busy" @click="save"><Save :size="15" />保存</button>
      </div>
      <p class="hint">浏览器工具：{{ tools.join('、') }}。操作和截图按上方权限分别开放。测试连接仅检测主人的 CDP，个人浏览器在首次操作时启动。</p>
    </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue';
import { PlugZap, RefreshCw, Save } from '@lucide/vue';
import { botScope } from '../bot-scope';
import { getAgentBrowser, saveAgentBrowser, testAgentBrowser } from '../api';
import type { BrowserScreenshotAccess, BrowserOperationAccess } from '../api';
import AppSelect from './AppSelect.vue';
import IdChipInput from './IdChipInput.vue';
import { toastError, toastSuccess } from '../toast';

const emit = defineEmits<{ saved: [] }>();
const cdpURL = ref(''), timeoutMS = ref(15000), tools = ref<string[]>([]);
const screenshotAccess = ref<BrowserScreenshotAccess>({ mode: 'owner_only', allowed_users: [], allowed_hosts: [], allowed_groups: [] });
const operationAccess = ref<BrowserOperationAccess>({ mode: 'owner_only', allowed_users: [], allowed_hosts: [] });
const screenshotAccessOptions = [
  { value: 'disabled', label: '停用（包括主人）' },
  { value: 'owner_only', label: '仅主人' },
  { value: 'whitelist', label: '指定用户' }
];
const loading = ref(false), loadError = ref(''), busy = ref(false);
const testResult = ref<{connected: boolean; browser?: string; error?: string} | null>(null);
let generation = 0;
let loadedProfile = '';
function normalizedScreenshotAccess(access?: BrowserScreenshotAccess): BrowserScreenshotAccess {
  const mode = ['disabled', 'owner_only', 'whitelist'].includes(access?.mode ?? '') ? access!.mode : 'owner_only';
  return { mode, allowed_users: [...(access?.allowed_users ?? [])], allowed_hosts: [...(access?.allowed_hosts ?? [])], allowed_groups: [...(access?.allowed_groups ?? [])] };
}
function normalizedOperationAccess(access?: BrowserOperationAccess): BrowserOperationAccess {
  const normalized = normalizedScreenshotAccess(access);
  return { mode: normalized.mode, allowed_users: normalized.allowed_users, allowed_hosts: normalized.allowed_hosts };
}
async function load() {
  const current = ++generation;
  const profile = botScope.value;
  loadedProfile = '';
  loading.value = true;
  loadError.value = '';
  testResult.value = null;
  try {
    const result = await getAgentBrowser(profile);
    if (current !== generation) return;
    cdpURL.value = result.cdp_url || '';
    timeoutMS.value = result.timeout_ms || 15000;
    tools.value = result.tools || [];
    screenshotAccess.value = normalizedScreenshotAccess(result.screenshot_access);
    operationAccess.value = normalizedOperationAccess(result.operation_access);
    loadedProfile = profile;
  } catch (e) {
    if (current === generation) loadError.value = String(e instanceof Error ? e.message : e);
  } finally {
    if (current === generation) loading.value = false;
  }
}
async function save() {
  const profile = botScope.value;
  if (!profile || loadedProfile !== profile || loading.value || loadError.value || busy.value) return;
  const current = generation;
  busy.value = true;
  try {
    const result = await saveAgentBrowser(profile, cdpURL.value, timeoutMS.value, screenshotAccess.value, operationAccess.value);
    if (current !== generation || profile !== botScope.value) return;
    cdpURL.value = result.cdp_url || '';
    timeoutMS.value = result.timeout_ms || timeoutMS.value;
    screenshotAccess.value = normalizedScreenshotAccess(result.screenshot_access);
    operationAccess.value = normalizedOperationAccess(result.operation_access);
    toastSuccess('已保存，后续会话生效');
    emit('saved');
  } catch (e) {
    toastError(String(e instanceof Error ? e.message : e));
  } finally {
    busy.value = false;
  }
}
async function test() {
  const profile = botScope.value;
  if (!profile) return;
  busy.value = true;
  testResult.value = null;
  try {
    testResult.value = await testAgentBrowser(profile, cdpURL.value);
  } catch (e) {
    testResult.value = {connected: false, error: String(e instanceof Error ? e.message : e)};
  } finally {
    busy.value = false;
  }
}
watch(botScope, load);
onMounted(load);
</script>

<style scoped>
.browser-form{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:14px}.browser-form .wide{grid-column:1 / -1}.browser-actions{margin-top:16px;gap:8px}.error-text{color:var(--danger)}@media(max-width:600px){.browser-form{grid-template-columns:1fr}}
</style>
