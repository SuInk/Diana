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
      <section class="event-filter-band" aria-label="事件筛选">
        <div class="event-filter-row">
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
        </div>
        <div class="event-filter-row">
          <div class="event-filter-copy">
            <Filter :size="16" aria-hidden="true" />
            <span>处理结果</span>
          </div>
          <div class="segmented event-result-filter" role="radiogroup" aria-label="按处理结果筛选事件">
            <button
              v-for="option in resultOptions"
              :key="option.value"
              type="button"
              :class="{ active: selectedResult === option.value }"
              :aria-checked="selectedResult === option.value"
              role="radio"
              @click="selectResult(option.value)"
            >
              <span>{{ option.label }}</span>
              <span class="event-filter-count">{{ formatNumber(resultOptionCount(option.value)) }}</span>
            </button>
          </div>
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
        <StatCard label="处理异常" :value="formatNumber(summary.errors)" foot="包含处理失败与投递失败">
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
          <span class="muted event-result-count">{{ resultCountText }}</span>
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

              <p v-if="event.text" class="event-detail-message">{{ event.text }}</p>
              <p v-else-if="!event.images?.length" class="event-detail-message">[无文本内容]</p>

              <div v-if="event.images?.length" class="event-image-grid" aria-label="消息图片">
                <template v-for="image in event.images" :key="image.index">
                  <div
                    v-if="image.unavailable || failedImages[imageKey(event, image.index)]"
                    class="event-image-preview unavailable"
                    :aria-label="`${imageAlt(image.index, image.summary)}，图片不可用`"
                  >
                    <span class="event-image-unavailable">
                      <ImageOff :size="22" aria-hidden="true" />
                      <span>图片不可用</span>
                    </span>
                  </div>
                  <a
                    v-else
                    class="event-image-preview"
                    :href="eventImageURL(event, image.index)"
                    target="_blank"
                    rel="noopener"
                    :aria-label="imageAriaLabel(image.index, image.summary)"
                    title="查看原图"
                  >
                    <img
                      :src="eventImageURL(event, image.index)"
                      :alt="imageAlt(image.index, image.summary)"
                      loading="lazy"
                      decoding="async"
                      @error="markImageFailed(event, image.index)"
                    />
                  </a>
                </template>
              </div>

              <div class="event-decision" :class="decisionClass(event)">
                <component :is="decisionIcon(event)" :size="16" aria-hidden="true" />
                <div>
                  <strong>{{ decisionReasonLabel(event) }}</strong>
                  <p>{{ event.reason || fallbackDecisionReason(event) }}</p>
                </div>
              </div>

              <div v-if="event.decision === 'replied' || event.handled" class="event-detail-reply">
                <strong>回复结果</strong>
                <p>{{ replyResultText(event) }}</p>
              </div>
              <div v-if="event.delivery_stage" class="event-delivery" :class="deliveryClass(event)">
                <component :is="deliveryIcon(event)" :size="16" aria-hidden="true" />
                <div>
                  <strong>{{ deliveryLabel(event.delivery_stage) }}</strong>
                  <p>{{ deliveryDetail(event) }}</p>
                </div>
              </div>
              <p v-if="event.error && !(event.reason || '').includes(event.error)" class="event-error">{{ event.error }}</p>
              <p v-if="event.delivery_error" class="event-error">{{ event.delivery_error }}</p>

              <div class="event-technical muted mono">
                <span v-if="event.message_id">消息 {{ event.message_id }}</span>
                <span>结果 {{ event.outcome || event.status }}</span>
                <span v-if="event.outbound_message_id">出站 {{ event.outbound_message_id }}</span>
                <span v-if="event.total_tokens">
                  Token {{ formatNumber(event.total_tokens) }}（输入 {{ formatNumber(event.input_tokens || 0) }} / 输出 {{ formatNumber(event.output_tokens || 0) }}）
                </span>
              </div>

              <div class="event-debug-trace">
                <button class="btn event-debug-toggle" type="button" :disabled="traceLoading[event.id]" @click="toggleTrace(event)">
                  <LoaderCircle v-if="traceLoading[event.id]" :size="14" class="spin" aria-hidden="true" />
                  <Bug v-else :size="14" aria-hidden="true" />
                  {{ traceOpen[event.id] ? "收起调用链" : "查看模型上下文与调用链" }}
                  <ChevronDown :size="14" :class="{ 'trace-chevron-open': traceOpen[event.id] }" aria-hidden="true" />
                </button>

                <div v-if="traceOpen[event.id]" class="debug-trace-panel">
                  <div v-if="traceLoading[event.id]" class="debug-trace-empty muted">正在读取调试记录</div>
                  <div v-else-if="(traceSteps[event.id]?.length ?? 0) === 0" class="debug-trace-empty muted">
                    这条事件没有调试记录。调试模式默认关闭，开启后仅记录新事件。
                  </div>
                  <ol v-else class="debug-trace-list">
                    <li v-for="(step, index) in traceSteps[event.id]" :key="step.id" class="debug-trace-step">
                      <div class="debug-step-header">
                        <span class="debug-step-index mono">{{ index + 1 }}</span>
                        <strong>{{ tracePhaseLabel(step) }}</strong>
                        <span class="muted mono">{{ formatClock(step.created_at) }}</span>
                        <span v-if="traceDuration(step)" class="muted">{{ traceDuration(step) }}</span>
                      </div>
                      <p v-if="traceSummary(step)" class="debug-step-summary">{{ traceSummary(step) }}</p>
                      <div v-if="traceJSON(step, 'request')" class="debug-payload">
                        <span>模型请求上下文</span>
                        <pre>{{ traceJSON(step, "request") }}</pre>
                      </div>
                      <div v-if="traceJSON(step, 'response')" class="debug-payload">
                        <span>模型响应</span>
                        <pre>{{ traceJSON(step, "response") }}</pre>
                      </div>
                      <div v-if="traceJSON(step, 'available_tools')" class="debug-payload">
                        <span>可用工具</span>
                        <pre>{{ traceJSON(step, "available_tools") }}</pre>
                      </div>
                      <div v-if="traceJSON(step, 'tool_input')" class="debug-payload">
                        <span>工具参数</span>
                        <pre>{{ traceJSON(step, "tool_input") }}</pre>
                      </div>
                      <div v-if="traceText(step, 'tool_output')" class="debug-payload">
                        <span>工具结果</span>
                        <pre>{{ traceText(step, "tool_output") }}</pre>
                      </div>
                      <div v-if="traceText(step, 'error')" class="debug-payload err">
                        <span>错误</span>
                        <pre>{{ traceText(step, "error") }}</pre>
                      </div>
                    </li>
                  </ol>
                </div>
              </div>
            </div>
          </article>
        </div>

        <div v-else-if="loading" class="event-loading">
          <LoaderCircle :size="20" class="spin" aria-hidden="true" />
          正在加载事件
        </div>
        <EmptyState v-else :title="emptyStateTitle" hint="切换处理结果或时间范围，或等待机器人收到新消息">
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
  Bug,
  CheckCircle2,
  ChevronDown,
  Clock3,
  Filter,
  ImageOff,
  LoaderCircle,
  MessageCircle,
  MessageCircleOff,
  MessageCircleReply,
  RefreshCw,
  Sigma,
  Send,
  TimerReset,
  TriangleAlert
} from "@lucide/vue";
import {
  getAssistantEventTrace,
  getAssistantEvents,
  type AppLogEntry,
  type AssistantEventDetail,
  type AssistantEventRange,
  type AssistantEventResultFilter,
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

const resultOptions: Array<{ value: AssistantEventResultFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "replied", label: "已回复" },
  { value: "not_replied", label: "未回复" },
  { value: "pending", label: "等待处理" },
  { value: "error", label: "处理异常" }
];

