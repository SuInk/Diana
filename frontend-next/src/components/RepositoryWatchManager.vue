<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="repository-watch-manager">
    <div class="repository-watch-manager-head">
      <div>
        <h3>仓库管理</h3>
        <p>每个仓库单独配置通知对象和 Issue 管理人员、草稿人；点击仓库右侧编辑按钮即可管理。</p>
      </div>
      <button class="btn small primary" type="button" @click="startCreate">
        <Plus :size="14" aria-hidden="true" />
        添加仓库
      </button>
    </div>

    <form v-if="editing" class="repository-watch-editor" @submit.prevent="save">
      <div class="form-grid repository-watch-form">
        <div class="field wide">
          <label for="plugin-watch-repository">GitHub 仓库</label>
          <input id="plugin-watch-repository" v-model.trim="form.repository" class="input" type="text" placeholder="owner/repository 或 GitHub 链接" />
          <span class="hint">下方的通知与 Issue 配置只作用于这一个仓库；公开仓库高频检查也建议配置上方 Token。</span>
        </div>
        <div class="field">
          <label for="plugin-watch-branch">分支</label>
          <input id="plugin-watch-branch" v-model.trim="form.branch" class="input" type="text" placeholder="留空使用默认分支" />
        </div>
        <div class="field">
          <div class="repository-watch-interval-label">
            <label for="plugin-watch-interval">检查周期</label>
            <span class="badge" :class="props.tokenConfigured ? 'accent' : 'warn'">
              {{ props.tokenConfigured ? "Token 模式" : "匿名模式" }}
            </span>
          </div>
          <div class="input-group">
            <input id="plugin-watch-interval" v-model.number="form.interval_seconds" class="input" type="number" :min="minimumIntervalSeconds" :max="maximumIntervalSeconds" step="1" />
            <span class="repository-watch-unit">秒</span>
          </div>
          <span class="hint">当前模式默认 {{ formatInterval(defaultIntervalSeconds) }}；可设置 30 秒至 365 天。启用类型越多，每轮 GitHub API 请求越多。</span>
        </div>
        <div class="field">
          <label for="plugin-watch-credential">使用的凭据</label>
          <AppSelect id="plugin-watch-credential" v-model="selectedCredential" :options="credentialOptions" :disabled="!repositoryKey(form.repository ?? '')" />
          <span class="hint">这个仓库的更新检查和 Issue 操作都走选中的凭据；留空则使用公共 Token。凭据在「Token」标签页里管理。</span>
        </div>
        <div class="field wide">
          <label for="plugin-watch-profile">发送机器人</label>
          <AppSelect id="plugin-watch-profile" v-model="form.profile_id" :options="profileOptions" />
        </div>
        <div class="field wide repository-notification-settings">
          <div class="repository-section-title"><label>仓库通知</label><label class="switch"><input v-model="form.notification_enabled" type="checkbox" /><span class="track" aria-hidden="true"></span></label></div>
          <span class="hint">开启后把选中类型的更新摘要发送到这里配置的私聊或群聊；关闭后仍保留仓库检查状态。</span>
          <div class="issue-role-block">
            <div class="issue-role-head">
              <label>监控内容</label>
              <span class="hint">选择要检查的更新类型；启用类型越多，每轮 GitHub API 请求越多。</span>
            </div>
            <div class="repository-watch-scopes">
              <label class="check-item"><input v-model="form.watch_commits" type="checkbox" />Commit</label>
              <label class="check-item"><input v-model="form.watch_pull_requests" type="checkbox" />PR</label>
              <label class="check-item"><input v-model="form.watch_issues" type="checkbox" />Issue</label>
              <label class="check-item"><input v-model="form.watch_releases" type="checkbox" />Release</label>
              <label class="check-item"><input v-model="form.watch_stars" type="checkbox" />Star</label>
            </div>
          </div>
          <div v-if="form.notification_enabled" class="target-list">
            <div v-for="(target, index) in form.notification_targets" :key="`target-${index}`" class="target-row">
              <AppSelect v-model="target.destination" :options="destinationOptions" />
              <AppSelect v-if="target.destination === 'group' && groupOptions.length" :model-value="target.group_id ?? ''" :options="groupOptions" @update:model-value="target.group_id = String($event ?? '')" />
              <input v-else-if="target.destination === 'group'" v-model.trim="target.group_id" class="input" type="text" placeholder="群号或 Chat ID" />
              <input v-else v-model.trim="target.user_id" class="input" type="text" placeholder="私聊对象 ID" />
              <button class="btn small ghost danger icon-only" type="button" title="移除通知对象" aria-label="移除通知对象" @click="removeTarget(index)"><Trash2 :size="14" aria-hidden="true" /></button>
            </div>
            <button class="btn small ghost" type="button" @click="addTarget"><Plus :size="14" aria-hidden="true" />添加通知对象</button>
            <p v-if="!form.notification_targets.length" class="hint">至少添加一个通知对象。</p>
          </div>
        </div>
        <div class="field wide repository-notification-settings">
          <div class="repository-section-title"><label>Issue 管理</label><label class="switch"><input v-model="form.issue_enabled" type="checkbox" /><span class="track" aria-hidden="true"></span></label></div>
          <span class="hint">开启后，此仓库允许通过机器人操作 Issue：管理人员可直接创建和管理，草稿人只能提交草稿、由管理人员确认写入。</span>
          <template v-if="form.issue_enabled">
            <div class="issue-role-block">
              <div class="issue-role-head"><label>管理人员</label><span class="hint">可直接创建、更新、评论、关闭 Issue，也负责确认草稿。</span></div>
              <div class="target-list">
                <div v-for="(member, index) in form.issue_managers" :key="`manager-${index}`" class="target-row">
                  <AppSelect v-model="member.destination" :options="destinationOptions" />
                  <AppSelect v-if="member.destination === 'group' && groupOptions.length" :model-value="member.group_id ?? ''" :options="groupOptions" @update:model-value="member.group_id = String($event ?? '')" />
                  <input v-else-if="member.destination === 'group'" v-model.trim="member.group_id" class="input" type="text" placeholder="群号或 Chat ID" />
                  <input v-else v-model.trim="member.user_id" class="input" type="text" placeholder="私聊用户 ID" />
                  <button class="btn small ghost danger icon-only" type="button" title="移除管理人员" aria-label="移除管理人员" @click="form.issue_managers.splice(index, 1)"><Trash2 :size="14" aria-hidden="true" /></button>
                </div>
                <button class="btn small ghost" type="button" @click="form.issue_managers.push({ destination: 'private', user_id: '' })"><Plus :size="14" aria-hidden="true" />添加管理人员</button>
                <p v-if="!form.issue_managers.length" class="hint">至少添加一名管理人员，草稿才有人确认。</p>
              </div>
            </div>
            <div class="issue-role-block">
              <div class="issue-role-head"><label>草稿人</label><span class="hint">可以提出需求并生成 Issue 草稿，不能直接写入 GitHub。</span></div>
              <div class="target-list">
                <div v-for="(member, index) in form.issue_drafters" :key="`drafter-${index}`" class="target-row">
                  <AppSelect v-model="member.destination" :options="destinationOptions" />
                  <AppSelect v-if="member.destination === 'group' && groupOptions.length" :model-value="member.group_id ?? ''" :options="groupOptions" @update:model-value="member.group_id = String($event ?? '')" />
                  <input v-else-if="member.destination === 'group'" v-model.trim="member.group_id" class="input" type="text" placeholder="群号或 Chat ID" />
                  <input v-else v-model.trim="member.user_id" class="input" type="text" placeholder="私聊用户 ID" />
                  <button class="btn small ghost danger icon-only" type="button" title="移除草稿人" aria-label="移除草稿人" @click="form.issue_drafters.splice(index, 1)"><Trash2 :size="14" aria-hidden="true" /></button>
                </div>
                <button class="btn small ghost" type="button" @click="form.issue_drafters.push({ destination: 'group', group_id: '' })"><Plus :size="14" aria-hidden="true" />添加草稿人</button>
              </div>
            </div>
          </template>
        </div>
      </div>
      <div class="repository-watch-editor-actions">
        <button class="btn small" type="button" :disabled="saving" @click="stopEditing">取消</button>
        <button class="btn small primary" type="submit" :disabled="saving">
          <LoaderCircle v-if="saving" :size="14" class="spin" aria-hidden="true" />
          {{ editingTask ? "保存订阅" : "创建订阅" }}
        </button>
      </div>
    </form>

    <div v-if="loading" class="repository-watch-manager-empty">
      <LoaderCircle :size="16" class="spin" aria-hidden="true" />
      正在加载订阅
    </div>
    <div v-else-if="watches.length" class="repository-watch-manager-list">
      <article v-for="task in watches" :key="task.id" class="repository-watch-manager-item">
        <div class="repository-watch-manager-main">
          <div class="cluster">
            <strong class="mono">{{ task.repository }}</strong>
            <span class="badge" :class="statusTone(task.status)">{{ statusLabel(task.status) }}</span>
          </div>
          <div class="task-facts">
            <span v-if="task.repository_branch">分支 <strong class="mono">{{ task.repository_branch }}</strong></span>
            <span>每 {{ formatInterval(task.interval_seconds || defaultIntervalSeconds) }}</span>
            <span>通知 <strong>{{ task.notification_enabled === false ? "已关闭" : (task.notification_targets?.length || (task.group_id || task.user_id ? 1 : 0)) + " 个对象" }}</strong></span>
            <span>{{ watchScopeLabel(task) }}</span>
            <span class="repository-access-fact">{{ issueFactLabel(task) }}</span>
          </div>
          <p v-if="task.last_error" class="repository-watch-manager-error">{{ task.last_error }}</p>
        </div>
        <div class="repository-watch-manager-actions">
          <button v-if="task.status !== 'cancelled'" class="btn small icon-only" type="button" title="立即检查" aria-label="立即检查" :disabled="busyID === task.id" @click="runNow(task)"><Play :size="14" aria-hidden="true" /></button>
          <button v-if="task.status !== 'cancelled'" class="btn small icon-only" type="button" title="编辑仓库和授权" aria-label="编辑仓库和授权" @click="startEdit(task)"><Pencil :size="14" aria-hidden="true" /></button>
          <button v-if="task.status !== 'cancelled'" class="btn small icon-only" type="button" title="取消订阅" aria-label="取消订阅" :disabled="busyID === task.id" @click="cancel(task)"><CircleX :size="14" aria-hidden="true" /></button>
          <button class="btn small ghost danger icon-only" type="button" title="删除订阅" aria-label="删除订阅" :disabled="busyID === task.id" @click="remove(task)"><Trash2 :size="14" aria-hidden="true" /></button>
        </div>
      </article>
    </div>
    <div v-else class="repository-watch-manager-empty repository-watch-manager-empty-guide">
      <strong>还没有仓库配置</strong>
      <span>点击“添加仓库”，填写仓库后即可配置该仓库的通知对象与 Issue 管理人员、草稿人；不同仓库互不影响。</span>
      <button class="btn small ghost" type="button" @click="startCreate">
        <Plus :size="14" aria-hidden="true" />
        添加仓库并配置权限
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { CircleX, LoaderCircle, Pencil, Play, Plus, Trash2 } from "@lucide/vue";
import {
  cancelRepositoryWatch,
  createRepositoryWatch,
  deleteRepositoryWatch,
  getAssistantTasks,
  getBotProfileConfig,
  listBotGroups,
  runRepositoryWatch,
  updateRepositoryWatch,
  type AssistantTask,
  type AssistantTaskStatus,
  type BotProfileConfig,
  type BotGroupSummary
} from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "./AppSelect.vue";

