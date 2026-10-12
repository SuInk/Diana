<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="repository-watch-manager credential-manager">
    <div class="repository-watch-manager-head">
      <div>
        <h3>GitHub 凭据</h3>
        <p>
          每条凭据对应一个 GitHub 账号；没在「仓库管理」里单独选凭据的仓库都用默认凭据。
          <a class="token-create-link" href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noreferrer"><ExternalLink :size="13" aria-hidden="true" />创建 Token</a>
        </p>
      </div>
      <div class="cluster" style="gap: 7px; flex-wrap: nowrap">
        <button class="btn small" type="button" :disabled="testing" @click="emit('test')">
          <LoaderCircle v-if="testing" :size="14" class="spin" aria-hidden="true" />
          <UserCheck v-else :size="14" aria-hidden="true" />
          检测账号
        </button>
        <button class="btn small primary" type="button" @click="startCreate"><Plus :size="14" aria-hidden="true" />添加凭据</button>
      </div>
    </div>

    <div class="repository-watch-manager-list">
      <!-- 正在编辑的那一行直接换成表单；新建的表单排在列表最后。 -->
      <template v-for="row in rows" :key="row.key">
        <form v-if="editingKey === row.key" class="credential-edit" @submit.prevent="commitEditing">
          <div class="credential-edit-row">
            <div v-if="editingKey === defaultKey" class="cluster credential-edit-name">
              <strong>默认凭据</strong>
              <span class="badge accent">默认</span>
            </div>
            <input
              v-else
              v-model.trim="form.name"
              class="input credential-edit-name"
              type="text"
              aria-label="凭据名称"
              placeholder="凭据名称，例如「组织 Token」"
            />
            <div class="segmented" role="radiogroup" aria-label="认证方式">
              <button
                v-for="option in formAuthOptions"
                :key="option.value"
                type="button"
                role="radio"
                :aria-checked="form.auth === option.value"
                :class="{ active: form.auth === option.value }"
                @click="form.auth = option.value"
              >{{ option.label }}</button>
            </div>
          </div>
          <div v-if="form.auth !== 'gh'" class="credential-edit-row">
            <input
              v-model="form.token"
              class="input credential-edit-token"
              type="password"
              autocomplete="off"
              aria-label="GitHub Token"
              :disabled="form.clearing"
              :placeholder="formTokenPlaceholder"
            />
            <button v-if="formTokenConfigured" class="btn small ghost" type="button" @click="form.clearing = !form.clearing">
              {{ form.clearing ? "撤销清除" : "清除" }}
            </button>
          </div>
          <div class="credential-edit-foot">
            <div class="credential-edit-status">
              <span v-if="formTesting" class="hint"><LoaderCircle :size="13" class="spin" aria-hidden="true" /> 检测中…</span>
              <template v-else-if="formCheck">
                <span :class="['badge', checkTone(formCheck)]">{{ checkLabel(formCheck) }}</span>
                <span v-if="formCheck.message" class="hint">{{ formCheck.message }}</span>
                <button v-if="testCredential" class="btn small ghost icon-only" type="button" title="重新检测" aria-label="重新检测" @click="testForm()"><RefreshCw :size="13" aria-hidden="true" /></button>
              </template>
              <span v-else-if="authHint" class="hint">{{ authHint }}</span>
            </div>
            <div class="cluster" style="gap: 7px; flex-wrap: nowrap">
              <button class="btn small" type="button" @click="stopEditing">取消</button>
              <button class="btn small primary" type="submit">{{ editingKey === newKey ? "添加" : "完成" }}</button>
            </div>
          </div>
        </form>
        <article v-else-if="row.key === defaultKey" class="repository-watch-manager-item">
          <div class="repository-watch-manager-main">
            <div class="cluster">
              <strong>默认凭据</strong>
              <span class="badge accent">默认</span>
              <span class="badge">{{ authLabel(effectiveDefaultAuth) }}</span>
              <span v-if="checkOf(defaultKey)" :class="['badge', checkTone(checkOf(defaultKey))]">{{ checkLabel(checkOf(defaultKey)) }}</span>
            </div>
            <div class="task-facts">
              <span>{{ defaultTokenState }}</span>
              <span>兜底所有未单独选凭据的仓库</span>
            </div>
            <p v-if="checkOf(defaultKey)?.message" :class="checkMessageClass(checkOf(defaultKey))">{{ checkOf(defaultKey)?.message }}</p>
          </div>
          <div class="repository-watch-manager-actions">
            <button class="btn small" type="button" @click="startEdit(defaultKey)"><Pencil :size="14" aria-hidden="true" />编辑</button>
          </div>
        </article>
        <article v-else-if="row.item" class="repository-watch-manager-item">
          <div class="repository-watch-manager-main">
            <div class="cluster">
              <strong>{{ row.item.name || "未命名凭据" }}</strong>
              <span class="badge">{{ authLabel(row.item.auth) }}</span>
              <span v-if="checkOf(row.item.id)" :class="['badge', checkTone(checkOf(row.item.id))]">{{ checkLabel(checkOf(row.item.id)) }}</span>
            </div>
            <div class="task-facts">
              <span v-if="row.item.auth !== 'gh'">{{ tokenState(row.item.id) }}</span>
              <span>{{ usageOf(row.item.id) || "还没有仓库选用" }}</span>
            </div>
            <p v-if="checkOf(row.item.id)?.message" :class="checkMessageClass(checkOf(row.item.id))">{{ checkOf(row.item.id)?.message }}</p>
          </div>
          <div class="repository-watch-manager-actions">
            <button class="btn small" type="button" @click="startEdit(row.item.id)"><Pencil :size="14" aria-hidden="true" />编辑</button>
            <button class="btn small ghost danger" type="button" @click="removeCredential(row.item.id)"><Trash2 :size="14" aria-hidden="true" />删除</button>
          </div>
        </article>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ExternalLink, LoaderCircle, Pencil, Plus, RefreshCw, Trash2, UserCheck } from "@lucide/vue";
