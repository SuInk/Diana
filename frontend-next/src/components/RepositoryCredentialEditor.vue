<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="stack credential-editor" style="gap: 10px">
    <div class="cluster" style="justify-content: space-between">
      <div class="stack" style="gap: 2px">
        <strong style="font-size: 13.5px">凭据列表</strong>
        <span class="hint">默认凭据给所有仓库兜底；个人仓库、组织仓库要用别的账号时，再添加凭据并在「仓库管理」里为仓库选用。</span>
      </div>
      <div class="cluster" style="gap: 6px">
        <button class="btn small" type="button" :disabled="testing" @click="emit('test')">
          <UserCheck :size="14" aria-hidden="true" />
          {{ testing ? "检测中…" : "检测账号" }}
        </button>
        <button class="btn small" type="button" @click="addCredential">
          <Plus :size="14" aria-hidden="true" />
          添加凭据
        </button>
      </div>
    </div>

    <ul class="credential-list">
      <li class="credential-row credential-row-default">
        <div class="credential-row-main">
          <div class="credential-title">
            <strong>默认凭据</strong>
            <span class="badge">默认</span>
          </div>
          <AppSelect
            :model-value="defaultAuth || 'token'"
            :options="defaultAuthOptions"
            aria-label="默认凭据的认证方式"
            @update:model-value="emit('update:default-auth', String($event))"
          />
          <span class="credential-action-spacer" aria-hidden="true"></span>
        </div>
        <div v-if="defaultAuth !== 'gh'" class="credential-row-secret">
          <input
            :value="defaultToken"
            class="input"
            type="password"
            autocomplete="off"
            :disabled="defaultClearing"
            :placeholder="defaultTokenPlaceholder"
            aria-label="默认凭据的 Token"
            @input="emit('update:default-token', ($event.target as HTMLInputElement).value)"
          />
          <button v-if="defaultTokenConfigured" class="btn small ghost" type="button" @click="emit('toggle-clear-default')">
            {{ defaultClearing ? "撤销清除" : "清除" }}
          </button>
        </div>
        <span class="hint">{{ defaultAuthHint }}</span>
        <div v-if="checkOf('default')" class="credential-account">
          <span :class="['badge', checkTone(checkOf('default'))]">{{ checkLabel(checkOf('default')) }}</span>
          <span v-if="checkOf('default')?.message" class="hint">{{ checkOf('default')?.message }}</span>
        </div>
      </li>

      <li v-for="(item, index) in credentials" :key="item.id" class="credential-row">
        <div class="credential-row-main">
          <input
            v-model.trim="item.name"
            class="input"
            type="text"
            placeholder="凭据名称，例如「组织 Token」"
            :aria-label="`第 ${index + 1} 条凭据的名称`"
            @input="emitCredentials"
          />
          <AppSelect
            v-model="item.auth"
            :options="authOptions"
            :aria-label="`第 ${index + 1} 条凭据的认证方式`"
            @update:model-value="emitCredentials"
          />
          <button
            class="btn small ghost danger icon-only"
            type="button"
            :title="`删除凭据 ${item.name || item.id}`"
            :aria-label="`删除凭据 ${item.name || item.id}`"
            @click="removeCredential(index)"
          >
            <Trash2 :size="14" aria-hidden="true" />
          </button>
        </div>
        <div v-if="item.auth !== 'gh'" class="credential-row-secret">
          <input
            v-model="tokenDrafts[item.id]"
            class="input"
            type="password"
            autocomplete="off"
            :placeholder="tokenPlaceholder(item.id)"
            :aria-label="`第 ${index + 1} 条凭据的 Token`"
            @input="emitTokens"
          />
          <span v-if="usageOf(item.id)" class="badge accent credential-usage">{{ usageOf(item.id) }}</span>
        </div>
        <span v-else class="hint">使用服务器上已登录的 GitHub CLI（gh auth login），不需要填 Token。</span>
        <div v-if="checkOf(item.id)" class="credential-account">
          <span :class="['badge', checkTone(checkOf(item.id))]">{{ checkLabel(checkOf(item.id)) }}</span>
          <span v-if="checkOf(item.id)?.message" class="hint">{{ checkOf(item.id)?.message }}</span>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Plus, Trash2, UserCheck } from "@lucide/vue";
import AppSelect from "./AppSelect.vue";
import type { CredentialCheck } from "../api";

interface Credential {
  id: string;
  name: string;
  auth: string;
}

const props = defineProps<{
  credentials?: Credential[];
  configuredIds?: string[];
  repositoryCredentials?: Record<string, string>;
  // 默认凭据就是原来的「公共 Token + 认证方式」，存储不变，只是和其他凭据放进同一张列表。
  defaultAuth?: string;
  defaultToken?: string;
  defaultTokenConfigured?: boolean;
  defaultClearing?: boolean;
  checks?: CredentialCheck[];
  testing?: boolean;
}>();

const emit = defineEmits<{
  "update:credentials": [Credential[]];
  "update:tokens": [Record<string, string>];
  "update:repository-credentials": [Record<string, string>];
  "update:default-auth": [string];
  "update:default-token": [string];
  "toggle-clear-default": [];
  test: [];
}>();

const defaultAuthOptions = [
  { value: "token", label: "Token" },
  { value: "gh", label: "服务器 gh CLI" },
  { value: "auto", label: "自动（有 Token 用 Token，否则 gh）" }
];