const props = defineProps<{
  prepareAccess?: () => Promise<void>;
  tokenConfigured?: boolean;
  issueEnabledRepositories?: string[];
  credentials?: Array<{ id: string; name: string; auth: string }>;
  repositoryCredentials?: Record<string, string>;
  userAccess?: string;
  groupAccess?: string;
  draftUserAccess?: string;
  draftGroupAccess?: string;
  managerUserAccess?: string;
  managerGroupAccess?: string;
  joinedGroups?: BotGroupSummary[];
  groupsLoading?: boolean;
  groupsWarning?: string;
}>();
const emit = defineEmits<{
  "update:issue-enabled-repositories": [string[]];
  "update:repository-credentials": [Record<string, string>];
  "update:user-access": [string];
  "update:group-access": [string];
  "update:draft-user-access": [string];
  "update:draft-group-access": [string];
  "update:manager-user-access": [string];
  "update:manager-group-access": [string];
}>();

type IssueMember = { destination: "private" | "group"; group_id?: string; user_id?: string };
const authenticatedIntervalSeconds = 60;
const anonymousIntervalSeconds = 60 * 60;
const minimumIntervalSeconds = 30;
const maximumIntervalSeconds = 365 * 24 * 60 * 60;
const defaultIntervalSeconds = computed(() => props.tokenConfigured ? authenticatedIntervalSeconds : anonymousIntervalSeconds);
const emptyForm = () => ({ repository: "", branch: "", interval_seconds: defaultIntervalSeconds.value, watch_commits: true, watch_pull_requests: true, watch_issues: true, watch_releases: true, watch_stars: true, issue_enabled: false, profile_id: "", notification_enabled: true, notification_targets: [] as IssueMember[], issue_managers: [] as IssueMember[], issue_drafters: [] as IssueMember[] });
const watches = ref<AssistantTask[]>([]);
const profiles = ref<BotProfileConfig[]>([]);
const joinedGroups = ref<BotGroupSummary[]>([]);
const loading = ref(false);
const saving = ref(false);
const busyID = ref("");
const editing = ref(false);
const editingTask = ref<AssistantTask | null>(null);
const form = ref(emptyForm());

