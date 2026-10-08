<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 从浏览器页「个人浏览器权限」一行弹出，和外接 CDP 的弹窗同一种做法。 -->
  <Modal title="个人浏览器权限" wide @close="emit('close')">
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取…</p>
    <template v-else-if="!botScope"><p class="hint">先在顶部选一个机器人。</p></template>
    <template v-else-if="!loadError">
      <p class="note">主人使用配置的内置浏览器或 CDP。指定用户使用各自独立的浏览器，只能操作白名单网站，不会连接主人的浏览器。公开网页由「网页渲染」插件使用临时浏览器访问。</p>
      <p v-if="untestedPlatform" class="note warn-text">{{ untestedPlatform }} 上的个人浏览器还没实测过，用户 ID 匹配和私聊判断可能有出入。目前实测过 QQ（OneBot v11）、QQ 官方机器人和 Telegram。</p>
      <div class="browser-form">
        <div class="field">
          <label for="browser-operation-access">浏览器操作权限</label>
          <AppSelect id="browser-operation-access" v-model="operationAccess.mode" :options="screenshotAccessOptions" />
          <span class="hint">默认仅主人。指定用户模式始终包含主人；普通用户只在私聊操作自己的独立浏览器。</span>
        </div>
        <div v-if="operationAccess.mode === 'whitelist'" class="field">
          <label for="browser-operation-users">允许操作的用户</label>
          <IdChipInput input-id="browser-operation-users" v-model="operationAccess.allowed_users" placeholder="填用户 ID 后回车" :resolve-names="resolveAccountNames" />
          <span v-if="!operationAccess.allowed_users?.length" class="hint error-text">至少填一个用户，否则只有主人能用。</span>
          <span class="hint">按本机器人所在平台的用户 ID 精确匹配，登录态按机器人、平台和用户分别保存。</span>
        </div>
        <div v-if="operationAccess.mode === 'whitelist'" class="field wide">
          <label for="browser-operation-hosts">允许操作的网站</label>
          <IdChipInput input-id="browser-operation-hosts" v-model="operationAccess.allowed_hosts" placeholder="例如 example.com，回车添加" />
          <span v-if="invalidHosts(operationAccess).length" class="hint error-text">格式不对：{{ invalidHosts(operationAccess).join('、') }}。只填域名或域名:端口，例如 example.com。</span>
          <span v-else-if="!operationAccess.allowed_hosts?.length" class="hint error-text">至少填一个网站，否则指定用户什么都打不开。</span>
          <span class="hint">必填精确域名，可带端口，不含子域名。业务网站、登录跳转和资源域名都需加入；不支持协议、路径或通配符，禁止内网地址。</span>
        </div>
        <div class="field">
          <label for="browser-screenshot-access">登录浏览器截图权限</label>
          <AppSelect id="browser-screenshot-access" v-model="screenshotAccess.mode" :options="screenshotAccessOptions" />
          <span v-if="screenshotWithoutOperation" class="hint warn-text">左边的操作权限没开给指定用户，这里的截图授权对他们不生效。</span>
          <span v-else-if="screenshotUsersWithoutOperation.length" class="hint warn-text">{{ screenshotUsersWithoutOperation.join('、') }} 不在允许操作的用户里，截图对他们不生效。</span>
          <span class="hint">默认仅主人。停用对主人也生效；指定用户截图还需开启上方的个人操作权限，只能截取自己的页面。</span>
        </div>
        <div v-if="screenshotAccess.mode === 'whitelist'" class="field">
          <label for="browser-screenshot-users">允许截图的用户</label>
          <IdChipInput input-id="browser-screenshot-users" v-model="screenshotAccess.allowed_users" placeholder="填用户 ID 后回车" :resolve-names="resolveAccountNames" />
          <span v-if="!screenshotAccess.allowed_users?.length" class="hint error-text">至少填一个用户，否则只有主人能用。</span>
          <span class="hint">用户和网站都填好才会开放，普通用户仅限私聊；扫码登录也需授权截图。</span>
        </div>
        <div v-if="screenshotAccess.mode === 'whitelist'" class="field wide">
          <label for="browser-screenshot-hosts">允许截图的网站</label>
          <IdChipInput input-id="browser-screenshot-hosts" v-model="screenshotAccess.allowed_hosts" placeholder="例如 example.com，回车添加" />
          <span v-if="invalidHosts(screenshotAccess).length" class="hint error-text">格式不对：{{ invalidHosts(screenshotAccess).join('、') }}。只填域名或域名:端口，例如 example.com。</span>
          <span v-else-if="!screenshotAccess.allowed_hosts?.length" class="hint error-text">至少填一个网站，否则指定用户什么都打不开。</span>
          <span class="hint">必填。精确匹配域名（可带端口），不包含子域名；不填协议、路径或通配符。嵌入页面也必须在授权范围内。</span>
        </div>
      </div>
      <p class="note">公共网页截图通过「网页渲染」插件提供，使用独立临时浏览器，不带登录态，不受此处登录浏览器截图权限影响。</p>
      <p v-if="screenshotAccess.mode === 'whitelist'" class="note">截图权限与操作权限分别设置。普通用户可查看和向当前私聊发送自己的登录页面；仅授权截图不能接入主人的浏览器，也不授予本地文件工具。</p>
      <p class="note">浏览器工具：{{ tools.join('、') }}。个人浏览器在首次操作时启动。</p>
      <div class="view-actions browser-actions">
        <button class="btn primary" :disabled="busy || !formValid" :title="formValid ? undefined : '先补全上方标红的项'" @click="save"><Save :size="15" />保存</button>
      </div>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { Save } from '@lucide/vue';
