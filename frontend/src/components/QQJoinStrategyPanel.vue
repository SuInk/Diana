<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="join-strategy">
    <span v-if="!profile" class="hint">先保存机器人并连上，再回这里配置入群自动审批。</span>
    <template v-else>
      <span class="hint">关联群里，白名单 QQ 号的入群申请由平台直接通过。机器人需是这些群的管理员；一个机器人最多 {{ maxStrategies }} 个策略，不填过期时间默认一年后过期。</span>
      <span v-if="loadError" class="hint join-strategy-error">{{ loadError }}</span>
      <div v-for="item in strategies" :key="item.strategy_id" class="join-strategy-row">
        <div class="join-strategy-head">
          <AppSwitch :model-value="item.is_enable !== 'off'" :disabled="busy" :aria-label="`启用策略 ${item.remark || item.strategy_id}`" @update:model-value="value => toggle(item, value)" />
          <strong>{{ item.remark || "未命名策略" }}</strong>
          <span class="hint mono">{{ groupsOf(item).length }} 个群 · 白名单约 {{ item.whitelist_user_count ?? 0 }} 人<template v-if="item.expire_at"> · {{ formatDate(item.expire_at) }} 过期</template></span>
          <span class="join-strategy-actions">
            <button class="btn" type="button" :disabled="busy" @click="startEdit(item)">编辑</button>
            <button class="btn" type="button" :disabled="busy" title="扫描关联群里待审的申请，约 10 分钟完成" @click="execute(item)">立即执行</button>
            <button class="btn danger" type="button" :disabled="busy" @click="remove(item)">删除</button>
          </span>
        </div>
        <form v-if="editing === item.strategy_id" class="join-strategy-form" @submit.prevent="saveEdit(item)">
          <label>备注<input v-model="draft.remark" class="input" maxlength="255" /></label>
          <label>过期时间<input v-model="draft.expire" class="input" type="datetime-local" /></label>
          <label>关联群（{{ idKindLabel(item) }}）<IdChipInput v-model="draft.groups" :resolve-names="item.group_ids?.length ? undefined : groupNames" placeholder="群标识，回车添加" /></label>
          <label>添加白名单 QQ 号<textarea v-model="draft.addUsers" class="input mono" rows="3" placeholder="每行或用逗号分隔一个 QQ 号" /></label>
          <label>移出白名单 QQ 号<textarea v-model="draft.delUsers" class="input mono" rows="2" placeholder="每行或用逗号分隔一个 QQ 号" /></label>
          <span class="join-strategy-actions">
            <button class="btn primary" type="submit" :disabled="busy">保存</button>
            <button class="btn" type="button" :disabled="busy" @click="editing = ''">取消</button>
          </span>
        </form>
      </div>
      <form v-if="creating" class="join-strategy-form" @submit.prevent="create">
        <label>备注<input v-model="draft.remark" class="input" maxlength="255" placeholder="例如：校友群白名单" /></label>
        <label>群标识方式
          <AppSelect v-model="draft.kind" :options="[{ value: 'openid', label: '群 openid（机器人见过的群）' }, { value: 'qq', label: 'QQ 群号' }]" />
        </label>
        <label>关联群<IdChipInput v-model="draft.groups" :resolve-names="draft.kind === 'openid' ? groupNames : undefined" :placeholder="draft.kind === 'openid' ? '群 openid，回车添加' : 'QQ 群号，回车添加'" /></label>
        <span v-if="draft.kind === 'openid' && knownGroups.length" class="join-strategy-known">
          <button v-for="group in knownGroups" :key="group.id" class="btn chip" type="button" :disabled="draft.groups.includes(group.id)" @click="draft.groups = [...draft.groups, group.id]">+ {{ group.name }}</button>
        </span>
        <label>过期时间<input v-model="draft.expire" class="input" type="datetime-local" /></label>
        <label>白名单 QQ 号<textarea v-model="draft.addUsers" class="input mono" rows="4" placeholder="每行或用逗号分隔一个 QQ 号，单次最多 10000 个" /></label>
        <span class="join-strategy-actions">
          <button class="btn primary" type="submit" :disabled="busy || draft.groups.length === 0">创建</button>
          <button class="btn" type="button" :disabled="busy" @click="creating = false">取消</button>
        </span>
      </form>
      <span v-else class="join-strategy-actions">
        <button class="btn" type="button" :disabled="busy || strategies.length >= maxStrategies" @click="startCreate">
          <Plus :size="13" aria-hidden="true" />
          新建策略
        </button>
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import { Plus } from "@lucide/vue";
import { listBotGroups, qqJoinStrategy, type QQJoinStrategy } from "../api";
import { askConfirm } from "../lib/confirm";
import { toastError, toastSuccess } from "../lib/toast";
import AppSelect from "./AppSelect.vue";
import AppSwitch from "./AppSwitch.vue";
import IdChipInput from "./IdChipInput.vue";

// 入群自动审批策略存在平台那边，这里只是遥控器：每次打开都现拉，不在本地留副本。
const props = defineProps<{ profile?: string }>();

const maxStrategies = 20;
const strategies = ref<QQJoinStrategy[]>([]);
const knownGroups = ref<{ id: string; name: string }[]>([]);
const loadError = ref("");
const busy = ref(false);
const creating = ref(false);
const editing = ref("");
const draft = reactive({ remark: "", expire: "", kind: "openid" as "openid" | "qq", groups: [] as string[], original: [] as string[], addUsers: "", delUsers: "" });

