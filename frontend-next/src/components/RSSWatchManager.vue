<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="repository-watch-manager">
    <div class="repository-watch-manager-head">
      <div>
        <h3>订阅与判断</h3>
        <p>发现新条目后先交给 LLM 判断，只有符合规则时才向指定会话发送回复。</p>
      </div>
      <button class="btn small primary" type="button" @click="startCreate"><Plus :size="14" aria-hidden="true" />添加订阅</button>
    </div>

    <form v-if="editing" class="repository-watch-editor" @submit.prevent="save">
      <div class="form-grid repository-watch-form">
        <div class="field wide">
          <label>订阅来源</label>
          <div class="segmented repository-watch-destination" role="radiogroup" aria-label="订阅来源">
            <button type="button" role="radio" :aria-checked="form.source === 'twitter'" :class="{ active: form.source === 'twitter' }" @click="form.source = 'twitter'">Twitter / X 用户</button>
            <button type="button" role="radio" :aria-checked="form.source === 'rss'" :class="{ active: form.source === 'rss' }" @click="form.source = 'rss'">RSS / Atom</button>
          </div>
        </div>
        <div v-if="form.source === 'twitter'" class="field wide">
          <label for="rss-watch-handle">Twitter 用户</label>
          <input id="rss-watch-handle" v-model.trim="form.twitter_handle" class="input" type="text" placeholder="@tibo、tibo 或用户主页链接" />
          <span class="hint">直接填就行，默认读取 X 的公开时间线，不需要额外部署。想换成自建 RSSHub 等其他来源，可在「插件 → RSS 订阅」里填 Twitter RSS 模板。</span>
        </div>
        <div v-else class="field wide">
          <label for="rss-watch-url">Feed URL</label>
          <input id="rss-watch-url" v-model.trim="form.feed_url" class="input" type="url" placeholder="https://example.com/feed.xml" />
          <span class="hint">支持 RSS 2.0 与 Atom，只允许公网 http/https 地址。</span>
        </div>
        <div class="field wide">
          <label for="rss-watch-judge">判断与回复规则</label>
          <textarea id="rss-watch-judge" v-model.trim="form.judge_prompt" class="input rss-watch-rule" rows="4" placeholder="例如：仅当推文明确提到额度重置、恢复或刷新时通知；用中文说明具体时间、原文依据并附链接。"></textarea>
          <span class="hint">规则应同时写清“什么情况下通知”和“通知时回复什么”。不满足时不会发送消息。</span>
        </div>
        <div class="field">
          <label for="rss-watch-interval">检查周期</label>
          <div class="input-group"><input id="rss-watch-interval" v-model.number="form.interval_seconds" class="input" type="number" :min="minimumIntervalSeconds" :max="maximumIntervalSeconds" step="60" /><span class="repository-watch-unit">秒</span></div>
          <span class="hint">可设置 5 分钟至 365 天；默认 15 分钟。</span>
        </div>
        <div v-if="!editingTask" class="field">
          <label for="rss-watch-profile">发送机器人</label>
          <AppSelect id="rss-watch-profile" v-model="form.profile_id" :options="profileOptions" />
        </div>
        <div v-if="!editingTask" class="field wide">
          <label>通知位置</label>
          <div class="segmented repository-watch-destination" role="radiogroup" aria-label="通知位置">
            <button type="button" role="radio" :aria-checked="form.destination === 'private'" :class="{ active: form.destination === 'private' }" @click="form.destination = 'private'">私聊对象</button>
            <button type="button" role="radio" :aria-checked="form.destination === 'group'" :class="{ active: form.destination === 'group' }" @click="form.destination = 'group'">群聊</button>
          </div>
        </div>
        <div v-if="!editingTask && form.destination === 'group'" class="field wide">
          <label for="rss-watch-group">群号 / Chat ID</label>
          <AppSelect v-if="groupOptions.length" id="rss-watch-group" v-model="form.group_id" :options="groupOptions" />
          <input v-else id="rss-watch-group" v-model.trim="form.group_id" class="input" type="text" placeholder="群号或 Telegram 群 Chat ID" />
        </div>
        <div v-if="!editingTask && form.destination === 'private'" class="field wide">
          <label for="rss-watch-user">私聊对象 ID</label>
          <input id="rss-watch-user" v-model.trim="form.user_id" class="input" type="text" placeholder="账号或 Telegram Chat ID" />
          <AccountNameHint :user-id="form.user_id" :profile="form.profile_id" />
        </div>
      </div>
      <div class="repository-watch-editor-actions">
        <button class="btn small" type="button" :disabled="saving" @click="stopEditing">取消</button>
        <button class="btn small primary" type="submit" :disabled="saving"><LoaderCircle v-if="saving" :size="14" class="spin" aria-hidden="true" />{{ editingTask ? "保存订阅" : "创建订阅" }}</button>
      </div>
    </form>

    <div v-if="loading" class="repository-watch-manager-empty"><LoaderCircle :size="16" class="spin" aria-hidden="true" />正在加载订阅</div>
    <div v-else-if="watches.length" class="repository-watch-manager-list">
      <article v-for="task in watches" :key="task.id" class="repository-watch-manager-item">
        <div class="repository-watch-manager-main">
          <div class="cluster"><strong>{{ task.feed_source === 'twitter' ? `@${task.feed_handle}` : task.message }}</strong><span class="badge" :class="statusTone(task.status)">{{ statusLabel(task.status) }}</span></div>
          <p class="rss-watch-rule-summary">{{ task.feed_judge_prompt }}</p>
          <div class="task-facts"><span>每 {{ formatInterval(task.interval_seconds || defaultIntervalSeconds) }}</span><span v-if="task.group_id">群 <strong class="mono">{{ task.group_id }}</strong></span><span v-else>私聊 <strong class="mono">{{ task.user_id || '—' }}</strong></span><a v-if="task.feed_url" :href="task.feed_url" target="_blank" rel="noreferrer">打开 Feed</a></div>
          <p v-if="task.last_error" class="repository-watch-manager-error">{{ task.last_error }}</p>
        </div>
        <div class="repository-watch-manager-actions">
          <button v-if="task.status !== 'cancelled'" class="btn small" type="button" :disabled="busyID === task.id" @click="startEdit(task)"><Pencil :size="14" aria-hidden="true" />编辑</button>
          <button v-if="task.status !== 'cancelled'" class="btn small" type="button" :disabled="busyID === task.id" @click="cancel(task)"><CircleX :size="14" aria-hidden="true" />取消</button>
          <button class="btn small ghost danger" type="button" :disabled="busyID === task.id" @click="remove(task)"><Trash2 :size="14" aria-hidden="true" />删除</button>
        </div>
      </article>
    </div>
    <p v-else class="repository-watch-manager-empty">还没有 RSS 或 Twitter 订阅</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { CircleX, LoaderCircle, Pencil, Plus, Trash2 } from "@lucide/vue";
