<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="issue-access-editor" :class="{ scoped: isScoped }">
    <header class="section-heading">
      <div>
        <h3>Issue 管理人员（私聊）</h3>
        <p>{{ isScoped ? `仅配置 ${repositoryKeyValue} 的管理人员；可直接创建和管理 Issue。` : "这些私聊用户可以直接创建和管理 Issue。" }}</p>
      </div>
      <span class="badge accent">{{ users.length }} 个用户</span>
    </header>

    <div class="rule-list">
      <article v-for="(user, index) in users" :key="`user-${index}`" class="access-rule">
        <div class="rule-header">
          <div class="identity">
            <img v-if="qqAvatarURL(user.id)" class="avatar" :src="qqAvatarURL(user.id)" alt="" />
            <strong>{{ user.id || "新授权用户" }}</strong>
          </div>
          <button class="btn small ghost danger icon-only" type="button" title="删除用户" aria-label="删除用户" @click="removeUser(index)">
            <Trash2 :size="15" aria-hidden="true" />
          </button>
        </div>
        <div class="user-fields">
          <label>
            <span>用户 ID</span>
            <input class="input mono" type="text" inputmode="numeric" :value="user.id" placeholder="QQ 用户 ID" @input="updateUserID(index, $event)" />
          </label>
          <label>
            <span>认证来源</span>
            <AppSelect v-model="user.authMode" :options="authModeOptions" @update:model-value="emitAll" />
          </label>
          <label v-if="user.authMode === 'token'" class="token-field">
            <span>独立 GitHub Token</span>
            <div class="token-input">
              <input class="input mono" type="password" autocomplete="new-password" :placeholder="user.tokenConfigured ? '已配置，留空则保持不变' : 'github_pat_…'" @input="updateToken(index, $event)" />
              <span class="token-state" :class="{ configured: user.tokenConfigured }">{{ user.tokenConfigured ? "已配置" : "未配置" }}</span>
              <button v-if="user.tokenConfigured" class="btn small ghost" type="button" @click="clearToken(index)">清除</button>
            </div>
          </label>
        </div>
        <RepositoryListEditor v-if="!isScoped" :repositories="user.repositories" placeholder="owner/repo 或 GitHub 仓库链接" @update="updateUserRepositories(index, $event)" />
      </article>
    </div>
    <button class="btn small ghost add-rule" type="button" @click="addUser">
      <UserPlus :size="15" aria-hidden="true" />
      添加私聊管理员
    </button>

    <div class="section-divider"></div>

    <header class="section-heading"><div><h3>Issue 管理人员（群聊）</h3><p>该群成员可以直接创建和管理 Issue，请只添加可信群聊。</p></div><span class="badge">{{ managerGroups.length }} 个群聊</span></header>
    <div class="rule-list"><article v-for="(group, index) in managerGroups" :key="`manager-group-${index}`" class="access-rule group-rule"><div class="rule-header"><div class="identity"><img v-if="groupSummary(group.id)?.avatar_url" class="avatar" :src="groupSummary(group.id)?.avatar_url" alt="" /><div><strong>{{ groupSummary(group.id)?.group_name || group.id || "新群聊" }}</strong><small class="mono">{{ group.id }}</small></div></div><button class="btn small ghost danger icon-only" type="button" title="删除群聊" aria-label="删除群聊" @click="removeManagerGroup(index)"><Trash2 :size="15" aria-hidden="true" /></button></div><input v-model.trim="group.id" class="input mono" type="text" placeholder="群号或 Chat ID" @input="emitAll" /><RepositoryListEditor v-if="!isScoped" :repositories="group.repositories" placeholder="owner/repo" @update="updateManagerGroupRepositories(index, $event)" /></article></div>
    <button class="btn small ghost add-rule" type="button" @click="addManagerGroup"><MessageSquarePlus :size="15" aria-hidden="true" />添加群聊管理员</button>

    <div class="section-divider"></div>
    <header class="section-heading"><div><h3>Issue 草稿提交者（私聊）</h3><p>这些私聊用户可以提交 Issue 草稿，但不能直接写入 GitHub。</p></div><span class="badge">{{ draftUsers.length }} 个用户</span></header>
    <div class="rule-list"><article v-for="(user, index) in draftUsers" :key="`draft-user-${index}`" class="access-rule"><div class="rule-header"><strong>{{ user.id || "新草稿提交者" }}</strong><button class="btn small ghost danger icon-only" type="button" title="删除用户" aria-label="删除用户" @click="removeDraftUser(index)"><Trash2 :size="15" aria-hidden="true" /></button></div><input v-model.trim="user.id" class="input mono" type="text" placeholder="QQ 用户 ID 或 Chat ID" @input="emitAll" /><RepositoryListEditor v-if="!isScoped" :repositories="user.repositories" placeholder="owner/repo" @update="updateDraftUserRepositories(index, $event)" /></article></div>
    <button class="btn small ghost add-rule" type="button" @click="addDraftUser"><UserPlus :size="15" aria-hidden="true" />添加私聊提交者</button>

    <div class="section-divider"></div>

    <header class="section-heading">
      <div>
        <h3>Issue 草稿提交者（群聊）</h3>
        <p>指定群聊内的成员都能提交需求并生成草稿，但不能直接写入 GitHub；仍需指定用户确认。</p>
      </div>
      <span class="badge">{{ groups.length }} 个群聊</span>
    </header>
    <div class="approval-flow">
      <span>成员提出需求</span><ArrowRight :size="14" /><span>机器人复述草稿</span><ArrowRight :size="14" /><span>指定用户确认</span><ArrowRight :size="14" /><span>创建 Issue</span>
    </div>
    <div class="rule-list">
      <article v-for="(group, index) in groups" :key="`group-${index}`" class="access-rule group-rule">
        <div class="rule-header">
          <div class="identity">
            <img v-if="groupSummary(group.id)?.avatar_url" class="avatar" :src="groupSummary(group.id)?.avatar_url" alt="" />
            <div><strong>{{ groupSummary(group.id)?.group_name || group.id }}</strong><small class="mono">{{ group.id }}</small></div>
          </div>
          <button class="btn small ghost danger icon-only" type="button" title="删除群聊" aria-label="删除群聊" @click="removeGroup(index)">
            <Trash2 :size="15" aria-hidden="true" />
          </button>
        </div>
        <RepositoryListEditor v-if="!isScoped" :repositories="group.repositories" placeholder="owner/repo 或 GitHub 仓库链接" @update="updateGroupRepositories(index, $event)" />
      </article>
    </div>
    <button class="btn small ghost add-rule" type="button" @click="showGroupPicker = !showGroupPicker">
      <MessageSquarePlus :size="15" aria-hidden="true" />
      添加指定群聊
    </button>
    <div v-if="showGroupPicker" class="group-picker">
      <input v-model="groupQuery" class="input" type="search" placeholder="搜索已加入的群名称或群号" />
      <p v-if="groupsLoading" class="picker-state">正在读取群列表…</p>
      <p v-else-if="groupsWarning" class="picker-state">{{ groupsWarning }}</p>
      <button v-for="group in availableGroups" :key="group.group_id" class="group-option" type="button" @click="addGroup(group.group_id)">
        <img v-if="group.avatar_url" class="avatar" :src="group.avatar_url" alt="" />
        <span><strong>{{ group.group_name || group.group_id }}</strong><small class="mono">{{ group.group_id }}</small></span>
      </button>
      <p v-if="!groupsLoading && !availableGroups.length" class="picker-state">没有匹配的可选群聊</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, ref, watch } from "vue";
