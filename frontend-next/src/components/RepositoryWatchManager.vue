<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="repository-watch-manager">
    <div class="repository-watch-manager-head">
      <div>
        <h3>仓库管理</h3>
        <p>每个仓库单独配置通知、Issue 能力，以及指定用户和指定群聊；点击仓库右侧编辑按钮即可管理。</p>
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
          <span class="hint">填写仓库后，下方会出现该仓库独立的“指定用户 / 指定群聊”配置；公开仓库高频检查也建议配置上方 Token。</span>
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
        <div class="field wide">
          <label for="plugin-watch-profile">发送机器人</label>
          <AppSelect id="plugin-watch-profile" v-model="form.profile_id" :options="profileOptions" />
        </div>
        <div class="field wide">
          <label>通知位置</label>
          <div class="segmented repository-watch-destination" role="radiogroup" aria-label="通知位置">
            <button type="button" role="radio" :aria-checked="form.destination === 'private'" :class="{ active: form.destination === 'private' }" @click="form.destination = 'private'">私聊对象</button>
            <button type="button" role="radio" :aria-checked="form.destination === 'group'" :class="{ active: form.destination === 'group' }" @click="form.destination = 'group'">群聊</button>
          </div>
        </div>
        <div v-if="form.destination === 'group'" class="field wide">
          <label for="plugin-watch-group">群号 / Chat ID</label>
          <AppSelect v-if="groupOptions.length" id="plugin-watch-group" v-model="form.group_id" :options="groupOptions" />
          <input v-else id="plugin-watch-group" v-model.trim="form.group_id" class="input" type="text" placeholder="QQ 群号或 Telegram 群 Chat ID" />
        </div>
        <div v-if="form.destination === 'private'" class="field wide">
          <label for="plugin-watch-user">私聊对象 ID</label>
          <input id="plugin-watch-user" v-model.trim="form.user_id" class="input" type="text" placeholder="QQ 号或 Telegram Chat ID" />
        </div>
        <div class="field wide">
          <label>监控内容</label>
          <div class="repository-watch-scopes">
            <label class="check-item"><input v-model="form.watch_commits" type="checkbox" />Commit</label>
            <label class="check-item"><input v-model="form.watch_pull_requests" type="checkbox" />PR</label>
            <label class="check-item"><input v-model="form.watch_releases" type="checkbox" />Release</label>
            <label class="check-item"><input v-model="form.watch_stars" type="checkbox" />Star</label>
          </div>
        </div>
        <div class="field wide repository-watch-capability">
          <label class="check-item">
            <input v-model="form.issue_enabled" type="checkbox" />
            <span>允许 LLM 操作这个仓库的 Issue</span>
          </label>
          <span class="hint">启用后，此仓库进入 Issue 操作白名单：授权用户可搜索、创建、更新、评论、关闭或重新打开 Issue；群内其他成员仍只能生成草稿，由授权成员确认写入。</span>
        </div>
        <div v-if="form.repository.trim()" class="wide repository-issue-access">
          <RepositoryAccessEditor
            :repository="form.repository"
            :user-access="props.userAccess ?? ''"
            :group-access="props.groupAccess ?? ''"
            :token-users="props.tokenUsers ?? ''"
            :user-auth-modes="props.userAuthModes ?? ''"
            :joined-groups="props.joinedGroups ?? []"
            :groups-loading="props.groupsLoading"
            :groups-warning="props.groupsWarning"
            @update:user-access="emit('update:user-access', $event)"
            @update:group-access="emit('update:group-access', $event)"
            @update:user-tokens="emit('update:user-tokens', $event)"
            @update:token-users="emit('update:token-users', $event)"
            @update:user-auth-modes="emit('update:user-auth-modes', $event)"
          />
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
            <span v-if="task.group_id">群 <strong class="mono">{{ task.group_id }}</strong></span>
            <span v-else>私聊 <strong class="mono">{{ task.user_id || "—" }}</strong></span>
            <span>{{ watchScopeLabel(task) }}</span>
            <span class="repository-access-fact">指定用户 <strong>{{ repositoryAccessCount(props.userAccess, task.repository) }}</strong></span>
            <span class="repository-access-fact">指定群聊 <strong>{{ repositoryAccessCount(props.groupAccess, task.repository) }}</strong></span>
          </div>
          <p v-if="task.last_error" class="repository-watch-manager-error">{{ task.last_error }}</p>
        </div>
        <div class="repository-watch-manager-actions">
          <button v-if="task.status !== 'cancelled'" class="btn small icon-only" type="button" title="编辑仓库和授权" aria-label="编辑仓库和授权" @click="startEdit(task)"><Pencil :size="14" aria-hidden="true" /></button>
          <button v-if="task.status !== 'cancelled'" class="btn small icon-only" type="button" title="取消订阅" aria-label="取消订阅" :disabled="busyID === task.id" @click="cancel(task)"><CircleX :size="14" aria-hidden="true" /></button>
          <button class="btn small ghost danger icon-only" type="button" title="删除订阅" aria-label="删除订阅" :disabled="busyID === task.id" @click="remove(task)"><Trash2 :size="14" aria-hidden="true" /></button>
        </div>
      </article>
    </div>
    <div v-else class="repository-watch-manager-empty repository-watch-manager-empty-guide">
      <strong>还没有仓库配置</strong>
      <span>点击“添加仓库”，填写仓库后即可配置该仓库的指定用户和指定群聊；不同仓库互不影响。</span>
      <button class="btn small ghost" type="button" @click="startCreate">
        <Plus :size="14" aria-hidden="true" />
        添加仓库并配置权限
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { CircleX, LoaderCircle, Pencil, Plus, Trash2 } from "@lucide/vue";
import {
  cancelRepositoryWatch,
  createRepositoryWatch,
  deleteRepositoryWatch,
  getAssistantTasks,
  getQQBotConfig,
  listQQBotGroups,
  updateRepositoryWatch,
  type AssistantTask,
  type AssistantTaskStatus,
  type QQBotConfig,
  type QQBotGroupSummary
} from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "./AppSelect.vue";
import RepositoryAccessEditor from "./RepositoryAccessEditor.vue";