const profileOptions = computed(() => profiles.value.map((profile) => ({ value: profile.id ?? "", label: profile.name || profile.platform || profile.id || "未命名机器人", hint: profile.platform })).filter((option) => option.value));
const selectedProfile = computed(() => profiles.value.find((profile) => profile.id === form.value.profile_id));
const groupOptions = computed(() => selectedProfile.value?.platform === "telegram" ? [] : joinedGroups.value.filter((group) => group.joined).map((group) => ({ value: group.group_id, label: group.group_name || `群 ${group.group_id}`, hint: group.group_name ? group.group_id : undefined })));
const destinationOptions = [{ value: "private", label: "私聊" }, { value: "group", label: "群聊" }];

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [tasks, config, groups] = await Promise.all([getAssistantTasks(), getBotProfileConfig(), listBotGroups().catch(() => ({ groups: [] }))]);
    watches.value = tasks.items.filter((task) => task.kind === "repository_watch");
    profiles.value = config.profiles?.length ? config.profiles : [config];
    joinedGroups.value = groups.groups;
    if (!form.value.profile_id) form.value.profile_id = config.active_profile_id || profiles.value[0]?.id || "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "仓库订阅加载失败");
  } finally {
    loading.value = false;
  }
}

function startCreate(): void {
  const profileID = form.value.profile_id || profiles.value[0]?.id || "";
  form.value = { ...emptyForm(), profile_id: profileID };
  editingTask.value = null;
  editing.value = true;
}

