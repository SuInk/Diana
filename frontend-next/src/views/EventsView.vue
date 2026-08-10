<template>
  <div>
    <header class="view-header event-view-header">
      <div class="view-title">
        <h1>事件明细</h1>
        <p>查看每条消息的处理结果和回复决策</p>
      </div>
      <div class="view-actions">
        <button class="btn" type="button" :disabled="loading" @click="load(true)">
          <RefreshCw :size="15" :class="{ spin: loading }" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <div class="stack">
      <section class="event-filter-band" aria-label="事件时间范围">
        <div class="event-filter-copy">
          <Clock3 :size="16" aria-hidden="true" />
          <span>时间范围</span>
        </div>
        <div class="segmented event-range" role="radiogroup" aria-label="按时间筛选事件">
          <button
            v-for="option in rangeOptions"
            :key="option.value"
            type="button"
            :class="{ active: selectedRange === option.value }"
            :aria-checked="selectedRange === option.value"
            role="radio"
            @click="selectRange(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
      </section>

      <div class="stat-grid event-stats">
        <StatCard label="范围内消息" :value="formatNumber(summary.total)" :foot="rangeDescription">
          <template #icon><MessageCircle :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="已回复" :value="formatNumber(summary.replied)" :foot="replyRate">
          <template #icon><MessageCircleReply :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="未回复" :value="formatNumber(summary.not_replied)" :foot="`${formatNumber(summary.pending)} 条等待处理`">
          <template #icon><MessageCircleOff :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="处理异常" :value="formatNumber(summary.errors)" foot="包含重试与投递失败">
          <template #icon><TriangleAlert :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="Token 总量" :value="formatNumber(summary.total_tokens)" :foot="tokenBreakdown">
          <template #icon><Sigma :size="14" aria-hidden="true" /></template>
        </StatCard>
      </div>

      <section class="card event-detail-card">
        <div class="card-header">
          <div class="cluster">
            <h2>消息处理记录</h2>
            <span class="badge" :class="stream.connected ? 'ok' : 'warn'">
              <span class="status-dot" :class="{ pulse: stream.connected }" aria-hidden="true" />
              {{ stream.connected ? "实时更新" : "实时连接中断" }}
            </span>
          </div>
          <span class="muted event-result-count">已显示 {{ formatNumber(events.length) }} / {{ formatNumber(summary.total) }}</span>
        </div>

        <div v-if="events.length > 0" class="event-detail-list">
          <article v-for="event in events" :key="event.id" class="event-detail-row">
            <div class="event-detail-time">
              <strong>{{ formatClock(event.at) }}</strong>
              <span>{{ formatDate(event.at) }}</span>
            </div>

            <div class="event-detail-main">
              <div class="event-detail-meta">
                <span v-if="event.platform" class="badge">{{ platformLabel(event.platform) }}</span>
                <span class="badge">{{ eventKindLabel(event.kind) }}</span>
                <span class="badge" :class="decisionClass(event)">{{ decisionLabel(event) }}</span>
                <span v-if="event.group_id" class="mono muted">群 {{ event.group_id }}</span>
                <span v-if="event.sender_name" class="muted">{{ event.sender_name }}</span>
                <span v-if="event.user_id" class="mono muted">{{ event.user_id }}</span>
                <span v-if="event.duration_ms" class="muted">{{ formatDuration(event.duration_ms) }}</span>
              </div>

              <p class="event-detail-message">{{ event.text || "[无文本内容]" }}</p>

              <div class="event-decision" :class="decisionClass(event)">
                <component :is="decisionIcon(event)" :size="16" aria-hidden="true" />
                <div>
                  <strong>{{ decisionReasonLabel(event) }}</strong>
                  <p>{{ event.reason }}</p>
                </div>
              </div>

              <div v-if="event.reply" class="event-detail-reply">
                <strong>机器人回复</strong>
                <p>{{ event.reply }}</p>
              </div>
              <p v-if="event.error && !event.reason.includes(event.error)" class="event-error">{{ event.error }}</p>

              <div class="event-technical muted mono">
                <span v-if="event.message_id">消息 {{ event.message_id }}</span>
                <span>结果 {{ event.outcome || event.status }}</span>
                <span v-if="event.total_tokens">
                  Token {{ formatNumber(event.total_tokens) }}（输入 {{ formatNumber(event.input_tokens || 0) }} / 输出 {{ formatNumber(event.output_tokens || 0) }}）
                </span>
              </div>
            </div>
          </article>
        </div>

        <div v-else-if="loading" class="event-loading">
          <LoaderCircle :size="20" class="spin" aria-hidden="true" />
          正在加载事件
        </div>
        <EmptyState v-else title="当前范围没有事件" hint="切换更长的时间范围，或等待机器人收到新消息">
          <template #icon><Activity :size="20" aria-hidden="true" /></template>
        </EmptyState>

        <div v-if="hasMore" class="event-load-more">
          <button class="btn" type="button" :disabled="loadingMore" @click="load(false)">
            <LoaderCircle v-if="loadingMore" :size="15" class="spin" aria-hidden="true" />
            <ChevronDown v-else :size="15" aria-hidden="true" />
            加载更多
          </button>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { Component } from "vue";
