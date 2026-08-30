<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
// 授权登录选择器，内嵌在「新建配置」弹窗里。
//
// 一开始它是页面底部一张独立的卡片，结果是：想用授权登录，得先关掉新建弹窗、
// 滚到页面最下面登录一次、再回来重新新建。选凭据和拿到凭据是同一件事的两步，
// 拆到两个地方就等于让人自己记着在哪儿。
//
// 所以这里全程内嵌，不开第二层弹窗：登录、粘回调、加自定义提供商都在同一张表单里
// 就地展开。弹窗套弹窗在这种「顺手做一下」的动作上尤其糟——关掉里层那个的时候，
// 人很容易连外层一起关掉，填了一半的配置就没了。
import { computed, onMounted, ref } from "vue";
import { ExternalLink, KeyRound, LogOut, Plus, RefreshCw, Trash2 } from "@lucide/vue";

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

const selected = defineModel<string>({ default: "" });
const emit = defineEmits<{ (event: "changed", providers: LLMOAuthStatus[]): void }>();

const statuses = ref<LLMOAuthStatus[]>([]);
const loading = ref(false);
const enabled = ref(true);
const error = ref("");

// 登录进行中：授权地址已经给出，等用户把回调粘回来。
const pendingLogin = ref<LLMOAuthPendingLogin | null>(null);
const pendingKey = ref("");
const callbackDraft = ref("");
const completing = ref(false);

// 自定义提供商就地展开，不另开弹窗。
const editorOpen = ref(false);
const editorSaving = ref(false);
const editor = ref<LLMOAuthProvider>(emptyProvider());
const scopeDraft = ref("");

function emptyProvider(): LLMOAuthProvider {
  return { key: "", label: "", authorize_url: "", token_url: "", client_id: "", client_secret: "", redirect_uri: "", scopes: [] };
}

const busy = computed(() => loading.value || completing.value || editorSaving.value);

function apply(next: LLMOAuthStatus[]) {
  statuses.value = next;
  emit("changed", next);
  // 绑着的提供商被退出或删掉后，把选择一起清掉——留着会让这份配置一直以
  // 「还没有登录」失败，而界面上看不出哪里不对。
  if (selected.value && !next.some((item) => item.provider.key === selected.value && item.logged_in)) {
    selected.value = "";
  }
}

async function refresh() {
  loading.value = true;
  error.value = "";
  try {
    const response = await listOAuthProviders();
    enabled.value = true;
    apply(response.providers ?? []);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    // 没配持久化存储时后端明确回 503。那不是错误，是这套部署没启用这个功能。
    if (message.includes("未启用")) enabled.value = false;
    else error.value = message;
  } finally {
    loading.value = false;
  }
}

