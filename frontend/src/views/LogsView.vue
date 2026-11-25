<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <!-- 刷新是整页的动作，和事件那一档的刷新落在同一个位置：两档共用一排页头，
         动作在档之间跳来跳去，人每次都得重新找一遍。 -->
    <Teleport v-if="actionsHost" :to="actionsHost">
      <label class="switch">
        <input v-model="autoRefresh" type="checkbox" />
        <span class="track" aria-hidden="true"></span>
        <span class="switch-label">自动刷新</span>
      </label>
      <button class="btn" type="button" :disabled="loading" @click="reload">
        <RefreshCw :size="15" aria-hidden="true" />
        刷新
      </button>
    </Teleport>

    <header class="view-header">
      <div class="view-title">
        <p>操作审计、接口错误与每次模型调用的记录</p>
      </div>
      <div class="view-actions">
        <div class="segmented" role="tablist" aria-label="日志类型">
          <button type="button" :class="{ active: kind === 'all' }" @click="switchKind('all')">全部</button>
          <button type="button" :class="{ active: kind === 'operation' }" @click="switchKind('operation')">操作日志</button>
          <button type="button" :class="{ active: kind === 'error' }" @click="switchKind('error')">错误日志</button>
          <button type="button" :class="{ active: kind === 'llm' }" @click="switchKind('llm')">LLM 调用</button>
        </div>
      </div>
    </header>

    <section class="card">
      <div class="card-body" style="padding-top: 8px">
        <div class="cluster" style="padding: 8px 0 12px">
          <div class="input-group" style="flex: 1 1 240px; min-width: 200px; max-width: 360px">
            <input v-model="query" class="input" :placeholder="kind === 'llm' ? '按模型 / 用途 / 群 / 错误搜索…' : '按内容 / 操作人 / 群搜索…'" />
          </div>
          <AppSelect id="log-range" class="log-filter" aria-label="日期范围" :model-value="range" :options="rangeOptions" @update:model-value="onSelectRange" />
          <DateRangePicker
            v-if="range === 'custom'"
            v-model:from="customFrom"
            v-model:to="customTo"
            :open-signal="datePickerSignal"
            @change="clearExactBounds"
          />
          <template v-if="kind === 'llm'">
            <AppSelect id="log-llm-status" class="log-filter" aria-label="调用结果" :model-value="llmStatus" :options="statusOptions" @update:model-value="onSelectStatus" />
            <AppSelect id="log-llm-model" v-model="llmModel" class="log-filter" aria-label="模型" :options="modelOptions" searchable search-placeholder="搜索模型" />
            <AppSelect id="log-llm-purpose" v-model="llmPurpose" class="log-filter" aria-label="用途" :options="purposeOptions" searchable search-placeholder="搜索用途" />
          </template>
          <span v-if="actionFilter" class="badge">动作：{{ actionFilter }}</span>
          <span v-if="profileFilter" class="badge">机器人：{{ profileFilter }}</span>
          <button v-if="filtersActive" class="btn ghost small" type="button" @click="clearFilters">清除筛选</button>
          <SkeletonBlock v-if="loading && !logs.length" width="48px" height="18px" />
          <span v-else class="muted" style="font-size: 12.5px">{{ countLabel }}</span>
        </div>

        <div v-if="logs.length > 0">
          <article
            v-for="log in logs"
            :id="`log-${log.id}`"
            :key="log.id"
            class="log-row"
            :class="{
              'log-row-failed': log.action === 'llm_call_failed' || log.kind === 'error',
              'log-row-highlight': highlightedLogID === log.id
            }"
          >
            <span class="log-time">{{ formatTime(log.created_at) }}</span>
            <div class="log-main">
              <div v-if="log.actor || log.group_id || log.target" class="cluster" style="gap: 6px; margin-bottom: 2px">
                <span v-if="log.actor" class="muted" style="font-size: 11.5px" :title="actorTitle(log)">{{ actorLabel(log) }}</span>
                <span v-if="log.group_id" class="muted" style="font-size: 11.5px">@ {{ groupLabel(log) }}</span>
                <span v-if="log.target" class="muted mono" style="font-size: 11.5px">→ {{ log.target }}</span>
              </div>
              <template v-if="isLLMCall(log)">
                <p class="log-message">
                  <span class="badge" :class="log.action === 'llm_call_failed' ? 'err' : 'ok'">{{ log.action === 'llm_call_failed' ? (metaFlag(log, 'recovered') ? '失败·已转后备' : '失败') : '成功' }}</span>
                  <span class="mono">{{ metaText(log, 'model') || '未知模型' }}</span>
                  <span v-if="metaText(log, 'provider')" class="muted">{{ metaText(log, 'provider') }}</span>
                  <span class="muted">· {{ usagePurposeLabel(metaText(log, 'purpose')) }}</span>
                </p>
                <p class="log-detail">{{ llmCallSummary(log) }}</p>
                <p v-if="log.detail" class="log-detail log-detail-error">{{ log.detail }}</p>
              </template>
              <template v-else>
                <p class="log-message">
                  <span v-if="log.kind === 'error'" class="badge err" style="margin-right: 6px">错误</span>
                  <span v-if="log.action" class="mono muted" style="font-size: 12px; margin-right: 6px">{{ log.action }}</span>
                  <span>{{ log.message }}</span>
                </p>
                <p v-if="log.detail && log.detail !== log.message" class="log-detail" :class="{ 'log-detail-error': log.kind === 'error' }">{{ log.detail }}</p>
              </template>
            </div>
          </article>
          <div v-if="nextCursor" class="log-more">
            <button class="btn" type="button" :disabled="loadingMore" @click="loadMore">{{ loadingMore ? '加载中…' : '加载更早的记录' }}</button>
          </div>
          <p v-else-if="logs.length >= pageSize" class="hint log-more">已经到底了</p>
        </div>
        <EmptyState v-else-if="!loading" :title="query || filtersActive ? '没有匹配的日志' : '暂无日志'" />
        <LoadingSkeleton v-else kind="logs" :count="6" label="正在加载日志" />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";