const selectedRange = ref<AssistantEventRange>("24h");
const selectedResult = ref<AssistantEventResultFilter>("all");
const events = ref<AssistantEventDetail[]>([]);
const response = ref<AssistantEventsResponse | null>(null);
const page = ref(1);
const loading = ref(false);
const loadingMore = ref(false);
const traceOpen = ref<Record<string, boolean>>({});
const traceLoading = ref<Record<string, boolean>>({});
const traceLoaded = ref<Record<string, boolean>>({});
const traceSteps = ref<Record<string, AppLogEntry[]>>({});
const failedImages = ref<Record<string, boolean>>({});
let refreshTimer: number | null = null;
let loadGeneration = 0;

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
const filteredTotal = computed(() => response.value?.filtered_total ?? summary.value.total);
const rangeDescription = computed(() => rangeOptions.find((item) => item.value === selectedRange.value)?.label ?? "当前范围");
const selectedResultLabel = computed(() => resultOptions.find((item) => item.value === selectedResult.value)?.label ?? "全部");
const resultCountText = computed(() => {
  const prefix = selectedResult.value === "all" ? "已显示" : selectedResultLabel.value;
  return `${prefix} ${formatNumber(events.value.length)} / ${formatNumber(filteredTotal.value)}`;
});
const emptyStateTitle = computed(() => selectedResult.value === "all" ? "当前范围没有事件" : `当前范围没有${selectedResultLabel.value}事件`);
const replyRate = computed(() => {
  if (summary.value.total <= 0) return "暂无处理记录";
  return `回复率 ${Math.round((summary.value.replied / summary.value.total) * 100)}%`;
});
const tokenBreakdown = computed(() =>
  `输入 ${formatNumber(summary.value.input_tokens)} / 输出 ${formatNumber(summary.value.output_tokens)} · ${formatNumber(summary.value.llm_calls)} 次调用`
);

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (["onebot-v11", "onebot", "napcat", "lagrange", "go-cqhttp"].includes(platform)) return "QQ";
  return platform;
}

