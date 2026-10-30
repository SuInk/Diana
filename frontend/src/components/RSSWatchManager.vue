<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="repository-watch-manager">
    <div class="repository-watch-manager-head">
      <div>
        <h3>订阅与判断</h3>
        <p>选择全部推送、正则匹配或 LLM 判断。</p>
      </div>
      <button class="btn small primary" type="button" @click="startCreate"><Plus :size="14" aria-hidden="true" />添加订阅</button>
    </div>

    <form v-if="editing" class="repository-watch-editor" @submit.prevent="saveEditor">
      <div class="form-grid repository-watch-form">
        <div class="field wide">
          <label>订阅来源</label>
          <div class="segmented repository-watch-destination" role="radiogroup" aria-label="订阅来源">
            <button type="button" role="radio" :aria-checked="form.source === 'twitter'" :class="{ active: form.source === 'twitter' }" @click="form.source = 'twitter'">Twitter / X 用户</button>
            <button type="button" role="radio" :aria-checked="form.source === 'rss'" :class="{ active: form.source === 'rss' }" @click="form.source = 'rss'">RSS / Atom</button>
            <button type="button" role="radio" :aria-checked="form.source === 'bilibili'" :class="{ active: form.source === 'bilibili' }" @click="form.source = 'bilibili'">B 站</button>
          </div>
        </div>
        <div v-if="form.source === 'twitter'" class="field wide">
          <label for="rss-watch-handle-0">Twitter 用户</label>
          <div class="target-list">
            <div v-for="(_, index) in form.twitter_handles" :key="`handle-${index}`" class="rss-source-row">
              <input :id="`rss-watch-handle-${index}`" v-model.trim="form.twitter_handles[index]" class="input" type="text" placeholder="@tibo、tibo 或用户主页链接" :aria-label="`Twitter 用户 ${index + 1}`" />
              <button v-if="form.twitter_handles.length > 1" class="btn small ghost danger icon-only" type="button" title="移除这个账号" aria-label="移除这个账号" @click="removeSource('twitter', index)"><Trash2 :size="14" aria-hidden="true" /></button>
            </div>
            <button v-if="form.twitter_handles.length < maximumSources" class="btn small ghost" type="button" @click="addSource('twitter')"><Plus :size="14" aria-hidden="true" />添加账号</button>
          </div>
          <span class="hint">直接填就行，默认读取 X 的公开时间线，不需要额外部署。可以一次填多个账号，它们共用下面这一套规则，命中的内容会合成一条通知发出，最多 {{ maximumSources }} 个。</span>
        </div>
        <div v-else-if="form.source === 'bilibili'" class="field wide">
          <label for="rss-watch-bili-0">B 站来源</label>
          <div class="target-list">
            <div v-for="(_, index) in form.bilibili" :key="`bili-${index}`" class="rss-source-row">
              <input :id="`rss-watch-bili-${index}`" v-model.trim="form.bilibili[index]" class="input" type="text" placeholder="dynamic:UID、live:UID 或 bangumi:ss12345" :aria-label="`B 站来源 ${index + 1}`" />
              <button v-if="form.bilibili.length > 1" class="btn small ghost danger icon-only" type="button" title="移除这个来源" aria-label="移除这个来源" @click="removeSource('bilibili', index)"><Trash2 :size="14" aria-hidden="true" /></button>
            </div>
            <button v-if="form.bilibili.length < maximumSources" class="btn small ghost" type="button" @click="addSource('bilibili')"><Plus :size="14" aria-hidden="true" />添加来源</button>
          </div>
          <span class="hint">动态填 dynamic:UID 或空间链接，包括新投稿；直播填 live:UID 或直播间链接，开播时通知一次；番剧填 bangumi:ss/ep 编号或播放页链接，新集上线时通知。动态接口容易被风控，建议在链接解析插件里配置 B 站 SESSDATA。最多 {{ maximumSources }} 个。</span>
        </div>
        <div v-else class="field wide">
          <label for="rss-watch-url-0">Feed URL</label>
          <div class="target-list">
            <div v-for="(_, index) in form.feed_urls" :key="`feed-${index}`" class="rss-source-row">
              <input :id="`rss-watch-url-${index}`" v-model.trim="form.feed_urls[index]" class="input" type="url" placeholder="https://example.com/feed.xml" :aria-label="`Feed URL ${index + 1}`" />
              <button v-if="form.feed_urls.length > 1" class="btn small ghost danger icon-only" type="button" title="移除这个 Feed" aria-label="移除这个 Feed" @click="removeSource('rss', index)"><Trash2 :size="14" aria-hidden="true" /></button>
            </div>
            <button v-if="form.feed_urls.length < maximumSources" class="btn small ghost" type="button" @click="addSource('rss')"><Plus :size="14" aria-hidden="true" />添加 Feed</button>
          </div>
          <span class="hint">支持 RSS 2.0 与 Atom，只允许公网 http/https 地址。可以一次填多个 Feed，它们共用下面这一套规则，最多 {{ maximumSources }} 个。</span>
        </div>
        <div class="field">
          <label for="rss-watch-mode">推送方式</label>
          <AppSelect id="rss-watch-mode" v-model="form.delivery_mode" :options="deliveryModeOptions" aria-label="推送方式" />
          <span v-if="form.delivery_mode === 'all'" class="hint">不筛选，发送全部新条目。</span>
        </div>
        <div v-if="form.delivery_mode === 'llm'" class="field">
          <label for="rss-watch-judge-model">判断模型</label>
          <AppSelect id="rss-watch-judge-model" v-model="form.judge_model_id" :options="judgeModelOptions" aria-label="判断模型" />
          <span class="hint">默认跟随通知机器人的对话模型。</span>
        </div>
        <div v-if="form.delivery_mode !== 'all'" class="field wide">
          <div class="rss-watch-field-label">
            <label for="rss-watch-judge">{{ form.delivery_mode === "regex" ? "正则表达式" : "判断与回复规则" }}</label>
            <button class="btn small ghost" type="button" @click="openAssist(form.delivery_mode === 'regex' ? 'regex' : 'judge')"><Sparkles :size="13" aria-hidden="true" />AI 帮写</button>
          </div>
          <textarea id="rss-watch-judge" v-model.trim="form.judge_prompt" class="input rss-watch-rule" rows="5" required :placeholder="form.delivery_mode === 'regex' ? '例如：(?i)release|更新|发布' : '例如：仅在正式版发布时通知。默认发送原文；需要处理时直接写“翻译成中文”或“总结三点”。'"></textarea>
          <span v-if="form.delivery_mode === 'regex'" class="hint">匹配标题和正文，命中后发送原文；筛选不调用模型。使用 Go 正则语法，支持 (?i) 忽略大小写；不支持前后查找和反向引用。</span>
          <span v-else class="hint">模型默认同时判断文字、图片、视频画面和音频。未明确要求加工时发送原文；明确要求翻译、总结或改写时才发送处理后的内容。分析工具不可用时退回文字判断，原文链接和媒体会自动保留。</span>
        </div>
        <div class="field">
          <label for="rss-watch-interval">检查周期</label>
          <div class="input-group"><input id="rss-watch-interval" v-model.number="form.interval_seconds" class="input" type="number" :min="minimumIntervalSeconds" :max="maximumIntervalSeconds" step="60" /><span class="repository-watch-unit">秒</span></div>
          <span class="hint">可设置 5 分钟至 365 天；默认 15 分钟。</span>
        </div>
        <div class="field wide"><label>通知目标</label><SubscriptionTargetsEditor v-model="form.notification_targets" :profiles="profiles" :default-profile="form.profile_id" :plugin-ids='["official.rss-watch"]' /></div>
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
          <div class="cluster"><strong>{{ watchTitle(task) }}</strong><span v-if="sourceCount(task) > 1" class="badge">{{ sourceCount(task) }} 个来源</span><span class="badge" :class="statusTone(task.status)">{{ statusLabel(task.status) }}</span></div>
          <p class="rss-watch-rule-summary">{{ task.feed_rule_mode === "regex" ? "正则匹配 · " + task.feed_judge_prompt : task.feed_judge_prompt || "全部新条目 · 不筛选" }}</p>
          <div class="task-facts"><span>每 {{ formatInterval(task.interval_seconds || defaultIntervalSeconds) }}</span><SubscriptionDestination v-for="(target, index) in task.notification_targets?.length ? task.notification_targets : [{ profile_id: task.profile_id, platform: task.platform, group_id: task.group_id, user_id: task.user_id }]" :key="index" :platform="target.platform || profiles.find(p => p.id === target.profile_id)?.platform" :profile-id="target.profile_id" :profile-name="profiles.find(p => p.id === target.profile_id)?.name" :group-id="target.group_id" :user-id="target.user_id" /><a v-for="source in taskSources(task)" :key="source.feed_url" :href="sourceHref(source)" target="_blank" rel="noreferrer" style="overflow-wrap: anywhere; max-width: 100%">{{ sourceLabel(source) }}</a></div>
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

    <Modal v-if="assistComposer" :title="assistTitle" initial-focus="#rss-assist-request" @close="closeAssist">
      <div class="stack" style="gap: 12px">
        <div class="field">
          <label for="rss-assist-request">{{ assistRequestLabel }}</label>
          <textarea id="rss-assist-request" v-model="assistComposer.request" class="textarea" rows="4" :placeholder="assistPlaceholder" @keydown.ctrl.enter.prevent="runAssist" @keydown.meta.enter.prevent="runAssist"></textarea>
          <span class="hint">写自然语言即可；生成结果会先在这里预览，不会自动保存订阅。</span>
        </div>
        <div class="field">
          <label>用哪个模型写</label>
          <div class="persona-composer-route">
            <AppSelect :model-value="assistChannelValue" :options="assistChannelOptions" placeholder="请选择提供商 / 分组" @update:model-value="setAssistChannel" />
            <AppSelect :model-value="assistModelValue" :options="assistModelOptions" :disabled="!assistRoute" :placeholder="assistRoute ? '请选择模型' : '跟随对话模型'" @update:model-value="setAssistModel" />
          </div>
          <span class="hint">默认跟随通知机器人的对话模型，也可以只为这次帮写指定其他模型。</span>
        </div>
        <div v-if="assistComposer.result" class="field">
          <label for="rss-assist-result">生成预览</label>
          <textarea id="rss-assist-result" :value="assistComposer.result" class="textarea rss-assist-preview" rows="5" readonly></textarea>
          <span class="hint">确认后点“应用到规则”；之后仍需保存订阅才会生效。</span>
        </div>
      </div>
      <template #footer>
        <button class="btn small" type="button" :disabled="assistBusy" @click="closeAssist">取消</button>
        <button class="btn small" type="button" :disabled="assistBusy || !assistComposer.request.trim()" @click="runAssist"><LoaderCircle v-if="assistBusy" :size="14" class="spin" aria-hidden="true" />{{ assistBusy ? "生成中…" : assistComposer.result ? "重新生成" : "生成草稿" }}</button>
        <button v-if="assistComposer.result" class="btn primary small" type="button" :disabled="assistBusy" @click="applyAssist">应用到规则</button>
      </template>
    </Modal>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { CircleX, LoaderCircle, Pencil, Plus, Sparkles, Trash2 } from "@lucide/vue";