import {
  Activity,
  CheckCircle2,
  ChevronDown,
  Clock3,
  LoaderCircle,
  MessageCircle,
  MessageCircleOff,
  MessageCircleReply,
  RefreshCw,
  Sigma,
  TimerReset,
  TriangleAlert
} from "@lucide/vue";
import {
  getAssistantEvents,
  type AssistantEventDetail,
  type AssistantEventRange,
  type AssistantEventsResponse
} from "../api";
import { formatClock, formatNumber } from "../format";
import { stream } from "../stream";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import StatCard from "../components/StatCard.vue";

const rangeOptions: Array<{ value: AssistantEventRange; label: string }> = [
  { value: "1h", label: "最近 1h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
  { value: "all", label: "更久" }
];

const selectedRange = ref<AssistantEventRange>("24h");
const events = ref<AssistantEventDetail[]>([]);
const response = ref<AssistantEventsResponse | null>(null);
const page = ref(1);
const loading = ref(false);
const loadingMore = ref(false);
let refreshTimer: number | null = null;

const summary = computed(() => ({
  total: response.value?.total ?? 0,
  replied: response.value?.replied ?? 0,
  not_replied: response.value?.not_replied ?? 0,
  pending: response.value?.pending ?? 0,
  errors: response.value?.errors ?? 0,
  llm_calls: response.value?.llm_calls ?? 0,
  input_tokens: response.value?.input_tokens ?? 0,
  output_tokens: response.value?.output_tokens ?? 0,
  total_tokens: response.value?.total_tokens ?? 0
}));
const hasMore = computed(() => response.value?.has_more ?? false);
const rangeDescription = computed(() => rangeOptions.find((item) => item.value === selectedRange.value)?.label ?? "当前范围");
const replyRate = computed(() => {
  if (summary.value.total <= 0) return "暂无处理记录";
  return `回复率 ${Math.round((summary.value.replied / summary.value.total) * 100)}%`;
});
const tokenBreakdown = computed(() =>
  `输入 ${formatNumber(summary.value.input_tokens)} / 输出 ${formatNumber(summary.value.output_tokens)} · ${formatNumber(summary.value.llm_calls)} 次调用`
);

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (platform === "napcat" || platform === "onebot") return "QQ";
  return platform;
}

async function load(reset: boolean): Promise<void> {
  if (reset) {
    loading.value = true;
    page.value = 1;
  } else {
    loadingMore.value = true;
  }
  try {
    const next = await getAssistantEvents(selectedRange.value, page.value, 50);
    response.value = next;
    if (reset) {
      events.value = next.events;
    } else {
      const seen = new Set(events.value.map((item) => item.id));
      events.value = [...events.value, ...next.events.filter((item) => !seen.has(item.id))];
    }
    if (next.has_more) page.value += 1;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "事件加载失败");
  } finally {
    loading.value = false;
    loadingMore.value = false;
  }
}

function selectRange(value: AssistantEventRange): void {
  if (selectedRange.value === value) return;
  selectedRange.value = value;
  void load(true);
}

function eventKindLabel(kind: string): string {
  const labels: Record<string, string> = { private: "私聊", group: "群聊", notice: "通知", meta: "元事件" };
  return labels[kind] ?? kind;
}

