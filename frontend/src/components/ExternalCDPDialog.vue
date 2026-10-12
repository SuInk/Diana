<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Modal title="外接浏览器（CDP）" @close="emit('close')">
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading" class="hint">正在读取…</p>
    <form v-else class="stack" @submit.prevent="save">
      <label class="field">
        CDP 地址
        <input v-model.trim="cdpURL" class="input" placeholder="http://127.0.0.1:9222" autofocus />
        <span class="hint">Chrome 带 <code>--remote-debugging-port</code> 启动后监听的地址。留空用默认的 http://127.0.0.1:9222。</span>
      </label>
      <label class="field">
        超时（毫秒）
        <input v-model.number="timeoutMS" class="input" inputmode="numeric" />
        <span class="hint">单次页面操作等多久，上限 60000。</span>
      </label>
      <p class="hint">模型会以这个浏览器里登录的账号身份操作，只接专门给机器人用的实例，别接日常那个。</p>
      <p v-if="testResult" :class="testResult.connected ? 'hint' : 'error-text'" role="status">{{ testResult.connected ? `连上了${testResult.browser ? '：' + testResult.browser : ''}` : `连不上：${testResult.error}` }}</p>
      <div class="view-actions">
        <button class="btn" type="button" :disabled="busy" @click="test"><PlugZap :size="15" />测试连接</button>
        <button class="btn primary" type="submit" :disabled="busy"><Save :size="15" />保存</button>
      </div>
    </form>
  </Modal>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { PlugZap, Save } from '@lucide/vue';
import { getAgentBrowser, saveAgentBrowser, testAgentBrowser } from '../api';
import Modal from './Modal.vue';
import { toastError, toastSuccess } from '../toast';

const props = defineProps<{ profile: string }>();
const emit = defineEmits<{ close: []; saved: [] }>();
const cdpURL = ref(''), timeoutMS = ref(15000);
const loading = ref(true), loadError = ref(''), busy = ref(false);
const testResult = ref<{connected: boolean; browser?: string; error?: string} | null>(null);

onMounted(async () => {
  try {
    const result = await getAgentBrowser(props.profile);
    cdpURL.value = result.cdp_url || '';
    timeoutMS.value = result.timeout_ms || 15000;
  } catch (e) {
    loadError.value = String(e instanceof Error ? e.message : e);
  } finally {
    loading.value = false;
  }
});

async function test() {
  busy.value = true;
  testResult.value = null;
  try {
    testResult.value = await testAgentBrowser(props.profile, cdpURL.value);
  } catch (e) {
    testResult.value = {connected: false, error: String(e instanceof Error ? e.message : e)};
  } finally {
    busy.value = false;
  }
}

// 不带权限字段，后端保持原有的操作和截图权限不变。
async function save() {
  busy.value = true;
  try {
    await saveAgentBrowser(props.profile, cdpURL.value, Number(timeoutMS.value) || 15000);
    toastSuccess('已保存，后续会话生效');
    emit('saved');
    emit('close');
  } catch (e) {
    toastError(String(e instanceof Error ? e.message : e));
  } finally {
    busy.value = false;
  }
}
</script>

<style scoped>
.error-text{color:var(--err)}
</style>