function imageKey(event: AssistantEventDetail, imageIndex: number): string {
  return `${event.id}:${imageIndex}`;
}

function eventImageURL(event: AssistantEventDetail, imageIndex: number): string {
  return `/api/assistant/events/${encodeURIComponent(event.id)}/images/${imageIndex}`;
}

function imageSummary(summary?: string): string {
  return (summary ?? "").replace(/^\[|\]$/g, "").trim();
}

function imageAlt(imageIndex: number, summary?: string): string {
  const label = imageSummary(summary);
  return label ? `图片 ${imageIndex}：${label}` : `消息图片 ${imageIndex}`;
}

function imageAriaLabel(imageIndex: number, summary?: string): string {
  return `${imageAlt(imageIndex, summary)}，在新窗口查看原图`;
}

function markImageFailed(event: AssistantEventDetail, imageIndex: number): void {
  failedImages.value = { ...failedImages.value, [imageKey(event, imageIndex)]: true };
}

function resultOptionCount(result: AssistantEventResultFilter): number {
  if (result === "all") return summary.value.total;
  if (result === "replied") return summary.value.replied;
  if (result === "not_replied") return summary.value.not_replied;
  if (result === "pending") return summary.value.pending;
  return summary.value.errors;
}

async function load(reset: boolean): Promise<void> {
  const generation = reset ? ++loadGeneration : loadGeneration;
  const requestedPage = reset ? 1 : page.value;
  const requestedRange = selectedRange.value;
  const requestedResult = selectedResult.value;
  if (reset) {
    loading.value = true;
    page.value = 1;
  } else {
    loadingMore.value = true;
  }
  try {
    const next = await getAssistantEvents(requestedRange, requestedResult, requestedPage, 50);
    if (generation !== loadGeneration) return;
    response.value = next;
    if (reset) {
      events.value = next.events;
    } else {
      const seen = new Set(events.value.map((item) => item.id));
      events.value = [...events.value, ...next.events.filter((item) => !seen.has(item.id))];
    }
    page.value = next.has_more ? next.page + 1 : next.page;
  } catch (error) {
    if (generation === loadGeneration) {
      toastError(error instanceof Error ? error.message : "事件加载失败");
    }
  } finally {
    if (generation === loadGeneration) {
      loading.value = false;
      loadingMore.value = false;
    }
  }
}

function selectRange(value: AssistantEventRange): void {
  if (selectedRange.value === value) return;
  selectedRange.value = value;
  events.value = [];
  response.value = null;
  void load(true);
}