const props = defineProps<{
  prepareAccess?: () => Promise<void>;
  tokenConfigured?: boolean;
  issueEnabledRepositories?: string[];
  userAccess?: string;
  groupAccess?: string;
  tokenUsers?: string;
  userAuthModes?: string;
  joinedGroups?: QQBotGroupSummary[];
  groupsLoading?: boolean;
  groupsWarning?: string;
}>();
const emit = defineEmits<{
  "update:issue-enabled-repositories": [string[]];
  "update:user-access": [string];
  "update:group-access": [string];
  "update:user-tokens": [string];
  "update:token-users": [string];
  "update:user-auth-modes": [string];
}>();
const authenticatedIntervalSeconds = 60;
const anonymousIntervalSeconds = 60 * 60;
const minimumIntervalSeconds = 30;
const maximumIntervalSeconds = 365 * 24 * 60 * 60;
const defaultIntervalSeconds = computed(() => props.tokenConfigured ? authenticatedIntervalSeconds : anonymousIntervalSeconds);
const emptyForm = () => ({ repository: "", branch: "", interval_seconds: defaultIntervalSeconds.value, watch_commits: true, watch_pull_requests: true, watch_releases: true, watch_stars: true, issue_enabled: false, profile_id: "", destination: "private" as "private" | "group", group_id: "", user_id: "" });
const watches = ref<AssistantTask[]>([]);
const profiles = ref<QQBotConfig[]>([]);
const joinedGroups = ref<QQBotGroupSummary[]>([]);
const loading = ref(false);
const saving = ref(false);
const busyID = ref("");
const editing = ref(false);
const editingTask = ref<AssistantTask | null>(null);
const form = ref(emptyForm());

