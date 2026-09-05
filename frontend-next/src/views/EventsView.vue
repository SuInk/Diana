<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>

    <div class="stack">
      <section class="event-filter-band" aria-label="事件筛选">
        <div class="event-filter-lead">
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
          <AppSelect
            class="event-group-filter"
            :model-value="selectedGroup"
            :options="groupOptions"
            searchable
            search-placeholder="搜索群聊或私聊"
            aria-label="按会话筛选事件"
            @update:model-value="(value) => selectGroup(String(value))"
          />
          <div class="event-search">
            <Search :size="14" class="event-search-icon" aria-hidden="true" />
            <input
              v-model="searchDraft"
              type="search"
              class="input event-search-input"
              placeholder="搜索消息、回复或消息号"
              aria-label="搜索事件"
              @keyup.enter="applySearch"
              @search="applySearch"
            />
          </div>
          <button class="btn event-filter-refresh" type="button" :disabled="loading" @click="load(true)">
            <RefreshCw :size="15" :class="{ spin: loading }" aria-hidden="true" />
            刷新
          </button>
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
      </section>

      <section v-if="contextBudget" class="card context-budget">
        <div class="card-head">
          <div>
            <h2>上下文预算 · 群 {{ contextBudget.group_id }}</h2>
            <span class="card-sub">
              窗口 {{ formatNumber(contextBudget.context_window) }} token，四层合计
              {{ formatNumber(contextBudget.allocated) }}，其余留给系统提示、当前消息、工具结果与输出
            </span>
          </div>
        </div>
        <div class="card-body">
          <div class="budget-bar" role="img" :aria-label="`四层合计 ${contextBudget.allocated} token，留白 ${contextBudget.headroom} token`">
            <span
              v-for="segment in contextBudgetSegments"
              :key="segment.key"
              class="budget-slice"
              :class="`budget-slice-${segment.key}`"
              :style="{ width: `${segment.percent}%` }"
              :title="`${segment.label} ${segment.tokens} token`"
            ></span>
            <span class="budget-slice budget-slice-headroom" :style="{ width: `${contextBudgetHeadroomPercent}%` }"></span>
          </div>
          <ul class="budget-legend">
            <li v-for="layer in contextBudget.layers" :key="layer.key">
              <span class="budget-dot" :class="`budget-slice-${layer.key}`" aria-hidden="true"></span>
              <span class="budget-legend-label">{{ layer.label }}</span>
              <span class="budget-legend-value mono">{{ formatNumber(layer.tokens) }}</span>
              <span class="budget-legend-foot">{{ contextBudgetLayerFoot(layer) }}</span>
            </li>
            <li>
              <span class="budget-dot budget-slice-headroom" aria-hidden="true"></span>
              <span class="budget-legend-label">留白</span>
              <span class="budget-legend-value mono">{{ formatNumber(contextBudget.headroom) }}</span>
              <span class="budget-legend-foot">系统提示 / 当前消息 / 工具结果 / 输出</span>
            </li>
          </ul>
        </div>
      </section>

      <p class="event-stats-line">
        <span>
          <MessageCircle :size="13" aria-hidden="true" />
          <strong>{{ formatNumber(summary.total) }}</strong> 条事件
          <span class="muted">{{ eventBreakdown }}</span>
        </span>
        <span>
          <Sigma :size="13" aria-hidden="true" />
          Token <strong>{{ formatNumber(summary.total_tokens) }}</strong>
          <span class="muted">{{ tokenBreakdown }}</span>
        </span>
        <span>
          <Gauge :size="13" aria-hidden="true" />
          <strong>{{ throughputText }}</strong>
          <span class="muted">{{ throughputFoot }}</span>
        </span>
      </p>

      <section class="card event-detail-card">
        <div class="card-header">
          <div class="cluster">
            <h2>事件记录</h2>
            <span class="badge" :class="stream.connected ? 'ok' : 'warn'">
              <span class="status-dot" :class="{ pulse: stream.connected }" aria-hidden="true" />
              {{ stream.connected ? "实时更新" : "实时连接中断" }}
            </span>
            <button v-if="pendingLiveEvents" class="btn small" type="button" @click="showLatestEvents">
              有新事件
            </button>
          </div>
          <span class="muted event-result-count">{{ resultCountText }}</span>
        </div>

        <div v-if="events.length > 0" class="event-detail-list">
          <template v-for="(event, index) in events" :key="event.id">
            <div v-if="showDateSeparator(index)" class="event-date-separator">{{ formatDate(event.at) }}</div>
            <article class="event-detail-row">
            <div class="event-detail-time">
              <strong>{{ formatClock(event.at) }}</strong>
            </div>

            <div class="event-detail-main">
              <div class="event-detail-meta">
                <span v-if="event.platform" class="badge">{{ platformLabel(event.platform) }}</span>
                <span class="badge">{{ eventKindLabel(event.kind) }}</span>
                <span class="badge" :class="decisionClass(event)">{{ decisionLabel(event) }}</span>
                <span class="event-sender" :title="senderTitle(event)">
                  <img
                    v-if="event.sender_avatar_url && !failedAvatars[event.id]"
                    class="event-avatar"
                    :src="event.sender_avatar_url"
                    alt=""
                    loading="lazy"
                    decoding="async"
                    @error="markAvatarFailed(event)"
                  />
                  <span v-else class="event-avatar event-avatar-fallback" aria-hidden="true">{{ senderInitial(event) }}</span>
                  <span class="event-sender-name">{{ senderDisplayName(event) }}</span>
                  <span v-if="event.sender_name && event.user_id" class="event-sender-id mono">{{ event.user_id }}</span>
                </span>
                <span v-if="senderLevelLabel(event)" class="badge" :title="senderLevelTitle(event)">{{ senderLevelLabel(event) }}</span>
                <template v-if="event.group_id">
                  <span class="event-meta-sep" aria-hidden="true">·</span>
                  <span class="muted event-meta-group" :title="displayEventGroup(event.group_id, event.group_name)">{{ groupShortName(event) }}</span>
                </template>
                <span v-if="event.duration_ms" class="muted">{{ formatDuration(event.duration_ms) }}</span>
              </div>

              <div v-if="isRecallEvent(event)" class="event-recall-summary">
                <strong>撤回记录</strong>
                <p>{{ recallActorText(event) }}</p>
                <span v-if="event.original_time" class="muted">原消息发送于 {{ formatClock(event.original_time) }}</span>
              </div>

              <p v-if="displayMessageText(event.text)" class="event-detail-message">{{ displayMessageText(event.text) }}</p>
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
                  <button
                    v-else
                    class="event-image-preview"
                    type="button"
                    :aria-label="imageAriaLabel(image.index, image.summary)"
                    title="查看原图"
                    @click="openImage(event, image.index, image.summary)"
                  >
                    <img
                      :src="eventImageThumbnailURL(event, image.index)"
                      :alt="imageAlt(image.index, image.summary)"
                      loading="lazy"
                      decoding="async"
                      @error="markImageFailed(event, image.index)"
                    />
                  </button>
                </template>
              </div>

              <div v-if="!isNoticeEvent(event)" class="event-decision" :class="decisionClass(event)">
                <component :is="decisionIcon(event)" :size="16" aria-hidden="true" />
                <div>
                  <strong>{{ decisionReasonLabel(event) }}</strong>
                  <p>{{ event.reason || fallbackDecisionReason(event) }}</p>
                </div>
              </div>

              <div v-if="event.decision === 'replied' || event.handled" class="event-detail-reply">
                <strong>回复结果</strong>
                <p>{{ replyResultText(event) }}</p>
                <p v-if="deliverySummary(event)" class="muted">{{ deliverySummary(event) }}</p>
              </div>
              <div v-if="event.subtasks?.length" class="event-subtasks">
                <strong>触发的后台任务</strong>
                <ul>
                  <li v-for="task in event.subtasks" :key="task.task_id" :class="subtaskClass(task)">
                    <span class="subtask-name">{{ task.name || task.kind }}</span>
                    <span class="badge" :class="subtaskClass(task)">{{ subtaskPhaseLabel(task) }}</span>
                    <span v-if="subtaskProgress(task)" class="muted">{{ subtaskProgress(task) }}</span>
                    <span v-if="subtaskDuration(task)" class="muted">{{ subtaskDuration(task) }}</span>
                    <span class="muted mono">{{ task.task_id }}</span>
                    <p v-if="task.error" class="event-error">{{ task.error }}</p>
                    <p v-else-if="task.detail" class="muted">{{ task.detail }}</p>
                  </li>
                </ul>
              </div>
              <div v-if="event.delivery_stage" class="event-delivery" :class="[deliveryClass(event), { quiet: deliverySettled(event) }]">
                <component :is="deliveryIcon(event)" :size="deliverySettled(event) ? 14 : 16" aria-hidden="true" />
                <div>
                  <strong>{{ deliveryLabel(event.delivery_stage) }}</strong>
                  <p v-if="!deliverySettled(event)">{{ deliveryDetail(event) }}</p>
                </div>
              </div>
              <p v-if="event.error && !(event.reason || '').includes(event.error)" class="event-error">{{ event.error }}</p>
              <p v-if="event.delivery_error" class="event-error">{{ event.delivery_error }}</p>

              <div class="event-technical muted mono">
                <span v-if="event.message_id">消息 {{ event.message_id }}</span>
                <span>结果 {{ event.outcome || event.status }}</span>
                <span v-if="event.outbound_message_id">出站 {{ event.outbound_message_id }}</span>
                <span v-if="event.total_tokens">
                  Token {{ formatNumber(event.total_tokens) }}（输入 {{ formatNumber(event.input_tokens || 0) }} / 输出 {{ formatNumber(event.output_tokens || 0) }}<template v-if="eventCacheHitText(event)"> / {{ eventCacheHitText(event) }}</template>）
                </span>
                <span v-if="event.output_tokens_per_second">
                  {{ event.output_tokens_per_second.toFixed(1) }} tok/s（模型耗时 {{ formatDurationMS(event.llm_duration_ms || 0) }}<template v-if="event.ttft_calls"> / 首 token {{ formatDurationMS(event.avg_ttft_ms || 0) }}</template>）
                </span>
              </div>

              <div v-if="!isNoticeEvent(event)" class="event-debug-trace">
                <button class="btn event-debug-toggle" type="button" :disabled="traceLoading[event.id]" @click="toggleTrace(event)">
                  <LoaderCircle v-if="traceLoading[event.id]" :size="14" class="spin" aria-hidden="true" />
                  <Bug v-else :size="14" aria-hidden="true" />
                  {{ traceOpen[event.id] ? "收起调用链" : "查看模型上下文与调用链" }}
                  <ChevronDown :size="14" :class="{ 'trace-chevron-open': traceOpen[event.id] }" aria-hidden="true" />
                </button>

                <div v-if="traceOpen[event.id]" class="debug-trace-panel">
                  <section v-if="event.memories?.length" class="event-memories" aria-label="本轮调用的长期记忆">
                    <button
                      class="event-memory-toggle"
                      type="button"
                      :aria-expanded="memoriesOpen[event.id] ? 'true' : 'false'"
                      @click="toggleMemories(event.id)"
                    >
                      <ChevronDown :size="13" :class="{ 'trace-chevron-open': memoriesOpen[event.id] }" aria-hidden="true" />
                      本轮调用的长期记忆（{{ event.memories.length }}）
                      <span v-if="!memoriesOpen[event.id]" class="event-memory-peek muted">{{ memoryPeek(event.memories) }}</span>
                    </button>
                    <div v-if="memoriesOpen[event.id]" class="event-memory-list">
                      <article v-for="memory in event.memories" :key="memory.id || `${memory.kind}:${memory.content}`" class="event-memory-item">
                        <div class="event-memory-head">
                          <span class="badge">{{ memoryKindLabel(memory.kind) }}</span>
                          <strong>{{ memory.topic || memory.entity || "未命名记忆" }}</strong>
                          <span v-if="memory.sensitive" class="badge warn">敏感</span>
                        </div>
                        <p>{{ memory.content }}</p>
                        <div class="event-memory-meta muted mono">
                          <span v-if="memory.source_type">来源 {{ memory.source_type }}</span>
                          <span v-if="memory.visibility">可见性 {{ memory.visibility }}</span>
                          <span v-if="memory.source_group_id">来源群 {{ memory.source_group_id }}</span>
                          <span v-if="memory.confidence">置信度 {{ formatMemoryScore(memory.confidence) }}</span>
                          <span v-if="memory.retrieval_score">召回分 {{ memory.retrieval_score.toFixed(3) }}</span>
                        </div>
                      </article>
                    </div>
                  </section>

                  <section v-if="event.temporary_memories?.length" class="event-memories temporary" aria-label="本轮调用的临时记忆">
                    <button
                      class="event-memory-toggle"
                      type="button"
                      :aria-expanded="temporaryMemoriesOpen[event.id] ? 'true' : 'false'"
                      @click="toggleTemporaryMemories(event.id)"
                    >
                      <ChevronDown :size="13" :class="{ 'trace-chevron-open': temporaryMemoriesOpen[event.id] }" aria-hidden="true" />
                      本轮调用的临时记忆（{{ event.temporary_memories.length }}）
                    </button>
                    <div v-if="temporaryMemoriesOpen[event.id]" class="event-memory-list">
                      <article v-for="memory in event.temporary_memories" :key="memory.id || `${memory.kind}:${memory.task_kind || memory.topic || ''}`" class="event-memory-item">
                        <div class="event-memory-head">
                          <span class="badge">{{ temporaryMemoryKindLabel(memory.kind) }}</span>
                          <span v-if="memory.scope" class="badge">{{ memory.scope === "session" ? "会话共享" : "用户私有" }}</span>
                          <strong>{{ memory.task_kind || memory.topic || "当前会话状态" }}</strong>
                          <span v-if="memory.version" class="badge">v{{ memory.version }}</span>
                        </div>
                        <pre>{{ temporaryMemoryContent(memory.content) }}</pre>
                        <div class="event-memory-meta muted mono">
                          <span v-if="memory.expires_at">到期 {{ formatDate(memory.expires_at) }}</span>
                          <span v-if="memory.source_message_id">来源消息 {{ memory.source_message_id }}</span>
                        </div>
                      </article>
                    </div>
                  </section>
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
          </template>
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

    <Teleport to="body">
      <div v-if="activeImage" class="event-image-lightbox" role="presentation" @click.self="closeImage">
        <div class="event-image-lightbox-dialog" role="dialog" aria-modal="true" :aria-label="activeImage.alt">
          <div class="event-image-lightbox-header">
            <span>{{ activeImage.alt }}</span>
            <button class="btn ghost icon-only small" type="button" aria-label="关闭原图" title="关闭" @click="closeImage">
              <X :size="17" aria-hidden="true" />
            </button>
          </div>
          <div class="event-image-lightbox-body">
            <img :src="activeImage.url" :alt="activeImage.alt" />
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { botScope } from "../bot-scope";
import type { Component } from "vue";
import {
  Activity,
  Bug,
  CheckCircle2,
  ChevronDown,
  Gauge,
  ImageOff,
  LoaderCircle,
  MessageCircle,
  MessageCircleOff,
  MessageCircleReply,
  RefreshCw,
  Search,
  Sigma,
  Send,
  TimerReset,
  TriangleAlert,
  X
} from "@lucide/vue";
import {
  getAssistantEventTrace,
  getAssistantEvents,
  type AppLogEntry,
  type AssistantEventDetail,
  type AssistantEventMemory,
  type AssistantEventDelivery,
  type AssistantEventSubtask,
  type AssistantEventRange,
  type AssistantEventResultFilter,
  type AssistantEventsResponse,
  type AssistantContextBudgetLayer
} from "../api";
import { formatClock, formatNumber } from "../format";
import { displayMessageText, displayChatIdentity } from "../message-display";
import { currentView } from "../router";
import { stream } from "../stream";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";

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
  { value: "error", label: "处理异常" },
  { value: "notice", label: "通知" }
];