import { cancelRSSWatch, createRSSWatch, deleteRSSWatch, getAssistantTasks, getBotProfileConfig, listBotGroups, updateRSSWatch, type AssistantTask, type AssistantTaskStatus, type BotProfileConfig, type BotGroupSummary } from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AccountNameHint from "./AccountNameHint.vue";
import AppSelect from "./AppSelect.vue";

const props = defineProps<{ prepareAccess?: () => Promise<void> }>();
const minimumIntervalSeconds = 5 * 60;
const maximumIntervalSeconds = 365 * 24 * 60 * 60;
const defaultIntervalSeconds = 15 * 60;
const emptyForm = () => ({ source: "twitter" as "twitter" | "rss", twitter_handle: "", feed_url: "", judge_prompt: "", interval_seconds: defaultIntervalSeconds, profile_id: "", destination: "private" as "private" | "group", group_id: "", user_id: "" });
const watches = ref<AssistantTask[]>([]);
const profiles = ref<BotProfileConfig[]>([]);
const joinedGroups = ref<BotGroupSummary[]>([]);
const loading = ref(false), saving = ref(false), busyID = ref(""), editing = ref(false);
const editingTask = ref<AssistantTask | null>(null);
const editorSnapshot = ref("");
const form = ref(emptyForm());
const profileOptions = computed(() => profiles.value.map((profile) => ({ value: profile.id ?? "", label: profile.name || profile.platform || profile.id || "未命名机器人", hint: profile.platform })).filter((option) => option.value));
const selectedProfile = computed(() => profiles.value.find((profile) => profile.id === form.value.profile_id));
const groupOptions = computed(() => selectedProfile.value?.platform === "telegram" ? [] : joinedGroups.value.filter((group) => group.joined).map((group) => ({ value: group.group_id, label: group.group_name || `群 ${group.group_id}`, hint: group.group_name ? group.group_id : undefined })));

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [tasks, config, groups] = await Promise.all([getAssistantTasks(), getBotProfileConfig(), listBotGroups().catch(() => ({ groups: [] }))]);
    watches.value = tasks.items.filter((task) => task.kind === "rss_watch"); profiles.value = config.profiles?.length ? config.profiles : [config]; joinedGroups.value = groups.groups;
    if (!form.value.profile_id) form.value.profile_id = profiles.value[0]?.id || "";
  } catch (error) { toastError(error instanceof Error ? error.message : "RSS 订阅加载失败"); } finally { loading.value = false; }
}
function startCreate(): void { const profileID = form.value.profile_id || profiles.value[0]?.id || ""; form.value = { ...emptyForm(), profile_id: profileID }; editingTask.value = null; editing.value = true; markEditorClean(); }