import { assistRSSRule, cancelRSSWatch, createRSSWatch, deleteRSSWatch, getAssistantTasks, getBotProfileConfig, getConfig, updateRSSWatch, type AssistantTask, type AssistantTaskStatus, type BotProfileConfig, type LLMConfig, type RSSWatchSource } from "../api";
import { askConfirm } from "../lib/confirm";
import { toastError, toastSuccess } from "../lib/toast";
import AppSelect, { type AppSelectOption } from "./AppSelect.vue";
import Modal from "./Modal.vue";
import SubscriptionTargetsEditor from "./SubscriptionTargetsEditor.vue";
import SubscriptionDestination from "./SubscriptionDestination.vue";
import { rssSourceLabel } from "../lib/rss-display";

const props = defineProps<{ prepareAccess?: () => Promise<void>; defaultProfileId?: string }>();
const minimumIntervalSeconds = 5 * 60;
const maximumIntervalSeconds = 365 * 24 * 60 * 60;
const defaultIntervalSeconds = 15 * 60;
// 和后端 maximumRSSWatchSources 保持一致：再多就该拆成两条订阅。
const maximumSources = 10;
const deliveryModeOptions = [{ value: "all", label: "全部推送" }, { value: "regex", label: "正则匹配" }, { value: "llm", label: "LLM 判断" }];
const emptyForm = () => ({ notification_targets: [] as import("../api").RepositoryWatchTarget[], source: "twitter" as SourceKind, twitter_handles: [""], feed_urls: [""], bilibili: [""], delivery_mode: "llm", judge_prompt: "", judge_model_id: "", interval_seconds: defaultIntervalSeconds, profile_id: "" });
const watches = ref<AssistantTask[]>([]);
const profiles = ref<BotProfileConfig[]>([]);
const llmChannels = ref<LLMConfig[]>([]);
const loading = ref(false), saving = ref(false), busyID = ref(""), editing = ref(false);
type AssistKind = "regex" | "judge";
type AssistRoute = { profile_id?: string; group?: string; model: string };
const assistComposer = ref<{ kind: AssistKind; request: string; result: string } | null>(null);
const assistRoute = ref<AssistRoute | undefined>(undefined);
const assistBusy = ref(false);
const assistLoaded = ref(false);
const followChatValue = "__follow_chat__";
const groupPrefix = "group:";
const editingTask = ref<AssistantTask | null>(null);
const editorSnapshot = ref("");
const form = ref(emptyForm());

