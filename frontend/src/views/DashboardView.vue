<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <p>机器人运行状态、消息量和实时事件</p>
      </div>
      <!-- 常用四档平铺，长跨度与自定义收进右侧下拉，保持原版表头节奏。 -->
      <div class="view-actions dashboard-range-actions">
        <div class="segmented" role="radiogroup" aria-label="常用统计范围">
          <button
            v-for="option in quickRangeOptions"
            :key="option.value"
            type="button"
            role="radio"
            :class="{ active: selectedRange === option.value }"
            :aria-checked="selectedRange === option.value"
            :disabled="overviewLoading"
            @click="selectRange(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
        <AppSelect
          id="dashboard-extra-range"
          class="dashboard-extra-select"
          :class="{ 'is-selected': !!extraRangeValue }"
          aria-label="更多统计范围"
          placeholder="更多范围"
          :model-value="extraRangeValue"
          :options="extraRangeOptions"
          :disabled="overviewLoading"
          @update:model-value="onSelectExtraRange"
        />
        <DateRangePicker
          v-if="selectedRange === 'custom'"
          v-model:from="customFrom"
          v-model:to="customTo"
          :open-signal="datePickerSignal"
          @change="onCustomDateChange"
        />
      </div>
    </header>

    <div class="stack">
      <section v-if="setupNeeded" class="setup-callout">
        <span class="setup-callout-icon">
          <Sparkles :size="22" aria-hidden="true" />
        </span>
        <div class="setup-callout-main">
          <strong>完成首次配置</strong>
          <p>配置模型渠道和机器人接入后，即可开始处理消息与查看统计趋势。</p>
        </div>
        <button class="btn primary" type="button" @click="navigate('setup')">
          开始配置
          <ArrowRight :size="15" aria-hidden="true" />
        </button>
      </section>

      <!-- 核心统计卡片（6 列网格） -->
      <div class="stat-grid dashboard-stats">
        <StatCard
          :label="`${cardRangeLabel}消息`"
          :loading="cardsLoading"
          :value="messagesValue"
          :foot="inboundFoot"
          clickable
          @activate="openDetail = 'messages'"
        >
          <template #icon><MessageCircle :size="14" aria-hidden="true" /></template>
        </StatCard>

        <StatCard
          :label="`${cardRangeLabel}已回复`"
          :loading="cardsLoading"
          :value="handledValue"
          :foot="handledFoot"
          clickable
          @activate="openDetail = 'handled'"
        >
          <template #icon><CheckCircle2 :size="14" aria-hidden="true" /></template>
        </StatCard>

        <StatCard
          :label="`${cardRangeLabel}错误`"
          :loading="cardsLoading"
          :value="errorsValue"
          :class="{ 'stat-card-error': totalAbnormalCount > 0 }"
          :foot="errorsFoot"
          clickable
          @activate="openDetail = 'errors'"
        >
          <template #icon><TriangleAlert :size="14" aria-hidden="true" /></template>
        </StatCard>

        <StatCard
          label="平均响应"
          :loading="cardsLoading"
          :value="avgReplyValue"
          :foot="avgReplyFoot"
          clickable
          @activate="openDetail = 'latency'"
        >
          <template #icon><Zap :size="14" aria-hidden="true" /></template>
        </StatCard>

        <StatCard
          label="模型并发"
          :loading="initialLoading"
          :value="formatNumber(status?.llm_concurrency?.active ?? 0)"
          :foot="llmConcurrencyFoot"
          clickable
          @activate="openDetail = 'concurrency'"
        >
          <template #icon><Gauge :size="14" aria-hidden="true" /></template>
        </StatCard>

        <StatCard
          :label="`${cardRangeLabel} Token`"
          :loading="cardsLoading"
          :value="formatCompactNumber(selectedUsage?.total_tokens ?? 0)"
          :foot="llmUsageFoot"
          clickable
          @activate="openDetail = 'usage'"
        >
          <template #icon><Coins :size="14" aria-hidden="true" /></template>
        </StatCard>
      </div>

      <!-- 第二排：与第一行统计卡共用 6 列网格，趋势占 4 列、运行信息占 2 列，竖边严格对齐 -->
      <div class="dashboard-insights">
        <section class="card dashboard-chart">
          <div class="card-header">
            <h2>{{ chartHeading }}</h2>
            <div class="dashboard-chart-controls">
              <div class="segmented" role="radiogroup" aria-label="统计指标">
                <button
                  type="button"
                  role="radio"
                  :class="{ active: chartMetric === 'messages' }"
                  :aria-checked="chartMetric === 'messages'"
                  @click="setChartMetric('messages')"
                >
                  消息
                </button>
                <button
                  type="button"
                  role="radio"
                  :class="{ active: chartMetric === 'tokens' }"
                  :aria-checked="chartMetric === 'tokens'"
                  @click="setChartMetric('tokens')"
                >
                  Token
                </button>
              </div>
              <div class="segmented dashboard-chart-switch" role="radiogroup" aria-label="消息量图表">
                <button
                  type="button"
                  role="radio"
                  :class="{ active: chartMode === 'trend' }"
                  :aria-checked="chartMode === 'trend'"
                  @click="setChartMode('trend')"
                >
                  24 小时
                </button>
                <button
                  type="button"
                  role="radio"
                  :class="{ active: chartMode === 'calendar' }"
                  :aria-checked="chartMode === 'calendar'"
                  @click="setChartMode('calendar')"
                >
                  按天
                </button>
              </div>
            </div>
          </div>
          <div class="card-body">
            <ActivityCalendar
              v-if="chartMode === 'calendar'"
              :profile-id="botScope"
              :metric="chartMetric"
              @caption="calendarCaption = $event"
            />
            <LoadingSkeleton v-else-if="cardsLoading && !overview?.trend?.length" kind="chart" label="正在加载趋势统计" />
            <DashboardTrendChart
              v-else-if="overview?.trend?.length"
              :buckets="overview.trend"
              :granularity="overview.granularity"
              :metric="chartMetric"
              @select-bucket="onSelectTrendBucket"
            />
            <HourlyBars v-else-if="stats && chartMetric === 'messages'" :buckets="hourlyBuckets" />
            <EmptyState v-else title="暂无统计数据" hint="机器人处理消息或调用模型后这里会出现趋势走势" />
          </div>
        </section>

        <section class="card dashboard-runtime">
          <div class="card-header">
            <h2>运行信息</h2>
          </div>
          <div class="card-body">
            <div class="dashboard-runtime-grid">
              <div v-for="item in runtimeItems" :key="item.label" class="dashboard-runtime-item">
                <span class="stat-label"><component :is="item.icon" :size="13" aria-hidden="true" />{{ item.label }}</span>
                <SkeletonBlock v-if="initialLoading" width="64px" height="20px" />
                <span v-else class="dashboard-runtime-value" :title="item.value">{{ item.value }}</span>
              </div>
            </div>
            <button
              v-if="status?.last_error"
              type="button"
              class="dashboard-runtime-error-btn text-err"
              :title="`点击进错误日志查看：${status.last_error}`"
              @click="openLogsWithRange({ kind: 'error', q: status.last_error.slice(0, 36) })"
            >
              <TriangleAlert :size="13" aria-hidden="true" />
              <span>{{ status.last_error }}</span>
            </button>
          </div>
        </section>
      </div>

      <!-- 实时事件流 -->
      <section class="card">
        <div class="card-header">
          <div class="cluster">
            <h2>实时事件</h2>
            <span class="badge" :class="stream.connected ? 'ok' : 'warn'">
              <span class="status-dot" :class="{ pulse: stream.connected }" aria-hidden="true" />
              {{ stream.connected ? "实时推送中" : "等待重连" }}
            </span>
          </div>
          <button class="btn ghost small" type="button" @click="openEventsWithRange()">
            查看明细
            <ArrowRight :size="14" aria-hidden="true" />
          </button>
        </div>

        <div class="card-body">
          <div v-if="feed.length > 0" class="event-feed">
            <article
              v-for="(event, index) in feed"
              :key="`${event.at}-${index}`"
              class="event-item dashboard-feed-item"
              :class="{ 'is-error': !!(event.error || event.decision === 'error') }"
              role="button"
              tabindex="0"
              title="点击跳转到事件明细查看完整处理过程与调试记录"
              @click="inspectLiveEvent(event)"
              @keydown.enter.prevent="inspectLiveEvent(event)"
            >
              <span class="event-time">{{ formatClock(event.at) }}</span>
              <div class="event-main">
                <div class="event-meta">
                  <span v-if="event.platform" class="badge">{{ platformLabel(event.platform) }}</span>
                  <span class="badge" :class="eventBadgeClass(event)">{{ eventKindLabel(event.kind) }}</span>
                  <span v-if="event.group_id" class="muted">{{ displayGroupIdentity(event.group_id, event.group_name) }}</span>
                  <span v-if="displayChatIdentity(event.sender_name, event.user_id)" class="muted">{{ displayChatIdentity(event.sender_name, event.user_id) }}</span>
                  <span v-if="event.duration_ms" class="muted">{{ (event.duration_ms / 1000).toFixed(1) }}s</span>
                  <span v-if="event.decision" class="badge" :class="eventDecisionClass(event)">{{ eventDecisionLabel(event) }}</span>
                  <button
                    v-if="event.error || event.decision === 'error'"
                    type="button"
                    class="feed-error-jump text-err"
                    @click.stop="inspectLiveEvent(event, true)"
                  >
                    查看调用链 ↗
                  </button>
                </div>
                <p v-if="displayMessageText(event.text)" class="event-text">{{ truncate(displayMessageText(event.text), 140) }}</p>
                <p v-if="displayMessageText(event.reply)" class="event-reply">{{ truncate(displayMessageText(event.reply), 200) }}</p>
                <p v-if="event.reason" class="event-reason">{{ event.handled ? "回复原因" : "未回复原因" }}：{{ event.reason }}</p>
                <p v-if="event.error" class="event-error">{{ event.error }}</p>
              </div>
            </article>
          </div>
          <LoadingSkeleton v-else-if="initialLoading" kind="feed" label="正在加载实时事件" />
          <EmptyState v-else title="还没有事件" hint="机器人收到消息后会实时显示在这里">
            <template #icon><Activity :size="20" aria-hidden="true" /></template>
          </EmptyState>
        </div>
      </section>
    </div>

    <!-- 所有统计卡片共用的明细弹窗 -->
    <Modal v-if="activeDetail"
      :title="activeDetail.title"
      :wide="openDetail === 'latency' || openDetail === 'usage' || openDetail === 'messages' || openDetail === 'errors'"
      @close="openDetail = null"
    >
      <div class="stack" style="gap: 14px">
        <div class="usage-detail-total">
          <span class="muted">{{ activeDetail.totalLabel }}</span>
          <strong>{{ activeDetail.total }}</strong>
        </div>
        <div v-if="activeDetail.rows.length" class="stack" style="gap: 10px; font-size: 13px">
          <div v-for="row in activeDetail.rows" :key="row.label" class="info-row">
            <span class="muted info-label">{{ row.label }}</span>
            <button
              v-if="row.onClick"
              type="button"
              class="detail-row-link"
              :class="row.tone"
              :title="row.hint"
              @click="row.onClick()"
            >
              {{ row.value }} ↗
            </button>
            <span v-else class="info-value" :class="row.tone" :title="row.hint">{{ row.value }}</span>
          </div>
        </div>
        <p class="hint" style="margin: 0">{{ activeDetail.note }}</p>

        <!-- 错误明细弹窗专属：快捷直达按钮、冷却恢复、分类聚合与最近异常逐条跳转 -->
        <div v-if="openDetail === 'errors'" class="stack error-modal-detail" style="gap: 12px">
          <div class="cluster" style="gap: 8px">
            <button class="btn small" type="button" @click="closeAndRun(() => openEventsWithRange({ result: 'error' }))">
              查看异常事件（{{ formatNumber(rangeEventErrors) }}）
            </button>
            <button class="btn small" type="button" @click="closeAndRun(() => openLogsWithRange({ kind: 'llm', status: 'failed' }))">
              失败的模型调用（{{ formatNumber(selectedUsage?.failed_calls ?? 0) }}）
            </button>
            <button class="btn small" type="button" @click="closeAndRun(() => openLogsWithRange({ kind: 'error' }))">
              运行错误日志（{{ formatNumber(rangeAppErrors) }}）
            </button>
          </div>

          <div v-if="coolingCandidates.length > 0" class="stack" style="gap: 6px">
            <h3 class="detail-subhead">当前冷却中的模型通道</h3>
            <div v-for="cand in coolingCandidates" :key="`${cand.profile_id}:${cand.model}`" class="error-cooldown-row">
              <span class="mono" style="font-weight: 600">{{ cand.model }}</span>
              <span class="muted">连续失败 {{ cand.failure_count }} 次 · 冷却至 {{ formatClock(cand.cooldown_until) }}</span>
              <div class="cluster" style="gap: 6px; margin-left: auto">
                <button
                  type="button"
                  class="btn ghost small"
                  @click="closeAndRun(() => openLogsWithRange({ kind: 'llm', status: 'failed', model: cand.model }))"
                >
                  失败日志
                </button>
                <button
                  type="button"
                  class="btn small"
                  :disabled="resettingCandidateKey === `${cand.profile_id}:${cand.model}`"
                  @click="onResetModelCandidate(cand)"
                >
                  解除冷却
                </button>
              </div>
            </div>
          </div>

          <div v-if="overview?.errors?.categories?.length" class="stack" style="gap: 6px">
            <h3 class="detail-subhead">按错误来源与类型归并</h3>
            <div class="error-category-list">
              <button
                v-for="cat in overview.errors.categories"
                :key="cat.key"
                type="button"
                class="error-category-row"
                @click="closeAndRun(() => inspectErrorCategory(cat))"
              >
                <div class="error-category-top">
                  <strong>{{ cat.label }}</strong>
                  <span class="badge err">{{ formatNumber(cat.count) }} 次</span>
                  <span v-if="cat.recovered_count" class="badge warn">转后备恢复 {{ formatNumber(cat.recovered_count) }}</span>
                  <span class="muted" style="margin-left: auto">{{ formatRelative(cat.latest_at) }} ↗</span>
                </div>
                <p v-if="cat.sample" class="muted error-category-sample">{{ truncate(cat.sample, 120) }}</p>
              </button>
            </div>
          </div>

          <div v-if="overview?.errors?.recent?.length" class="stack" style="gap: 6px">
            <h3 class="detail-subhead">最近异常明细（点击直达对应日志行或事件调用链）</h3>
            <div class="error-category-list">
              <button
                v-for="item in overview.errors.recent"
                :key="`${item.source}:${item.id}`"
                type="button"
                class="error-category-row"
                @click="closeAndRun(() => inspectErrorItem(item))"
              >
                <div class="error-category-top">
                  <span class="badge" :class="item.recovered ? 'warn' : 'err'">
                    {{ item.source === 'event' ? '消息异常' : item.source === 'llm' ? '模型失败' : '错误日志' }}
                  </span>
                  <span v-if="item.model" class="mono muted">{{ item.model }}</span>
                  <span v-else-if="item.action" class="mono muted">{{ item.action }}</span>
                  <span v-if="item.group_id" class="muted">{{ displayGroupIdentity(item.group_id, item.group_name) }}</span>
                  <span class="muted" style="margin-left: auto">{{ formatTime(item.at) }} ↗</span>
                </div>
                <p class="muted error-category-sample">{{ truncate(item.detail || item.message, 140) }}</p>
              </button>
            </div>
          </div>
        </div>

        <LatencyDetail
          v-if="openDetail === 'latency'"
          :profile-id="botScope"
          :initial-window="initialLatencyWindow"
          :group-label="displayGroupIdentity"
          @inspect-event="onInspectLatencySample"
        />
        <ActivityHeatmap v-if="openDetail === 'messages'" :profile-id="botScope" :groups="groupOptions" />
        <TokenUsageBreakdown v-if="openDetail === 'usage'" :entries="selectedUsage?.breakdown" :range-label="rangeLabel" @inspect-failures="onInspectUsageFailures">
          <template #groups>
            <GroupTokenUsage :profile="botScope" :groups="tokenGroups" embedded />
          </template>
        </TokenUsageBreakdown>
      </div>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { useConfigurationRefresh } from "../lib/configuration-sync";