function groupsOf(item: QQJoinStrategy): string[] {
  return item.group_openids?.length ? item.group_openids : item.group_ids ?? [];
}
function idKindLabel(item: QQJoinStrategy): string {
  return item.group_ids?.length ? "QQ 群号" : "群 openid";
}
function splitIDs(text: string): string[] {
  return [...new Set(text.split(/[\s,，、]+/).map(id => id.trim()).filter(Boolean))];
}
function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString();
}
// datetime-local 和 RFC3339 互转；空串表示交给平台默认（一年后）。
function toLocalInput(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}
function toRFC3339(value: string): string {
  return value ? new Date(value).toISOString().replace(/\.\d{3}Z$/, "Z") : "";
}
async function groupNames(ids: string[]): Promise<Record<string, string>> {
  const names: Record<string, string> = {};
  for (const group of knownGroups.value) if (ids.includes(group.id)) names[group.id] = group.name;
  return names;
}

async function call<T>(op: Parameters<typeof qqJoinStrategy>[1], id = "", body?: Record<string, unknown>): Promise<T | null> {
  if (!props.profile) return null;
  busy.value = true;
  try {
    return await qqJoinStrategy<T>(props.profile, op, id, body);
  } catch (err) {
    toastError(err instanceof Error ? err.message : String(err));
    return null;
  } finally {
    busy.value = false;
  }
}

async function load(): Promise<void> {
  strategies.value = [];
  loadError.value = "";
  if (!props.profile) return;
  try {
    const data = await qqJoinStrategy<{ strategies: QQJoinStrategy[] }>(props.profile, "list");
    strategies.value = data.strategies ?? [];
  } catch (err) {
    loadError.value = `拉取策略失败：${err instanceof Error ? err.message : String(err)}`;
  }
  try {
    const data = await listBotGroups(false, props.profile);
    knownGroups.value = data.groups.map(group => ({ id: group.group_id, name: group.group_name || group.group_id }));
  } catch {
    knownGroups.value = [];
  }
}

function resetDraft(): void {
  Object.assign(draft, { remark: "", expire: "", kind: "openid", groups: [], original: [], addUsers: "", delUsers: "" });
}
function startCreate(): void {
  resetDraft();
  editing.value = "";
  creating.value = true;
}
function startEdit(item: QQJoinStrategy): void {
  resetDraft();
  creating.value = false;
  editing.value = item.strategy_id;
  Object.assign(draft, { remark: item.remark ?? "", expire: toLocalInput(item.expire_at), groups: [...groupsOf(item)], original: [...groupsOf(item)] });
}

async function create(): Promise<void> {
  const key = draft.kind === "openid" ? "group_openids" : "group_ids";
  const body: Record<string, unknown> = { [key]: draft.groups, is_enable: "on", remark: draft.remark };
  if (draft.expire) body.expire_at = toRFC3339(draft.expire);
  const created = await call<{ strategy_id?: string }>("create", "", body);
  if (!created) return;
  const users = splitIDs(draft.addUsers);
  if (created.strategy_id && users.length && !(await call("whitelist", created.strategy_id, { op: "add", whitelist_users: users }))) {
    toastError("策略已创建，但白名单没加上，请在编辑里重试");
  } else {
    toastSuccess("策略已创建");
  }
  creating.value = false;
  await load();
}

async function saveEdit(item: QQJoinStrategy): Promise<void> {
  const key = item.group_ids?.length ? "group_ids" : "group_openids";
  const body: Record<string, unknown> = { remark: draft.remark };
  if (draft.expire) body.expire_at = toRFC3339(draft.expire);
  if (!(await call("update", item.strategy_id, body))) return;
  // 平台的群增删一次只能是一种动作，分两次提交。
  const added = draft.groups.filter(id => !draft.original.includes(id));
  const removed = draft.original.filter(id => !draft.groups.includes(id));
  for (const [op, ids] of [["add", added], ["del", removed]] as const) {
    if (ids.length && !(await call("update", item.strategy_id, { group_action: { op, [key]: ids } }))) return;
  }
  for (const [op, text] of [["add", draft.addUsers], ["del", draft.delUsers]] as const) {
    const users = splitIDs(text);
    if (users.length && !(await call("whitelist", item.strategy_id, { op, whitelist_users: users }))) return;
  }
  toastSuccess("策略已保存");
  editing.value = "";
  await load();
}

async function toggle(item: QQJoinStrategy, on: boolean): Promise<void> {
  if (await call("update", item.strategy_id, { is_enable: on ? "on" : "off" })) item.is_enable = on ? "on" : "off";
}

async function execute(item: QQJoinStrategy): Promise<void> {
  if (await call("execute", item.strategy_id)) toastSuccess("已开始扫描关联群的待审申请，约 10 分钟完成");
}

async function remove(item: QQJoinStrategy): Promise<void> {
  const ok = await askConfirm({ title: "删除入群自动审批策略", message: `删除「${item.remark || item.strategy_id}」后，关联群的白名单申请不再自动通过。`, confirmLabel: "删除", danger: true });
  if (ok && (await call("delete", item.strategy_id))) {
    toastSuccess("策略已删除");
    await load();
  }
}

onMounted(load);
watch(() => props.profile, () => {
  creating.value = false;
  editing.value = "";
  void load();
});
</script>

<style scoped>
.join-strategy {
  display: grid;
  gap: 8px;
  min-width: 0;
}
.join-strategy-row {
  display: grid;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  min-width: 0;
}
.join-strategy-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.join-strategy-actions,
.join-strategy-known {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.join-strategy-head .join-strategy-actions {
  margin-left: auto;
}
.join-strategy-form {
  display: grid;
  gap: 8px;
  max-width: 560px;
}
.join-strategy-form label {
  display: grid;
  gap: 4px;
}
.join-strategy-error {
  color: var(--err);
}
</style>