import { ArrowRight, MessageSquarePlus, Trash2, UserPlus, X } from "@lucide/vue";
import type { QQBotGroupSummary } from "../api";
import AppSelect, { type AppSelectOption } from "./AppSelect.vue";

type AccessRule = { id: string; repositories: string[] };
type UserRule = AccessRule & { tokenConfigured: boolean; authMode: "inherit" | "gh" | "token" };

const props = defineProps<{ userAccess: string; groupAccess: string; draftUserAccess?: string; draftGroupAccess?: string; managerUserAccess?: string; managerGroupAccess?: string; tokenUsers: string; userAuthModes: string; repository?: string; joinedGroups: QQBotGroupSummary[]; groupsLoading?: boolean; groupsWarning?: string }>();
const emit = defineEmits<{
  "update:userAccess": [string];
  "update:groupAccess": [string];
  "update:userTokens": [string];
  "update:tokenUsers": [string];
  "update:userAuthModes": [string];
  "update:allowedRepositories": [string];
  "update:draft-user-access": [string];
  "update:draft-group-access": [string];
  "update:manager-user-access": [string];
  "update:manager-group-access": [string];
}>();

const configuredTokenUsers = ref(parseIDs(props.tokenUsers));
const initialAuthModes = parseAuthModes(props.userAuthModes);
const isScoped = computed(() => Boolean(normalizeRepository(props.repository ?? "")));
const repositoryKeyValue = computed(() => normalizeRepository(props.repository ?? ""));
const managerUserSource = props.managerUserAccess || props.userAccess;
const draftGroupSource = props.draftGroupAccess || props.groupAccess;
const users = ref<UserRule[]>(rulesForDisplay(managerUserSource, isScoped.value).map((rule) => ({ ...rule, tokenConfigured: configuredTokenUsers.value.has(rule.id), authMode: initialAuthModes[rule.id] || "token" })));
const groups = ref<AccessRule[]>(rulesForDisplay(draftGroupSource, isScoped.value));
const draftUsers = ref<AccessRule[]>(rulesForDisplay(props.draftUserAccess ?? "", isScoped.value));
const managerGroups = ref<AccessRule[]>(rulesForDisplay(props.managerGroupAccess ?? "", isScoped.value));
const tokenChanges = ref<Record<string, string | null>>({});
const showGroupPicker = ref(false);
const groupQuery = ref("");
const authModeOptions: AppSelectOption[] = [
  { value: "inherit", label: "沿用插件全局认证" },
  { value: "gh", label: "服务器 GitHub CLI (gh)" },
  { value: "token", label: "独立 Token" }
];
const availableGroups = computed(() => {
  const selected = new Set(groups.value.map((group) => group.id));
  const query = groupQuery.value.trim().toLowerCase();
  return props.joinedGroups.filter((group) => group.joined && !selected.has(group.group_id) && (!query || `${group.group_name ?? ""} ${group.group_id}`.toLowerCase().includes(query)));
});