const judgeModelOptions = computed<AppSelectOption[]>(() => {
  const options: AppSelectOption[] = [{ value: "", label: "跟随对话" }];
  for (const channel of llmChannels.value) {
    if (!channel.id || ["image", "embedding", "tts", "stt", "video"].includes(channel.group || "")) continue;
    options.push({ value: channel.id, label: `${channel.name || channel.id} · ${channel.model || "默认模型"}` });
  }
  const selected = form.value.judge_model_id;
  if (selected && !options.some((option) => option.value === selected)) options.push({ value: selected, label: `配置已不可用：${selected}` });
  return options;
});

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [tasks, config, models] = await Promise.all([getAssistantTasks(), getBotProfileConfig(), getConfig()]);
    llmChannels.value = models.profiles?.length ? models.profiles : [models];
    assistLoaded.value = true;
    watches.value = tasks.items.filter((task) => task.kind === "rss_watch"); profiles.value = (config.profiles?.length ? config.profiles : [config]);
    if (!form.value.profile_id) form.value.profile_id = props.defaultProfileId || profiles.value[0]?.id || "";
  } catch (error) { toastError(error instanceof Error ? error.message : "RSS 订阅加载失败"); } finally { loading.value = false; }
}
function startCreate(): void { const profileID = props.defaultProfileId || form.value.profile_id || profiles.value[0]?.id || ""; form.value = { ...emptyForm(), profile_id: profileID }; editingTask.value = null; editing.value = true; markEditorClean(); }