async function beginLogin(status: LLMOAuthStatus) {
  error.value = "";
  try {
    const response = await startOAuthLogin(status.provider.key);
    pendingLogin.value = response.login;
    pendingKey.value = status.provider.key;
    callbackDraft.value = "";
    window.open(response.login.authorize_url, "_blank", "noopener");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function finishLogin() {
  if (!pendingLogin.value) return;
  completing.value = true;
  error.value = "";
  try {
    const response = await completeOAuthLogin(pendingKey.value, pendingLogin.value.id, callbackDraft.value.trim());
    apply(response.providers ?? []);
    // 登录成功就直接选中它：来这儿登录本来就是为了用它。
    selected.value = pendingKey.value;
    closeLogin(false);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    completing.value = false;
  }
}

function closeLogin(cancelOnServer = true) {
  if (cancelOnServer && pendingLogin.value) {
    void cancelOAuthLogin(pendingLogin.value.id).catch(() => undefined);
  }
  pendingLogin.value = null;
  pendingKey.value = "";
  callbackDraft.value = "";
}

async function logout(status: LLMOAuthStatus) {
  error.value = "";
  try {
    apply((await logoutOAuthProvider(status.provider.key)).providers ?? []);
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
    const response = await saveOAuthProvider({
      ...editor.value,
      scopes: scopeDraft.value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean)
    });
    apply(response.providers ?? []);
    editorOpen.value = false;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    editorSaving.value = false;
  }
}

async function removeProvider(status: LLMOAuthStatus) {
  error.value = "";
  try {
    apply((await deleteOAuthProvider(status.provider.key)).providers ?? []);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function statusText(status: LLMOAuthStatus): string {
  if (!status.logged_in) return "未登录";
  if (status.expired && !status.refreshable) return "登录已过期";
  if (status.expired) return "已登录 · 下次调用前自动续期";
  if (!status.expires_at) return "已登录 · 长期有效";
  return `已登录 · ${new Date(status.expires_at).toLocaleString()} 前有效`;
}

defineExpose({ refresh });
onMounted(refresh);
</script>

<template>
  <div class="stack" style="gap: 10px">
    <p v-if="!enabled" class="hint">当前部署没有配置持久化存储，授权登录不可用；可以改用 API Key。</p>
    <p v-if="error" class="hint danger">{{ error }}</p>

    <template v-if="enabled">
      <ul class="oauth-list">
        <li
          v-for="status in statuses"
          :key="status.provider.key"
          class="oauth-row"
          :class="{ picked: selected === status.provider.key }"
        >
          <label class="oauth-pick">
            <input
              type="radio"
              :value="status.provider.key"
              :checked="selected === status.provider.key"
              :disabled="!status.logged_in"
              @change="selected = status.provider.key"
            />
            <span class="oauth-copy">
              <span class="oauth-title">
                {{ status.provider.label }}
                <span class="tag" :class="{ on: status.logged_in && !status.expired }">{{ statusText(status) }}</span>
              </span>
              <span v-if="status.provider.notes" class="hint">{{ status.provider.notes }}</span>
            </span>
          </label>
          <div class="oauth-actions">
            <button class="btn small" type="button" :disabled="busy" @click="beginLogin(status)">
              <KeyRound :size="13" aria-hidden="true" />
              {{ status.logged_in ? "重新登录" : "登录" }}
            </button>
            <button v-if="status.logged_in" class="btn small ghost" type="button" :disabled="busy" @click="logout(status)">
              <LogOut :size="13" aria-hidden="true" />
            </button>
            <button v-if="!status.provider.built_in" class="btn small ghost" type="button" @click="openEditor(status)">编辑</button>
            <button
              v-if="!status.provider.built_in"
              class="btn small ghost danger"
              type="button"
              :disabled="busy"
              @click="removeProvider(status)"
            >
              <Trash2 :size="13" aria-hidden="true" />
            </button>
          </div>
        </li>
      </ul>

      <!-- 登录就地展开：控制台和浏览器常常不在同一台机器上，回调回不来，
           所以要用户把地址栏那条粘回来。 -->
      <div v-if="pendingLogin" class="oauth-callback stack" style="gap: 8px">
        <a class="oauth-authorize mono" :href="pendingLogin.authorize_url" target="_blank" rel="noopener">
          <ExternalLink :size="13" aria-hidden="true" />
          已在新标签打开授权页；被拦截的话点这里
        </a>
        <textarea
          v-model="callbackDraft"
          class="input mono"
          rows="2"
          placeholder="授权完成后，把浏览器地址栏那条完整地址粘到这里（只粘其中的 code 也可以）"
        ></textarea>
        <div class="cluster" style="gap: 8px">
          <button class="btn small primary" type="button" :disabled="completing || callbackDraft.trim() === ''" @click="finishLogin">
            {{ completing ? "校验中…" : "完成登录" }}
          </button>
          <button class="btn small ghost" type="button" @click="closeLogin()">取消</button>
        </div>
      </div>

      <!-- 自定义提供商同样就地展开。 -->
      <div v-if="editorOpen" class="oauth-editor stack" style="gap: 10px">
        <div class="field">
          <label for="oauth-key">标识</label>
          <input id="oauth-key" v-model="editor.key" class="input mono" placeholder="my-gateway" autocomplete="off" />
          <span class="hint">保存后不要再改：配置档和已登录的令牌都按它归档。</span>
        </div>
        <div class="field">
          <label for="oauth-label">显示名</label>
          <input id="oauth-label" v-model="editor.label" class="input" placeholder="我的网关" autocomplete="off" />
        </div>
        <div class="field">
          <label for="oauth-authorize">授权地址</label>
          <input id="oauth-authorize" v-model="editor.authorize_url" class="input mono" placeholder="https://example.com/oauth/authorize" autocomplete="off" />
        </div>
        <div class="field">
          <label for="oauth-token">令牌地址</label>
          <input id="oauth-token" v-model="editor.token_url" class="input mono" placeholder="https://example.com/oauth/token" autocomplete="off" />
          <span class="hint">两个地址都必须是 https；本机回环地址（127.0.0.1）可以用 http。</span>
        </div>
        <div class="field">
          <label for="oauth-client-id">Client ID</label>
          <input id="oauth-client-id" v-model="editor.client_id" class="input mono" autocomplete="off" />
        </div>
        <div class="field">
          <label for="oauth-client-secret">Client Secret（可选）</label>
          <input id="oauth-client-secret" v-model="editor.client_secret" class="input mono" type="password" autocomplete="off" />
          <span class="hint">留空表示公共客户端，走 PKCE。已保存的值这里显示为 ***，原样提交表示不修改。</span>
        </div>
        <div class="field">
          <label for="oauth-redirect">回调地址</label>
          <input id="oauth-redirect" v-model="editor.redirect_uri" class="input mono" autocomplete="off" />
          <span class="hint">必须和提供商那边登记的完全一致。授权完成后你从地址栏复制的就是它。</span>
        </div>
        <div class="field">
          <label for="oauth-scopes">Scope（可选）</label>
          <input id="oauth-scopes" v-model="scopeDraft" class="input mono" placeholder="多个用空格分隔" autocomplete="off" />
        </div>
        <div class="cluster" style="gap: 8px">
          <button class="btn small primary" type="button" :disabled="editorSaving" @click="saveProvider">
            {{ editorSaving ? "保存中…" : "保存提供商" }}
          </button>
          <button class="btn small ghost" type="button" @click="editorOpen = false">取消</button>
        </div>
      </div>

      <div v-if="!editorOpen" class="cluster" style="gap: 8px">
        <button class="btn small ghost" type="button" @click="openEditor()">
          <Plus :size="13" aria-hidden="true" />
          自定义提供商
        </button>
        <button class="btn small ghost" type="button" :disabled="busy" @click="refresh">
          <RefreshCw :size="13" aria-hidden="true" />
          刷新
        </button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.oauth-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.oauth-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
}

.oauth-row.picked {
  border-color: var(--accent);
  background: var(--accent-soft);
}

.oauth-pick {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
  cursor: pointer;
}

.oauth-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.oauth-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}

.oauth-actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.oauth-callback,
.oauth-editor {
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface-muted, transparent);
}

.oauth-authorize {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}

.tag {
  font-size: 11.5px;
  font-weight: 400;
  padding: 1px 7px;
  border-radius: 999px;
  border: 1px solid var(--border);
  color: var(--muted);
}

.tag.on {
  border-color: var(--ok, var(--accent));
  color: var(--ok, var(--accent));
}

.hint.danger {
  color: var(--err);
}
</style>