// 凭据下拉：留空表示沿用公共 Token，与后端「未绑定就回落」的行为一致。
const credentialOptions = computed(() => [
  { value: "", label: "使用公共 Token" },
  ...(props.credentials ?? []).map((item) => ({
    value: item.id,
    label: item.auth === "gh" ? `${item.name || item.id}（gh CLI）` : item.name || item.id
  }))
]);

const selectedCredential = computed({
  get(): string {
    const target = repositoryKey(form.value.repository ?? "").toLowerCase();
    if (!target) return "";
    return props.repositoryCredentials?.[target] ?? "";
  },
  set(value: string) {
    const target = repositoryKey(form.value.repository ?? "").toLowerCase();
    if (!target) return;
    const next = { ...(props.repositoryCredentials ?? {}) };
    if (value) next[target] = value;
    else delete next[target];
    emit("update:repository-credentials", next);
  }
});

function repositoryKey(value: string): string {
  return value.trim().replace(/^https?:\/\/(www\.)?github\.com\//i, "").replace(/\.git\/?$/i, "").replace(/\/$/, "");
}

function parseAccessRules(value: string | undefined): Array<{ id: string; repositories: string[] }> {
  return String(value ?? "").split(/[;；\n\r]/).map((line) => line.trim()).filter(Boolean).map((line) => {
    const separator = line.indexOf("=");
    return {
      id: (separator >= 0 ? line.slice(0, separator) : line).trim(),
      repositories: (separator >= 0 ? line.slice(separator + 1) : "").split(/[,，]/).map((item) => item.trim()).filter(Boolean)
    };
  }).filter((rule) => rule.id);
}

function accessIDsFor(value: string | undefined, repository: string): string[] {
  const target = repositoryKey(repository).toLowerCase();
  if (!target) return [];
  const ids = new Set<string>();
  for (const rule of parseAccessRules(value)) {
    if (rule.repositories.some((item) => repositoryKey(item).toLowerCase() === target)) ids.add(rule.id);
  }
  return [...ids];
}

function mergeRepositoryAccess(original: string | undefined, repository: string, ids: string[]): string {
  const target = repositoryKey(repository);
  const targetLower = target.toLowerCase();
  const merged = new Map<string, string[]>();
  for (const rule of parseAccessRules(original)) {
    const kept = (merged.get(rule.id) ?? []).concat(rule.repositories.filter((item) => repositoryKey(item).toLowerCase() !== targetLower));
    merged.set(rule.id, [...new Set(kept)]);
  }
  for (const id of ids) {
    const repositories = merged.get(id) ?? [];
    if (!repositories.some((item) => repositoryKey(item).toLowerCase() === targetLower)) repositories.push(target);
    merged.set(id, repositories);
  }
  return [...merged.entries()].filter(([, repositories]) => repositories.length).map(([id, repositories]) => `${id} = ${repositories.join(", ")}`).join("\n");
}

function issueMemberIDs(members: IssueMember[], destination: "private" | "group"): string[] {
  return [...new Set(members.filter((member) => member.destination === destination).map((member) => (destination === "group" ? member.group_id : member.user_id)?.trim() ?? "").filter(Boolean))];
}

function issueMembersFrom(userValue: string | undefined, groupValue: string | undefined, repository: string): IssueMember[] {
  return [
    ...accessIDsFor(userValue, repository).map((id) => ({ destination: "private" as const, user_id: id })),
    ...accessIDsFor(groupValue, repository).map((id) => ({ destination: "group" as const, group_id: id }))
  ];
}

function repositoryIssueEnabled(repository: string | undefined): boolean {
  const target = repositoryKey(repository ?? "").toLowerCase();
  return Boolean(target) && props.issueEnabledRepositories?.some((item) => repositoryKey(item).toLowerCase() === target) === true;
}

function issueFactLabel(task: AssistantTask): string {
  const repository = task.repository ?? "";
  if (!repositoryIssueEnabled(repository)) return "Issue 关闭";
  const managers = issueMembersFrom(props.managerUserAccess || props.userAccess, props.managerGroupAccess, repository).length;
  const drafters = issueMembersFrom(props.draftUserAccess, props.draftGroupAccess || props.groupAccess, repository).length;
  return `Issue 管理 ${managers} · 草稿 ${drafters}`;
}

function startEdit(task: AssistantTask): void {
  editingTask.value = task;
  const repository = task.repository ?? "";
  const legacyTarget = task.group_id ? [{ destination: "group" as const, group_id: task.group_id }] : task.user_id ? [{ destination: "private" as const, user_id: task.user_id }] : [];
  form.value = { repository, branch: task.repository_branch ?? "", interval_seconds: task.interval_seconds || defaultIntervalSeconds.value, watch_commits: task.watch_commits === true, watch_pull_requests: task.watch_pull_requests === true, watch_issues: task.watch_issues === true, watch_releases: task.watch_releases === true, watch_stars: task.watch_stars === true, issue_enabled: repositoryIssueEnabled(repository), profile_id: task.profile_id ?? "", notification_enabled: task.notification_enabled !== false, notification_targets: (task.notification_targets?.length ? task.notification_targets.map((target) => ({ destination: target.destination, group_id: target.group_id, user_id: target.user_id })) : legacyTarget), issue_managers: issueMembersFrom(props.managerUserAccess || props.userAccess, props.managerGroupAccess, repository), issue_drafters: issueMembersFrom(props.draftUserAccess, props.draftGroupAccess || props.groupAccess, repository) };
  editing.value = true;
}

function stopEditing(): void {
  if (saving.value) return;
  editing.value = false;
  editingTask.value = null;
}

async function save(): Promise<void> {
  if (!form.value.repository) return toastError("请填写 GitHub 仓库");
  if (form.value.interval_seconds < minimumIntervalSeconds) return toastError("检查周期不能低于 30 秒");
  if (form.value.interval_seconds > maximumIntervalSeconds) return toastError("检查周期不能超过 365 天");
  if (!form.value.watch_commits && !form.value.watch_pull_requests && !form.value.watch_issues && !form.value.watch_releases && !form.value.watch_stars) return toastError("Commit、PR、Issue、Release 和 Star 至少选择一项");
  if (!form.value.profile_id) return toastError("请选择发送机器人");
  if (form.value.notification_enabled && !form.value.notification_targets.some((target) => target.destination === "group" ? target.group_id : target.user_id)) return toastError("请至少添加一个通知对象");
  const managerUserIDs = issueMemberIDs(form.value.issue_managers, "private");
  const managerGroupIDs = issueMemberIDs(form.value.issue_managers, "group");
  const drafterUserIDs = issueMemberIDs(form.value.issue_drafters, "private");
  const drafterGroupIDs = issueMemberIDs(form.value.issue_drafters, "group");
  if (form.value.issue_enabled && !managerUserIDs.length && !managerGroupIDs.length) return toastError("开启 Issue 管理后，请至少添加一名管理人员");
  saving.value = true;
  try {
    const common = { repository: form.value.repository, branch: form.value.branch, interval_seconds: form.value.interval_seconds, watch_commits: form.value.watch_commits, watch_pull_requests: form.value.watch_pull_requests, watch_issues: form.value.watch_issues, watch_releases: form.value.watch_releases, watch_stars: form.value.watch_stars };
    const delivery = { profile_id: form.value.profile_id, notification_enabled: form.value.notification_enabled, notification_targets: form.value.notification_enabled ? form.value.notification_targets : [] };
    const repository = repositoryKey(form.value.repository);
    const enabledRepositories = [...(props.issueEnabledRepositories ?? [])].filter((item) => repositoryKey(item).toLowerCase() !== repository.toLowerCase());
    if (form.value.issue_enabled && repository) enabledRepositories.push(repository);
    // 所有授权字段先 emit 回父组件表单，再由 prepareAccess 一并落库。
    emit("update:issue-enabled-repositories", enabledRepositories);
    const scopedManagerUsers = form.value.issue_enabled ? managerUserIDs : [];
    const scopedManagerGroups = form.value.issue_enabled ? managerGroupIDs : [];
    const scopedDrafterUsers = form.value.issue_enabled ? drafterUserIDs : [];
    const scopedDrafterGroups = form.value.issue_enabled ? drafterGroupIDs : [];
    const managerUserAccess = mergeRepositoryAccess(props.managerUserAccess || props.userAccess, repository, scopedManagerUsers);
    const draftGroupAccess = mergeRepositoryAccess(props.draftGroupAccess || props.groupAccess, repository, scopedDrafterGroups);
    emit("update:manager-user-access", managerUserAccess);
    emit("update:manager-group-access", mergeRepositoryAccess(props.managerGroupAccess, repository, scopedManagerGroups));
    emit("update:draft-user-access", mergeRepositoryAccess(props.draftUserAccess, repository, scopedDrafterUsers));
    emit("update:draft-group-access", draftGroupAccess);
    // 旧字段与新字段保持同一份内容，后端在新字段为空时才回退旧字段。
    emit("update:user-access", managerUserAccess);
    emit("update:group-access", draftGroupAccess);
    await props.prepareAccess?.();
    if (editingTask.value) await updateRepositoryWatch(editingTask.value.id, { ...common, ...delivery });
    else await createRepositoryWatch({ ...common, ...delivery });
    toastSuccess(editingTask.value ? "仓库订阅已更新" : "仓库订阅已创建，当前状态已作为基线");
    editing.value = false;
    editingTask.value = null;
    await load();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "仓库订阅保存失败");
  } finally {
    saving.value = false;
  }
}

