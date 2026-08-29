<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
// 授权登录面板。
//
// 控制台跑在服务器上，用户的浏览器未必和它同一台机器，所以这里不做「等回调自己
// 落回来」那套：后端给出授权地址，用户在自己的浏览器里点完同意，把地址栏里那条
// 回调地址整条粘回来。这是 Pi 在远程机器上的做法，对 WebUI 来说它是主路径。
import { computed, onMounted, ref } from "vue";
import { KeyRound, LogOut, Plus, RefreshCw, Trash2, ExternalLink } from "@lucide/vue";

import AppSelect from "./AppSelect.vue";
import Modal from "./Modal.vue";
import {
  cancelOAuthLogin,
  completeOAuthLogin,
  deleteOAuthProvider,
  listOAuthProviders,
  logoutOAuthProvider,
  saveOAuthProvider,
  startOAuthLogin,
  type LLMOAuthPendingLogin,
  type LLMOAuthProvider,
  type LLMOAuthStatus
} from "../api";

const emit = defineEmits<{ (event: "changed", providers: LLMOAuthStatus[]): void }>();

const statuses = ref<LLMOAuthStatus[]>([]);
const loading = ref(false);
const enabled = ref(true);
const error = ref("");
const notice = ref("");

// 登录进行中的状态。
const pendingLogin = ref<LLMOAuthPendingLogin | null>(null);
const pendingProvider = ref<LLMOAuthStatus | null>(null);
const callbackDraft = ref("");
const completing = ref(false);

// 自定义提供商编辑器。
const editorOpen = ref(false);
const editorSaving = ref(false);
const editor = ref<LLMOAuthProvider>(emptyProvider());

function emptyProvider(): LLMOAuthProvider {
  return {
    key: "",
    label: "",
    authorize_url: "",
    token_url: "",
    client_id: "",
    client_secret: "",
    redirect_uri: "",
    scopes: [],
    notes: ""
  };
}

const scopeDraft = ref("");

const busy = computed(() => loading.value || completing.value || editorSaving.value);

function applyProviders(next: LLMOAuthStatus[]) {
  statuses.value = next;
  emit("changed", next);
}

async function refresh() {
  loading.value = true;
  error.value = "";
  try {
    const response = await listOAuthProviders();
    enabled.value = true;
    applyProviders(response.providers ?? []);
  } catch (err) {
    // 没配持久化存储时后端明确回 503。这不是错误状态，是这套部署没启用这个功能，
    // 用红色报错框展示只会让人以为是坏了。
    const message = err instanceof Error ? err.message : String(err);
    if (message.includes("未启用")) {
      enabled.value = false;
    } else {
      error.value = message;
    }
  } finally {
    loading.value = false;
  }
}