const selectedRange = ref<AssistantEventRange>("24h");
const selectedResult = ref<AssistantEventResultFilter>("all");
const selectedGroup = ref("");
// 搜索按回车提交，不做输入即查：事件表没有全文索引，每敲一个字就查一次
// 既慢又会把结果刷得跳来跳去。
const searchDraft = ref("");
const searchTerm = ref("");

// 换了机器人就重新拉：事件按 profile 在服务端过滤，前端筛没有意义（分页会漏）。
watch(botScope, () => {
  void load(true);
});
const events = ref<AssistantEventDetail[]>([]);
const response = ref<AssistantEventsResponse | null>(null);
const page = ref(1);
const loading = ref(false);
const loadingMore = ref(false);

function memoryKindLabel(kind?: string): string {
  const labels: Record<string, string> = {
    fact: "事实", preference: "偏好", episode: "情景", instruction: "长期要求",
    summary: "摘要", thread: "会话状态"
  };
  return labels[kind ?? ""] ?? kind ?? "记忆";
}

function temporaryMemoryKindLabel(kind?: string): string {
  if (kind === "private_thread_state") return "私有任务状态";
  if (kind === "session_thread") return "会话线程便签";
  return kind || "临时记忆";
}

function temporaryMemoryContent(content: unknown): string {
  if (typeof content === "string") return content;
  try {
    return JSON.stringify(content, null, 2);
  } catch {
    return String(content ?? "");
  }
}