import type { CredentialCheck } from "../api";
import { askConfirm } from "../confirm";
import { toastError } from "../toast";

export interface CredentialTestInput {
  key: string;
  name: string;
  auth: string;
  token: string;
  clearing: boolean;
}

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
  // 用表单里还没点「完成」的值检测这一条凭据；key 为 default、已有凭据 ID，或新建时的占位。
  testCredential?: (input: CredentialTestInput) => Promise<CredentialCheck | undefined>;
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

const defaultKey = "default";
const newKey = "__new__";

const credentials = ref<Credential[]>([]);
// Token 是密钥，后端不会回显；这里只收本次输入的新值，留空表示沿用已存的。
const tokenDrafts = ref<Record<string, string>>({});
// 本次弹窗里被删掉、且服务端存过 Token 的凭据 ID；保存时要连 Token 一起清掉。
const removedIDs = ref<Set<string>>(new Set());

watch(
  () => props.credentials,
  (value) => {
    credentials.value = (value ?? []).map((item) => ({ ...item }));
  },
  { immediate: true, deep: true }
);

const configured = computed(() => new Set(props.configuredIds ?? []));

// 编辑表单：和 RSS 订阅一样，列表只展示，点「编辑」或「添加」才打开表单；
// 「完成」只改本地草稿，真正落库仍然跟着弹窗底部的「保存」。
const editingKey = ref("");
const form = ref({ name: "", auth: "token", token: "", clearing: false });
const formCheck = ref<CredentialCheck | undefined>();
const formTesting = ref(false);

// 填写后自动检测：切换认证方式立刻测，输入 Token 停顿一会儿再测。结果总是对应表单
// 当前的值——改动一发生就先清掉旧结果，晚到的旧请求用序号丢弃。
const autoTestDelayMs = 800;
// GitHub 的 Token 都远长于这个长度，短于它多半还没粘贴完，不必发请求。
const minimumTokenLength = 20;
let autoTestTimer: ReturnType<typeof setTimeout> | undefined;
let testSequence = 0;

function formTestable(): boolean {
  if (form.value.auth === "gh") return true;
  if (form.value.clearing) return false;
  const token = form.value.token.trim();
  return token ? token.length >= minimumTokenLength : formTokenConfigured.value;
}

function cancelAutoTest(): void {
  if (autoTestTimer) clearTimeout(autoTestTimer);
  autoTestTimer = undefined;
  testSequence++;
  formTesting.value = false;
}

function scheduleAutoTest(delay: number): void {
  cancelAutoTest();
  formCheck.value = undefined;
  if (!props.testCredential || !editingKey.value || !formTestable()) return;
  autoTestTimer = setTimeout(() => void testForm(false), delay);
}

watch(() => form.value.token, () => scheduleAutoTest(autoTestDelayMs));
watch(() => [form.value.auth, form.value.clearing], () => scheduleAutoTest(0));

async function testForm(manual = true): Promise<void> {
  if (!props.testCredential || !editingKey.value) return;
  if (autoTestTimer) clearTimeout(autoTestTimer);
  autoTestTimer = undefined;
  const sequence = ++testSequence;
  formTesting.value = true;
  try {
    const result = await props.testCredential({ key: editingKey.value, ...form.value });
    if (sequence === testSequence) formCheck.value = result;
  } catch (error) {
    if (sequence !== testSequence) return;
    // 自动检测失败不弹提示，只在表单里标出来；手动点的才弹。
    if (manual) toastError(error instanceof Error ? error.message : "凭据检测失败");
    else formCheck.value = { key: editingKey.value, label: "", configured: true, state: "error", message: "检测请求失败，可以稍后点「重新检测」。" };
  } finally {
    if (sequence === testSequence) formTesting.value = false;
  }
}