function selectResult(value: AssistantEventResultFilter): void {
  if (selectedResult.value === value) return;
  selectedResult.value = value;
  events.value = [];
  response.value = null;
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

function fallbackDecisionReason(event: AssistantEventDetail): string {
  if (event.decision === "pending") return "消息仍在等待机器人处理";
  if (event.decision === "error") return event.error?.trim() || "消息处理发生异常，但没有保存更详细的错误";
  if (event.decision === "replied" || event.handled) return "消息已通过回复路由并完成回答";
  if (event.outcome) return `消息未回复，处理结果为 ${event.outcome}`;
  return "消息未命中回复规则，旧记录没有保存更详细的判断原因";
}

function replyResultText(event: AssistantEventDetail): string {
  if (event.reply?.trim()) return event.reply;
  if (event.error?.trim()) return `机器人已发送错误说明：${event.error}`;
  return "已完成回复，但该历史记录未保存回复正文";
}

function deliveryLabel(stage?: string): string {
  const labels: Record<string, string> = {
    generated: "回复已生成",
    send_attempted: "已发起发送，等待确认",
    acknowledged: "OneBot 已确认接收",
    echo_persisted: "自回显已落库",
    failed: "发送失败"
  };
  return labels[stage ?? ""] ?? `发送阶段：${stage}`;
}

function deliveryDetail(event: AssistantEventDetail): string {
  if (event.delivery_stage === "echo_persisted") return "已收到机器人自身消息回显并完成持久化";
  if (event.delivery_stage === "acknowledged") return event.outbound_message_id ? `已收到 ACK，消息 ID ${event.outbound_message_id}` : "已收到 OneBot API ACK";
  if (event.delivery_stage === "send_attempted") return "请求已经写入发送链路，但尚无可核验 ACK";
  if (event.delivery_stage === "generated") return "模型或插件已生成回复，尚未发起发送";
  return event.delivery_error?.trim() || "发送链路未完成";
}

function deliveryClass(event: AssistantEventDetail): string {
  if (event.delivery_stage === "acknowledged" || event.delivery_stage === "echo_persisted") return "ok";
  if (event.delivery_stage === "failed") return "err";
  return "warn";
}

function deliveryIcon(event: AssistantEventDetail): Component {
  if (event.delivery_stage === "acknowledged" || event.delivery_stage === "echo_persisted") return CheckCircle2;
  if (event.delivery_stage === "failed") return TriangleAlert;
  return Send;
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

async function toggleTrace(event: AssistantEventDetail): Promise<void> {
  if (traceOpen.value[event.id]) {
    traceOpen.value = { ...traceOpen.value, [event.id]: false };
    return;
  }
  traceOpen.value = { ...traceOpen.value, [event.id]: true };
  if (traceLoaded.value[event.id]) return;
  traceLoading.value = { ...traceLoading.value, [event.id]: true };
  try {
    const result = await getAssistantEventTrace(event.id);
    traceSteps.value = { ...traceSteps.value, [event.id]: result.steps ?? [] };
    traceLoaded.value = { ...traceLoaded.value, [event.id]: true };
  } catch (error) {
    traceOpen.value = { ...traceOpen.value, [event.id]: false };
    toastError(error instanceof Error ? error.message : "调试调用链加载失败");
  } finally {
    traceLoading.value = { ...traceLoading.value, [event.id]: false };
  }
}

function traceMetadata(step: AppLogEntry): Record<string, unknown> {
  return step.metadata ?? {};
}

function tracePhaseLabel(step: AppLogEntry): string {
  const phase = String(traceMetadata(step).phase ?? "");
  const labels: Record<string, string> = {
    model_request: "模型请求",
    agent_started: "Agent 启动",
    agent_tool_started: "工具调用开始",
    agent_tool_completed: "工具调用完成",
    agent_protocol_repair: "Agent 协议修正",
    agent_completed: "Agent 完成",
    agent_failed: "Agent 失败"
  };
  return labels[phase] ?? step.message;
}

function traceSummary(step: AppLogEntry): string {
  const metadata = traceMetadata(step);
  const parts: string[] = [];
  if (metadata.purpose) parts.push(`用途：${String(metadata.purpose)}`);
  if (metadata.provider) parts.push(`Provider：${String(metadata.provider)}`);
  if (metadata.model) parts.push(`模型：${String(metadata.model)}`);
  if (metadata.tool) parts.push(`工具：${String(metadata.tool)}`);
  if (metadata.finish_reason) parts.push(`结束原因：${String(metadata.finish_reason)}`);
  return parts.join(" · ");
}

function traceJSON(step: AppLogEntry, key: string): string {
  const value = traceMetadata(step)[key];
  if (value === undefined || value === null) return "";
  if (Array.isArray(value) && value.length === 0) return "";
  if (typeof value === "object" && !Array.isArray(value) && Object.keys(value as Record<string, unknown>).length === 0) return "";
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function traceText(step: AppLogEntry, key: string): string {
  const value = traceMetadata(step)[key];
  return value === undefined || value === null ? "" : String(value);
}

function traceDuration(step: AppLogEntry): string {
  const value = Number(traceMetadata(step).duration_ms ?? 0);
  return value > 0 ? formatDuration(value) : "";
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
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}

.event-filter-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  width: 100%;
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

.event-range,
.event-result-filter {
  max-width: 100%;
  overflow-x: auto;
}

.event-range button {
  min-width: 74px;
  white-space: nowrap;
}

.event-result-filter button {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  white-space: nowrap;
}

.event-filter-count {
  color: var(--muted);
  font-family: var(--font-mono);
  font-size: 11px;
}

.event-result-filter button.active .event-filter-count {
  color: var(--text-secondary);
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

.event-debug-trace {
  margin-top: 14px;
}

.event-debug-toggle {
  min-height: 34px;
  font-size: 12px;
}

.event-debug-toggle svg:last-child {
  transition: transform 160ms ease;
}

.trace-chevron-open {
  transform: rotate(180deg);
}

.debug-trace-panel {
  margin-top: 14px;
  border-top: 1px solid var(--border);
}

.debug-trace-empty {
  padding: 18px 0;
  font-size: 12px;
}

.debug-trace-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.debug-trace-step {
  padding: 16px 0;
  border-bottom: 1px solid var(--border);
}

.debug-trace-step:last-child {
  border-bottom: 0;
}

.debug-step-header {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
  font-size: 12px;
}

.debug-step-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: 1px solid var(--border-strong);
  border-radius: 4px;
  color: var(--muted);
}

.debug-step-summary {
  margin: 8px 0 0 36px;
  color: var(--muted);
  font-size: 12px;
}

.debug-payload {
  margin: 12px 0 0 36px;
}

.debug-payload > span {
  display: block;
  margin-bottom: 6px;
  color: var(--muted);
  font-size: 11px;
}

.debug-payload pre {
  max-height: 520px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--surface-2);
  color: var(--text);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.debug-payload.err pre {
  border-color: color-mix(in srgb, var(--err) 36%, var(--border));
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

.event-image-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(132px, 168px));
  gap: 10px;
  margin-top: 12px;
}

.event-image-preview {
  display: grid;
  width: 100%;
  aspect-ratio: 1;
  place-items: center;
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--surface-2);
  color: var(--muted);
  transition: border-color 150ms ease, background 150ms ease;
}

.event-image-preview:hover {
  border-color: var(--border-strong);
  background: var(--surface);
}

.event-image-preview:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.event-image-preview img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.event-image-preview.unavailable {
  cursor: default;
}

.event-image-preview.unavailable:hover {
  border-color: var(--border);
  background: var(--surface-2);
}

.event-image-unavailable {
  display: inline-flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  font-size: 12px;
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

.event-delivery {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  gap: 10px;
  align-items: start;
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--muted);
}

.event-delivery.ok { color: var(--ok); }
.event-delivery.warn { color: var(--warn); }
.event-delivery.err { color: var(--err); }
.event-delivery p { margin: 3px 0 0; color: var(--text-secondary); }

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
  .event-filter-row {
    align-items: flex-start;
    flex-direction: column;
    gap: 7px;
  }

  .event-range,
  .event-result-filter {
    width: 100%;
  }

  .event-range button {
    flex: 1 0 auto;
  }

  .event-result-filter {
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    overflow: visible;
  }

  .event-result-filter button {
    grid-column: span 2;
    justify-content: center;
    min-width: 0;
    padding-inline: 7px;
  }

  .event-result-filter button:nth-last-child(-n + 2) {
    grid-column: span 3;
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

  .event-image-grid {
    grid-template-columns: repeat(auto-fill, minmax(116px, 1fr));
  }

  .debug-step-summary,
  .debug-payload {
    margin-left: 0;
  }
}
</style>