import { RefreshCw } from "@lucide/vue";
import { getGroupTokenUsage, listAppLogs, type AppLogEntry, type AppLogQueryOptions, type AppLogTab } from "../api";
import { formatNumber, formatTime } from "../lib/format";
import { usagePurposeLabel } from "../lib/llm-usage";
import { displayChatIdentity } from "../lib/message-display";
import { toastError } from "../lib/toast";
import { recordsActionsHost } from "../lib/records-actions";
import { viewQuery } from "../router";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import DateRangePicker from "../components/DateRangePicker.vue";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";

// 页头动作位由 RecordsView 提供；拿不到就说明这一档被单独用在别处，按钮不渲染。
const actionsHost = inject(recordsActionsHost, ref<HTMLElement | null>(null));

type LogTab = AppLogTab;
const kind = ref<LogTab>("all");
const logs = ref<AppLogEntry[]>([]);
const loading = ref(true);
const loadingMore = ref(false);
const nextCursor = ref("");
const query = ref("");
const autoRefresh = ref(false);
const highlightedLogID = ref("");
// 一页 100 条，往下翻按游标接着取；搜索交给后端，翻到多早都搜得到。
const pageSize = 100;

// 日期范围对所有页签生效；支持至 90 天、近一年及自定义日期区间。
type LogRange = "" | "today" | "1h" | "12h" | "24h" | "7d" | "30d" | "90d" | "365d" | "custom";
type LLMStatus = "" | "ok" | "failed";
const validRanges = new Set<LogRange>(["", "today", "1h", "12h", "24h", "7d", "30d", "90d", "365d", "custom"]);
const validKinds = new Set<LogTab>(["all", "operation", "error", "llm"]);

const range = ref<LogRange>("");
const customFrom = ref("");
const customTo = ref("");
const datePickerSignal = ref(0);
// 从仪表盘按精确小时桶深链过来时保留精确起止时间；手动改日期输入框后退回按天换算。
const exactSince = ref("");
const exactUntil = ref("");
const actionFilter = ref("");
const profileFilter = ref("");
const llmStatus = ref<LLMStatus>("");
const llmModel = ref("");
const llmPurpose = ref("");
const rangeOptions: AppSelectOption[] = [
  { value: "", label: "全部时间" },
  { value: "today", label: "今天" },
  { value: "1h", label: "最近 1 小时" },
  { value: "12h", label: "最近 12 小时" },
  { value: "24h", label: "最近 24 小时" },
  { value: "7d", label: "最近 7 天" },
  { value: "30d", label: "最近 30 天" },
  { value: "90d", label: "最近 90 天" },
  { value: "365d", label: "最近 1 年" },
  { value: "custom", label: "自定义日期" }
];
const statusOptions: AppSelectOption[] = [
  { value: "", label: "全部结果" },
  { value: "ok", label: "成功" },
  { value: "failed", label: "失败" }
];
// 模型和用途的候选取最近 90 天的用量明细，失败过的模型也在里面。
const usageModels = ref<string[]>([]);
const usagePurposes = ref<string[]>([]);
const modelOptions = computed<AppSelectOption[]>(() => {
  const list = [...usageModels.value];
  if (llmModel.value && !list.includes(llmModel.value)) list.unshift(llmModel.value);
  return [{ value: "", label: "全部模型" }, ...list.map((model) => ({ value: model, label: model }))];
});
const purposeOptions = computed<AppSelectOption[]>(() => {
  const list = [...usagePurposes.value];
  if (llmPurpose.value && !list.includes(llmPurpose.value)) list.unshift(llmPurpose.value);
  return [{ value: "", label: "全部用途" }, ...list.map((purpose) => ({ value: purpose, label: usagePurposeLabel(purpose), hint: purpose }))];
});
const filtersActive = computed(
  () =>
    !!range.value ||
    !!actionFilter.value ||
    !!profileFilter.value ||
    (kind.value === "llm" && !!(llmStatus.value || llmModel.value || llmPurpose.value))
);

