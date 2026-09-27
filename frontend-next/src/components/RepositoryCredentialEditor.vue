<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="stack credential-editor" style="gap: 8px">
    <div class="credential-toolbar">
      <span class="hint">{{ credentials.length + 1 }} 条凭据 · 没单独选凭据的仓库用默认凭据</span>
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
      <li class="credential-item" :class="{ open: expanded.has('default') }">
        <div class="credential-summary">
          <button class="credential-summary-main" type="button" :aria-expanded="expanded.has('default')" @click="toggle('default')">
            <ChevronRight :size="14" class="credential-chevron" aria-hidden="true" />
            <span class="credential-name">默认凭据</span>
            <span class="badge">默认</span>
            <span class="credential-meta">{{ defaultMeta }}</span>
          </button>
          <span v-if="checkOf('default')" :class="['badge', 'credential-account', checkTone(checkOf('default'))]" :title="checkOf('default')?.message">{{ checkLabel(checkOf('default')) }}</span>
          <span class="credential-action-spacer" aria-hidden="true"></span>
        </div>
        <div v-if="expanded.has('default')" class="credential-body">
          <div class="credential-fields">
            <AppSelect
              :model-value="defaultAuth || 'token'"
              :options="defaultAuthOptions"
              aria-label="默认凭据的认证方式"
              @update:model-value="emit('update:default-auth', String($event))"
            />
            <input
              v-if="defaultAuth !== 'gh'"
              :value="defaultToken"
              class="input"
              type="password"
              autocomplete="off"
              :disabled="defaultClearing"
              :placeholder="defaultTokenPlaceholder"
              aria-label="默认凭据的 Token"
              @input="emit('update:default-token', ($event.target as HTMLInputElement).value)"
            />
            <button v-if="defaultAuth !== 'gh' && defaultTokenConfigured" class="btn small ghost" type="button" @click="emit('toggle-clear-default')">
              {{ defaultClearing ? "撤销清除" : "清除" }}
            </button>
          </div>
          <span class="hint">{{ defaultAuthHint }}</span>
          <span v-if="checkOf('default')?.message" class="hint">{{ checkOf('default')?.message }}</span>
        </div>
      </li>

      <li v-for="(item, index) in credentials" :key="item.id" class="credential-item" :class="{ open: expanded.has(item.id) }">
        <div class="credential-summary">
          <button class="credential-summary-main" type="button" :aria-expanded="expanded.has(item.id)" @click="toggle(item.id)">
            <ChevronRight :size="14" class="credential-chevron" aria-hidden="true" />
            <span class="credential-name" :class="{ placeholder: !item.name }">{{ item.name || "未命名凭据" }}</span>
            <span class="credential-meta">{{ credentialMeta(item) }}</span>
          </button>
          <span v-if="checkOf(item.id)" :class="['badge', 'credential-account', checkTone(checkOf(item.id))]" :title="checkOf(item.id)?.message">{{ checkLabel(checkOf(item.id)) }}</span>
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
        <div v-if="expanded.has(item.id)" class="credential-body">
          <div class="credential-fields">
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
          </div>
          <input
            v-if="item.auth !== 'gh'"
            v-model="tokenDrafts[item.id]"
            class="input"
            type="password"
            autocomplete="off"
            :placeholder="tokenPlaceholder(item.id)"
            :aria-label="`第 ${index + 1} 条凭据的 Token`"
            @input="emitTokens"
          />
          <span v-else class="hint">使用服务器上已登录的 GitHub CLI（gh auth login），不需要填 Token。</span>
          <span v-if="checkOf(item.id)?.message" class="hint">{{ checkOf(item.id)?.message }}</span>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ChevronRight, Plus, Trash2, UserCheck } from "@lucide/vue";
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

// 列表默认只显示摘要，点开一行才编辑；新加的凭据直接展开。
const expanded = ref<Set<string>>(new Set());

function toggle(id: string): void {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
}

const defaultMeta = computed(() => {
  if (props.defaultAuth === "gh") return "服务器 gh CLI";
  const token = props.defaultClearing ? "保存后清除 Token" : props.defaultToken?.trim() ? "Token 待保存" : props.defaultTokenConfigured ? "Token 已配置" : "未填 Token";
  return props.defaultAuth === "auto" ? `自动 · ${token}` : token;
});

function credentialMeta(item: Credential): string {
  const parts = [item.auth === "gh" ? "服务器 gh CLI" : tokenDrafts.value[item.id]?.trim() ? "Token 待保存" : configured.value.has(item.id) ? "Token 已配置" : "未填 Token"];
  const usage = usageOf(item.id);
  if (usage) parts.push(usage);
  return parts.join(" · ");
}

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
      return `@${check.account}`;
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
  const id = newCredentialID();
  credentials.value.push({ id, name: "", auth: "token" });
  expanded.value = new Set(expanded.value).add(id);
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
.credential-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.credential-list {
  margin: 0;
  padding: 0;
  list-style: none;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
}

.credential-item + .credential-item {
  border-top: 1px solid var(--border);
}

.credential-item.open {
  background: var(--surface-2);
}

.credential-summary {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 44px;
  padding: 4px 8px 4px 0;
}

.credential-summary-main {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 8px 0 8px 12px;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.credential-chevron {
  flex: 0 0 auto;
  color: var(--muted);
  transition: transform 0.15s ease;
}

.credential-item.open .credential-chevron {
  transform: rotate(90deg);
}

.credential-name {
  flex: 0 1 auto;
  overflow: hidden;
  font-weight: 600;
  font-size: 13.5px;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.credential-name.placeholder {
  color: var(--muted);
  font-weight: 500;
}

.credential-meta {
  flex: 1 1 auto;
  overflow: hidden;
  color: var(--muted);
  font-size: 12.5px;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.credential-account {
  flex: 0 1 auto;
  max-width: 40%;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.credential-action-spacer {
  flex: 0 0 30px;
}

.credential-body {
  display: grid;
  gap: 8px;
  padding: 0 12px 12px 34px;
}

.credential-fields {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 2fr) auto;
  align-items: center;
  gap: 8px;
}

.credential-fields:has(> :nth-child(2):last-child) {
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
}

.credential-fields:has(> :only-child) {
  grid-template-columns: minmax(0, 1fr);
}

@media (max-width: 640px) {
  .credential-meta {
    display: none;
  }

  .credential-body {
    padding-left: 12px;
  }

  .credential-fields,
  .credential-fields:has(> :nth-child(2):last-child) {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