watch(() => [props.userAccess, props.repository], ([value]) => {
  const nextValue = props.managerUserAccess || value || "";
  if (nextValue !== serializeEditorRules(users.value, props.managerUserAccess || props.userAccess)) users.value = rulesForDisplay(nextValue, isScoped.value).map((rule) => ({ ...rule, tokenConfigured: configuredTokenUsers.value.has(rule.id), authMode: parseAuthModes(props.userAuthModes)[rule.id] || "token" }));
});
watch(() => [props.groupAccess, props.repository], ([value]) => {
  const nextValue = props.draftGroupAccess || value || "";
  if (nextValue !== serializeEditorRules(groups.value, props.draftGroupAccess || props.groupAccess)) groups.value = rulesForDisplay(nextValue, isScoped.value);
});
watch(() => [props.draftUserAccess, props.repository], ([value]) => { const nextValue = value ?? ""; if (nextValue !== serializeEditorRules(draftUsers.value, props.draftUserAccess ?? "")) draftUsers.value = rulesForDisplay(nextValue, isScoped.value); });
watch(() => [props.managerGroupAccess, props.repository], ([value]) => { const nextValue = value ?? ""; if (nextValue !== serializeEditorRules(managerGroups.value, props.managerGroupAccess ?? "")) managerGroups.value = rulesForDisplay(nextValue, isScoped.value); });
watch(() => props.tokenUsers, (value) => {
  configuredTokenUsers.value = parseIDs(value);
  for (const user of users.value) user.tokenConfigured = configuredTokenUsers.value.has(user.id);
});