// 和仓库编辑器同样的处理：改动只在本地表单里，关掉之前必须问一次。
function markEditorClean(): void { editorSnapshot.value = JSON.stringify(form.value); }

function editorDirty(): boolean { return editing.value && JSON.stringify(form.value) !== editorSnapshot.value; }

// 外层弹窗的「保存」也要能把正在编辑的订阅一起提交，否则点了保存订阅却没落库。
defineExpose({ hasUnsavedChanges: editorDirty, saveEditor });
function taskSources(task: AssistantTask): RSSWatchSource[] {
  // 老任务没有 feed_sources，按单来源字段回落，编辑框不会一打开就空一格。
  if (task.feed_sources?.length) return task.feed_sources;
  return task.feed_url ? [{ feed_url: task.feed_url, source: task.feed_source, handle: task.feed_handle }] : [];
}
function sourceCount(task: AssistantTask): number { return taskSources(task).length; }
type SourceKind = "twitter" | "rss" | "bilibili";
function isBilibiliSource(source?: string): boolean { return (source ?? "").startsWith("bilibili_"); }
// 编辑框里显示用户填过的短写法，不显示接口地址；后端两种都认。
function bilibiliInputValue(source: RSSWatchSource): string {
  const handle = (source.handle ?? "").trim();
  if (source.source === "bilibili_dynamic") return `dynamic:${handle}`;
  if (source.source === "bilibili_bangumi") return `bangumi:${handle}`;
  if (handle.startsWith("UID ")) return `live:${handle.slice(4)}`;
  if (handle.startsWith("直播间 ")) return `https://live.bilibili.com/${handle.slice(4)}`;
  return source.feed_url;
}
function sourceHref(source: RSSWatchSource): string {
  const handle = (source.handle ?? "").trim();
  if (source.source === "bilibili_dynamic") return `https://space.bilibili.com/${handle}/dynamic`;
  if (source.source === "bilibili_bangumi") return `https://www.bilibili.com/bangumi/play/${handle}`;
  if (handle.startsWith("UID ")) return `https://space.bilibili.com/${handle.slice(4)}`;
  if (handle.startsWith("直播间 ")) return `https://live.bilibili.com/${handle.slice(4)}`;
  return source.feed_url;
}
function sourceLabel(source: RSSWatchSource): string { return rssSourceLabel(source); }
function watchTitle(task: AssistantTask): string { const sources = taskSources(task); return sources.length > 1 ? sources.map(sourceLabel).join("、") : sources.length === 1 ? sourceLabel(sources[0]) : task.message; }
function sourceList(kind: SourceKind): string[] { return kind === "twitter" ? form.value.twitter_handles : kind === "bilibili" ? form.value.bilibili : form.value.feed_urls; }
function addSource(kind: SourceKind): void { const list = sourceList(kind); if (list.length < maximumSources) list.push(""); }
function removeSource(kind: SourceKind, index: number): void { const list = sourceList(kind); if (list.length > 1) list.splice(index, 1); }
function filledSources(values: string[]): string[] { return values.map((value) => value.trim()).filter((value) => value !== ""); }