import { computed, onMounted, ref, watch } from "vue";
import {
  Activity,
  ArrowRight,
  CheckCircle2,
  Clock,
  Coins,
  Cpu,
  Gauge,
  HardDrive,
  MemoryStick,
  MessageCircle,
  Puzzle,
  Sparkles,
  TriangleAlert,
  Zap
} from "@lucide/vue";
import {
  getConfig,
  getBotStatus,
  getModelHealth,
  getStats,
  getStatsOverview,
  listBotGroups,
  resetModelHealth,
  type BotGroupSummary,
  type LatencyWindowID,
  type LLMUsageBreakdown,
  type ModelCandidateHealth,
  type ModelHealthSnapshot,
  type ReplyLatencySample,
  type StatsErrorCategory,
  type StatsErrorItem,
  type StatsHourBucket,
  type StatsOverviewRange,
  type StatsOverviewResponse,
  type StatsTrendBucket
} from "../api";
import { pushStatsSnapshot, pushStatusSnapshot, scopedStats, stream, type BotEvent } from "../lib/stream";
import { navigate } from "../router";
import { botScope, matchesBotScope } from "../lib/bot-scope";
import {
  formatBytes,
  formatClock,
  formatCompactNumber,
  formatDurationMS,
  formatNumber,
  formatRelative,
  formatTime,
  formatUptime,
  truncate
} from "../lib/format";
import { displayMessageText, displayChatIdentity } from "../lib/message-display";
import { toastError, toastSuccess } from "../lib/toast";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import DateRangePicker from "../components/DateRangePicker.vue";
import StatCard from "../components/StatCard.vue";
import HourlyBars from "../components/HourlyBars.vue";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";
import ActivityCalendar from "../components/ActivityCalendar.vue";
import ActivityHeatmap from "../components/ActivityHeatmap.vue";
import LatencyDetail from "../components/LatencyDetail.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import TokenUsageBreakdown from "../components/TokenUsageBreakdown.vue";
import GroupTokenUsage from "../components/GroupTokenUsage.vue";
import DashboardTrendChart from "../components/DashboardTrendChart.vue";
import { cacheUsageDisplay, cacheUtilization, splitCacheUsage, type UsageGroup } from "../lib/llm-usage";