// 和仓库编辑器同样的处理：改动只在本地表单里，关掉之前必须问一次。
function markEditorClean(): void { editorSnapshot.value = JSON.stringify(form.value); }

function editorDirty(): boolean { return editing.value && JSON.stringify(form.value) !== editorSnapshot.value; }

defineExpose({ hasUnsavedChanges: editorDirty });
function startEdit(task: AssistantTask): void { editingTask.value = task; form.value = { source: task.feed_source ?? "rss", twitter_handle: task.feed_handle ?? "", feed_url: task.feed_url ?? "", judge_prompt: task.feed_judge_prompt ?? "", interval_seconds: task.interval_seconds || defaultIntervalSeconds, profile_id: task.profile_id ?? "", destination: task.group_id ? "group" : "private", group_id: task.group_id ?? "", user_id: task.user_id ?? "" }; editing.value = true; markEditorClean(); }
async function stopEditing(): Promise<void> {
  if (saving.value) return;
  if (editorDirty() && !(await askConfirm({ title: "放弃未保存的订阅配置？", message: "这条 RSS 订阅的改动还没保存，关闭后会丢失。", confirmLabel: "放弃改动", danger: true }))) return;
  editing.value = false;
  editingTask.value = null;
  editorSnapshot.value = "";
}
async function save(): Promise<void> {
  if (form.value.source === "twitter" && !form.value.twitter_handle) return toastError("请填写 Twitter 用户");
  if (form.value.source === "rss" && !form.value.feed_url) return toastError("请填写 Feed URL");
  if (!form.value.judge_prompt) return toastError("请填写判断与回复规则");
  if (form.value.interval_seconds < minimumIntervalSeconds || form.value.interval_seconds > maximumIntervalSeconds) return toastError("检查周期必须在 5 分钟到 365 天之间");
  if (!editingTask.value && !form.value.profile_id) return toastError("请选择发送机器人");
  if (!editingTask.value && form.value.destination === "group" && !form.value.group_id) return toastError("请填写群号或 Chat ID");
  if (!editingTask.value && form.value.destination === "private" && !form.value.user_id) return toastError("请填写私聊对象 ID");
  saving.value = true;
  try {
    await props.prepareAccess?.();
    const source = form.value.source === "twitter" ? { twitter_handle: form.value.twitter_handle } : { feed_url: form.value.feed_url };
    const common = { ...source, judge_prompt: form.value.judge_prompt, interval_seconds: form.value.interval_seconds };
    if (editingTask.value) await updateRSSWatch(editingTask.value.id, common); else await createRSSWatch({ ...common, profile_id: form.value.profile_id, destination: form.value.destination, group_id: form.value.destination === "group" ? form.value.group_id : undefined, user_id: form.value.destination === "private" ? form.value.user_id : undefined });
    toastSuccess(editingTask.value ? "RSS 订阅已更新" : "RSS 订阅已创建，当前内容已作为基线"); editing.value = false; editingTask.value = null; await load();
  } catch (error) { toastError(error instanceof Error ? error.message : "RSS 订阅保存失败"); } finally { saving.value = false; }
}
async function cancel(task: AssistantTask): Promise<void> { if (!await askConfirm({ title: "取消 RSS 订阅", message: `停止 ${task.feed_handle ? `@${task.feed_handle}` : task.message} 的订阅？`, confirmLabel: "取消订阅", danger: true })) return; busyID.value = task.id; try { await cancelRSSWatch(task.id); toastSuccess("RSS 订阅已取消"); await load(); } catch (error) { toastError(error instanceof Error ? error.message : "取消失败"); } finally { busyID.value = ""; } }
async function remove(task: AssistantTask): Promise<void> { if (!await askConfirm({ title: "删除 RSS 订阅", message: `永久删除 ${task.feed_handle ? `@${task.feed_handle}` : task.message} 的订阅记录？`, confirmLabel: "删除", danger: true })) return; busyID.value = task.id; try { await deleteRSSWatch(task.id); toastSuccess("RSS 订阅已删除"); await load(); } catch (error) { toastError(error instanceof Error ? error.message : "删除失败"); } finally { busyID.value = ""; } }
function statusLabel(value: AssistantTaskStatus): string { return { active: "运行中", retrying: "重试中", used: "已执行", cancelled: "已取消" }[value] ?? value; }
function statusTone(value: AssistantTaskStatus): string { return value === "active" ? "ok" : value === "retrying" ? "warn" : value === "cancelled" ? "err" : ""; }
function formatInterval(seconds: number): string { return seconds % 86400 === 0 ? `${seconds / 86400} 天` : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : seconds % 60 === 0 ? `${seconds / 60} 分钟` : `${seconds} 秒`; }
onMounted(() => void load());
</script>