const assistTitle = computed(() => assistComposer.value?.kind === "regex" ? "AI 帮写正则表达式" : "AI 帮写判断与回复规则");
const assistRequestLabel = computed(() => assistComposer.value?.kind === "regex" ? "想筛选什么内容" : "想在什么情况下通知，是否需要处理原文");
const assistPlaceholder = computed(() => assistComposer.value?.kind === "regex" ? "例如：只匹配正式版、稳定版和版本发布" : "例如：仅通知已正式发布的新版本；翻译成中文并总结三个要点");
const assistChannelOptions = computed<AppSelectOption[]>(() => {
  const options: AppSelectOption[] = [{ value: followChatValue, label: "跟随对话", hint: "使用通知机器人的对话模型" }];
  const groups = new Map<string, number>();
  for (const channel of llmChannels.value) { const group = channel.group?.trim() || "default"; groups.set(group, (groups.get(group) ?? 0) + 1); }
  for (const [group, count] of groups) if (count > 1) options.push({ value: groupPrefix + group, label: `${group === "default" ? "默认分组" : group}（提供商分组）`, hint: `${count} 个提供商按顺序降级` });
  for (const channel of llmChannels.value) if (channel.id) options.push({ value: channel.id, label: channel.name || channel.id, hint: `${channel.models?.filter(model => !model.disabled).length || (channel.model ? 1 : 0)} 个模型` });
  return options;
});
const assistChannelValue = computed(() => assistRoute.value ? (assistRoute.value.group ? groupPrefix + assistRoute.value.group : assistRoute.value.profile_id || "") : followChatValue);
const assistModelOptions = computed<AppSelectOption[]>(() => {
  if (!assistRoute.value) return [];
  const channels = assistRoute.value.group ? llmChannels.value.filter(channel => (channel.group?.trim() || "default") === assistRoute.value?.group) : llmChannels.value.filter(channel => channel.id === assistRoute.value?.profile_id);
  const models = new Map<string, string>();
  for (const channel of channels) {
    if (channel.model) models.set(channel.model, channel.model);
    for (const model of channel.models ?? []) if (!model.disabled && model.id) models.set(model.id, model.name && model.name !== model.id ? `${model.name} (${model.id})` : model.id);
  }
  return [...models].map(([value, label]) => ({ value, label }));
});
const assistModelValue = computed(() => assistRoute.value?.model ?? "");

