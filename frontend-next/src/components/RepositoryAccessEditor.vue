<template>
  <section class="repository-access-editor">
    <div class="repository-access-heading">
      <div>
        <h3>仓库访问范围</h3>
        <p>所有个人与群聊授权都限制在这里列出的仓库内。</p>
      </div>
      <span class="badge accent">{{ repositories.length }} 个仓库</span>
    </div>

    <form class="repository-access-add" @submit.prevent="addRepository">
      <input
        v-model.trim="repositoryDraft"
        class="input mono"
        type="text"
        autocomplete="off"
        placeholder="owner/repo"
        aria-label="新增允许写入的仓库"
      />
      <button class="btn small" type="submit" title="添加仓库" aria-label="添加仓库">
        <Plus :size="14" aria-hidden="true" />
        添加
      </button>
    </form>
    <p v-if="repositoryError" class="repository-access-error">{{ repositoryError }}</p>
    <div v-if="repositories.length" class="repository-access-repositories">
      <span v-for="repository in repositories" :key="repository" class="repository-access-chip">
        <span class="mono">{{ repository }}</span>
        <button type="button" :title="`移除 ${repository}`" :aria-label="`移除 ${repository}`" @click="removeRepository(repository)">
          <X :size="12" aria-hidden="true" />
        </button>
      </span>
    </div>
    <p v-else class="repository-access-empty">尚未允许任何仓库</p>

    <div class="repository-access-divider"></div>

    <div class="segmented repository-access-tabs" role="tablist" aria-label="授权类型">
      <button type="button" role="tab" :aria-selected="mode === 'users'" :class="{ active: mode === 'users' }" @click="mode = 'users'">
        <UserRound :size="14" aria-hidden="true" />
        用户授权
        <span v-if="userRules.length" class="badge">{{ userRules.length }}</span>
      </button>
      <button type="button" role="tab" :aria-selected="mode === 'groups'" :class="{ active: mode === 'groups' }" @click="mode = 'groups'">
        <MessagesSquare :size="14" aria-hidden="true" />
        群聊授权
        <span v-if="groupRules.length" class="badge">{{ groupRules.length }}</span>
      </button>
    </div>

    <div v-if="mode === 'users'" class="repository-access-rules" role="tabpanel">
      <RepositoryAccessRuleRow
        v-for="(rule, index) in userRules"
        :key="`user-${index}`"
        kind="user"
        :rule="rule"
        :repositories="repositories"
        @update="updateRule('users', index, $event)"
        @remove="removeRule('users', index)"
      />
      <button class="btn small ghost repository-access-add-rule" type="button" @click="addRule('users')">
        <Plus :size="14" aria-hidden="true" />
        添加用户
      </button>
    </div>

    <div v-else class="repository-access-rules" role="tabpanel">
      <RepositoryAccessRuleRow
        v-for="(rule, index) in groupRules"
        :key="`group-${index}`"
        kind="group"
        :rule="rule"
        :repositories="repositories"
        @update="updateRule('groups', index, $event)"
        @remove="removeRule('groups', index)"
      />
      <button class="btn small ghost repository-access-add-rule" type="button" @click="addRule('groups')">
        <Plus :size="14" aria-hidden="true" />
        添加群聊
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { MessagesSquare, Plus, UserRound, X } from "@lucide/vue";
import RepositoryAccessRuleRow, { type RepositoryAccessRule } from "./RepositoryAccessRuleRow.vue";

type AccessMode = "users" | "groups";
type AccessRule = RepositoryAccessRule;

const props = defineProps<{
  allowedRepositories: string;
  userAccess: string;
  groupAccess: string;
}>();
const emit = defineEmits<{
  "update:allowedRepositories": [string];
  "update:userAccess": [string];
  "update:groupAccess": [string];
}>();

const mode = ref<AccessMode>("users");
const repositoryDraft = ref("");
const repositoryError = ref("");
const repositories = ref(parseRepositories(props.allowedRepositories));
const userRules = ref(parseRules(props.userAccess));
const groupRules = ref(parseRules(props.groupAccess));