function parseRepositories(value: string): string[] {
  return [...new Set(String(value ?? "").split(/[,;；\n\r]/).map((item) => item.trim().replace(/\.git$/i, "")).filter(Boolean))];
}
function parseRules(value: string): AccessRule[] {
  return String(value ?? "").split(/[;；\n\r]/).map((line) => line.trim()).filter(Boolean).map((line) => {
    const separator = line.indexOf("=");
    return { id: (separator >= 0 ? line.slice(0, separator) : line).trim(), repositories: parseRepositories(separator >= 0 ? line.slice(separator + 1) : "") };
  });
}
function rulesForDisplay(value: string, scoped: boolean): AccessRule[] {
  const rules = parseRules(value);
  if (!scoped) return rules;
  const repository = repositoryKeyValue.value.toLowerCase();
  return rules.map((rule) => ({ ...rule, repositories: rule.repositories.filter((item) => normalizeRepository(item).toLowerCase() === repository) })).filter((rule) => rule.repositories.length > 0);
}
function parseIDs(value: string): Set<string> {
  return new Set(String(value ?? "").split(/[,;；\n\r]/).map((item) => item.trim()).filter(Boolean));
}
function parseAuthModes(value: string): Record<string, UserRule["authMode"]> {
  try {
    const parsed = JSON.parse(value || "{}") as Record<string, unknown>;
    return Object.fromEntries(Object.entries(parsed).filter((entry): entry is [string, UserRule["authMode"]] => ["inherit", "gh", "token"].includes(String(entry[1]))));
  } catch { return {}; }
}
function qqAvatarURL(userID: string): string {
  return /^\d{5,12}$/.test(userID.trim()) ? `https://q1.qlogo.cn/g?b=qq&nk=${encodeURIComponent(userID.trim())}&s=100` : "";
}
function groupSummary(groupID: string): QQBotGroupSummary | undefined { return props.joinedGroups.find((group) => group.group_id === groupID); }
function serializeRules(value: AccessRule[]): string {
  return value.filter((rule) => rule.id.trim()).map((rule) => `${rule.id.trim()} = ${rule.repositories.join(", ")}`).join("\n");
}
function serializeEditorRules(value: AccessRule[], original: string): string {
  if (!isScoped.value) return serializeRules(value);
  const repository = repositoryKeyValue.value.toLowerCase();
  const merged = new Map<string, string[]>();
  for (const rule of parseRules(original)) {
    const id = rule.id.trim();
    if (!id) continue;
    const repositories = (merged.get(id) ?? []).filter((item) => normalizeRepository(item).toLowerCase() !== repository);
    merged.set(id, repositories);
  }
  for (const rule of value) {
    const id = rule.id.trim();
    if (!id || (!isScoped.value && !rule.repositories.length)) continue;
    const repositories = merged.get(id) ?? [];
    if (!repositories.some((item) => normalizeRepository(item).toLowerCase() === repository)) repositories.push(repositoryKeyValue.value);
    merged.set(id, repositories);
  }
  return [...merged.entries()].filter(([, repositories]) => repositories.length).map(([id, repositories]) => `${id} = ${repositories.join(", ")}`).join("\n");
}
function serializeAuthModes(userAccess: string): string {
  const modes = parseAuthModes(props.userAuthModes);
  const configuredUsers = new Set(parseRules(userAccess).map((rule) => rule.id.trim()).filter(Boolean));
  for (const user of users.value) {
    const id = user.id.trim();
    if (id) modes[id] = user.authMode;
  }
  for (const id of Object.keys(modes)) if (!configuredUsers.has(id)) delete modes[id];
  return JSON.stringify(modes);
}
function emitAll(): void {
  const userAccess = serializeEditorRules(users.value, props.userAccess);
  emit("update:userAccess", userAccess);
  emit("update:groupAccess", serializeEditorRules(groups.value, props.groupAccess));
  emit("update:draft-user-access", serializeEditorRules(draftUsers.value, props.draftUserAccess ?? ""));
  emit("update:draft-group-access", serializeEditorRules(groups.value, props.draftGroupAccess ?? props.groupAccess));
  emit("update:manager-user-access", userAccess);
  emit("update:manager-group-access", serializeEditorRules(managerGroups.value, props.managerGroupAccess ?? ""));
  emit("update:userTokens", Object.keys(tokenChanges.value).length ? JSON.stringify(tokenChanges.value) : "");
  emit("update:tokenUsers", [...configuredTokenUsers.value].join("\n"));
  emit("update:userAuthModes", serializeAuthModes(userAccess));
  if (!isScoped.value) {
    const repositories = new Set([...users.value, ...groups.value].flatMap((rule) => rule.repositories));
    emit("update:allowedRepositories", [...repositories].join("\n"));
  }
}
function addUser(): void { users.value.push({ id: "", repositories: [], tokenConfigured: false, authMode: "inherit" }); }
function removeUser(index: number): void {
  const id = users.value[index]?.id.trim();
  if (id && !isScoped.value) { tokenChanges.value[id] = null; configuredTokenUsers.value.delete(id); }
  users.value.splice(index, 1); emitAll();
}
function updateUserID(index: number, event: Event): void {
  const previous = users.value[index].id.trim();
  const next = (event.target as HTMLInputElement).value.trim();
  if (previous && previous !== next && users.value[index].tokenConfigured && !isScoped.value) {
    tokenChanges.value[previous] = null;
    configuredTokenUsers.value.delete(previous);
    users.value[index].tokenConfigured = false;
  }
  users.value[index].id = next; emitAll();
}
function updateToken(index: number, event: Event): void {
  const id = users.value[index].id.trim();
  const token = (event.target as HTMLInputElement).value.trim();
  if (!id || !token) return;
  tokenChanges.value[id] = token; configuredTokenUsers.value.add(id); users.value[index].tokenConfigured = true; emitAll();
}
function clearToken(index: number): void {
  const id = users.value[index].id.trim();
  if (!id) return;
  tokenChanges.value[id] = null; configuredTokenUsers.value.delete(id); users.value[index].tokenConfigured = false; emitAll();
}
function updateUserRepositories(index: number, value: string[]): void { users.value[index].repositories = value; emitAll(); }
function addGroup(groupID: string): void { groups.value.push({ id: groupID, repositories: [] }); showGroupPicker.value = false; groupQuery.value = ""; emitAll(); }
function removeGroup(index: number): void { groups.value.splice(index, 1); emitAll(); }
function updateGroupRepositories(index: number, value: string[]): void { groups.value[index].repositories = value; emitAll(); }
function updateDraftUserRepositories(index: number, value: string[]): void { draftUsers.value[index].repositories = value; emitAll(); }
function updateManagerGroupRepositories(index: number, value: string[]): void { managerGroups.value[index].repositories = value; emitAll(); }
function addManagerGroup(): void { managerGroups.value.push({ id: "", repositories: [] }); emitAll(); }
function removeManagerGroup(index: number): void { managerGroups.value.splice(index, 1); emitAll(); }
function addDraftUser(): void { draftUsers.value.push({ id: "", repositories: [] }); emitAll(); }
function removeDraftUser(index: number): void { draftUsers.value.splice(index, 1); emitAll(); }

