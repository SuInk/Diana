<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 浏览器页「高级」里的外接浏览器，卡片头和页面其余卡片用同一套：标题、一句说明、右侧小刷新。 -->
  <section class="card">
    <div class="card-header">
      <h2>外接浏览器（CDP）</h2>
      <span class="card-sub">{{ botScope ? '模型通过 CDP 操作一个真实浏览器，用的是那个浏览器已有的登录态' : '选择机器人后配置' }}</span>
      <button class="btn small ghost" type="button" :disabled="loading" title="刷新" aria-label="刷新外接浏览器配置" @click="load"><RefreshCw :size="14" aria-hidden="true" /></button>
    </div>
    <div class="card-body">
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取…</p>
    <template v-else-if="!botScope"><p class="hint">先在顶部选一个机器人。</p></template>
    <template v-else>
      <!-- 这一条要摆在输入框前面：它是这个功能唯一的真实风险，看到地址再想起来就晚了。 -->
      <p class="hint">这几个工具操作的是一个已经开着的浏览器，它登录着谁的账号，模型就以谁的身份点下去。只接你自己起的、专门给它用的浏览器实例，别接日常那个。要读公开网页用不着它——那是一次性无头渲染，不带登录态、一直可用。</p>
      <div class="browser-form">
        <label class="field">
          CDP 地址
          <input v-model.trim="cdpURL" class="input" placeholder="http://127.0.0.1:9222" />
          <span class="hint">Chrome 带 <code>--remote-debugging-port</code> 启动后监听的地址。留空用默认的 http://127.0.0.1:9222。</span>
        </label>
        <label class="field">
          超时（毫秒）
          <input v-model.number="timeoutMS" class="input" inputmode="numeric" />
          <span class="hint">单次页面操作等多久，上限 60000。</span>
        </label>
      </div>
      <p v-if="testResult" :class="testResult.connected ? 'hint' : 'error-text'" role="status">{{ testResult.connected ? `连上了${testResult.browser ? '：' + testResult.browser : ''}` : `连不上：${testResult.error}` }}</p>
      <div class="view-actions browser-actions">
        <button class="btn" :disabled="busy" @click="test"><PlugZap :size="15" />测试连接</button>
        <button class="btn primary" :disabled="busy" @click="save"><Save :size="15" />保存</button>
      </div>
      <p class="hint">接上之后模型可用：{{ tools.join('、') }}。地址连不上时这些工具会调用失败，不会自动停用。</p>
    </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue';
import { PlugZap, RefreshCw, Save } from '@lucide/vue';
import { botScope } from '../bot-scope';
import { getAgentBrowser, saveAgentBrowser, testAgentBrowser } from '../api';
import { toastError, toastSuccess } from '../toast';

// 保存后通知浏览器页，顶上「外接浏览器（CDP）」那一行的状态跟着变。
const emit = defineEmits<{ saved: [] }>();
const cdpURL = ref(''), timeoutMS = ref(15000), tools = ref<string[]>([]);
const loading = ref(false), loadError = ref(''), busy = ref(false);
const testResult = ref<{connected: boolean; browser?: string; error?: string} | null>(null);
let generation = 0;
async function load() {
  const current = ++generation;
  loading.value = true;
  loadError.value = '';
  testResult.value = null;
  try {
    const result = await getAgentBrowser(botScope.value);
    if (current !== generation) return;
    cdpURL.value = result.cdp_url || '';
    timeoutMS.value = result.timeout_ms || 15000;
    tools.value = result.tools || [];
  } catch (e) {
    if (current === generation) loadError.value = String(e instanceof Error ? e.message : e);
  } finally {
    if (current === generation) loading.value = false;
  }
}
async function save() {
  const profile = botScope.value;
  if (!profile) return;
  busy.value = true;
  try {
    const result = await saveAgentBrowser(profile, cdpURL.value, timeoutMS.value);
    cdpURL.value = result.cdp_url || '';
    timeoutMS.value = result.timeout_ms || timeoutMS.value;
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
.browser-form{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:14px}.browser-actions{margin-top:16px;gap:8px}.error-text{color:var(--danger)}@media(max-width:600px){.browser-form{grid-template-columns:1fr}}
</style>