function onSelectRange(value: string): void {
  exactSince.value = "";
  exactUntil.value = "";
  range.value = (validRanges.has(value as LogRange) ? value : "") as LogRange;
  if (range.value === "custom") datePickerSignal.value++;
}

function onSelectStatus(value: string): void {
  llmStatus.value = (value === "ok" || value === "failed" ? value : "") as LLMStatus;
}

function clearExactBounds(): void {
  exactSince.value = "";
  exactUntil.value = "";
}

function localDayStart(value: string): Date | null {
  const [year, month, day] = value.split("-").map(Number);
  return year && month && day ? new Date(year, month - 1, day) : null;
}

function formatLocalDateInput(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

// 日期按本地时区换算成边界再交给后端，结束日期含当天。
function rangeBounds(): { since?: string; until?: string } {
  if (exactSince.value || exactUntil.value) {
    return { since: exactSince.value || undefined, until: exactUntil.value || undefined };
  }
  const now = new Date();
  switch (range.value) {
    case "today":
      return { since: new Date(now.getFullYear(), now.getMonth(), now.getDate()).toISOString() };
    case "1h":
      return { since: new Date(now.getTime() - 3600_000).toISOString() };
    case "12h":
      return { since: new Date(now.getTime() - 12 * 3600_000).toISOString() };
    case "24h":
      return { since: new Date(now.getTime() - 24 * 3600_000).toISOString() };
    case "7d":
      return { since: new Date(now.getTime() - 7 * 86_400_000).toISOString() };
    case "30d":
      return { since: new Date(now.getTime() - 30 * 86_400_000).toISOString() };
    case "90d":
      return { since: new Date(now.getTime() - 90 * 86_400_000).toISOString() };
    case "365d":
      return { since: new Date(now.getTime() - 365 * 86_400_000).toISOString() };
    case "custom": {
      const from = localDayStart(customFrom.value);
      const to = localDayStart(customTo.value);
      if (to) to.setDate(to.getDate() + 1);
      return { since: from?.toISOString(), until: to?.toISOString() };
    }
  }
  return {};
}

function queryOptions(before?: string): AppLogQueryOptions {
  const options: AppLogQueryOptions = {
    query: query.value,
    before,
    profile: profileFilter.value || undefined,
    actions: actionFilter.value ? [actionFilter.value] : undefined,
    ...rangeBounds()
  };
  if (kind.value === "llm") Object.assign(options, { status: llmStatus.value, model: llmModel.value, purpose: llmPurpose.value });
  return options;
}

function clearFilters(): void {
  range.value = "";
  customFrom.value = "";
  customTo.value = "";
  exactSince.value = "";
  exactUntil.value = "";
  actionFilter.value = "";
  profileFilter.value = "";
  llmStatus.value = "";
  llmModel.value = "";
  llmPurpose.value = "";
  query.value = "";
}

let usageOptionsLoaded = false;
async function loadUsageOptions(): Promise<void> {
  if (usageOptionsLoaded) return;
  usageOptionsLoaded = true;
  try {
    const report = await getGroupTokenUsage("", 2160);
    const breakdown = report.usage.breakdown ?? [];
    usageModels.value = [...new Set(breakdown.map((entry) => entry.model.trim()).filter((model) => model && model !== "unknown"))].sort();
    usagePurposes.value = [...new Set(breakdown.map((entry) => entry.purpose.trim()).filter(Boolean))].sort();
  } catch {
    usageOptionsLoaded = false;
  }
}

let timer: number | undefined;
let queryTimer: number | undefined;
let highlightTimer: number | undefined;
let seeding = false;
// 只认最后一次请求的结果：切页签、改搜索词时上一次请求可能比这一次晚回来。
let requestSeq = 0;

// 日志里的 actor 形如 qq:30001，光一串号码认不出是谁。有群名片先用群名片，
// 没有再用昵称，显示成「名字（账号）」；原样的 actor 和昵称退到悬浮提示里。
function actorLabel(log: AppLogEntry): string {
  const actor = (log.actor ?? "").trim();
  const name = (log.actor_card ?? "").trim() || (log.actor_name ?? "").trim();
  if (!name) return actor;
  return displayChatIdentity(name, actorAccount(actor)) || actor;
}

function actorTitle(log: AppLogEntry): string {
  const nickname = (log.actor_name ?? "").trim();
  const card = (log.actor_card ?? "").trim();
  return nickname && card && nickname !== card ? `${log.actor} · 昵称：${nickname}` : (log.actor ?? "");
}

function groupLabel(log: AppLogEntry): string {
  const groupID = (log.group_id ?? "").trim();
  const name = (log.group_name ?? "").trim();
  return name ? displayChatIdentity(name, groupID) || groupID : `群 ${groupID}`;
}

function actorAccount(actor: string): string {
  const separator = actor.indexOf(":");
  return separator < 0 ? actor : actor.slice(separator + 1).trim();
}

const countLabel = computed(() => `${nextCursor.value ? "已加载 " : ""}${formatNumber(logs.value.length)} 条${nextCursor.value ? "，还有更早的" : ""}`);

function isLLMCall(log: AppLogEntry): boolean {
  return log.action === "llm_usage" || log.action === "llm_call_failed";
}

function metaText(log: AppLogEntry, key: string): string {
  const value = log.metadata?.[key];
  return typeof value === "string" ? value.trim() : "";
}

function metaNumber(log: AppLogEntry, key: string): number {
  const value = log.metadata?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function metaFlag(log: AppLogEntry, key: string): boolean {
  return log.metadata?.[key] === true;
}

// 一行写清这次调用花了多少、多久：成功的给 token 和耗时，失败的只有耗时。
function llmCallSummary(log: AppLogEntry): string {
  const parts: string[] = [];
  if (log.action === "llm_usage") {
    const input = metaNumber(log, "input_tokens");
    const output = metaNumber(log, "output_tokens");
    const cached = metaNumber(log, "cached_input_tokens");
    if (input || output) {
      parts.push(`输入 ${formatNumber(input)}${cached ? `（缓存 ${formatNumber(cached)}）` : ""} · 输出 ${formatNumber(output)} Token`);
    } else {
      parts.push("上游没报用量");
    }
  }
  const duration = metaNumber(log, "duration_ms");
  if (duration > 0) parts.push(`耗时 ${(duration / 1000).toFixed(1)}s`);
  const ttft = metaNumber(log, "ttft_ms");
  if (ttft > 0) parts.push(`首 Token ${(ttft / 1000).toFixed(1)}s`);
  return parts.join(" · ");
}

async function scrollToHighlightedLog(): Promise<void> {
  const targetID = highlightedLogID.value;
  if (!targetID) return;
  await nextTick();
  const el = document.getElementById(`log-${targetID}`);
  if (el) {
    el.scrollIntoView({ behavior: "smooth", block: "center" });
    if (highlightTimer !== undefined) window.clearTimeout(highlightTimer);
    highlightTimer = window.setTimeout(() => {
      highlightedLogID.value = "";
      highlightTimer = undefined;
    }, 3200);
  }
}

async function reload(): Promise<void> {
  const seq = ++requestSeq;
  loading.value = true;
  try {
    const response = await listAppLogs(kind.value, pageSize, queryOptions());
    if (seq !== requestSeq) return;
    logs.value = response.logs;
    nextCursor.value = response.next_cursor ?? "";
    void scrollToHighlightedLog();
  } catch (error) {
    if (seq === requestSeq) toastError(error instanceof Error ? error.message : "加载日志失败");
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

async function loadMore(): Promise<void> {
  if (!nextCursor.value || loadingMore.value) return;
  const seq = requestSeq;
  loadingMore.value = true;
  try {
    const response = await listAppLogs(kind.value, pageSize, queryOptions(nextCursor.value));
    if (seq !== requestSeq) return;
    const seen = new Set(logs.value.map((log) => log.id));
    logs.value = [...logs.value, ...response.logs.filter((log) => !seen.has(log.id))];
    nextCursor.value = response.next_cursor ?? "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载日志失败");
  } finally {
    loadingMore.value = false;
  }
}

// 自动刷新只取最新一页、把新来的插到最前面：已经往下翻过的记录不能被刷新冲掉。
async function refreshHead(): Promise<void> {
  if (logs.value.length <= pageSize) {
    await reload();
    return;
  }
  const seq = requestSeq;
  try {
    const response = await listAppLogs(kind.value, pageSize, queryOptions());
    if (seq !== requestSeq) return;
    const seen = new Set(logs.value.map((log) => log.id));
    const fresh = response.logs.filter((log) => !seen.has(log.id));
    if (fresh.length) logs.value = [...fresh, ...logs.value];
  } catch {
    // 后台刷新失败不打扰人，下一轮再试。
  }
}

function switchKind(next: LogTab): void {
  if (kind.value !== next) {
    kind.value = next;
    actionFilter.value = "";
    if (next === "llm") void loadUsageOptions();
    void reload();
  }
}

// 自定义日期两头都没填且无精确边界时不重查：刚切到「自定义」还没选日期，结果跟「全部时间」一样。
watch([range, customFrom, customTo, exactSince, exactUntil, actionFilter, profileFilter, llmStatus, llmModel, llmPurpose], ([nextRange], [previousRange]) => {
  if (seeding) return;
  if (nextRange === "custom" && previousRange !== "custom" && !customFrom.value && !customTo.value && !exactSince.value && !exactUntil.value) return;
  void reload();
});

watch(query, () => {
  if (seeding) return;
  if (queryTimer !== undefined) window.clearTimeout(queryTimer);
  queryTimer = window.setTimeout(() => {
    queryTimer = undefined;
    void reload();
  }, 300);
});

function applyAutoRefresh(): void {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
  if (autoRefresh.value) {
    timer = window.setInterval(() => {
      void refreshHead();
    }, 5000);
  }
}

watch(autoRefresh, applyAutoRefresh);

// 支持从仪表盘或浏览器页带着筛选条件深链跳转（kind、range、from/to、since/until、status、model、purpose、action、profile、q、id）。
function seedQueryFromLocation(): boolean {
  const params = viewQuery();
  if (params.size === 0) return false;
  seeding = true;
  try {
    const nextKind = params.get("kind");
    if (nextKind && validKinds.has(nextKind as LogTab)) {
      kind.value = nextKind as LogTab;
      if (kind.value === "llm") void loadUsageOptions();
    }
    const nextRange = params.get("range");
    const paramSince = params.get("since") ?? "";
    const paramUntil = params.get("until") ?? "";
    const paramFrom = params.get("from") ?? "";
    const paramTo = params.get("to") ?? "";
    if (paramSince || paramUntil) {
      range.value = "custom";
      exactSince.value = paramSince;
      exactUntil.value = paramUntil;
      customFrom.value = paramFrom || (paramSince ? formatLocalDateInput(paramSince) : "");
      customTo.value = paramTo || (paramUntil ? formatLocalDateInput(new Date(new Date(paramUntil).getTime() - 1).toISOString()) : "");
    } else if (paramFrom || paramTo) {
      range.value = "custom";
      exactSince.value = "";
      exactUntil.value = "";
      customFrom.value = paramFrom;
      customTo.value = paramTo;
    } else if (nextRange !== null && validRanges.has(nextRange as LogRange)) {
      range.value = nextRange as LogRange;
      exactSince.value = "";
      exactUntil.value = "";
    }
    const nextStatus = params.get("status");
    if (nextStatus === "ok" || nextStatus === "failed" || nextStatus === "") {
      llmStatus.value = nextStatus;
    } else if (kind.value === "llm" && !params.has("status")) {
      llmStatus.value = "";
    }
    if (params.has("model")) llmModel.value = params.get("model") ?? "";
    if (params.has("purpose")) llmPurpose.value = params.get("purpose") ?? "";
    actionFilter.value = params.get("action") ?? "";
    profileFilter.value = params.get("profile") ?? "";
    if (params.has("q")) query.value = params.get("q") ?? "";
    highlightedLogID.value = params.get("id") ?? "";
  } finally {
    seeding = false;
  }
  return true;
}

onMounted(() => {
  seedQueryFromLocation();
  if (kind.value === "llm") void loadUsageOptions();
  void reload();
});

onActivated(() => {
  const seeded = seedQueryFromLocation();
  if (seeded) void reload();
  applyAutoRefresh();
});

onDeactivated(() => {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
});

onBeforeUnmount(() => {
  if (timer !== undefined) {
    window.clearInterval(timer);
  }
  if (queryTimer !== undefined) window.clearTimeout(queryTimer);
  if (highlightTimer !== undefined) window.clearTimeout(highlightTimer);
});
</script>

<style scoped>
.log-row-highlight {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-radius: var(--radius-sm);
  transition: background 0.35s ease;
}
</style>