const RepositoryListEditor = defineComponent({
  props: { repositories: { type: Array as () => string[], required: true }, placeholder: { type: String, required: true } },
  emits: ["update"],
  setup(editorProps, { emit: editorEmit }) {
    const draft = ref("");
    const error = ref("");
    const add = () => {
      const repository = normalizeRepository(draft.value);
      if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) { error.value = "请填写 owner/repo"; return; }
      error.value = "";
      if (!editorProps.repositories.some((item) => item.toLowerCase() === repository.toLowerCase())) editorEmit("update", [...editorProps.repositories, repository]);
      draft.value = "";
    };
    return () => h("div", { class: "repository-editor" }, [
      h("div", { class: "repository-add" }, [
        h("input", { class: "input mono", value: draft.value, placeholder: editorProps.placeholder, onInput: (event: Event) => { draft.value = (event.target as HTMLInputElement).value; }, onKeydown: (event: KeyboardEvent) => { if (event.key === "Enter") { event.preventDefault(); add(); } } }),
        h("button", { class: "btn small", type: "button", onClick: add }, "添加")
      ]),
      error.value ? h("p", { class: "repository-error" }, error.value) : null,
      h("div", { class: "repository-list" }, editorProps.repositories.map((repository) => h("span", { class: "repository-chip mono" }, [
        repository,
        h("button", { type: "button", title: `移除 ${repository}`, onClick: () => editorEmit("update", editorProps.repositories.filter((item) => item !== repository)) }, [h(X, { size: 12 })])
      ])))
    ]);
  }
});