function addTarget(): void { form.value.notification_targets.push({ destination: "private", user_id: "" }); }
function removeTarget(index: number): void { form.value.notification_targets.splice(index, 1); }

async function runNow(task: AssistantTask): Promise<void> {
  busyID.value = task.id;
  try { await runRepositoryWatch(task.id); toastSuccess("已安排立即检查，几秒后刷新可见结果"); await load(); }
  catch (error) { toastError(error instanceof Error ? error.message : "立即检查失败"); }
  finally { busyID.value = ""; }
}

async function cancel(task: AssistantTask): Promise<void> {
  if (!await askConfirm({ title: "取消仓库订阅", message: `停止监控 ${task.repository || task.id}？`, confirmLabel: "取消订阅", danger: true })) return;
  busyID.value = task.id;
  try { await cancelRepositoryWatch(task.id); toastSuccess("仓库订阅已取消"); await load(); }
  catch (error) { toastError(error instanceof Error ? error.message : "取消失败"); }
  finally { busyID.value = ""; }
}

async function remove(task: AssistantTask): Promise<void> {
  if (!await askConfirm({ title: "删除仓库订阅", message: `永久删除 ${task.repository || task.id} 的订阅记录？`, confirmLabel: "删除", danger: true })) return;
  busyID.value = task.id;
  try { await deleteRepositoryWatch(task.id); toastSuccess("仓库订阅已删除"); await load(); }
  catch (error) { toastError(error instanceof Error ? error.message : "删除失败"); }
  finally { busyID.value = ""; }
}