function formatMemoryScore(value: number): string {
  return `${Math.round(value * 100)}%`;
}
// 记忆块默认收起：一条消息动辄召回十来条长期记忆，全展开会把事件本身挤出屏幕。
const memoriesOpen = ref<Record<string, boolean>>({});
const temporaryMemoriesOpen = ref<Record<string, boolean>>({});
const traceOpen = ref<Record<string, boolean>>({});
const traceLoading = ref<Record<string, boolean>>({});
const traceLoaded = ref<Record<string, boolean>>({});
const traceSteps = ref<Record<string, AppLogEntry[]>>({});
const failedImages = ref<Record<string, boolean>>({});
const activeImage = ref<{ url: string; alt: string } | null>(null);
const pendingLiveEvents = ref(false);
let refreshTimer: number | null = null;
let loadGeneration = 0;
const LIVE_SYNC_TOP_PX = 96;

function pageScrollTop(): number {
  return window.scrollY || document.documentElement.scrollTop || 0;
}

function isReadingBelowTop(): boolean {
  return pageScrollTop() > LIVE_SYNC_TOP_PX;
}

const summary = computed(() => ({
  total: response.value?.total ?? 0,
  replied: response.value?.replied ?? 0,
  not_replied: response.value?.not_replied ?? 0,
  pending: response.value?.pending ?? 0,
  errors: response.value?.errors ?? 0,
  notices: response.value?.notices ?? 0,
  llm_calls: response.value?.llm_calls ?? 0,
  input_tokens: response.value?.input_tokens ?? 0,
  output_tokens: response.value?.output_tokens ?? 0,
  total_tokens: response.value?.total_tokens ?? 0,
  cached_input_tokens: response.value?.cached_input_tokens ?? 0,
  llm_duration_ms: response.value?.llm_duration_ms ?? 0,
  output_tokens_per_second: response.value?.output_tokens_per_second ?? 0,
  avg_ttft_ms: response.value?.avg_ttft_ms ?? 0,
  ttft_calls: response.value?.ttft_calls ?? 0
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
// 按状态拆开的计数（已回复 / 未回复 / 等待处理 / 异常 / 通知）在上面的筛选标签里
// 已经逐项列出，而且点了还能筛。这里只补标签给不出的东西：统计范围和回复率。
const eventBreakdown = computed(() => {
  const messageTotal = summary.value.total - summary.value.notices;
  if (messageTotal <= 0) return `${rangeDescription.value} · 暂无处理记录`;
  const rate = Math.round((summary.value.replied / messageTotal) * 100);
  const parts = [rangeDescription.value, `回复率 ${rate}%`];
  if (summary.value.pending > 0) parts.push(`${formatNumber(summary.value.pending)} 条等待处理`);
  return parts.join(" · ");
});
const tokenBreakdown = computed(() => {
  const cached = cacheHitRate(summary.value.cached_input_tokens, summary.value.input_tokens);
  // 缓存命中率原本独占一张卡，但它的分母就是这里的「输入」，拆成两张只是把同一个
  // 数字写两遍。挂在输入后面，命中多少一眼就能对上。
  const input = cached === null
    ? `输入 ${formatNumber(summary.value.input_tokens)}`
    : `输入 ${formatNumber(summary.value.input_tokens)}（缓存命中 ${Math.round(cached * 100)}%）`;
  return `${input} / 输出 ${formatNumber(summary.value.output_tokens)} · ${formatNumber(summary.value.llm_calls)} 次调用`;
});

// 速率算的是输出 token / 模型墙钟耗时。这里不叫 TTFT——回复链路走的是非流式
// Generate，整份回复一次到达，没有「首 token」这个时刻可测。
const throughputText = computed(() => {
  if (summary.value.output_tokens_per_second <= 0) return "—";
  return `${summary.value.output_tokens_per_second.toFixed(1)} tok/s`;
});

const throughputFoot = computed(() => {
  if (summary.value.llm_duration_ms <= 0) {
    // 判的是「没有耗时数据」，不是「没有调用」。调用次数就在旁边那张卡上，
    // 这里再说一句「没有模型调用」会自相矛盾。
    return summary.value.llm_calls > 0 ? "暂无耗时样本" : "当前范围没有模型调用";
  }
  // 输出 token 总量在上一张卡里，这里不再重复，只留速率本身的分母和 TTFT。
  const base = `模型耗时 ${formatDurationMS(summary.value.llm_duration_ms)}`;
  // TTFT 只有开了流式、且底层没退化成非流式时才有样本；没样本就不提，
  // 免得显示成「首 token 0ms」这种看着正常实际是缺数据的东西。
  if (summary.value.ttft_calls > 0) {
    return `${base} · 首 token ${formatDurationMS(summary.value.avg_ttft_ms)}`;
  }
  return base;
});

// 毫秒转成人读得懂的长度。跨度可能从几百毫秒到几小时（一个范围的累计耗时）。
function formatDurationMS(ms: number): string {
  if (ms <= 0) return "0s";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m${Math.round(seconds % 60)}s`;
  return `${Math.floor(minutes / 60)}h${minutes % 60}m`;
}

// 命中的部分本来就算在输入 token 里，不是额外的量，所以分母是输入而不是总量。
// 供应商按缓存命中打折计价，这个比例直接对应省下来的钱。
function cacheHitRate(cached: number, input: number): number | null {
  if (input <= 0) return null;
  return Math.min(1, cached / input);
}

function eventCacheHitText(event: AssistantEventDetail): string {
  const rate = cacheHitRate(event.cached_input_tokens || 0, event.input_tokens || 0);
  if (rate === null || !event.cached_input_tokens) return "";
  return `缓存命中 ${formatNumber(event.cached_input_tokens)}（${Math.round(rate * 100)}%）`;
}

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (["onebot-v11", "onebot", "napcat", "lagrange", "go-cqhttp"].includes(platform)) return "OneBot v11";
  return platform;
}

function imageKey(event: AssistantEventDetail, imageIndex: number): string {
  return `${event.id}:${imageIndex}`;
}

function eventImageURL(event: AssistantEventDetail, imageIndex: number): string {
  return `/api/assistant/events/${encodeURIComponent(event.id)}/images/${imageIndex}`;
}

function eventImageThumbnailURL(event: AssistantEventDetail, imageIndex: number): string {
  return `${eventImageURL(event, imageIndex)}?thumbnail=1`;
}

function imageSummary(summary?: string): string {
  return (summary ?? "").replace(/^\[|\]$/g, "").trim();
}

function imageAlt(imageIndex: number, summary?: string): string {
  const label = imageSummary(summary);
  return label ? `图片 ${imageIndex}：${label}` : `消息图片 ${imageIndex}`;
}

function imageAriaLabel(imageIndex: number, summary?: string): string {
  return `${imageAlt(imageIndex, summary)}，点击查看原图`;
}

function openImage(event: AssistantEventDetail, imageIndex: number, summary?: string): void {
  activeImage.value = { url: eventImageURL(event, imageIndex), alt: imageAlt(imageIndex, summary) };
}

function closeImage(): void {
  activeImage.value = null;
}

function onImageKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape" && activeImage.value) closeImage();
}

function markImageFailed(event: AssistantEventDetail, imageIndex: number): void {
  failedImages.value = { ...failedImages.value, [imageKey(event, imageIndex)]: true };
}

function resultOptionCount(result: AssistantEventResultFilter): number {
  if (result === "all") return summary.value.total;
  if (result === "replied") return summary.value.replied;
  if (result === "not_replied") return summary.value.not_replied;
  if (result === "pending") return summary.value.pending;
  if (result === "notice") return summary.value.notices;
  return summary.value.errors;
}

function isNoticeEvent(event: AssistantEventDetail): boolean {
  return event.decision === "notice" || event.kind === "notice";
}

function isRecallEvent(event: AssistantEventDetail): boolean {
  return event.sub_type === "group_recall" || event.sub_type === "friend_recall";
}

// 身份取值现在是平台无关的 group_*（后端 GroupRole）。老库里的事件仍存着各平台
// 的原始说法，所以两套键都要认，否则历史记录会退化成显示英文原文。
const groupRoleLabels: Record<string, string> = {
  group_owner: "群主",
  group_admin: "管理员",
  group_member: "群成员",
  owner: "群主",
  admin: "管理员",
  member: "群成员",
  creator: "群主",
  administrator: "管理员"
};

function recallRoleLabel(role?: string): string {
  const key = (role ?? "").trim();
  const labels: Record<string, string> = {
    ...groupRoleLabels,
    bot: "机器人",
    history_backfill: "断线回补"
  };
  return labels[key] ?? key;
}

function recallActorText(event: AssistantEventDetail): string {
  if (event.operator_role === "history_backfill") return "断线回补确认消息已撤回，平台历史接口未提供实际操作者";
  const selfRecall = event.operator_id && event.operator_id === event.user_id;
  const name = event.operator_name?.trim() || (selfRecall ? event.sender_name?.trim() : "");
  const identity = [name, event.operator_id].filter(Boolean).join(" · ") || "未知操作者";
  const role = recallRoleLabel(event.operator_role);
  if (selfRecall) return `${identity} 撤回了自己的消息`;
  return `${identity}${role ? `（${role}）` : ""} 撤回了 ${event.sender_name?.trim() || event.user_id || "一名成员"} 的消息`;
}

async function load(reset: boolean): Promise<void> {
  const generation = reset ? ++loadGeneration : loadGeneration;
  const requestedPage = reset ? 1 : page.value;
  const requestedRange = selectedRange.value;
  const requestedResult = selectedResult.value;
  const requestedGroup = selectedGroup.value;
  const requestedSearch = searchTerm.value;
  if (reset) {
    loading.value = true;
    page.value = 1;
    pendingLiveEvents.value = false;
  } else {
    loadingMore.value = true;
  }
  try {
    const next = await getAssistantEvents(
      requestedRange,
      requestedResult,
      requestedPage,
      50,
      requestedGroup.startsWith(USER_PREFIX) ? "" : requestedGroup.replace(GROUP_PREFIX, ""),
      botScope.value,
      requestedGroup.startsWith(USER_PREFIX) ? requestedGroup.slice(USER_PREFIX.length) : "",
      requestedSearch
    );
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

function applySearch(): void {
  const next = searchDraft.value.trim();
  if (next === searchTerm.value) return;
  searchTerm.value = next;
  events.value = [];
  response.value = null;
  void load(true);
}

function selectGroup(value: string): void {
  if (selectedGroup.value === value) return;
  selectedGroup.value = value;
  events.value = [];
  response.value = null;
  void load(true);
}

// 群选项跟着时间范围走，只列这段时间里真有事件的群：机器人可能进了几十个群，
// 绝大多数一条事件都没有，全列出来反而找不到要看的那个。
// 会话筛选器同时列群和私聊。值带前缀区分二者：私聊没有群号，后端也要按不同
// 字段筛，混在一起会把「群 123」和「用户 123」当成同一个会话。
const GROUP_PREFIX = "g:";
const USER_PREFIX = "u:";

const groupOptions = computed(() => {
  const options: AppSelectOption[] = [{ value: "", label: "全部会话" }];
  // 群聊和私聊分开列：两类会话的编号体系不一样，混在一条长列表里很难扫。
  for (const group of response.value?.groups ?? []) {
    options.push({ ...groupOption(group.group_id, group.events, group.group_name, group.avatar_url), group: "群聊" });
  }
  for (const chat of response.value?.private_chats ?? []) {
    options.push({ ...privateChatOption(chat.user_id, chat.events, chat.user_name), group: "私聊" });
  }
  // 选中的会话这一轮可能已经没有事件了（换了更短的时间范围），选项要留着，
  // 否则下拉框显示空白、也没法切回去。
  if (selectedGroup.value && !options.some((option) => option.value === selectedGroup.value)) {
    const id = selectedSessionID.value;
    options.push(selectedGroup.value.startsWith(USER_PREFIX) ? privateChatOption(id, 0) : groupOption(id, 0));
  }
  return options;
});

// selectedSessionID 去掉前缀，拿到真正的群号或账号。
const selectedSessionID = computed(() => {
  const value = selectedGroup.value;
  if (value.startsWith(GROUP_PREFIX)) return value.slice(GROUP_PREFIX.length);
  if (value.startsWith(USER_PREFIX)) return value.slice(USER_PREFIX.length);
  return value;
});

// 头像取不回来（号码注销、外网被挡）时退回首字母占位，不要留一个碎图标。
const failedAvatars = ref<Record<string, boolean>>({});

function markAvatarFailed(event: AssistantEventDetail): void {
  failedAvatars.value = { ...failedAvatars.value, [event.id]: true };
}

function senderDisplayName(event: AssistantEventDetail): string {
  return (event.sender_name ?? "").trim() || (event.user_id ?? "").trim() || "未知发送者";
}

function senderInitial(event: AssistantEventDetail): string {
  return [...senderDisplayName(event)][0] ?? "?";
}

// 昵称那段整体挂一个悬浮：昵称可能被省略号截断，账号也未必都摆在外面。
function senderTitle(event: AssistantEventDetail): string {
  const identity = displayChatIdentity(event.sender_name, event.user_id);
  return identity || senderDisplayName(event);
}

// 群名单独显示，群号退到悬浮里：一行里群号和账号两串数字挨着，眼睛分不开它们，
// 而群号本身很少是要扫的目标。
function groupShortName(event: AssistantEventDetail): string {
  const id = (event.group_id ?? "").trim();
  const full = displayEventGroup(id, event.group_name);
  const name = full.endsWith(`（${id}）`) ? full.slice(0, -(id.length + 2)) : full;
  return name.trim() || `群 ${id}`;
}

// 同一天的日期没必要每行重复，只在换天的地方插一条分隔。
function showDateSeparator(index: number): boolean {
  if (index === 0) return true;
  return dateKey(events.value[index].at) !== dateKey(events.value[index - 1].at);
}

function dateKey(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toDateString();
}

function displayEventGroup(groupID?: string, eventName?: string): string {
  const id = (groupID ?? "").trim();
  const option = groupOptions.value.find((item) => item.value === id);
  const name = (eventName ?? (option?.label.startsWith("群 ") ? "" : option?.label) ?? "").trim();
  return name ? `${name}（${id}）` : `群 ${id}`;
}

// 群名当主标题、群号退到副行：一串纯数字认不出是哪个群，而名字可能重复或为空，
// 所以号码不能省，只是不该占主位。
function groupOption(groupID: string, events: number, name?: string, avatar?: string): AppSelectOption {
  const label = (name ?? "").trim();
  return {
    value: GROUP_PREFIX + groupID,
    label: label || `群 ${groupID}`,
    hint: label ? `${groupID} · ${formatNumber(events)} 条` : `${formatNumber(events)} 条`,
    avatar
  };
}

function privateChatOption(userID: string, events: number, name?: string): AppSelectOption {
  const label = (name ?? "").trim();
  return {
    value: USER_PREFIX + userID,
    label: label || `私聊 ${userID}`,
    hint: label ? `${userID} · ${formatNumber(events)} 条` : `${formatNumber(events)} 条`
  };
}

const contextBudget = computed(() => response.value?.context_budget ?? null);

// 每一层在整条窗口里占的宽度。留白单独算，它是「没有分配出去」的部分。
const contextBudgetSegments = computed(() => {
  const budget = contextBudget.value;
  if (!budget || budget.context_window <= 0) return [];
  return budget.layers.map((layer) => ({
    ...layer,
    percent: (layer.tokens / budget.context_window) * 100
  }));
});

const contextBudgetHeadroomPercent = computed(() => {
  const budget = contextBudget.value;
  if (!budget || budget.context_window <= 0) return 0;
  return (budget.headroom / budget.context_window) * 100;
});

function contextBudgetLayerFoot(layer: AssistantContextBudgetLayer): string {
  const rule = layer.capped_by_ceiling ? `上限 ${formatNumber(layer.ceiling)}` : `窗口 ${layer.share_percent}%`;
  return layer.configurable ? `${rule} · 可配` : rule;
}

function eventKindLabel(kind: string): string {
  const labels: Record<string, string> = { private: "私聊", group: "群聊", notice: "通知", meta: "元事件" };
  return labels[kind] ?? kind;
}

// 与 recallRoleLabel 共用一份映射，别再各写一套。

// 群等级：优先显示平台给的等级名（「冒泡」「潜水」这类），没有就显示数字等级。
// 回复门槛按等级卡人，排查「这条为什么没回」时要能直接看到当时的等级。
function senderLevelLabel(event: AssistantEventDetail): string {
  const label = event.sender_level_label?.trim();
  if (label) return `Lv.${label}`;
  if (typeof event.sender_level === "number" && event.sender_level > 0) return `Lv.${event.sender_level}`;
  return "";
}

function senderLevelTitle(event: AssistantEventDetail): string {
  const parts: string[] = [];
  if (typeof event.sender_level === "number" && event.sender_level > 0) parts.push(`群等级 ${event.sender_level}`);
  const label = event.sender_level_label?.trim();
  if (label) parts.push(label);
  const role = event.sender_role?.trim();
  if (role) parts.push(groupRoleLabels[role] ?? role);
  return parts.join(" · ");
}

function decisionLabel(event: AssistantEventDetail): string {
  if (isNoticeEvent(event)) return "已记录";
  if (event.outcome === "merged_into_reply") return "已并入";
  if (event.decision === "replied" || event.handled) return "已回复";
  if (event.decision === "pending") return "等待处理";
  if (event.decision === "error") return "处理异常";
  return "未回复";
}

function decisionClass(event: AssistantEventDetail): string {
  if (isNoticeEvent(event)) return "quiet";
  if (event.outcome === "merged_into_reply") return "ok";
  if (event.decision === "replied" || event.handled) return "ok";
  if (event.decision === "pending") return "warn";
  if (event.decision === "error") return "err";
  return "quiet";
}

function decisionIcon(event: AssistantEventDetail): Component {
  if (event.outcome === "merged_into_reply") return MessageCircleReply;
  if (event.decision === "replied" || event.handled) return CheckCircle2;
  if (event.decision === "pending") return TimerReset;
  if (event.decision === "error") return TriangleAlert;
  return MessageCircleOff;
}

// 结论已经写在 badge 上了，这行只说「原因」就够，省下的宽度留给原因本身。
function decisionReasonLabel(event: AssistantEventDetail): string {
  if (event.outcome === "merged_into_reply") return "状态";
  if (event.decision === "pending") return "状态";
  return "原因";
}

function fallbackDecisionReason(event: AssistantEventDetail): string {
  if (event.decision === "pending") return "消息仍在等待机器人处理";
  if (event.decision === "error") return event.error?.trim() || "消息处理发生异常，但没有保存更详细的错误";
  if (event.decision === "replied" || event.handled) return "消息已通过回复路由并完成回答";
  if (event.outcome) return `消息未回复，处理结果为 ${event.outcome}`;
  return "消息未命中回复规则，旧记录没有保存更详细的判断原因";
}

function subtaskPhaseLabel(task: AssistantEventSubtask): string {
  switch (task.phase) {
    case "queued":
      return "排队中";
    case "running":
      return "进行中";
    case "completed":
      return "已完成";
    case "failed":
      return "失败";
    default:
      return task.phase || "进行中";
  }
}

function subtaskClass(task: AssistantEventSubtask): string {
  if (task.phase === "failed" || task.error) return "err";
  if (task.phase === "completed") return "ok";
  return "warn";
}

function subtaskProgress(task: AssistantEventSubtask): string {
  if (!task.total || task.total <= 1) return "";
  return `${task.completed || 0}/${task.total}`;
}

// 未完成的任务按「已经跑了多久」显示：卡住的任务正是要在这里一眼看出来的。
function subtaskDuration(task: AssistantEventSubtask): string {
  const started = Date.parse(task.started_at);
  if (Number.isNaN(started)) return "";
  const end = task.finished_at ? Date.parse(task.finished_at) : Date.now();
  if (Number.isNaN(end) || end < started) return "";
  const elapsed = end - started;
  const text = formatDuration(elapsed);
  return task.finished_at ? text : `已运行 ${text}`;
}

// 纯文字回复时不显示这一行——「本轮发出：1 条消息」没有信息量。只有发过媒体或
// 转发卡片时才补，那些恰恰是回复正文说不出来的部分。
function deliverySummary(event: AssistantEventDetail): string {
  const delivery = event.delivery;
  if (!delivery) return "";
  const hasMedia = Boolean(delivery.images || delivery.videos || delivery.audios || delivery.forward_cards);
  if (!hasMedia) return "";
  return `本轮发出：${deliveryParts(delivery).join("、")}`;
}

function deliveryParts(delivery?: AssistantEventDelivery): string[] {
  if (!delivery) return [];
  const parts: string[] = [];
  if (delivery.forward_cards) {
    const nodes = delivery.forward_nodes ? `（${delivery.forward_nodes} 条）` : "";
    parts.push(`${delivery.forward_cards} 张转发卡片${nodes}`);
  }
  if (delivery.messages) parts.push(`${delivery.messages} 条消息`);
  if (delivery.images) parts.push(`${delivery.images} 张图片`);
  if (delivery.videos) parts.push(`${delivery.videos} 个视频`);
  if (delivery.audios) parts.push(`${delivery.audios} 条语音`);
  return parts;
}

function replyResultText(event: AssistantEventDetail): string {
  if (event.reply?.trim()) return displayMessageText(event.reply);
  if (event.error?.trim()) return `机器人已发送错误说明：${displayMessageText(event.error)}`;
  // 以前这里一律写「未保存回复正文」。发媒体不发文字是正常情况，说成没保存是误导。
  const parts = deliveryParts(event.delivery);
  if (parts.length) return `本轮没有文字回复，只发了${parts.join("、")}`;
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

// 送达成功是常态：只留一行浅色说明。它的补充文案没有额外信息，出站消息 ID
// 下方的技术信息里已经有了；未完成和失败才值得占一整块卡片提醒。
function deliverySettled(event: AssistantEventDetail): boolean {
  return event.delivery_stage === "acknowledged" || event.delivery_stage === "echo_persisted";
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

function toggleMemories(eventID: string): void {
  memoriesOpen.value = { ...memoriesOpen.value, [eventID]: !memoriesOpen.value[eventID] };
}

function toggleTemporaryMemories(eventID: string): void {
  temporaryMemoriesOpen.value = { ...temporaryMemoriesOpen.value, [eventID]: !temporaryMemoriesOpen.value[eventID] };
}

// 收起时给一行提要，让人不展开也知道这轮召回的大致是什么。
function memoryPeek(memories: AssistantEventMemory[]): string {
  const titles = memories
    .map((memory) => (memory.topic || memory.entity || "").trim())
    .filter((title) => title !== "");
  if (titles.length === 0) return "";
  const shown = titles.slice(0, 3).join("、");
  return titles.length > 3 ? `${shown} 等` : shown;
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
  if (metadata.provider) parts.push(`提供商：${String(metadata.provider)}`);
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

function mergeLiveEvents(incoming: AssistantEventDetail[]): void {
  if (events.value.length === 0) {
    events.value = incoming;
    return;
  }
  const existingIndex = new Map(events.value.map((item, index) => [item.id, index]));
  const next = [...events.value];
  const prepend: AssistantEventDetail[] = [];
  for (const item of incoming) {
    const index = existingIndex.get(item.id);
    if (index === undefined) {
      prepend.push(item);
      continue;
    }
    next[index] = item;
  }
  events.value = prepend.length > 0 ? [...prepend, ...next] : next;
}

async function syncLiveEvents(): Promise<void> {
  if (loading.value || loadingMore.value) return;
  if (currentView.value !== "events" || isReadingBelowTop()) {
    pendingLiveEvents.value = true;
    return;
  }
  try {
    const next = await getAssistantEvents(
      selectedRange.value,
      selectedResult.value,
      1,
      50,
      selectedGroup.value.startsWith(USER_PREFIX) ? "" : selectedGroup.value.replace(GROUP_PREFIX, ""),
      botScope.value,
      selectedGroup.value.startsWith(USER_PREFIX) ? selectedSessionID.value : "",
      searchTerm.value
    );
    if (currentView.value !== "events" || isReadingBelowTop()) {
      pendingLiveEvents.value = true;
      return;
    }
    response.value = next;
    mergeLiveEvents(next.events);
    pendingLiveEvents.value = false;
  } catch {
    /* 实时同步失败时不打断正在阅读的列表 */
  }
}

function showLatestEvents(): void {
  pendingLiveEvents.value = false;
  window.scrollTo(0, 0);
  void load(true);
}

watch(
  () => stream.lastEventAt,
  (value) => {
    if (!value) return;
    if (refreshTimer !== null) window.clearTimeout(refreshTimer);
    refreshTimer = window.setTimeout(() => {
      void syncLiveEvents();
    }, 2500);
  }
);

onMounted(() => {
  document.addEventListener("keydown", onImageKeydown);
  void load(true);
});
onBeforeUnmount(() => {
  if (refreshTimer !== null) window.clearTimeout(refreshTimer);
  document.removeEventListener("keydown", onImageKeydown);
});
</script>

<style scoped>
/* 筛选压成一条工具栏：原来三行各带一个文字标签，光筛选就吃掉 130px，
   首屏留给事件本身的高度还不到四分之一。控件形态本身已经说明了它筛的是什么，
   标签只留图标，语义交给 aria-label。 */
.event-filter-band {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.event-filter-lead {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 10px;
  min-width: 0;
}

/* 搜索图标画进输入框里。之前每个控件前面都挂一个裸图标，它们不属于任何一个
   控件的边框，浮在背景上显得突兀；时间和会话两处的图标直接去掉——一排时间
   按钮和一个写着「全部会话」的下拉，本来就不需要再标注。 */
.event-search {
  position: relative;
  flex: 1 1 160px;
  min-width: 0;
  max-width: 280px;
}

.event-search-icon {
  position: absolute;
  top: 50%;
  left: 9px;
  color: var(--muted);
  transform: translateY(-50%);
  pointer-events: none;
}

.event-search-input {
  width: 100%;
  min-width: 0;
  height: 30px;
  padding: 0 10px 0 28px;
  font-size: 12px;
}

.event-filter-refresh {
  margin-left: auto;
}

/* 这几个数字是参考值，卡片给它们的分量太重了：一张卡就要一百多像素高，
   而首屏本来该留给下面的事件列表。压成一行文字，需要时扫一眼就够。 */
.event-stats-line {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  margin: 0;
  gap: 4px 18px;
  font-size: 12px;
  line-height: 1.6;
}

.event-stats-line > span {
  display: inline-flex;
  align-items: baseline;
  gap: 5px;
  min-width: 0;
}

.event-stats-line svg {
  align-self: center;
  flex: none;
  color: var(--muted);
}

.event-stats-line strong {
  font-size: 14px;
  font-variant-numeric: tabular-nums;
}

.event-group-filter {
  min-width: 180px;
  /* 不封顶的话它会一路撑开，三个控件正好占满一行，刷新只能掉到下一行。 */
  max-width: 240px;
  max-width: 320px;
}

/* 四层是同一份窗口切出来的有序片段，不是互不相干的分类，所以用主题色的一条
   明度梯度，而不是四种色相：既表达了「同一个整体」，也不会跟四套可选主题色
   里的任何一种撞车。留白用中性色，它不属于任何一层。 */
.budget-bar {
  display: flex;
  width: 100%;
  height: 22px;
  border-radius: 999px;
  overflow: hidden;
  background: var(--surface-2);
}

.budget-slice {
  display: block;
  height: 100%;
  /* 大窗口下常驻记忆只占 0.5%，不给最小宽度就是一条看不见的缝。 */
  min-width: 3px;
}

.budget-slice-recent_history {
  background: var(--accent);
}

.budget-slice-session_thread {
  background: color-mix(in srgb, var(--accent) 62%, transparent);
}

.budget-slice-retrieved_memory {
  background: color-mix(in srgb, var(--accent) 38%, transparent);
}

.budget-slice-core_memory {
  background: color-mix(in srgb, var(--accent) 20%, transparent);
}

.budget-slice-headroom {
  background: transparent;
  min-width: 0;
}

.budget-legend {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 10px 18px;
  margin: 14px 0 0;
  padding: 0;
  list-style: none;
}

.budget-legend li {
  display: grid;
  grid-template-columns: 10px auto 1fr;
  align-items: baseline;
  gap: 4px 8px;
}

.budget-dot {
  width: 10px;
  height: 10px;
  border-radius: 3px;
  align-self: center;
}

.budget-dot.budget-slice-headroom {
  background: var(--surface-2);
}

.budget-legend-label {
  color: var(--text-secondary);
  font-size: 13px;
}

.budget-legend-value {
  justify-self: end;
  font-size: 13px;
}

.budget-legend-foot {
  grid-column: 2 / -1;
  color: var(--muted);
  font-size: 12px;
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
  overflow: visible;
}

.event-detail-card > .card-header {
  position: sticky;
  top: var(--topbar-height);
  z-index: 8;
  margin: 0;
  padding: 16px 20px 12px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
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

/* 发送者是排错时最先要认的东西，整块连在一起，别被 flex 的 gap 拆散。 */
.event-sender {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 320px;
}

.event-avatar {
  width: 20px;
  height: 20px;
  flex: 0 0 auto;
  border-radius: 50%;
  object-fit: cover;
  background: var(--bg-raised);
}

.event-avatar-fallback {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border);
  color: var(--muted);
  font-size: 10.5px;
  line-height: 1;
}

.event-sender-name {
  color: var(--text);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.event-sender-id {
  flex: 0 0 auto;
  color: var(--muted);
  font-size: 11.5px;
}

.event-meta-sep {
  color: var(--border-strong);
}

.event-meta-group {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
}

/* 一屏里基本都是同一天，日期每行重复只是噪声，换天时给一条就够。 */
.event-date-separator {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: 10px 0 6px;
  background: var(--surface);
  color: var(--muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
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

.event-recall-summary {
  margin-top: 12px;
  padding: 10px 12px;
  border-left: 3px solid var(--warn);
  background: var(--surface-2);
}

.event-recall-summary strong,
.event-recall-summary span {
  display: block;
  font-size: 12px;
}

.event-recall-summary p {
  margin: 4px 0;
  color: var(--text);
  line-height: 1.55;
}

.event-image-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 12px;
}

.event-memories {
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: color-mix(in srgb, var(--surface-2) 72%, transparent);
}

.event-memories > strong {
  display: block;
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--muted);
}

/* 收起时整块只占一行；展开后才让出空间给条目。 */
.event-memory-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 0;
  border: 0;
  background: none;
  color: var(--muted);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.event-memory-toggle:hover {
  color: var(--text);
}

.event-memory-peek {
  overflow: hidden;
  flex: 1;
  min-width: 0;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.event-memory-list {
  display: grid;
  margin-top: 8px;
  gap: 6px;
}

.event-memory-item {
  padding: 7px 9px;
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  background: var(--surface-1);
}

.event-memory-head,
.event-memory-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 10px;
}

.event-memory-head strong {
  font-size: 13px;
}

.event-memory-item p {
  margin: 5px 0 4px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.event-memory-item pre {
  margin: 7px 0 6px;
  padding: 8px;
  overflow: auto;
  border-radius: 4px;
  background: var(--surface-2);
  font: inherit;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.event-memories.temporary .event-memory-item {
  border-left-color: var(--warn);
}

.event-memory-meta {
  font-size: 11px;
}

.event-image-preview {
  display: grid;
  width: 96px;
  flex: 0 0 96px;
  aspect-ratio: 1;
  padding: 0;
  place-items: center;
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--surface-2);
  color: var(--muted);
  cursor: zoom-in;
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

.event-image-lightbox {
  position: fixed;
  inset: 0;
  z-index: 220;
  display: grid;
  place-items: center;
  padding: 20px;
  background: rgba(10, 8, 11, 0.82);
  backdrop-filter: blur(4px);
}

.event-image-lightbox-dialog {
  display: flex;
  width: min(1120px, 100%);
  max-height: calc(100dvh - 40px);
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--surface);
  box-shadow: var(--shadow-lg);
}

.event-image-lightbox-header {
  display: flex;
  min-height: 48px;
  align-items: center;
  gap: 12px;
  padding: 8px 10px 8px 16px;
  border-bottom: 1px solid var(--border);
}

.event-image-lightbox-header > span {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  color: var(--muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.event-image-lightbox-body {
  display: grid;
  min-height: 0;
  place-items: center;
  overflow: auto;
  padding: 16px;
  background: #0b090c;
}

.event-image-lightbox-body img {
  display: block;
  max-width: 100%;
  max-height: calc(100dvh - 120px);
  object-fit: contain;
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

.event-subtasks {
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
}

.event-subtasks > strong {
  display: block;
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--muted);
}

.event-subtasks ul {
  margin: 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 6px;
}

.event-subtasks li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}

.event-subtasks li p {
  flex-basis: 100%;
  margin: 0;
  font-size: 12px;
}

.subtask-name {
  font-weight: 500;
}

.event-delivery.quiet {
  margin-top: 8px;
  padding: 0;
  border: 0;
  grid-template-columns: 16px minmax(0, 1fr);
  gap: 8px;
  align-items: center;
}

.event-delivery.quiet strong {
  color: var(--muted);
  font-size: 12px;
  font-weight: 500;
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
    gap: 8px;
  }

  .event-image-preview {
    width: 84px;
    flex-basis: 84px;
  }

  .event-image-lightbox {
    padding: 10px;
  }

  .event-image-lightbox-dialog {
    max-height: calc(100dvh - 20px);
  }

  .event-image-lightbox-body {
    padding: 8px;
  }

  .debug-step-summary,
  .debug-payload {
    margin-left: 0;
  }
}
</style>
