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
          <label for="rss-watch-platform">订阅平台</label>
          <AppSelect id="rss-watch-platform" v-model="form.platform" :options="platformOptions" />
          <span class="hint">{{ platformField.summary }}</span>
        </div>
        <div class="field wide">
          <label for="rss-watch-target">{{ platformField.label }}</label>
          <input id="rss-watch-target" v-model.trim="form.target" class="input" :type="form.platform === 'rss' ? 'url' : 'text'" :placeholder="platformField.placeholder" />
          <span class="hint">{{ platformField.hint }}</span>
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
          <div class="cluster"><strong>{{ watchTitle(task) }}</strong><span class="badge">{{ platformLabel(task.feed_source) }}</span><span class="badge" :class="statusTone(task.status)">{{ statusLabel(task.status) }}</span></div>
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
    <p v-else class="repository-watch-manager-empty">还没有任何内容订阅</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { CircleX, LoaderCircle, Pencil, Plus, Trash2 } from "@lucide/vue";
import { cancelRSSWatch, createRSSWatch, deleteRSSWatch, getAssistantTasks, getBotProfileConfig, listBotGroups, updateRSSWatch, type AssistantTask, type AssistantTaskStatus, type BotProfileConfig, type BotGroupSummary, type RSSWatchPlatform } from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "./AppSelect.vue";