function statusLabel(value: AssistantTaskStatus): string { return { active: "运行中", retrying: "重试中", used: "已执行", cancelled: "已取消" }[value] ?? value; }
function statusTone(value: AssistantTaskStatus): string { return value === "active" ? "ok" : value === "retrying" ? "warn" : value === "cancelled" ? "err" : ""; }
function watchScopeLabel(task: AssistantTask): string { return [task.watch_commits ? "Commit" : "", task.watch_pull_requests ? "PR" : "", task.watch_issues ? "Issue" : "", task.watch_releases ? "Release" : "", task.watch_stars ? "Star" : ""].filter(Boolean).join(" + "); }
function formatInterval(seconds: number): string { return seconds % 86400 === 0 ? `${seconds / 86400} 天` : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : seconds % 60 === 0 ? `${seconds / 60} 分钟` : `${seconds} 秒`; }

watch(() => props.tokenConfigured, (configured, previous) => {
  const previousDefault = previous ? authenticatedIntervalSeconds : anonymousIntervalSeconds;
  const nextDefault = configured ? authenticatedIntervalSeconds : anonymousIntervalSeconds;
  if (!editingTask.value && form.value.interval_seconds === previousDefault) {
    form.value.interval_seconds = nextDefault;
    return;
  }
});

onMounted(() => void load());
</script>