const pending = ref(true);
const setupNeeded = ref(false);
const groupNames = ref<Record<string, string>>({});
const tokenGroups = ref<BotGroupSummary[]>([]);
const modelHealth = ref<ModelHealthSnapshot | null>(null);
const resettingCandidateKey = ref("");
let groupNamesGeneration = 0;
const groupOptions = ref<{ id: string; name: string }[]>([]);

async function loadGroupNames(): Promise<void> {
  const generation = ++groupNamesGeneration;
  tokenGroups.value = [];
  try {
    const response = await listBotGroups(false, botScope.value);
    if (generation !== groupNamesGeneration) return;
    tokenGroups.value = response.groups;
    groupNames.value = Object.fromEntries(response.groups.filter((group) => group.group_name).map((group) => [group.group_id, group.group_name ?? ""]));
    groupOptions.value = response.groups.map((group) => ({
      id: group.group_id,
      name: group.group_name ? `${group.group_name}（${group.group_id}）` : `群 ${group.group_id}`
    }));
  } catch {
    if (generation !== groupNamesGeneration) return;
    groupNames.value = {};
    groupOptions.value = [];
  }
}

async function loadHealth(): Promise<void> {
  try {
    modelHealth.value = await getModelHealth();
  } catch {
    // 候选健康接口可选，读取失败不影响主统计。
  }
}

function displayGroupIdentity(groupID?: string, eventName?: string): string {
  const id = (groupID ?? "").trim();
  const name = (eventName ?? groupNames.value[id] ?? "").trim();
  return name ? `${name}（${id}）` : `群 ${id}`;
}