const formAuthOptions = [
  { value: "token", label: "Token" },
  { value: "gh", label: "服务器 gh" }
];

// 界面不再提供「自动」。旧配置里存着 auto 的，按它实际会走的那条显示：有 Token 用
// Token，没有就是 gh；这样打开再点「完成」也不会改变原来的行为。
const effectiveDefaultAuth = computed(() => {
  if (props.defaultAuth === "gh") return "gh";
  if (props.defaultAuth === "auto") return props.defaultTokenConfigured || props.defaultToken?.trim() ? "token" : "gh";
  return "token";
});

// 表单里只留一句必须知道的：默认凭据选 gh 时，后台的仓库更新检查并不会跟着走 gh。
const authHint = computed(() =>
  editingKey.value === defaultKey && form.value.auth === "gh" ? "仓库更新检查不走 gh，会匿名读取公开仓库。" : ""
);

const formTokenConfigured = computed(() => {
  if (editingKey.value === defaultKey) return Boolean(props.defaultTokenConfigured);
  return editingKey.value !== newKey && configured.value.has(editingKey.value);
});

const formTokenPlaceholder = computed(() => {
  if (form.value.clearing) return "保存后将清除";
  return formTokenConfigured.value ? "已配置 — 留空沿用，填写则覆盖" : "填写 GitHub Token";
});

// 一次只能编辑一条。正在编辑的那条改过还没点「完成」时，切到别的之前先问一次。
async function confirmSwitch(target: string): Promise<boolean> {
  if (!editingKey.value || editingKey.value === target || !editorDirty()) return true;
  const current = editingKey.value === defaultKey ? "默认凭据" : editingKey.value === newKey ? "新凭据" : `「${form.value.name || credentials.value.find((entry) => entry.id === editingKey.value)?.name || "未命名凭据"}」`;
  return askConfirm({ title: "放弃未完成的改动？", message: `${current}的改动还没点「完成」，切换后会丢失。`, confirmLabel: "放弃改动", danger: true });
}

async function startCreate(): Promise<void> {
  if (editingKey.value === newKey) return;
  if (!(await confirmSwitch(newKey))) return;
  editingKey.value = newKey;
  form.value = { name: "", auth: "token", token: "", clearing: false };
  scheduleAutoTest(0);
}

async function startEdit(key: string): Promise<void> {
  if (!(await confirmSwitch(key))) return;
  editingKey.value = key;
  if (key === defaultKey) {
    form.value = { name: "默认凭据", auth: effectiveDefaultAuth.value, token: props.defaultToken ?? "", clearing: Boolean(props.defaultClearing) };
  } else {
    const item = credentials.value.find((entry) => entry.id === key);
    form.value = { name: item?.name ?? "", auth: item?.auth ?? "token", token: tokenDrafts.value[key] ?? "", clearing: removedIDs.value.has(key) };
  }
  // 打开已有凭据就先测一次，已存的 Token 或 gh 登录的是谁一眼可见。
  scheduleAutoTest(0);
}

function stopEditing(): void {
  cancelAutoTest();
  editingKey.value = "";
  formCheck.value = undefined;
}

function commitEditing(): boolean {
  const key = editingKey.value;
  if (!key) return true;
  if (key === defaultKey) {
    if (form.value.auth !== effectiveDefaultAuth.value) emit("update:default-auth", form.value.auth);
    emit("update:default-token", form.value.clearing ? "" : form.value.token);
    if (form.value.clearing !== Boolean(props.defaultClearing)) emit("toggle-clear-default");
    editingKey.value = "";
    return true;
  }
  if (!form.value.name) {
    toastError("请填写凭据名称");
    return false;
  }
  if (key === newKey) {
    const id = newCredentialID();
    credentials.value.push({ id, name: form.value.name, auth: form.value.auth });
    if (form.value.auth !== "gh" && form.value.token.trim()) tokenDrafts.value[id] = form.value.token;
  } else {
    const item = credentials.value.find((entry) => entry.id === key);
    if (item) {
      item.name = form.value.name;
      item.auth = form.value.auth;
    }
    if (form.value.clearing) {
      delete tokenDrafts.value[key];
      removedIDs.value.add(key);
    } else {
      removedIDs.value.delete(key);
      if (form.value.token.trim()) tokenDrafts.value[key] = form.value.token;
      else delete tokenDrafts.value[key];
    }
  }
  emitCredentials();
  emitTokens();
  cancelAutoTest();
  editingKey.value = "";
  formCheck.value = undefined;
  return true;
}