function normalizeRepository(value: string): string {
  let repository = value.trim();
  try {
    const url = new URL(repository.includes("://") ? repository : `https://${repository}`);
    if (/^(www\.)?github\.com$/i.test(url.hostname)) repository = url.pathname.split("/").filter(Boolean).slice(0, 2).join("/");
  } catch { /* Let the owner/repo validator report malformed input. */ }
  return repository.replace(/^github\.com\//i, "").replace(/\.git\/?$/i, "").replace(/\/$/, "");
}
</script>

<style scoped>
.issue-access-editor { display: flex; flex-direction: column; gap: 14px; margin-bottom: 18px; }
.section-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.section-heading h3 { margin: 0; font-size: 15px; }
.section-heading p { margin: 4px 0 0; color: var(--muted); font-size: 12px; }
.rule-list { display: flex; flex-direction: column; gap: 10px; }
.access-rule { padding: 14px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-2); }
.rule-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.user-fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 10px; margin-bottom: 12px; }
.token-field { grid-column: auto; }
.user-fields label { display: flex; flex-direction: column; gap: 6px; color: var(--muted); font-size: 12px; }
.token-input { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 8px; }
.token-state { color: var(--muted); font-size: 12px; white-space: nowrap; }
.token-state.configured { color: var(--ok); }
.identity { display: flex; min-width: 0; align-items: center; gap: 9px; }
.identity div, .group-option span { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
.identity small, .group-option small { color: var(--muted); font-size: 11px; }
.avatar { width: 32px; height: 32px; flex: 0 0 32px; border-radius: 50%; object-fit: cover; background: var(--surface); }
.group-picker { display: flex; flex-direction: column; gap: 6px; padding: 10px; border: 1px solid var(--border); background: var(--surface-2); }
.group-option { display: flex; align-items: center; gap: 9px; padding: 8px; border: 0; background: transparent; color: inherit; text-align: left; cursor: pointer; }
.group-option:hover { background: var(--surface); }
.picker-state { margin: 4px; color: var(--muted); font-size: 12px; }
.section-divider { height: 1px; margin: 4px 0; background: var(--border); }
.approval-flow { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; color: var(--muted); font-size: 12px; }
.add-rule { align-self: flex-start; }
:deep(.repository-editor) { display: flex; flex-direction: column; gap: 8px; }
:deep(.repository-add) { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
:deep(.repository-list) { display: flex; flex-wrap: wrap; gap: 7px; }
:deep(.repository-chip) { display: inline-flex; align-items: center; gap: 6px; min-height: 30px; padding: 5px 8px; border: 1px solid var(--border); background: var(--surface); font-size: 12px; }
:deep(.repository-chip button) { display: inline-grid; place-items: center; padding: 0; border: 0; background: transparent; color: var(--muted); cursor: pointer; }
:deep(.repository-error) { margin: 0; color: var(--err); font-size: 12px; }
@media (max-width: 720px) { .user-fields { grid-template-columns: 1fr; } .token-input { grid-template-columns: minmax(0, 1fr) auto; } .token-input .btn { grid-column: 1 / -1; justify-self: start; } }
</style>