watch(() => props.allowedRepositories, (value) => {
  if (value !== serializeRepositories(repositories.value)) repositories.value = parseRepositories(value);
});
watch(() => props.userAccess, (value) => {
  if (value !== serializeRules(userRules.value)) userRules.value = parseRules(value);
});
watch(() => props.groupAccess, (value) => {
  if (value !== serializeRules(groupRules.value)) groupRules.value = parseRules(value);
});

function parseRepositories(value: string): string[] {
  return [...new Set(String(value ?? "").split(/[,;；\n\r]/).map((item) => item.trim()).filter(Boolean))];
}

function parseRules(value: string): AccessRule[] {
  return String(value ?? "")
    .split(/[;；\n\r]/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const separator = line.indexOf("=");
      const id = (separator >= 0 ? line.slice(0, separator) : line).trim();
      const assigned = separator >= 0 ? line.slice(separator + 1) : "";
      return { id, repositories: parseRepositories(assigned) };
    });
}

function serializeRepositories(value: string[]): string {
  return value.join("\n");
}

function serializeRules(value: AccessRule[]): string {
  return value.map((rule) => `${rule.id.trim()} = ${rule.repositories.join(", ")}`).join("\n");
}

function emitRepositories(): void {
  emit("update:allowedRepositories", serializeRepositories(repositories.value));
}

function emitRules(kind: AccessMode): void {
  const rules = kind === "users" ? userRules.value : groupRules.value;
  if (kind === "users") emit("update:userAccess", serializeRules(rules));
  else emit("update:groupAccess", serializeRules(rules));
}

function addRepository(): void {
  const value = repositoryDraft.value.trim().replace(/\.git$/i, "");
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+(?:\.git)?$/.test(value)) {
    repositoryError.value = "请填写精确的 owner/repo";
    return;
  }
  repositoryError.value = "";
  if (!repositories.value.some((item) => item.toLowerCase() === value.toLowerCase())) {
    repositories.value.push(value);
    emitRepositories();
  }
  repositoryDraft.value = "";
}

function removeRepository(repository: string): void {
  repositories.value = repositories.value.filter((item) => item !== repository);
  for (const rules of [userRules.value, groupRules.value]) {
    for (const rule of rules) rule.repositories = rule.repositories.filter((item) => item !== repository);
  }
  emitRepositories();
  emitRules("users");
  emitRules("groups");
}

function addRule(kind: AccessMode): void {
  const target = kind === "users" ? userRules.value : groupRules.value;
  target.push({ id: "", repositories: [] });
  emitRules(kind);
}

function updateRule(kind: AccessMode, index: number, rule: AccessRule): void {
  const target = kind === "users" ? userRules.value : groupRules.value;
  target[index] = rule;
  emitRules(kind);
}

function removeRule(kind: AccessMode, index: number): void {
  const target = kind === "users" ? userRules.value : groupRules.value;
  target.splice(index, 1);
  emitRules(kind);
}

</script>

<style scoped>
.repository-access-editor {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 18px;
}

.repository-access-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.repository-access-heading h3 {
  margin: 0;
  font-size: 14px;
}

.repository-access-heading p,
.repository-access-empty {
  margin: 4px 0 0;
  color: var(--muted);
  font-size: 12px;
}

.repository-access-add {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
}

.repository-access-repositories {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
}

.repository-access-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 30px;
  padding: 5px 8px;
  border: 1px solid var(--border);
  background: var(--surface-2);
  font-size: 12px;
}

.repository-access-chip button {
  display: inline-grid;
  place-items: center;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
}

.repository-access-divider {
  height: 1px;
  margin: 4px 0;
  background: var(--border);
}

.repository-access-tabs {
  align-self: flex-start;
}

.repository-access-tabs button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.repository-access-rules {
  display: flex;
  flex-direction: column;
}

.repository-access-add-rule {
  align-self: flex-start;
  margin-top: 10px;
}

.repository-access-error {
  margin: -5px 0 0;
  color: var(--err);
  font-size: 12px;
}

</style>