// 表单还开着且改过内容时，外层「保存」要先把它收进草稿，否则点了保存却没生效。
function editorDirty(): boolean {
  const key = editingKey.value;
  if (!key) return false;
  if (key === newKey) return Boolean(form.value.name || form.value.token);
  if (key === defaultKey) {
    return form.value.auth !== effectiveDefaultAuth.value || form.value.token !== (props.defaultToken ?? "") || form.value.clearing !== Boolean(props.defaultClearing);
  }
  const item = credentials.value.find((entry) => entry.id === key);
  return form.value.name !== (item?.name ?? "") || form.value.auth !== (item?.auth ?? "token") || form.value.token !== (tokenDrafts.value[key] ?? "") || form.value.clearing !== removedIDs.value.has(key);
}

// 列表的行：默认凭据、各条凭据，新建时末尾再多一行只放表单。
const rows = computed(() => [
  { key: defaultKey, item: undefined as Credential | undefined },
  ...credentials.value.map((item) => ({ key: item.id, item })),
  ...(editingKey.value === newKey ? [{ key: newKey, item: undefined as Credential | undefined }] : [])
]);

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

function checkMessageClass(check: CredentialCheck | undefined): string {
  return check?.state === "invalid" ? "repository-watch-manager-error" : "credential-check-message";
}

function authLabel(auth: string): string {
  return auth === "gh" ? "服务器 gh" : "Token";
}

const defaultTokenState = computed(() => {
  if (effectiveDefaultAuth.value === "gh") return "不使用 Token";
  if (props.defaultClearing) return "保存后清除 Token";
  if (props.defaultToken?.trim()) return "Token 待保存";
  return props.defaultTokenConfigured ? "Token 已配置" : "未填 Token";
});

function tokenState(id: string): string {
  if (removedIDs.value.has(id)) return "保存后清除 Token";
  if (tokenDrafts.value[id]?.trim()) return "Token 待保存";
  return configured.value.has(id) ? "Token 已配置" : "未填 Token";
}

// 统计每条凭据被多少个仓库选用，删除前能看出影响面。
function usageOf(id: string): string {
  const count = Object.values(props.repositoryCredentials ?? {}).filter((value) => value === id).length;
  return count > 0 ? `${count} 个仓库在用` : "";
}

function newCredentialID(): string {
  // 只用于区分几条凭据，不参与鉴权，够随机即可。
  const random = Math.random().toString(36).slice(2, 8);
  return `cred-${Date.now().toString(36)}-${random}`;
}

async function removeCredential(id: string): Promise<void> {
  const item = credentials.value.find((entry) => entry.id === id);
  if (!item) return;
  const usage = usageOf(id);
  const confirmed = await askConfirm({
    title: "删除凭据",
    message: usage ? `「${item.name || "未命名凭据"}」有 ${usage}，删除后这些仓库改用默认凭据。` : `删除「${item.name || "未命名凭据"}」？`,
    confirmLabel: "删除",
    danger: true
  });
  if (!confirmed) return;
  credentials.value = credentials.value.filter((entry) => entry.id !== id);
  if (editingKey.value === id) editingKey.value = "";
  delete tokenDrafts.value[id];
  // 删掉的凭据要显式提交一次空串，后端才会把它的 Token 从库里删掉。
  // 只从列表里去掉 ID 的话，明文 Token 会一直留在 github_credential_tokens 里。
  if (configured.value.has(id)) removedIDs.value.add(id);
  // 删掉凭据的同时解绑仓库，否则仓库会指向一条不存在的凭据。
  const bindings = { ...(props.repositoryCredentials ?? {}) };
  let changed = false;
  for (const [repository, boundID] of Object.entries(bindings)) {
    if (boundID === id) {
      delete bindings[repository];
      changed = true;
    }
  }
  if (changed) emit("update:repository-credentials", bindings);
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
  hasUnsavedChanges: editorDirty,
  commitEditing,
  clearDrafts(): void {
    tokenDrafts.value = {};
    removedIDs.value = new Set();
    editingKey.value = "";
  }
});
</script>

<style scoped>
.credential-manager {
  margin-top: 0;
  padding-top: 0;
  border-top: none;
}

/* 编辑中的那一行：和列表行同一位置，底色稍亮，内容只有输入框和操作。 */
.credential-edit {
  display: grid;
  gap: 10px;
  margin: 6px -12px;
  padding: 12px;
  border-radius: var(--radius-md);
  background: var(--surface-2);
}

.credential-edit-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.credential-edit-name {
  flex: 1;
  min-width: 0;
}

.credential-edit-token {
  flex: 1;
  min-width: 0;
}

.credential-edit-foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.credential-edit-status {
  display: flex;
  flex: 1;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
  min-width: 0;
  min-height: 30px;
}

.credential-edit-status .hint {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.credential-check-message {
  margin: 7px 0 0;
  color: var(--muted);
  font-size: 12px;
}
</style>