const status = computed(() => stream.status);
const stats = computed(() => scopedStats(botScope.value));
const initialLoading = computed(() => pending.value && !stats.value);

// 常用四档按钮平铺，其余跨度和自定义日期收进下拉框，避免顶栏按钮成串拥挤。
const quickRangeOptions: { value: StatsOverviewRange; label: string }[] = [
  { value: "today", label: "今日" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" }
];

const extraRangeOptions: AppSelectOption[] = [
  { value: "1h", label: "最近 1 小时" },
  { value: "12h", label: "最近 12 小时" },
  { value: "90d", label: "最近 90 天" },
  { value: "365d", label: "近一年" },
  { value: "custom", label: "自定义日期" }
];

const extraRangeSet = new Set<StatsOverviewRange>(["1h", "12h", "90d", "365d", "custom"]);
const selectedRange = ref<StatsOverviewRange>("today");
const extraRangeValue = computed(() => (extraRangeSet.has(selectedRange.value) ? selectedRange.value : ""));
const customFrom = ref("");
const customTo = ref("");
const datePickerSignal = ref(0);
const overview = ref<StatsOverviewResponse | null>(null);
const overviewLoading = ref(false);
const overviewError = ref("");
let overviewSeq = 0;

function formatDateInput(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

function localDayStart(value: string): Date | null {
  const [year, month, day] = value.split("-").map(Number);
  return year && month && day ? new Date(year, month - 1, day) : null;
}

function customOverviewBounds(): { since?: string; until?: string } {
  if (selectedRange.value !== "custom") return {};
  const from = localDayStart(customFrom.value);
  const to = localDayStart(customTo.value);
  if (to) to.setDate(to.getDate() + 1);
  return { since: from?.toISOString(), until: to?.toISOString() };
}

const rangeLabel = computed(() => {
  if (selectedRange.value === "custom" && (customFrom.value || customTo.value)) {
    return `${customFrom.value || "起点"} ~ ${customTo.value || "今天"}`;
  }
  const labels: Record<StatsOverviewRange, string> = {
    today: "今日",
    "1h": "最近 1 小时",
    "12h": "最近 12 小时",
    "24h": "最近 24 小时",
    "7d": "最近 7 天",
    "30d": "最近 30 天",
    "90d": "最近 90 天",
    "365d": "近一年",
    custom: "自定义范围"
  };
  return labels[selectedRange.value] ?? "今日";
});

// 统计卡左上角标题短促对齐：选了自定义日期时写「所选范围」，具体起止日期已在顶栏日期按钮和弹窗里写清。
const cardRangeLabel = computed(() => (selectedRange.value === "custom" ? "所选范围" : rangeLabel.value));

const effectiveGranularity = computed<"hour" | "day">(() => overview.value?.granularity ?? (selectedRange.value === "7d" ? "day" : "hour"));

async function loadOverview(): Promise<void> {
  if (selectedRange.value === "custom" && !customFrom.value && !customTo.value) return;
  const seq = ++overviewSeq;
  overviewLoading.value = true;
  overviewError.value = "";
  try {
    const bounds = customOverviewBounds();
    const res = await getStatsOverview({
      range: selectedRange.value,
      since: bounds.since,
      until: bounds.until,
      granularity: "auto",
      profileId: botScope.value
    });
    if (seq === overviewSeq) overview.value = res;
  } catch (error) {
    if (seq === overviewSeq) {
      overviewError.value = error instanceof Error ? error.message : "读取统计概览失败";
      if (selectedRange.value !== "today") toastError(overviewError.value);
    }
  } finally {
    if (seq === overviewSeq) overviewLoading.value = false;
  }
}

function selectRange(value: StatsOverviewRange): void {
  selectedRange.value = value;
  if (value === "custom") {
    if (!customFrom.value && !customTo.value) {
      const now = new Date();
      const weekAgo = new Date(now.getTime() - 6 * 86_400_000);
      customFrom.value = formatDateInput(weekAgo);
      customTo.value = formatDateInput(now);
    }
    datePickerSignal.value++;
  }
  void loadOverview();
}

function onSelectExtraRange(value: string): void {
  if (!value) return;
  selectRange(value as StatsOverviewRange);
}

function onCustomDateChange(): void {
  if (customFrom.value || customTo.value) void loadOverview();
}

const rangePending = computed(() => selectedRange.value !== "today" && overviewLoading.value && !overview.value);
const cardsLoading = computed(() => initialLoading.value || rangePending.value);

// 指标取值：优先取 overview（支持全跨度与按机器人过滤），今日尚未返回时可由 SSE 实时统计平滑兜底。
const rangeMessages = computed(() => {
  if (overview.value) return overview.value.current.messages;
  return selectedRange.value === "today" ? (stats.value?.today_events ?? 0) : 0;
});
const rangeHandled = computed(() => {
  if (overview.value) return overview.value.current.handled;
  return selectedRange.value === "today" ? (stats.value?.today_handled ?? 0) : 0;
});
const rangeEventErrors = computed(() => {
  if (overview.value) return overview.value.current.errors;
  return selectedRange.value === "today" ? (stats.value?.today_errors ?? 0) : 0;
});
const rangeAppErrors = computed(() => overview.value?.current.app_error_logs ?? 0);

interface UsageDetail {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
  total_tokens: number;
  missing_usage_calls?: number;
  failed_calls?: number;
  recovered_calls?: number;
  duration_ms?: number;
  output_tokens_per_second?: number;
  avg_ttft_ms?: number;
  ttft_calls?: number;
  breakdown?: readonly LLMUsageBreakdown[];
}

const selectedUsage = computed<UsageDetail | null>(() => {
  if (overview.value) {
    const u = overview.value.current.usage;
    return {
      ...u,
      calls: u.recorded_calls,
      missing_usage_calls: u.usage_missing_calls
    };
  }
  if (selectedRange.value === "today") {
    const today = status.value?.llm_usage?.today;
    return today ? { ...today } : null;
  }
  return null;
});

const totalAbnormalCount = computed(
  () => rangeEventErrors.value + (selectedUsage.value?.failed_calls ?? 0) + rangeAppErrors.value
);

const messagesValue = computed(() => formatNumber(rangeMessages.value));
const handledValue = computed(() => formatNumber(rangeHandled.value));
const errorsValue = computed(() => formatNumber(totalAbnormalCount.value));

function deltaText(current: number, previous: number | undefined): string {
  if (previous === undefined || previous <= 0) return "";
  const pct = Math.round(((current - previous) / previous) * 100);
  if (Math.abs(pct) < 2) return "环比持平";
  return pct > 0 ? `环比 +${pct}%` : `环比 ${pct}%`;
}

const inboundFoot = computed(() => {
  const pendingEvents = status.value?.pending_events ?? 0;
  if (pendingEvents > 0) return `队列积压 ${formatNumber(pendingEvents)}`;
  const delta = deltaText(rangeMessages.value, overview.value?.previous.messages);
  const base =
    overview.value && overview.value.current.active_users > 0
      ? `${formatNumber(overview.value.current.active_users)} 人 · ${formatNumber(overview.value.current.active_groups)} 群`
      : `累计 ${formatNumber(stats.value?.total_events ?? 0)}`;
  return delta ? `${base} · ${delta}` : base;
});

const handledFoot = computed(() => {
  const base =
    rangeMessages.value > 0
      ? `回复率 ${((rangeHandled.value / rangeMessages.value) * 100).toFixed(1)}%`
      : `累计 ${formatNumber(stats.value?.handled_events ?? 0)}`;
  const delta = deltaText(rangeHandled.value, overview.value?.previous.handled);
  return delta ? `${base} · ${delta}` : base;
});

const errorsFoot = computed(() => {
  if (totalAbnormalCount.value === 0) return "无异常记录";
  const parts: string[] = [];
  if (rangeEventErrors.value > 0) parts.push(`事件 ${formatNumber(rangeEventErrors.value)}`);
  if ((selectedUsage.value?.failed_calls ?? 0) > 0) parts.push(`模型 ${formatNumber(selectedUsage.value?.failed_calls ?? 0)}`);
  if (rangeAppErrors.value > 0) parts.push(`日志 ${formatNumber(rangeAppErrors.value)}`);
  return parts.join(" · ");
});

const avgReplyValue = computed(() => {
  if (overview.value) {
    return overview.value.current.replies_measured > 0 ? `${(overview.value.current.avg_reply_ms / 1000).toFixed(1)}s` : "—";
  }
  return stats.value && stats.value.avg_reply_ms > 0 ? `${(stats.value.avg_reply_ms / 1000).toFixed(1)}s` : "—";
});

const avgReplyFoot = computed(() => {
  const tps = selectedUsage.value?.output_tokens_per_second ?? 0;
  if (tps > 0) return `吞吐 ${tps.toFixed(1)} tok/s`;
  return `回复并发 ${status.value?.active_workers ?? 0} / 后台任务 ${status.value?.active_subagent_tasks ?? 0}`;
});

const coolingCandidates = computed(() =>
  (modelHealth.value?.candidates ?? []).filter((cand) => {
    if (!cand.cooldown_until) return false;
    const ts = Date.parse(cand.cooldown_until);
    return Number.isFinite(ts) && ts > Date.now();
  })
);

const llmConcurrencyFoot = computed(() => {
  const concurrency = status.value?.llm_concurrency;
  const peak = `实时 · 峰值 ${formatNumber(concurrency?.peak ?? 0)}`;
  if (coolingCandidates.value.length > 0) {
    return `${peak} · ${coolingCandidates.value.length} 冷却`;
  }
  const busiest = concurrency?.models?.[0];
  return busiest ? `${peak} · ${busiest.model} ×${busiest.active}` : peak;
});

const llmUsageFoot = computed(() => {
  const u = selectedUsage.value;
  const calls = formatNumber(u?.calls ?? 0);
  const split = u?.breakdown?.length ? splitCacheUsage(u.breakdown) : null;
  const cacheStr = split
    ? cacheUtilization(split.cached_input_tokens, split.input_tokens)
    : cacheUtilization(u?.cached_input_tokens ?? 0, u?.input_tokens ?? 0);
  return cacheStr !== "—" ? `${calls} 次 · 缓存 ${cacheStr}` : `调用 ${calls} 次`;
});

const initialLatencyWindow = computed<LatencyWindowID>(() => {
  switch (selectedRange.value) {
    case "1h":
      return "1h";
    case "7d":
      return "7d";
    case "30d":
      return "30d";
    case "90d":
    case "365d":
      return "90d";
    default:
      return "24h";
  }
});

// 深链跳转工具：把当前总览的时间跨度原样带进日志页或事件明细页。
function currentRangeQuery(): Record<string, string> {
  if (selectedRange.value === "custom") {
    const out: Record<string, string> = { range: "custom" };
    if (customFrom.value) out.from = customFrom.value;
    if (customTo.value) out.to = customTo.value;
    return out;
  }
  return { range: selectedRange.value };
}

function openLogsWithRange(extra: {
  kind?: "all" | "operation" | "error" | "llm";
  status?: "" | "ok" | "failed";
  model?: string;
  purpose?: string;
  action?: string;
  q?: string;
  id?: string;
  since?: string;
  until?: string;
}): void {
  const query: Record<string, string> = extra.since || extra.until ? { range: "custom" } : currentRangeQuery();
  if (extra.kind) query.kind = extra.kind;
  if (extra.status) query.status = extra.status;
  if (extra.model) query.model = extra.model;
  if (extra.purpose) query.purpose = extra.purpose;
  if (extra.action) query.action = extra.action;
  if (extra.q) query.q = extra.q;
  if (extra.id) query.id = extra.id;
  if (extra.since) query.since = extra.since;
  if (extra.until) query.until = extra.until;
  if (botScope.value && extra.kind === "llm") query.profile = botScope.value;
  navigate("logs", query);
}

function openEventsWithRange(extra: {
  result?: "all" | "replied" | "not_replied" | "pending" | "error" | "notice";
  group?: string;
  q?: string;
  event?: string;
  since?: string;
  until?: string;
} = {}): void {
  const query: Record<string, string> = extra.since || extra.until ? { range: "custom" } : currentRangeQuery();
  if (extra.result) query.result = extra.result;
  if (extra.group) query.group = extra.group;
  if (extra.q) query.q = extra.q;
  if (extra.event) query.event = extra.event;
  if (extra.since) query.since = extra.since;
  if (extra.until) query.until = extra.until;
  navigate("events", query);
}

function onInspectUsageFailures(row: UsageGroup): void {
  openDetail.value = null;
  openLogsWithRange({
    kind: "llm",
    status: "failed",
    model: row.raw_model && row.raw_model !== "unknown" ? row.raw_model : undefined,
    purpose: row.raw_purpose && row.raw_purpose !== "unlabeled" ? row.raw_purpose : undefined,
    q: !row.raw_model && !row.raw_purpose && row.detail ? row.detail : undefined
  });
}

function onInspectLatencySample(sample: ReplyLatencySample): void {
  openDetail.value = null;
  openEventsWithRange({
    q: sample.message_id || undefined,
    event: sample.event_id
  });
}

function inspectErrorItem(item: StatsErrorItem): void {
  if (item.source === "event") {
    openEventsWithRange({
      result: "error",
      group: item.group_id || undefined,
      q: item.message_id || undefined,
      event: item.id
    });
    return;
  }
  if (item.source === "llm") {
    openLogsWithRange({
      kind: "llm",
      status: "failed",
      model: item.model && item.model !== "unknown" ? item.model : undefined,
      id: item.id
    });
    return;
  }
  openLogsWithRange({
    kind: "error",
    action: item.action || undefined,
    id: item.id
  });
}

function inspectErrorCategory(cat: StatsErrorCategory): void {
  if (cat.source === "event") {
    openEventsWithRange({ result: "error" });
    return;
  }
  if (cat.source === "llm") {
    openLogsWithRange({
      kind: "llm",
      status: "failed",
      model: cat.model && cat.model !== "unknown" ? cat.model : undefined
    });
    return;
  }
  openLogsWithRange({
    kind: "error",
    action: cat.action || undefined
  });
}

function inspectLiveEvent(event: BotEvent, errorOnly = false): void {
  const isErr = Boolean(event.error || event.decision === "error");
  openEventsWithRange({
    result: errorOnly || isErr ? "error" : undefined,
    group: event.group_id || undefined,
    q: event.message_id || undefined
  });
}

function onSelectTrendBucket(bucket: StatsTrendBucket): void {
  const spanSeconds = effectiveGranularity.value === "day" ? 86_400 : 3600;
  const since = new Date(bucket.start_unix * 1000).toISOString();
  const until = new Date((bucket.start_unix + spanSeconds) * 1000).toISOString();
  if (chartMetric.value === "messages") {
    openEventsWithRange({
      result: bucket.errors > 0 ? "error" : undefined,
      since,
      until
    });
    return;
  }
  openLogsWithRange({
    kind: "llm",
    since,
    until
  });
}

async function onResetModelCandidate(cand: ModelCandidateHealth): Promise<void> {
  const key = `${cand.profile_id}:${cand.model}`;
  resettingCandidateKey.value = key;
  try {
    const res = await resetModelHealth(cand.profile_id ? [cand.profile_id] : [], cand.model);
    modelHealth.value = res.health;
    toastSuccess(`已恢复模型 ${cand.model} 的调度状态`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "恢复模型冷却失败");
  } finally {
    resettingCandidateKey.value = "";
  }
}

// 明细弹窗逻辑
type DetailID = "messages" | "handled" | "errors" | "latency" | "concurrency" | "usage";

interface DetailRow {
  label: string;
  value: string;
  hint?: string;
  tone?: string;
  onClick?: () => void;
}

interface DetailView {
  title: string;
  totalLabel: string;
  total: string;
  rows: DetailRow[];
  note: string;
}

const openDetail = ref<DetailID | null>(null);

function closeAndRun(fn: () => void): void {
  openDetail.value = null;
  fn();
}

function shareOfMessages(value: number): string | undefined {
  if (rangeMessages.value <= 0 || value <= 0) return undefined;
  return `占收到消息 ${((value / rangeMessages.value) * 100).toFixed(1)}%`;
}

const usageRangeNote = computed(() => {
  if (selectedRange.value === "today") {
    return "统计今日零点以来的模型调用与 Token 消耗（支持按当前左上角选中的机器人过滤）；跨重启保留。";
  }
  return "统计所选时间范围内已落库的全部模型调用，包括主对话回复、路由判断、后台子任务、记忆抽取与控制台测试。";
});

const detailViews = computed<Record<DetailID, DetailView>>(() => {
  const usage = selectedUsage.value;
  const concurrency = status.value?.llm_concurrency;
  const share = (value: number): DetailRow[] => {
    const text = shareOfMessages(value);
    return text ? [{ label: "占比", value: text }] : [];
  };
  return {
    messages: {
      title: `${rangeLabel.value}消息`,
      totalLabel: "合计",
      total: formatNumber(rangeMessages.value),
      rows: [
        { label: "已回复", value: formatNumber(rangeHandled.value), onClick: () => closeAndRun(() => openEventsWithRange({ result: "replied" })) },
        { label: "未回复", value: formatNumber(overview.value?.current.not_replied ?? 0), onClick: () => closeAndRun(() => openEventsWithRange({ result: "not_replied" })) },
        {
          label: "消息异常",
          value: formatNumber(rangeEventErrors.value),
          tone: rangeEventErrors.value > 0 ? "text-err" : undefined,
          onClick: () => closeAndRun(() => openEventsWithRange({ result: "error" }))
        },
        ...(overview.value
          ? [
              { label: "活跃会话 / 成员", value: `${formatNumber(overview.value.current.active_groups)} 个群 · ${formatNumber(overview.value.current.active_users)} 人` },
              { label: "前一等长周期", value: `${formatNumber(overview.value.previous.messages)} 条` }
            ]
          : []),
        { label: "队列积压", value: formatNumber(status.value?.pending_events ?? 0), hint: "此刻还没处理完的消息，实时值" },
        { label: "累计收到", value: formatNumber(stats.value?.total_events ?? 0), hint: "自有记录以来的总数" }
      ],
      note: "统计所选范围内进入处理队列的全部消息，按消息发生时间计；点击上方分项可直接进入事件明细。"
    },
    handled: {
      title: `${rangeLabel.value}已回复`,
      totalLabel: "合计",
      total: formatNumber(rangeHandled.value),
      rows: [
        ...share(rangeHandled.value),
        { label: "平均响应", value: avgReplyValue.value },
        ...(overview.value ? [{ label: "前一等长周期", value: `${formatNumber(overview.value.previous.handled)} 条` }] : []),
        { label: "查看已回复事件", value: "打开事件明细", onClick: () => closeAndRun(() => openEventsWithRange({ result: "replied" })) },
        { label: "累计已回复", value: formatNumber(stats.value?.handled_events ?? 0) }
      ],
      note: "回复发出去了就算，按处理完成的时间计。"
    },
    errors: {
      title: `${rangeLabel.value}异常与错误`,
      totalLabel: "异常总计",
      total: formatNumber(totalAbnormalCount.value),
      rows: [
        {
          label: "消息处理 / 投递异常",
          value: `${formatNumber(rangeEventErrors.value)} 条`,
          tone: rangeEventErrors.value > 0 ? "text-err" : undefined,
          onClick: () => closeAndRun(() => openEventsWithRange({ result: "error" }))
        },
        {
          label: "模型调用失败",
          value: `${formatNumber(usage?.failed_calls ?? 0)} 次${(usage?.recovered_calls ?? 0) > 0 ? `（其中转后备恢复 ${formatNumber(usage?.recovered_calls)} 次）` : ""}`,
          tone: (usage?.failed_calls ?? 0) > 0 ? "text-err" : undefined,
          hint: llmSuccessRateHint(usage),
          onClick: () => closeAndRun(() => openLogsWithRange({ kind: "llm", status: "failed" }))
        },
        {
          label: "运行错误日志",
          value: `${formatNumber(rangeAppErrors.value)} 条`,
          tone: rangeAppErrors.value > 0 ? "text-err" : undefined,
          onClick: () => closeAndRun(() => openLogsWithRange({ kind: "error" }))
        },
        ...share(rangeEventErrors.value),
        { label: "累计历史异常事件", value: formatNumber(stats.value?.error_events ?? 0) }
      ],
      note: "汇总消息处理/发送失败、模型请求失败和运行期错误日志三类来源；点击任意行或下方列表可直接定位到对应日志或调试调用链。"
    },
    latency: {
      title: "平均响应与吞吐",
      totalLabel: "平均响应",
      total: avgReplyValue.value,
      rows: [
        overview.value
          ? { label: "回复样本", value: `${formatNumber(overview.value.current.replies_measured)} 次回复` }
          : { label: "回复样本", value: "本次运行的全部回复", hint: "重启后从零开始计" },
        { label: "模型输出吞吐", value: (usage?.output_tokens_per_second ?? 0) > 0 ? `${usage!.output_tokens_per_second!.toFixed(1)} tok/s` : "—" },
        { label: "流式首 Token 均值", value: (usage?.ttft_calls ?? 0) > 0 ? formatDurationMS(usage?.avg_ttft_ms) : "—" },
        { label: "回复并发", value: formatNumber(status.value?.active_workers ?? 0), hint: "此刻正在生成回复的 worker 数，实时值" },
        { label: "后台任务", value: formatNumber(status.value?.active_subagent_tasks ?? 0), hint: "此刻在跑的后台子任务，实时值" }
      ],
      note: "整轮回复耗时含模型调用、工具执行与发送；下方最慢/最快样本可直接点击跳去事件调用链排查。"
    },
    concurrency: {
      title: "模型并发与通道健康",
      totalLabel: "当前在飞",
      total: formatNumber(concurrency?.active ?? 0),
      rows: [
        { label: "本次运行峰值", value: formatNumber(concurrency?.peak ?? 0), hint: "重启后从零开始计" },
        ...(concurrency?.models ?? []).map((entry) => ({
          label: `${entry.model}${entry.provider ? `（${entry.provider}）` : ""}`,
          value: `×${entry.active}`,
          hint: `最早一次开始于 ${formatRelative(entry.started_at)}`
        })),
        ...coolingCandidates.value.map((cand) => ({
          label: `冷却中 · ${cand.model}`,
          value: `失败 ${cand.failure_count} 次 · 至 ${formatClock(cand.cooldown_until)}`,
          tone: "text-warn",
          onClick: () => closeAndRun(() => openLogsWithRange({ kind: "llm", status: "failed", model: cand.model }))
        }))
      ],
      note: "模型并发是此刻发出去还没回来的实时请求数；若有候选进入降级冷却，可直接点击跳去该模型的失败日志。"
    },
    usage: {
      title: `${rangeLabel.value} Token`,
      totalLabel: "合计",
      total: formatNumber(usage?.total_tokens ?? 0),
      rows: [
        { label: "输入", value: formatNumber(usage?.input_tokens ?? 0) },
        ...cacheRows(usage),
        { label: "输出", value: formatNumber(usage?.output_tokens ?? 0) },
        {
          label: "成功调用",
          value: `${formatNumber(usage?.calls ?? 0)} 次`,
          onClick: () => closeAndRun(() => openLogsWithRange({ kind: "llm", status: "ok" }))
        },
        {
          label: "失败请求",
          value: `${formatNumber(usage?.failed_calls ?? 0)} 次`,
          tone: (usage?.failed_calls ?? 0) > 0 ? "text-err" : undefined,
          hint: llmSuccessRateHint(usage),
          onClick: () => closeAndRun(() => openLogsWithRange({ kind: "llm", status: "failed" }))
        },
        ...(overview.value
          ? [{ label: "前一等长周期", value: `${formatNumber(overview.value.previous.total_tokens)} Token（${formatNumber(overview.value.previous.recorded_calls)} 次调用）` }]
          : []),
        ...((usage?.missing_usage_calls ?? 0) > 0
          ? [
              {
                label: "上游没报用量",
                value: `${formatNumber(usage?.missing_usage_calls ?? 0)} 次`,
                hint: "这些调用的 token 没被计入，合计只会偏少"
              }
            ]
          : [])
      ],
      note: usageRangeNote.value
    }
  };
});

const activeDetail = computed<DetailView | null>(() => (openDetail.value ? detailViews.value[openDetail.value] : null));

function cacheRows(usage: UsageDetail | null): DetailRow[] {
  const hint = "利用率 = 缓存命中 ÷ 输入 Token，缓存已经包含在输入里";
  if (!usage?.breakdown?.length) {
    return [{ label: "其中缓存命中", value: cacheUsageDisplay(usage?.cached_input_tokens ?? 0, usage?.input_tokens ?? 0), hint }];
  }
  const split = splitCacheUsage(usage.breakdown);
  const rows: DetailRow[] = [{ label: "其中缓存命中", value: cacheUsageDisplay(split.cached_input_tokens, split.input_tokens), hint: `不含发送前审核和回复触发判断；${hint}` }];
  if (split.excluded_input_tokens > 0) {
    rows.push({ label: "审核和触发判断", value: cacheUsageDisplay(split.excluded_cached_input_tokens, split.excluded_input_tokens), hint: "请求短、前缀几乎不复用，单独列出；各用途明细见下方分类表" });
  }
  return rows;
}

function llmSuccessRateHint(usage: UsageDetail | null): string {
  const ok = usage?.calls ?? 0;
  const failed = usage?.failed_calls ?? 0;
  if (ok + failed <= 0) return "降级链里失败的每一档各算一次；点击打开日志页「LLM 调用」";
  return `请求成功率 ${((ok / (ok + failed)) * 100).toFixed(1)}%；点击直接查看失败调用日志`;
}

// 图表双维切换：口径（消息 / Token） × 展示（趋势 / 年历），两者正交生效。
type ChartMetric = "messages" | "tokens";
type ChartMode = "trend" | "calendar";
const chartViewKey = "diana.dashboard.chart-view";
const chartMetricKey = "diana.dashboard.chart-metric";

function readChartMode(): ChartMode {
  try {
    const saved = window.localStorage.getItem(chartViewKey);
    if (saved === "hourly" || saved === "trend") return "trend";
    return "calendar";
  } catch {
    return "calendar";
  }
}

function readChartMetric(): ChartMetric {
  try {
    const savedMetric = window.localStorage.getItem(chartMetricKey) || window.localStorage.getItem(chartViewKey);
    return savedMetric === "tokens" ? "tokens" : "messages";
  } catch {
    return "messages";
  }
}

const chartMode = ref<ChartMode>(readChartMode());
const chartMetric = ref<ChartMetric>(readChartMetric());
const calendarCaption = ref("");

const chartHeading = computed(() => {
  if (chartMode.value === "calendar") {
    return calendarCaption.value || (chartMetric.value === "tokens" ? "每日 Token 用量" : "每日消息量");
  }
  const unit = effectiveGranularity.value === "day" ? "按天" : "按小时";
  if (chartMetric.value === "tokens") return `${rangeLabel.value} Token 趋势（${unit}）`;
  return `${rangeLabel.value}消息与回复趋势（${unit}）`;
});

function setChartMode(value: ChartMode): void {
  chartMode.value = value;
  try {
    window.localStorage.setItem(chartViewKey, value);
  } catch {
    // 隐私模式写不进去不影响当次切换。
  }
}

function setChartMetric(value: ChartMetric): void {
  chartMetric.value = value;
  try {
    window.localStorage.setItem(chartMetricKey, value);
  } catch {
    // 隐私模式写不进去不影响当次切换。
  }
}

const hourlyBuckets = computed<StatsHourBucket[]>(() => (stats.value ? [...stats.value.hourly] : []));
const processMetricsReady = computed(() => {
  const server = stats.value?.server;
  return !!server && !server.process_metrics_unavailable && server.process_cpu_percent !== undefined;
});

const feed = computed<BotEvent[]>(() => {
  const source = stream.events.length > 0 ? stream.events : (stream.status?.recent_events ?? []);
  return source.filter((event) => matchesBotScope(event.profile_id));
});

function eventKindLabel(kind: string): string {
  const labels: Record<string, string> = { private: "私聊", group: "群聊", notice: "通知", meta: "元事件" };
  return labels[kind] ?? kind;
}

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (["onebot-v11", "onebot", "lagrange", "go-cqhttp"].includes(platform)) return "OneBot v11";
  return platform;
}

function eventBadgeClass(event: BotEvent): string {
  if (event.error) return "err";
  if (event.handled) return "ok";
  return "";
}

function eventDecisionLabel(event: BotEvent): string {
  if (event.decision === "replied" || event.handled) return "已回复";
  if (event.decision === "pending") return "等待判断";
  if (event.decision === "error" || event.error) return "处理异常";
  return "未回复";
}

function eventDecisionClass(event: BotEvent): string {
  if (event.decision === "replied" || event.handled) return "ok";
  if (event.decision === "pending") return "warn";
  if (event.decision === "error" || event.error) return "err";
  return "";
}

const runtimeItems = computed(() => {
  const server = stats.value?.server;
  return [
    { label: "运行时长", icon: Clock, value: stats.value ? formatUptime(stats.value.uptime_seconds) : "—" },
    { label: "插件", icon: Puzzle, value: stats.value ? `${stats.value.bot.plugins_enabled} / ${stats.value.bot.plugins_total}` : "—" },
    { label: "CPU", icon: Cpu, value: processMetricsReady.value ? formatPercent(server?.process_cpu_percent) : "—" },
    { label: "内存", icon: MemoryStick, value: processMetricsReady.value ? formatBytes(server?.process_memory_bytes) : "—" },
    { label: "存储", icon: HardDrive, value: server?.process_storage_bytes ? formatBytes(server.process_storage_bytes) : "统计中…" },
    { label: "最近事件", icon: Activity, value: stats.value?.last_event_at ? formatRelative(stats.value.last_event_at) : "—" }
  ];
});

function formatPercent(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return "—";
  return `${value.toFixed(value >= 10 ? 1 : 2)}%`;
}

async function refresh(): Promise<void> {
  try {
    const [statusResult, statsResult, llmConfig] = await Promise.all([getBotStatus(), getStats(), getConfig()]);
    pushStatusSnapshot(statusResult);
    pushStatsSnapshot(statsResult);
    setupNeeded.value = !llmConfig.model || !llmConfig.api_key_configured;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "刷新失败");
  } finally {
    pending.value = false;
  }
}