const props = defineProps<{ prepareAccess?: () => Promise<void> }>();
const minimumIntervalSeconds = 5 * 60;
const maximumIntervalSeconds = 365 * 24 * 60 * 60;
const defaultIntervalSeconds = 15 * 60;
type SelectablePlatform = Exclude<RSSWatchPlatform, "twitter">;
// 平台内容由后端内置抓取器直接读取，不依赖 RSSHub；想走自建中转就选「自定义 RSS / Atom」。
const platformFields: Record<SelectablePlatform, { label: string; summary: string; placeholder: string; hint: string }> = {
  x: { label: "X 用户", summary: "内置抓取 X 公开时间线，无需账号。", placeholder: "@tibo、tibo 或用户主页链接", hint: "被限流时可在上方填写自定义 Feed 模板，改走自建中转。" },
  bilibili: { label: "UP 主 UID", summary: "内置抓取 UP 主投稿；配置 Cookie 后改抓完整动态。", placeholder: "2267573 或 https://space.bilibili.com/2267573", hint: "填 UID 数字或空间主页链接。" },
  douyin: { label: "抖音用户", summary: "用本机无头浏览器打开主页并截取官方接口，需要装有 Chrome/Chromium。", placeholder: "https://www.douyin.com/user/MS4wLjABAAAA…", hint: "填用户主页链接，或链接里 MS4wLjABAAAA 开头的 sec_uid。" },
  xiaohongshu: { label: "小红书用户", summary: "内置读取主页服务端渲染数据；配置 Cookie 后能带上笔记直达链接。", placeholder: "593032945e87e77791e03696 或用户主页链接", hint: "填主页链接末尾的 24 位用户 ID。" },
  github: { label: "GitHub 仓库或用户", summary: "内置读取 GitHub 官方 Atom 源，无需 Token。", placeholder: "SuInk/Diana 或 SuInk/Diana/commits/main", hint: "owner/repo 默认跟 Release；也可写 owner/repo/commits[/分支]、owner/repo/tags 或单个用户名。" },
  rss: { label: "Feed URL", summary: "任意 RSS 2.0 或 Atom 地址，包括自建 RSSHub。", placeholder: "https://example.com/feed.xml", hint: "只允许公网 http/https 地址。" }
};
const platformOptions = [
  { value: "x", label: "X (Twitter)" },
  { value: "bilibili", label: "哔哩哔哩" },
  { value: "douyin", label: "抖音" },
  { value: "xiaohongshu", label: "小红书" },
  { value: "github", label: "GitHub" },
  { value: "rss", label: "自定义 RSS / Atom" }
];
function selectablePlatform(value?: RSSWatchPlatform | string): SelectablePlatform {
  const platform = value === "twitter" ? "x" : value ?? "";
  return platform in platformFields ? (platform as SelectablePlatform) : "rss";
}
const emptyForm = () => ({ platform: "x" as SelectablePlatform, target: "", judge_prompt: "", interval_seconds: defaultIntervalSeconds, profile_id: "", destination: "private" as "private" | "group", group_id: "", user_id: "" });
const watches = ref<AssistantTask[]>([]);
const profiles = ref<BotProfileConfig[]>([]);
const joinedGroups = ref<BotGroupSummary[]>([]);
const loading = ref(false), saving = ref(false), busyID = ref(""), editing = ref(false);
const editingTask = ref<AssistantTask | null>(null);
const form = ref(emptyForm());
const platformField = computed(() => platformFields[form.value.platform]);
const profileOptions = computed(() => profiles.value.map((profile) => ({ value: profile.id ?? "", label: profile.name || profile.platform || profile.id || "未命名机器人", hint: profile.platform })).filter((option) => option.value));
const selectedProfile = computed(() => profiles.value.find((profile) => profile.id === form.value.profile_id));
const groupOptions = computed(() => selectedProfile.value?.platform === "telegram" ? [] : joinedGroups.value.filter((group) => group.joined).map((group) => ({ value: group.group_id, label: group.group_name || `群 ${group.group_id}`, hint: group.group_name ? group.group_id : undefined })));

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [tasks, config, groups] = await Promise.all([getAssistantTasks(), getBotProfileConfig(), listBotGroups().catch(() => ({ groups: [] }))]);
    watches.value = tasks.items.filter((task) => task.kind === "rss_watch"); profiles.value = config.profiles?.length ? config.profiles : [config]; joinedGroups.value = groups.groups;
    if (!form.value.profile_id) form.value.profile_id = config.active_profile_id || profiles.value[0]?.id || "";
  } catch (error) { toastError(error instanceof Error ? error.message : "RSS 订阅加载失败"); } finally { loading.value = false; }
}
function startCreate(): void { const profileID = form.value.profile_id || profiles.value[0]?.id || ""; form.value = { ...emptyForm(), profile_id: profileID }; editingTask.value = null; editing.value = true; }
function startEdit(task: AssistantTask): void {
  const platform = selectablePlatform(task.feed_source);
  editingTask.value = task;
  form.value = { platform, target: task.feed_handle || (platform === "rss" ? task.feed_url ?? "" : ""), judge_prompt: task.feed_judge_prompt ?? "", interval_seconds: task.interval_seconds || defaultIntervalSeconds, profile_id: task.profile_id ?? "", destination: task.group_id ? "group" : "private", group_id: task.group_id ?? "", user_id: task.user_id ?? "" };
  editing.value = true;
}
function stopEditing(): void { if (saving.value) return; editing.value = false; editingTask.value = null; }
async function save(): Promise<void> {
  if (!form.value.target) return toastError(`请填写${platformField.value.label}`);
  if (!form.value.judge_prompt) return toastError("请填写判断与回复规则");
  if (form.value.interval_seconds < minimumIntervalSeconds || form.value.interval_seconds > maximumIntervalSeconds) return toastError("检查周期必须在 5 分钟到 365 天之间");
  if (!editingTask.value && !form.value.profile_id) return toastError("请选择发送机器人");
  if (!editingTask.value && form.value.destination === "group" && !form.value.group_id) return toastError("请填写群号或 Chat ID");
  if (!editingTask.value && form.value.destination === "private" && !form.value.user_id) return toastError("请填写私聊对象 ID");
  saving.value = true;
  try {
    await props.prepareAccess?.();
    const common = { platform: form.value.platform, target: form.value.target, judge_prompt: form.value.judge_prompt, interval_seconds: form.value.interval_seconds };
    if (editingTask.value) await updateRSSWatch(editingTask.value.id, common); else await createRSSWatch({ ...common, profile_id: form.value.profile_id, destination: form.value.destination, group_id: form.value.destination === "group" ? form.value.group_id : undefined, user_id: form.value.destination === "private" ? form.value.user_id : undefined });
    toastSuccess(editingTask.value ? "RSS 订阅已更新" : "RSS 订阅已创建，当前内容已作为基线"); editing.value = false; editingTask.value = null; await load();
  } catch (error) { toastError(error instanceof Error ? error.message : "RSS 订阅保存失败"); } finally { saving.value = false; }
}
async function cancel(task: AssistantTask): Promise<void> { if (!await askConfirm({ title: "取消 RSS 订阅", message: `停止 ${watchTitle(task)} 的订阅？`, confirmLabel: "取消订阅", danger: true })) return; busyID.value = task.id; try { await cancelRSSWatch(task.id); toastSuccess("RSS 订阅已取消"); await load(); } catch (error) { toastError(error instanceof Error ? error.message : "取消失败"); } finally { busyID.value = ""; } }
async function remove(task: AssistantTask): Promise<void> { if (!await askConfirm({ title: "删除 RSS 订阅", message: `永久删除 ${watchTitle(task)} 的订阅记录？`, confirmLabel: "删除", danger: true })) return; busyID.value = task.id; try { await deleteRSSWatch(task.id); toastSuccess("RSS 订阅已删除"); await load(); } catch (error) { toastError(error instanceof Error ? error.message : "删除失败"); } finally { busyID.value = ""; } }
function platformLabel(value?: RSSWatchPlatform): string { return platformOptions.find((option) => option.value === selectablePlatform(value))?.label ?? "自定义 RSS / Atom"; }
function watchTitle(task: AssistantTask): string {
  const platform = selectablePlatform(task.feed_source);
  if (!task.feed_handle) return task.message || task.feed_url || "未命名订阅";
  return platform === "x" ? `@${task.feed_handle}` : task.feed_handle;
}
function statusLabel(value: AssistantTaskStatus): string { return { active: "运行中", retrying: "重试中", used: "已执行", cancelled: "已取消" }[value] ?? value; }
function statusTone(value: AssistantTaskStatus): string { return value === "active" ? "ok" : value === "retrying" ? "warn" : value === "cancelled" ? "err" : ""; }
function formatInterval(seconds: number): string { return seconds % 86400 === 0 ? `${seconds / 86400} 天` : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : seconds % 60 === 0 ? `${seconds / 60} 分钟` : `${seconds} 秒`; }
onMounted(() => void load());
</script>
