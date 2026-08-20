<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="stack credential-editor" style="gap: 10px">
    <div class="cluster" style="justify-content: space-between">
      <div class="stack" style="gap: 2px">
        <strong style="font-size: 13.5px">凭据列表</strong>
        <span class="hint">个人仓库、组织仓库可以各用一份凭据；在「仓库管理」里为每个仓库选用。</span>
      </div>
      <button class="btn small" type="button" @click="addCredential">
        <Plus :size="14" aria-hidden="true" />
        添加凭据
      </button>
    </div>

    <p v-if="!credentials.length" class="hint credential-empty">
      还没有凭据。不添加也可以——所有仓库会使用下方的公共 Token。
    </p>

    <ul v-else class="credential-list">
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
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Plus, Trash2 } from "@lucide/vue";
import AppSelect from "./AppSelect.vue";

interface Credential {
  id: string;
  name: string;
  auth: string;
}

const props = defineProps<{
  credentials?: Credential[];
  configuredIds?: string[];
  repositoryCredentials?: Record<string, string>;
}>();

const emit = defineEmits<{
  "update:credentials": [Credential[]];
  "update:tokens": [Record<string, string>];
  "update:repository-credentials": [Record<string, string>];
}>();

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
  for (const [id, value] of Object.entries(tokenDrafts.value)) {
    const token = value.trim();
    if (token) tokens[id] = token;
  }
  emit("update:tokens", tokens);
}

defineExpose({
  clearDrafts(): void {
    tokenDrafts.value = {};
  }
});
</script>

<style scoped>
.credential-empty {
  margin: 0;
  padding: 10px 12px;
  border: 1px dashed var(--border);
  border-radius: 8px;
}

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

.credential-usage {
  flex: 0 0 auto;
}

@media (max-width: 640px) {
  .credential-row-main {
    grid-template-columns: minmax(0, 1fr) auto;
  }
}
</style>