const defaultTokenPlaceholder = computed(() => {
  if (props.defaultClearing) return "保存后将清除";
  return props.defaultTokenConfigured ? "已配置 — 留空沿用，填写则覆盖" : "填写 GitHub Token";
});

// 仓库更新检查是后台任务，不调用 gh；选 gh 时要说清它那边的去向。
const defaultAuthHint = computed(() => {
  switch (props.defaultAuth) {
    case "gh":
      return "Issue、PR 操作使用服务器上 gh 登录的账号；仓库更新检查不走 gh，会匿名读取公开仓库。";
    case "auto":
      return "填了 Token 就用 Token，没填时 Issue、PR 操作改用服务器 gh；仓库更新检查只用 Token。";
    default:
      return "仓库更新检查和 Issue、PR 操作都用这个 Token；不填时公开仓库匿名读取，请求额度较低。";
  }
});

const checksByKey = computed(() => new Map((props.checks ?? []).map((check) => [check.key, check])));

function checkOf(key: string): CredentialCheck | undefined {
  return checksByKey.value.get(key);
}

function checkTone(check: CredentialCheck | undefined): string {
  switch (check?.state) {
    case "valid":
      return "ok";
    case "invalid":
      return "err";
    case "unconfigured":
      return "";
    default:
      return "warn";
  }
}

// 账号名是这一行的重点：换了 Token 却不知道实际生效的是哪个号，就靠它一眼看出来。
function checkLabel(check: CredentialCheck | undefined): string {
  switch (check?.state) {
    case "valid":
      return `GitHub 账号 · ${check.account}`;
    case "invalid":
      return "已失效";
    case "unconfigured":
      return "未填写";
    default:
      return "暂时测不了";
  }
}

const authOptions = [
  { value: "token", label: "Token" },
  { value: "gh", label: "服务器 gh CLI" }
];

const credentials = ref<Credential[]>([]);
// Token 是密钥，后端不会回显；这里只收本次输入的新值，留空表示沿用已存的。
const tokenDrafts = ref<Record<string, string>>({});

watch(
  () => props.credentials,
  (value) => {
    credentials.value = (value ?? []).map((item) => ({ ...item }));
  },
  { immediate: true, deep: true }
);

const configured = computed(() => new Set(props.configuredIds ?? []));
// 本次弹窗里被删掉、且服务端存过 Token 的凭据 ID；保存时要连 Token 一起清掉。
const removedIDs = ref<Set<string>>(new Set());

// 统计每条凭据被多少个仓库选用，删除前能看出影响面。
function usageOf(id: string): string {
  const count = Object.values(props.repositoryCredentials ?? {}).filter((value) => value === id).length;
  return count > 0 ? `${count} 个仓库在用` : "";
}

function tokenPlaceholder(id: string): string {
  return configured.value.has(id) ? "已配置 — 留空沿用，填写则覆盖" : "填写 GitHub Token";
}

function newCredentialID(): string {
  // 只用于区分几条凭据，不参与鉴权，够随机即可。
  const random = Math.random().toString(36).slice(2, 8);
  return `cred-${Date.now().toString(36)}-${random}`;
}

function addCredential(): void {
  credentials.value.push({ id: newCredentialID(), name: "", auth: "token" });
  emitCredentials();
}

function removeCredential(index: number): void {
  const [removed] = credentials.value.splice(index, 1);
  if (removed) {
    delete tokenDrafts.value[removed.id];
    // 删掉的凭据要显式提交一次空串，后端才会把它的 Token 从库里删掉。
    // 只从列表里去掉 ID 的话，明文 Token 会一直留在 github_credential_tokens 里。
    if (configured.value.has(removed.id)) removedIDs.value.add(removed.id);
    // 删掉凭据的同时解绑仓库，否则仓库会指向一条不存在的凭据。
    const bindings = { ...(props.repositoryCredentials ?? {}) };
    let changed = false;
    for (const [repository, id] of Object.entries(bindings)) {
      if (id === removed.id) {
        delete bindings[repository];
        changed = true;
      }
    }
    if (changed) emit("update:repository-credentials", bindings);
  }
  emitCredentials();
  emitTokens();
}

function emitCredentials(): void {
  emit("update:credentials", credentials.value.map((item) => ({ ...item })));
}

function emitTokens(): void {
  const tokens: Record<string, string> = {};
  // 空串是「删除这条凭据的 Token」，非空是「覆盖」，没出现的键沿用已存值。
  for (const id of removedIDs.value) tokens[id] = "";
  for (const [id, value] of Object.entries(tokenDrafts.value)) {
    const token = value.trim();
    if (token) tokens[id] = token;
  }
  emit("update:tokens", tokens);
}

defineExpose({
  clearDrafts(): void {
    tokenDrafts.value = {};
    removedIDs.value = new Set();
  }
});
</script>

<style scoped>
.credential-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.credential-row {
  display: grid;
  gap: 6px;
  padding: 10px 11px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-2);
}

.credential-row-main {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(140px, 200px) auto;
  align-items: center;
  gap: 8px;
}

.credential-row-secret {
  display: flex;
  align-items: center;
  gap: 8px;
}

.credential-row-secret .input {
  flex: 1;
  min-width: 0;
}

.credential-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.credential-action-spacer {
  width: 30px;
}

.credential-account {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
}

.credential-usage {
  flex: 0 0 auto;
}

@media (max-width: 640px) {
  .credential-row-main {
    grid-template-columns: minmax(0, 1fr) auto;
  }
}
</style>