async function refreshAll(): Promise<void> {
  await Promise.all([refresh(), loadOverview(), loadHealth()]);
}

onMounted(() => {
  void loadGroupNames();
  void loadOverview();
  void loadHealth();
  if (stream.status && stream.stats) {
    pending.value = false;
    void getConfig()
      .then((llmConfig) => {
        setupNeeded.value = !llmConfig.model || !llmConfig.api_key_configured;
      })
      .catch(() => undefined);
    return;
  }
  void refresh();
});

watch(botScope, () => {
  void loadGroupNames();
  void loadOverview();
});
useConfigurationRefresh(["bot", "llm"], refreshAll);
</script>

<style scoped>
.dashboard-range-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.dashboard-range-actions .segmented button,
.dashboard-chart-controls .segmented button {
  white-space: nowrap;
  flex-shrink: 0;
}

.dashboard-chart-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
  flex-shrink: 0;
}

.dashboard-chart-controls .dashboard-chart-switch {
  margin-left: 0;
}

.dashboard-extra-select {
  width: 124px;
  min-width: 116px;
}

.dashboard-extra-select :deep(.app-select-trigger) {
  height: 32px;
  padding-inline: 10px;
  font-size: 12.5px;
}

.dashboard-extra-select.is-selected :deep(.app-select-trigger) {
  border-color: var(--accent);
  color: var(--accent-strong);
  background: var(--accent-soft);
}