async function beginLogin(status: LLMOAuthStatus) {
  error.value = "";
  notice.value = "";
  try {
    const response = await startOAuthLogin(status.provider.key);
    pendingLogin.value = response.login;
    pendingProvider.value = status;
    callbackDraft.value = "";
    // 直接开新标签页，省掉一次复制粘贴；弹窗被拦时下面仍然显示完整地址可以手动打开。
    window.open(response.login.authorize_url, "_blank", "noopener");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function finishLogin() {
  if (!pendingLogin.value || !pendingProvider.value) return;
  completing.value = true;
  error.value = "";
  try {
    const response = await completeOAuthLogin(
      pendingProvider.value.provider.key,
      pendingLogin.value.id,
      callbackDraft.value.trim()
    );
    applyProviders(response.providers ?? []);
    notice.value = `${pendingProvider.value.provider.label} 登录成功`;
    closeLogin(false);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    completing.value = false;
  }
}

function closeLogin(cancelOnServer = true) {
  if (cancelOnServer && pendingLogin.value) {
    // 放弃时把服务端那条待完成授权一起清掉，别把 verifier 留在内存里等过期。
    void cancelOAuthLogin(pendingLogin.value.id).catch(() => undefined);
  }
  pendingLogin.value = null;
  pendingProvider.value = null;
  callbackDraft.value = "";
}

async function logout(status: LLMOAuthStatus) {
  error.value = "";
  try {
    const response = await logoutOAuthProvider(status.provider.key);
    applyProviders(response.providers ?? []);
    notice.value = `已退出 ${status.provider.label}`;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function openEditor(status?: LLMOAuthStatus) {
  editor.value = status ? { ...status.provider } : emptyProvider();
  scopeDraft.value = (status?.provider.scopes ?? []).join(" ");
  editorOpen.value = true;
}

async function saveProvider() {
  editorSaving.value = true;
  error.value = "";
  try {
    const payload: LLMOAuthProvider = {
      ...editor.value,
      scopes: scopeDraft.value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean)
    };
    const response = await saveOAuthProvider(payload);
    applyProviders(response.providers ?? []);
    editorOpen.value = false;
    notice.value = "已保存提供商";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    editorSaving.value = false;
  }
}

async function removeProvider(status: LLMOAuthStatus) {
  error.value = "";
  try {
    const response = await deleteOAuthProvider(status.provider.key);
    applyProviders(response.providers ?? []);
    notice.value = `已删除 ${status.provider.label}`;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function statusText(status: LLMOAuthStatus): string {
  if (!status.logged_in) return "未登录";
  if (status.expired && !status.refreshable) return "登录已过期，需要重新登录";
  if (status.expired) return "令牌已过期，下次调用时自动续期";
  if (!status.expires_at) return "已登录 · 长期有效";
  return `已登录 · ${formatTime(status.expires_at)} 前有效`;
}

function formatTime(value?: string): string {
  if (!value) return "";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString();
}

defineExpose({ refresh });
onMounted(refresh);
</script>

<template>
  <section class="card">
    <div class="card-header">
      <h2>授权登录</h2>
      <div class="header-actions">
        <button class="btn" type="button" :disabled="busy" @click="refresh">
          <RefreshCw :size="14" aria-hidden="true" />
          刷新
        </button>
        <button v-if="enabled" class="btn" type="button" @click="openEditor()">
          <Plus :size="14" aria-hidden="true" />
          自定义提供商
        </button>
      </div>
    </div>
    <div class="card-body stack" style="gap: 14px">
      <p class="hint">
        用授权登录代替 API Key。登录后在配置档的「凭据方式」里选中对应提供商即可，
        令牌过期会在下次调用前自动续期。
      </p>

      <p v-if="!enabled" class="hint muted">
        当前部署没有配置持久化存储，授权登录不可用；配置档仍可使用 API Key。
      </p>
      <p v-if="error" class="hint danger">{{ error }}</p>
      <p v-else-if="notice" class="hint ok">{{ notice }}</p>

      <ul v-if="enabled" class="provider-list">
        <li v-for="status in statuses" :key="status.provider.key" class="provider-row">
          <div class="provider-copy">
            <div class="provider-title">
              <span>{{ status.provider.label }}</span>
              <span class="tag" :class="{ on: status.logged_in && !status.expired }">{{ statusText(status) }}</span>
            </div>
            <span v-if="status.account" class="hint">账号：{{ status.account }}</span>
            <span v-if="status.provider.notes" class="hint">{{ status.provider.notes }}</span>
            <span v-if="!status.provider.built_in" class="hint mono">{{ status.provider.authorize_url }}</span>
          </div>
          <div class="provider-actions">
            <button class="btn" type="button" :disabled="busy" @click="beginLogin(status)">
              <KeyRound :size="14" aria-hidden="true" />
              {{ status.logged_in ? "重新登录" : "登录" }}
            </button>
            <button v-if="status.logged_in" class="btn" type="button" :disabled="busy" @click="logout(status)">
              <LogOut :size="14" aria-hidden="true" />
              退出
            </button>
            <button v-if="!status.provider.built_in" class="btn" type="button" @click="openEditor(status)">编辑</button>
            <button
              v-if="!status.provider.built_in"
              class="btn danger"
              type="button"
              :disabled="busy"
              @click="removeProvider(status)"
            >
              <Trash2 :size="14" aria-hidden="true" />
            </button>
          </div>
        </li>
      </ul>
      <p v-if="enabled && statuses.length === 0 && !loading" class="hint muted">还没有可用的提供商。</p>
    </div>
  </section>

  <!-- 授权进行中：给出地址，等用户把回调粘回来。 -->
  <Modal v-if="pendingLogin" :title="`登录 ${pendingProvider?.provider.label ?? ''}`" @close="closeLogin()">
    <div class="stack" style="gap: 12px">
      <p class="hint">
        已在新标签页打开授权页面。如果被浏览器拦截，可以手动打开下面的地址：
      </p>
      <a class="authorize-link mono" :href="pendingLogin.authorize_url" target="_blank" rel="noopener">
        <ExternalLink :size="14" aria-hidden="true" />
        {{ pendingLogin.authorize_url }}
      </a>
      <div class="field wide">
        <label for="oauth-callback">授权后的回调地址</label>
        <textarea
          id="oauth-callback"
          v-model="callbackDraft"
          class="input mono"
          rows="3"
          placeholder="把浏览器地址栏里那条完整地址粘贴到这里，只粘其中的 code 也可以"
        ></textarea>
        <span class="hint">
          控制台可能和你的浏览器不在同一台机器上，所以回调不会自动回来，需要你手动带回来一次。
        </span>
      </div>
    </div>
    <template #footer>
      <button class="btn" type="button" @click="closeLogin()">取消</button>
      <button class="btn primary" type="button" :disabled="completing || callbackDraft.trim() === ''" @click="finishLogin">
        {{ completing ? "校验中…" : "完成登录" }}
      </button>
    </template>
  </Modal>

  <!-- 自定义提供商。 -->
  <Modal v-if="editorOpen" title="自定义 OAuth 提供商" @close="editorOpen = false">
    <div class="stack" style="gap: 12px">
      <div class="field wide">
        <label for="oauth-key">标识</label>
        <input id="oauth-key" v-model="editor.key" class="input mono" placeholder="my-gateway" autocomplete="off" />
        <span class="hint">保存后不要再改：配置档和已登录的令牌都按它归档。</span>
      </div>
      <div class="field wide">
        <label for="oauth-label">显示名</label>
        <input id="oauth-label" v-model="editor.label" class="input" placeholder="我的网关" autocomplete="off" />
      </div>
      <div class="field wide">
        <label for="oauth-authorize">授权地址</label>
        <input id="oauth-authorize" v-model="editor.authorize_url" class="input mono" placeholder="https://example.com/oauth/authorize" autocomplete="off" />
      </div>
      <div class="field wide">
        <label for="oauth-token">令牌地址</label>
        <input id="oauth-token" v-model="editor.token_url" class="input mono" placeholder="https://example.com/oauth/token" autocomplete="off" />
        <span class="hint">两个地址都必须是 https；本机回环地址（127.0.0.1）可以用 http。</span>
      </div>
      <div class="field wide">
        <label for="oauth-client-id">Client ID</label>
        <input id="oauth-client-id" v-model="editor.client_id" class="input mono" autocomplete="off" />
      </div>
      <div class="field wide">
        <label for="oauth-client-secret">Client Secret（可选）</label>
        <input id="oauth-client-secret" v-model="editor.client_secret" class="input mono" type="password" autocomplete="off" />
        <span class="hint">留空表示公共客户端，走 PKCE。已保存的值这里显示为 ***，原样提交表示不修改。</span>
      </div>
      <div class="field wide">
        <label for="oauth-redirect">回调地址</label>
        <input id="oauth-redirect" v-model="editor.redirect_uri" class="input mono" autocomplete="off" />
        <span class="hint">必须和提供商那边登记的完全一致。授权完成后你从地址栏复制的就是它。</span>
      </div>
      <div class="field wide">
        <label for="oauth-scopes">Scope（可选）</label>
        <input id="oauth-scopes" v-model="scopeDraft" class="input mono" placeholder="多个用空格分隔" autocomplete="off" />
      </div>
    </div>
    <template #footer>
      <button class="btn" type="button" @click="editorOpen = false">取消</button>
      <button class="btn primary" type="button" :disabled="editorSaving" @click="saveProvider">
        {{ editorSaving ? "保存中…" : "保存" }}
      </button>
    </template>
  </Modal>
</template>

<style scoped>
.header-actions {
  display: flex;
  gap: 8px;
}

.provider-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.provider-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: 10px;
}

.provider-copy {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.provider-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}

.provider-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.authorize-link {
  display: flex;
  align-items: center;
  gap: 6px;
  word-break: break-all;
  font-size: 12px;
}

.tag {
  font-size: 12px;
  font-weight: 400;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--border);
  color: var(--muted);
}

.tag.on {
  border-color: var(--ok, var(--accent));
  color: var(--ok, var(--accent));
}

.hint.danger {
  color: var(--danger, #d33);
}

.hint.ok {
  color: var(--ok, var(--accent));
}

.hint.muted {
  color: var(--muted);
}
</style>