import { botScope } from '../bot-scope';
import { fetchAssistantUserNames, getAgentBrowser, getBotProfileConfig, saveAgentBrowser } from '../api';
import type { BrowserScreenshotAccess, BrowserOperationAccess } from '../api';
import AppSelect from './AppSelect.vue';
import Modal from './Modal.vue';
import IdChipInput from './IdChipInput.vue';
import { toastError, toastSuccess } from '../toast';

const emit = defineEmits<{ saved: []; close: [] }>();
// CDP 地址和超时在「外接浏览器」弹窗里改；这里只原样带回，后端保存接口要求一起提交。
const cdpURL = ref(''), timeoutMS = ref(15000), tools = ref<string[]>([]);
const screenshotAccess = ref<BrowserScreenshotAccess>({ mode: 'owner_only', allowed_users: [], allowed_hosts: [], allowed_groups: [] });
const operationAccess = ref<BrowserOperationAccess>({ mode: 'owner_only', allowed_users: [], allowed_hosts: [] });
const screenshotAccessOptions = [
  { value: 'disabled', label: '停用（包括主人）' },
  { value: 'owner_only', label: '仅主人' },
  { value: 'whitelist', label: '指定用户' }
];
const loading = ref(false), loadError = ref(''), busy = ref(false);
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
// 和后端 ValidBrowserScreenshotHost 同一个规则：只认域名或域名:端口，带协议、路径、通配符都不行。
function validHost(host: string): boolean {
  const value = host.trim().toLowerCase();
  if (!value || /[*\\\s/%?#@]/.test(value)) return false;
  // URL 会吃掉默认端口 443，后端不会，所以两种写法都认。
  try { const host = new URL(`https://${value}`).host; return host === value || `${host}:443` === value; } catch { return false; }
}
function invalidHosts(access: { allowed_hosts?: string[] }): string[] {
  return (access.allowed_hosts ?? []).filter(host => !validHost(host));
}
// 指定用户模式下，用户或网站缺一样后端都会当成「谁也不开放」，保存成功却没效果，所以在这里拦住。
function whitelistComplete(access: { mode: string; allowed_users?: string[]; allowed_hosts?: string[] }): boolean {
  if (access.mode !== 'whitelist') return true;
  return !!access.allowed_users?.length && !!access.allowed_hosts?.length && !invalidHosts(access).length;
}
// 后端要求普通用户截图时也有操作权限（截的是他自己的独立浏览器），只开截图不会生效。
const screenshotWithoutOperation = computed(() => screenshotAccess.value.mode === 'whitelist' && operationAccess.value.mode !== 'whitelist');
const screenshotUsersWithoutOperation = computed(() => {
  if (screenshotAccess.value.mode !== 'whitelist' || operationAccess.value.mode !== 'whitelist') return [];
  const operators = new Set(operationAccess.value.allowed_users ?? []);
  return (screenshotAccess.value.allowed_users ?? []).filter(user => !operators.has(user));
});
// 个人浏览器按平台用户 ID 隔离，目前只在这几个平台上实测过。
const testedPlatforms: Record<string, true> = { '': true, 'onebot-v11': true, 'qq-official': true, telegram: true };
const platformNames: Record<string, string> = { dingtalk: '钉钉', feishu: '飞书', wecom: '企业微信', weixin: '微信', imessage: 'iMessage', discord: 'Discord' };
const platform = ref('');
const untestedPlatform = computed(() => testedPlatforms[platform.value] ? '' : (platformNames[platform.value] ?? platform.value));
async function loadPlatform(profile: string) {
  platform.value = '';
  try {
    const config = await getBotProfileConfig();
    const profiles = config.profiles?.length ? config.profiles : [config];
    if (profile === botScope.value) platform.value = profiles.find(item => item.id === profile)?.platform ?? '';
  } catch {
    // 读不到平台只是少一条提示，不影响配置权限。
  }
}
const formValid = computed(() => whitelistComplete(operationAccess.value) && whitelistComplete(screenshotAccess.value));
async function resolveAccountNames(ids: string[]): Promise<Record<string, string>> {
  const response = await fetchAssistantUserNames(ids, botScope.value);
  return response.names ?? {};
}
async function load() {
  const current = ++generation;
  const profile = botScope.value;
  loadedProfile = '';
  loading.value = true;
  loadError.value = '';
  try {
    void loadPlatform(profile);
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
  if (!profile || loadedProfile !== profile || loading.value || loadError.value || busy.value || !formValid.value) return;
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
    emit('close');
  } catch (e) {
    toastError(String(e instanceof Error ? e.message : e));
  } finally {
    busy.value = false;
  }
}
watch(botScope, load);
onMounted(load);
</script>

<style scoped>
.browser-form{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:14px}.browser-form .wide{grid-column:1 / -1}.browser-actions{margin-top:16px;gap:8px}.note.warn-text{color:var(--warn)}.note{margin:12px 0 0;font-size:13px;line-height:1.6;color:var(--muted)}.browser-form+.note.warn-text{color:var(--warn)}.note{margin-top:16px}.error-text{color:var(--err)}@media(max-width:600px){.browser-form{grid-template-columns:1fr}}
</style>
