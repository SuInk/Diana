<template>
  <section class="issue-access-editor">
    <header class="section-heading">
      <div>
        <h3>授权用户</h3>
        <p>只有群内且对目标仓库有授权的用户，才能批准并创建 Issue。</p>
      </div>
      <span class="badge accent">{{ users.length }} 个用户</span>
    </header>

    <div class="rule-list">
      <article v-for="(user, index) in users" :key="`user-${index}`" class="access-rule">
        <div class="rule-header">
          <strong>{{ user.id || "新授权用户" }}</strong>
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
            <span>GitHub Token</span>
            <div class="token-input">
              <input class="input mono" type="password" autocomplete="new-password" :placeholder="user.tokenConfigured ? '已配置，留空则保持不变' : 'github_pat_…'" @input="updateToken(index, $event)" />
              <span class="token-state" :class="{ configured: user.tokenConfigured }">{{ user.tokenConfigured ? "已配置" : "未配置" }}</span>
              <button v-if="user.tokenConfigured" class="btn small ghost" type="button" @click="clearToken(index)">清除</button>
            </div>
          </label>
        </div>
        <RepositoryListEditor :repositories="user.repositories" placeholder="owner/repo" @update="updateUserRepositories(index, $event)" />
      </article>
    </div>
    <button class="btn small ghost add-rule" type="button" @click="addUser">
      <UserPlus :size="15" aria-hidden="true" />
      添加授权用户
    </button>

    <div class="section-divider"></div>

    <header class="section-heading">
      <div>
        <h3>群聊草稿范围</h3>
        <p>群内所有成员都能提交需求并生成草稿，但不能直接写入 GitHub。</p>
      </div>
      <span class="badge">{{ groups.length }} 个群聊</span>
    </header>
    <div class="approval-flow">
      <span>成员提出需求</span><ArrowRight :size="14" /><span>机器人复述草稿</span><ArrowRight :size="14" /><span>授权成员确认</span><ArrowRight :size="14" /><span>创建 Issue</span>
    </div>
    <div class="rule-list">
      <article v-for="(group, index) in groups" :key="`group-${index}`" class="access-rule group-rule">
        <div class="rule-header">
          <input class="input mono group-id" type="text" inputmode="numeric" :value="group.id" placeholder="群 ID" aria-label="群 ID" @input="updateGroupID(index, $event)" />
          <button class="btn small ghost danger icon-only" type="button" title="删除群聊" aria-label="删除群聊" @click="removeGroup(index)">
            <Trash2 :size="15" aria-hidden="true" />
          </button>
        </div>
        <RepositoryListEditor :repositories="group.repositories" placeholder="允许发起草稿的 owner/repo" @update="updateGroupRepositories(index, $event)" />
      </article>
    </div>
    <button class="btn small ghost add-rule" type="button" @click="addGroup">
      <MessageSquarePlus :size="15" aria-hidden="true" />
      添加群聊
    </button>
  </section>
</template>

<script setup lang="ts">
import { defineComponent, h, ref, watch } from "vue";
import { ArrowRight, MessageSquarePlus, Trash2, UserPlus, X } from "@lucide/vue";

type AccessRule = { id: string; repositories: string[] };
type UserRule = AccessRule & { tokenConfigured: boolean };

const props = defineProps<{ userAccess: string; groupAccess: string; tokenUsers: string }>();
const emit = defineEmits<{
  "update:userAccess": [string];
  "update:groupAccess": [string];
  "update:userTokens": [string];
  "update:tokenUsers": [string];
  "update:allowedRepositories": [string];
}>();

const configuredTokenUsers = ref(parseIDs(props.tokenUsers));
const users = ref<UserRule[]>(parseRules(props.userAccess).map((rule) => ({ ...rule, tokenConfigured: configuredTokenUsers.value.has(rule.id) })));
const groups = ref<AccessRule[]>(parseRules(props.groupAccess));
const tokenChanges = ref<Record<string, string | null>>({});

watch(() => props.userAccess, (value) => {
  if (value !== serializeRules(users.value)) users.value = parseRules(value).map((rule) => ({ ...rule, tokenConfigured: configuredTokenUsers.value.has(rule.id) }));
});
watch(() => props.groupAccess, (value) => {
  if (value !== serializeRules(groups.value)) groups.value = parseRules(value);
});
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
function parseIDs(value: string): Set<string> {
  return new Set(String(value ?? "").split(/[,;；\n\r]/).map((item) => item.trim()).filter(Boolean));
}
function serializeRules(value: AccessRule[]): string {
  return value.filter((rule) => rule.id.trim()).map((rule) => `${rule.id.trim()} = ${rule.repositories.join(", ")}`).join("\n");
}
function emitAll(): void {
  emit("update:userAccess", serializeRules(users.value));
  emit("update:groupAccess", serializeRules(groups.value));
  emit("update:userTokens", Object.keys(tokenChanges.value).length ? JSON.stringify(tokenChanges.value) : "");
  emit("update:tokenUsers", [...configuredTokenUsers.value].join("\n"));
  const repositories = new Set([...users.value, ...groups.value].flatMap((rule) => rule.repositories));
  emit("update:allowedRepositories", [...repositories].join("\n"));
}
function addUser(): void { users.value.push({ id: "", repositories: [], tokenConfigured: false }); }
function removeUser(index: number): void {
  const id = users.value[index]?.id.trim();
  if (id) { tokenChanges.value[id] = null; configuredTokenUsers.value.delete(id); }
  users.value.splice(index, 1); emitAll();
}
function updateUserID(index: number, event: Event): void {
  const previous = users.value[index].id.trim();
  const next = (event.target as HTMLInputElement).value.trim();
  if (previous && previous !== next && users.value[index].tokenConfigured) {
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
function addGroup(): void { groups.value.push({ id: "", repositories: [] }); }
function removeGroup(index: number): void { groups.value.splice(index, 1); emitAll(); }
function updateGroupID(index: number, event: Event): void { groups.value[index].id = (event.target as HTMLInputElement).value.trim(); emitAll(); }
function updateGroupRepositories(index: number, value: string[]): void { groups.value[index].repositories = value; emitAll(); }

const RepositoryListEditor = defineComponent({
  props: { repositories: { type: Array as () => string[], required: true }, placeholder: { type: String, required: true } },
  emits: ["update"],
  setup(editorProps, { emit: editorEmit }) {
    const draft = ref("");
    const error = ref("");
    const add = () => {
      const repository = draft.value.trim().replace(/\.git$/i, "");
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
</script>

<style scoped>
.issue-access-editor { display: flex; flex-direction: column; gap: 14px; margin-bottom: 18px; }
.section-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.section-heading h3 { margin: 0; font-size: 15px; }
.section-heading p { margin: 4px 0 0; color: var(--muted); font-size: 12px; }
.rule-list { display: flex; flex-direction: column; gap: 10px; }
.access-rule { padding: 14px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-2); }
.rule-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.user-fields { display: grid; grid-template-columns: minmax(150px, .42fr) minmax(280px, 1fr); gap: 10px; margin-bottom: 12px; }
.user-fields label { display: flex; flex-direction: column; gap: 6px; color: var(--muted); font-size: 12px; }
.token-input { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 8px; }
.token-state { color: var(--muted); font-size: 12px; white-space: nowrap; }
.token-state.configured { color: var(--ok); }
.group-id { width: min(320px, 100%); }
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