const profileOptions = computed(() => profiles.value.map((profile) => ({ value: profile.id ?? "", label: profile.name || profile.platform || profile.id || "未命名机器人", hint: profile.platform })).filter((option) => option.value));
const selectedProfile = computed(() => profiles.value.find((profile) => profile.id === form.value.profile_id));
const groupOptions = computed(() => selectedProfile.value?.platform === "telegram" ? [] : joinedGroups.value.filter((group) => group.joined).map((group) => ({ value: group.group_id, label: group.group_name || `群 ${group.group_id}`, hint: group.group_name ? group.group_id : undefined })));

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [tasks, config, groups] = await Promise.all([getAssistantTasks(), getQQBotConfig(), listQQBotGroups().catch(() => ({ groups: [] }))]);
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

function repositoryKey(value: string): string {
  return value.trim().replace(/^https?:\/\/(www\.)?github\.com\//i, "").replace(/\.git\/?$/i, "").replace(/\/$/, "");
}

function repositoryAccessCount(value: string | undefined, repository: string | undefined): number {
  const target = repositoryKey(repository ?? "").toLowerCase();
  if (!target) return 0;
  const ids = new Set<string>();
  for (const line of String(value ?? "").split(/[;；\n\r]/)) {
    const entry = line.trim();
    if (!entry) continue;
    const separator = entry.indexOf("=");
    const id = (separator >= 0 ? entry.slice(0, separator) : entry).trim();
    const repositories = separator >= 0 ? entry.slice(separator + 1) : "";
    if (id && repositories.split(/[,，]/).some((item) => repositoryKey(item).toLowerCase() === target)) ids.add(id);
  }
  return ids.size;
}

function startEdit(task: AssistantTask): void {
  editingTask.value = task;
  const repository = task.repository ?? "";
  form.value = { repository, branch: task.repository_branch ?? "", interval_seconds: task.interval_seconds || defaultIntervalSeconds.value, watch_commits: task.watch_commits === true, watch_pull_requests: task.watch_pull_requests === true, watch_releases: task.watch_releases === true, watch_stars: task.watch_stars === true, issue_enabled: props.issueEnabledRepositories?.some((item) => repositoryKey(item).toLowerCase() === repositoryKey(repository).toLowerCase()) === true, profile_id: task.profile_id ?? "", destination: task.group_id ? "group" : "private", group_id: task.group_id ?? "", user_id: task.user_id ?? "" };
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
  if (!form.value.watch_commits && !form.value.watch_pull_requests && !form.value.watch_releases && !form.value.watch_stars) return toastError("Commit、PR、Release 和 Star 至少选择一项");
  if (!form.value.profile_id) return toastError("请选择发送机器人");
  if (form.value.destination === "group" && !form.value.group_id) return toastError("请填写群号或 Chat ID");
  if (form.value.destination === "private" && !form.value.user_id) return toastError("请填写私聊对象 ID");
  saving.value = true;
  try {
    await props.prepareAccess?.();
    const common = { repository: form.value.repository, branch: form.value.branch, interval_seconds: form.value.interval_seconds, watch_commits: form.value.watch_commits, watch_pull_requests: form.value.watch_pull_requests, watch_releases: form.value.watch_releases, watch_stars: form.value.watch_stars };
    const delivery = { profile_id: form.value.profile_id, destination: form.value.destination, group_id: form.value.destination === "group" ? form.value.group_id : undefined, user_id: form.value.destination === "private" ? form.value.user_id : undefined };
    const repository = repositoryKey(form.value.repository);
    const enabledRepositories = [...(props.issueEnabledRepositories ?? [])].filter((item) => repositoryKey(item).toLowerCase() !== repository.toLowerCase());
    if (form.value.issue_enabled && repository) enabledRepositories.push(repository);
    emit("update:issue-enabled-repositories", enabledRepositories);
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
function watchScopeLabel(task: AssistantTask): string { return [task.watch_commits ? "Commit" : "", task.watch_pull_requests ? "PR" : "", task.watch_releases ? "Release" : "", task.watch_stars ? "Star" : ""].filter(Boolean).join(" + "); }
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