async function openAssist(kind: AssistKind): Promise<void> {
  assistRoute.value = undefined;
  assistComposer.value = { kind, request: form.value.judge_prompt, result: "" };
  if (assistLoaded.value) return;
  assistLoaded.value = true;
  try { llmChannels.value = (await getConfig()).profiles ?? []; } catch { llmChannels.value = []; }
}
function closeAssist(): void { if (!assistBusy.value) assistComposer.value = null; }
function setAssistChannel(value: string): void {
  if (value === followChatValue) { assistRoute.value = undefined; return; }
  assistRoute.value = value.startsWith(groupPrefix) ? { group: value.slice(groupPrefix.length), model: "" } : { profile_id: value, model: "" };
  assistRoute.value.model = assistModelOptions.value[0]?.value ?? "";
}
function setAssistModel(value: string): void { if (assistRoute.value) assistRoute.value.model = value; }
async function runAssist(): Promise<void> {
  if (assistBusy.value || !assistComposer.value) return;
  const request = assistComposer.value.request.trim();
  if (!request) { toastError("请先写一句需求"); return; }
  const botID = form.value.notification_targets[0]?.profile_id || form.value.profile_id;
  const chatRoute = profiles.value.find(profile => profile.id === botID)?.model_roles?.chat;
  const route = assistRoute.value ?? { profile_id: chatRoute?.profile_id || chatRoute?.provider_id, group: chatRoute?.group, model: chatRoute?.model_id || chatRoute?.model || "" };
  const composer = assistComposer.value;
  assistBusy.value = true;
  try {
    const result = await assistRSSRule(composer.kind, request, { profile_id: route.profile_id, group: route.group, model: route.model });
    const text = result.text?.trim();
    if (!text) { toastError("模型没有返回可用内容"); return; }
    if (assistComposer.value === composer) composer.result = text;
  } catch (error) { toastError(error instanceof Error ? error.message : "AI 帮写失败"); } finally { assistBusy.value = false; }
}
function applyAssist(): void {
  const composer = assistComposer.value;
  if (!composer?.result) return;
  const expectedMode = composer.kind === "judge" ? "llm" : composer.kind;
  if (form.value.delivery_mode !== expectedMode) { toastError("推送方式已变化，未应用 AI 草稿"); return; }
  form.value.judge_prompt = composer.result;
  assistComposer.value = null;
  toastSuccess("AI 草稿已应用，保存订阅后生效");
}
function startEdit(task: AssistantTask): void {
  editingTask.value = task;
  const sources = taskSources(task);
  // 编辑框按订阅来源的类型决定看到哪一栏：混着填两种来源的订阅极少见，
  // 以第一个来源为准，另一栏留一个空行。
  const first = sources[0]?.source ?? "";
  const kind: SourceKind = first === "twitter" ? "twitter" : isBilibiliSource(first) ? "bilibili" : "rss";
  const handles = sources.filter((source) => source.source === "twitter").map((source) => source.handle ?? "");
  const urls = sources.filter((source) => source.source !== "twitter" && !isBilibiliSource(source.source)).map((source) => source.feed_url);
  const bilibili = sources.filter((source) => isBilibiliSource(source.source)).map(bilibiliInputValue);
  form.value = { notification_targets: task.notification_targets?.length ? task.notification_targets.map(t => ({ ...t, profile_id: t.profile_id || task.profile_id })) : [{ profile_id: task.profile_id, destination: task.group_id ? "group" : "private", group_id: task.group_id, user_id: task.user_id }], source: kind, twitter_handles: handles.length ? handles : [""], feed_urls: urls.length ? urls : [""], bilibili: bilibili.length ? bilibili : [""], delivery_mode: task.feed_rule_mode || (task.feed_judge_prompt?.trim() ? "llm" : "all"), judge_prompt: task.feed_judge_prompt ?? "", judge_model_id: task.feed_judge_model_id ?? "", interval_seconds: task.interval_seconds || defaultIntervalSeconds, profile_id: task.profile_id ?? "" };
  editing.value = true;
  markEditorClean();
}
async function stopEditing(): Promise<void> {
  if (saving.value) return;
  if (editorDirty() && !(await askConfirm({ title: "放弃未保存的订阅配置？", message: "这条 RSS 订阅的改动还没保存，关闭后会丢失。", confirmLabel: "放弃改动", danger: true }))) return;
  editing.value = false;
  editingTask.value = null;
  editorSnapshot.value = "";
}
async function saveEditor(): Promise<boolean> {
  const handles = filledSources(form.value.twitter_handles);
  const urls = filledSources(form.value.feed_urls);
  const bilibili = filledSources(form.value.bilibili);
  if (form.value.source === "bilibili" && !bilibili.length) return rejectEditor("请填写 B 站来源");
  if (form.value.source === "twitter" && !handles.length) return rejectEditor("请填写 Twitter 用户");
  if (form.value.source === "rss" && !urls.length) return rejectEditor("请填写 Feed URL");
  if (form.value.delivery_mode !== "all" && !form.value.judge_prompt.trim()) return rejectEditor("请填写筛选与回复规则，或选择全部推送");
  if (form.value.interval_seconds < minimumIntervalSeconds || form.value.interval_seconds > maximumIntervalSeconds) return rejectEditor("检查周期必须在 5 分钟到 365 天之间");

  if (!form.value.notification_targets.length || form.value.notification_targets.some(t => !t.profile_id || !(t.destination === "group" ? t.group_id : t.user_id))) return rejectEditor("请为每个通知目标选择机器人并填写会话 ID");
  if (saving.value) return false;
  saving.value = true;
  try {
    await props.prepareAccess?.();
    const source = form.value.source === "twitter" ? { twitter_handles: handles, feed_urls: [] } : { feed_urls: form.value.source === "bilibili" ? bilibili : urls, twitter_handles: [] };
    const common = { notification_targets: form.value.notification_targets, ...source, reply_mode: "decision", judge_model_id: form.value.delivery_mode === "llm" ? form.value.judge_model_id : "", media_mode: "video_audio", media_failure: "text", rule_mode: form.value.delivery_mode, judge_prompt: form.value.delivery_mode === "all" ? "" : form.value.judge_prompt, interval_seconds: form.value.interval_seconds };
    if (editingTask.value) await updateRSSWatch(editingTask.value.id, common); else await createRSSWatch({ ...common, profile_id: form.value.notification_targets[0]?.profile_id });
    toastSuccess(editingTask.value ? "RSS 订阅已更新" : "RSS 订阅已创建，当前内容已作为基线"); editing.value = false; editingTask.value = null; await load();
    return true;
  } catch (error) { toastError(error instanceof Error ? error.message : "RSS 订阅保存失败"); return false; } finally { saving.value = false; }
}
function rejectEditor(message: string): false { toastError(message); return false; }
async function cancel(task: AssistantTask): Promise<void> { if (!await askConfirm({ title: "取消 RSS 订阅", message: `停止 ${watchTitle(task)} 的订阅？`, confirmLabel: "取消订阅", danger: true })) return; busyID.value = task.id; try { await cancelRSSWatch(task.id); toastSuccess("RSS 订阅已取消"); await load(); } catch (error) { toastError(error instanceof Error ? error.message : "取消失败"); } finally { busyID.value = ""; } }
async function remove(task: AssistantTask): Promise<void> { if (!await askConfirm({ title: "删除 RSS 订阅", message: `永久删除 ${watchTitle(task)} 的订阅记录？`, confirmLabel: "删除", danger: true })) return; busyID.value = task.id; try { await deleteRSSWatch(task.id); toastSuccess("RSS 订阅已删除"); await load(); } catch (error) { toastError(error instanceof Error ? error.message : "删除失败"); } finally { busyID.value = ""; } }
function statusLabel(value: AssistantTaskStatus): string { return { active: "运行中", retrying: "重试中", used: "已执行", cancelled: "已取消", expired: "已到期" }[value] ?? value; }
function statusTone(value: AssistantTaskStatus): string { return value === "active" ? "ok" : value === "retrying" ? "warn" : value === "cancelled" ? "err" : ""; }
function formatInterval(seconds: number): string { return seconds % 86400 === 0 ? `${seconds / 86400} 天` : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : seconds % 60 === 0 ? `${seconds / 60} 分钟` : `${seconds} 秒`; }
onMounted(() => void load());
</script>