function decisionLabel(event: AssistantEventDetail): string {
  if (event.decision === "replied" || event.handled) return "已回复";
  if (event.decision === "pending") return "等待处理";
  if (event.decision === "error") return "处理异常";
  return "未回复";
}

function decisionClass(event: AssistantEventDetail): string {
  if (event.decision === "replied" || event.handled) return "ok";
  if (event.decision === "pending") return "warn";
  if (event.decision === "error") return "err";
  return "quiet";
}

function decisionIcon(event: AssistantEventDetail): Component {
  if (event.decision === "replied" || event.handled) return CheckCircle2;
  if (event.decision === "pending") return TimerReset;
  if (event.decision === "error") return TriangleAlert;
  return MessageCircleOff;
}

function decisionReasonLabel(event: AssistantEventDetail): string {
  if (event.decision === "replied" || event.handled) return "回复原因";
  if (event.decision === "pending") return "当前状态";
  if (event.decision === "error") return "异常原因";
  return "未回复原因";
}

function formatDate(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleDateString("zh-CN", { month: "2-digit", day: "2-digit" });
}

function formatDuration(durationMS: number): string {
  if (durationMS < 1000) return `${durationMS}ms`;
  return `${(durationMS / 1000).toFixed(1)}s`;
}

watch(
  () => stream.lastEventAt,
  (value) => {
    if (!value) return;
    if (refreshTimer !== null) window.clearTimeout(refreshTimer);
    refreshTimer = window.setTimeout(() => void load(true), 1200);
  }
);

onMounted(() => void load(true));
onBeforeUnmount(() => {
  if (refreshTimer !== null) window.clearTimeout(refreshTimer);
});
</script>

<style scoped>
.event-view-header {
  align-items: flex-end;
}

.event-filter-band {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-width: 0;
}

.event-filter-copy {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--muted);
  font-size: 13px;
  white-space: nowrap;
}

.event-range {
  max-width: 100%;
  overflow-x: auto;
}

.event-range button {
  min-width: 74px;
  white-space: nowrap;
}

.event-detail-card {
  overflow: hidden;
}

.event-result-count {
  font-size: 12px;
}

.event-detail-list {
  padding: 0 28px;
}

.event-detail-row {
  display: grid;
  grid-template-columns: 92px minmax(0, 1fr);
  gap: 24px;
  padding: 22px 0;
  border-bottom: 1px solid var(--border);
}

.event-detail-row:last-child {
  border-bottom: 0;
}

.event-detail-time {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

.event-detail-time strong {
  color: var(--text);
  font-family: var(--font-mono);
  font-size: 13px;
}

.event-detail-time span {
  font-size: 12px;
}

.event-detail-main {
  min-width: 0;
}

.event-detail-meta,
.event-technical {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
}

.event-detail-message,
.event-detail-reply p,
.event-decision p {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.event-detail-message {
  margin-top: 12px;
  color: var(--text);
  line-height: 1.65;
}

.event-decision {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  gap: 10px;
  margin-top: 14px;
  padding: 12px 14px;
  border-left: 3px solid var(--border-strong);
  background: var(--surface-2);
}

.event-decision.ok {
  border-left-color: var(--ok);
}

.event-decision.warn {
  border-left-color: var(--warn);
}

.event-decision.err {
  border-left-color: var(--err);
}

.event-decision strong,
.event-detail-reply strong {
  display: block;
  margin-bottom: 4px;
  font-size: 12px;
}

.event-decision p,
.event-detail-reply p {
  color: var(--muted);
  font-size: 13px;
  line-height: 1.55;
}

.event-detail-reply {
  margin-top: 14px;
  padding-left: 14px;
  border-left: 3px solid var(--accent);
}

.event-technical {
  margin-top: 14px;
  font-size: 11px;
}

.event-loading,
.event-load-more {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 30px;
  color: var(--muted);
}

.event-load-more {
  border-top: 1px solid var(--border);
  padding: 18px;
}

@media (max-width: 720px) {
  .event-filter-band {
    align-items: flex-start;
    flex-direction: column;
  }

  .event-range {
    width: 100%;
  }

  .event-range button {
    flex: 1 0 auto;
  }

  .event-detail-list {
    padding: 0 18px;
  }

  .event-detail-row {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
  }

  .event-detail-time {
    flex-direction: row;
    align-items: baseline;
  }
}
</style>