.stat-card-error :deep(.stat-value),
.stat-card-error :deep(.stat-label) {
  color: var(--err);
}

.dashboard-runtime-error-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 6px 8px;
  border: 1px solid color-mix(in srgb, var(--err) 35%, var(--border));
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--err) 8%, transparent);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.dashboard-runtime-error-btn span {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dashboard-runtime-error-btn:hover {
  background: color-mix(in srgb, var(--err) 14%, transparent);
}

.dashboard-feed-item {
  cursor: pointer;
  border-radius: var(--radius-sm);
  transition: background 0.15s ease;
}

.dashboard-feed-item:hover {
  background: var(--surface-2);
}

.dashboard-feed-item.is-error {
  border-left: 3px solid var(--err);
  padding-left: 10px;
}

.feed-error-jump {
  margin-left: auto;
  padding: 0;
  border: 0;
  background: none;
  font: inherit;
  font-size: 11.5px;
  font-weight: 500;
  cursor: pointer;
  text-decoration: underline;
}

.detail-row-link {
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  font-weight: 600;
  cursor: pointer;
  text-decoration: underline;
  text-underline-offset: 2px;
}

.detail-row-link:hover {
  color: var(--accent);
}

.detail-subhead {
  margin: 0;
  font-size: 12.5px;
  font-weight: 600;
}

.error-modal-detail {
  border-top: 1px solid var(--border);
  padding-top: 12px;
}

.error-cooldown-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-size: 12.5px;
}

.error-category-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 240px;
  overflow-y: auto;
}

.error-category-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  color: inherit;
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.error-category-row:hover {
  border-color: var(--accent);
}

.error-category-top {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  width: 100%;
}

.error-category-sample {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.45;
  word-break: break-word;
}
</style>
