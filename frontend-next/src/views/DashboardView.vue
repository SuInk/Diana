<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <p>机器人运行状态、消息量和实时事件</p>
      </div>
      <!-- 一个时间选择管住下面所有卡片，不用每张卡各点各的。 -->
      <div class="view-actions">
        <div class="segmented" role="radiogroup" aria-label="统计时间范围">
          <button
            v-for="option in rangeOptions"
            :key="option.value"
            type="button"
            role="radio"
            :class="{ active: selectedRange === option.value }"
            :aria-checked="selectedRange === option.value"
            :disabled="rangesLoading"
            @click="selectRange(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
      </div>
    </header>

    <div class="stack">
      <section v-if="setupNeeded" class="setup-callout">
        <span class="setup-callout-icon">
          <Sparkles :size="22" aria-hidden="true" />
        </span>
        <div class="setup-callout-main">
          <strong>完成首次配置</strong>
          <p>配置模型渠道和机器人接入后，即可开始处理 OneBot v11 消息。</p>
        </div>
        <button class="btn primary" type="button" @click="navigate('setup')">
          开始配置
          <ArrowRight :size="15" aria-hidden="true" />
        </button>
      </section>


      <!-- 统计卡片 -->
      <div class="stat-grid dashboard-stats">
        <StatCard :label="`${rangeLabel}消息`" :loading="cardsLoading" :value="messagesValue" :foot="inboundFoot" clickable @activate="openDetail = 'messages'">
          <template #icon><MessageCircle :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard
          :label="`${rangeLabel}已回复`"
          :loading="cardsLoading"
          :value="handledValue"
          :foot="`累计 ${formatNumber(stats?.handled_events ?? 0)}`"
          clickable
          @activate="openDetail = 'handled'"
        >
          <template #icon><CheckCircle2 :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard
          :label="`${rangeLabel}错误`"
          :loading="cardsLoading"
          :value="errorsValue"
          :foot="`累计 ${formatNumber(stats?.error_events ?? 0)}`"
          clickable
          @activate="openDetail = 'errors'"
        >
          <template #icon><TriangleAlert :size="14" aria-hidden="true" /></template>
        </StatCard>
        <!-- 队列积压和后台子任务并发是排查「机器人怎么不理我」最直接的两个指标，
             后端一直在算，前端此前没有读。 -->
        <StatCard label="平均响应" :loading="cardsLoading" :value="avgReplyValue" :foot="avgReplyFoot" clickable @activate="openDetail = 'latency'">
          <template #icon><Zap :size="14" aria-hidden="true" /></template>
        </StatCard>
        <!-- 回复并发数的是消息 worker；一个 worker 一轮要打好几次模型，撞限流和涨账单
             的是这一个数，所以它单独占一张卡。
             并发是此刻在飞的请求数，从来没有被记录成时间序列，所以它不跟随上面的
             时间选择，始终是实时值；卡面和明细里都写明了。 -->
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
        <!-- 并发说明此刻压力有多大，token 说明这些调用花掉了什么，两个数出自同一批调用。 -->
        <StatCard
          :label="`${rangeLabel} Token`"
          :loading="cardsLoading"
          :value="formatCompactNumber(selectedUsage?.total_tokens ?? 0)"
          :foot="llmUsageFoot"
          clickable
          @activate="openDetail = 'usage'"
        >
          <template #icon><Coins :size="14" aria-hidden="true" /></template>
        </StatCard>
      </div>

      <div class="dashboard-insights">
        <!-- 24h 消息量：与第一行统计卡共用同一套网格，和另外两张洞察卡均分，两排竖边对齐。 -->
        <section class="card dashboard-chart">
          <div class="card-header">
            <h2>最近 24 小时消息量</h2>
            <span v-if="stats?.last_event_at" class="badge">最近事件 {{ formatRelative(stats.last_event_at) }}</span>
          </div>
          <div class="card-body">
            <LoadingSkeleton v-if="initialLoading" kind="chart" label="正在加载消息统计" />
            <HourlyBars v-else-if="stats" :buckets="hourlyBuckets" />
            <EmptyState v-else title="暂无统计数据" hint="机器人处理消息后这里会出现走势" />
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2>运行信息</h2>
          </div>
          <div class="card-body stack" style="gap: 10px; font-size: 13px">
            <div class="info-row">
              <span class="muted info-label">运行时长</span>
              <SkeletonBlock v-if="initialLoading" width="72px" height="18px" />
              <span v-else class="info-value">{{ stats ? formatUptime(stats.uptime_seconds) : "—" }}</span>
            </div>
            <div class="info-row">
              <span class="muted info-label">插件</span>
              <SkeletonBlock v-if="initialLoading" width="48px" height="18px" />
              <span v-else class="info-value">{{ stats ? `${stats.bot.plugins_enabled} / ${stats.bot.plugins_total}` : "—" }}</span>
            </div>
            <div class="info-row">
              <span class="muted info-label">实时</span>
              <span
                class="info-value"
                :class="stream.connected ? 'text-ok' : 'text-err'"
                :title="
                  stream.connected
                    ? '服务器实时推送（SSE）通道正常，页面数据会自动刷新'
                    : '实时推送已断开，正在重连；期间页面数据可能不是最新'
                "
              >{{ stream.connected ? "已连接" : "重连中…" }}</span>
            </div>
            <div v-if="status?.last_error" class="info-row">
              <span class="muted info-label">错误</span>
              <span class="info-value text-err" :title="status.last_error">{{ status.last_error }}</span>
            </div>
          </div>
        </section>

        <!-- 资源占用和服务状态是两件事，各占一张卡片。这里只报 Diana 自己吃掉
             的，整机容量不是这张卡片要回答的问题；存储读的是数据目录体积。 -->
        <section class="card">
          <div class="card-header">
            <h2>资源占用</h2>
          </div>
          <div class="card-body stack" style="gap: 10px; font-size: 13px">
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">CPU</span>
              <SkeletonBlock v-if="initialLoading" width="48px" height="18px" />
              <span v-else>{{ processMetricsReady ? formatPercent(stats?.server?.process_cpu_percent) : "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">内存</span>
              <SkeletonBlock v-if="initialLoading" width="64px" height="18px" />
              <span v-else>{{ processMetricsReady ? formatBytes(stats?.server?.process_memory_bytes) : "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">存储</span>
              <SkeletonBlock v-if="initialLoading" width="64px" height="18px" />
              <span v-else>{{ stats?.server?.process_storage_bytes ? formatBytes(stats.server.process_storage_bytes) : "统计中…" }}</span>
            </div>
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
          <button class="btn ghost small" type="button" @click="navigate('events')">
            查看明细
            <ArrowRight :size="14" aria-hidden="true" />
          </button>
        </div>
        <div class="card-body">
          <div v-if="feed.length > 0" class="event-feed">
            <article v-for="(event, index) in feed" :key="`${event.at}-${index}`" class="event-item">
              <span class="event-time">{{ formatClock(event.at) }}</span>
              <div class="event-main">
                <div class="event-meta">
                  <span v-if="event.platform" class="badge">{{ platformLabel(event.platform) }}</span>
                  <span class="badge" :class="eventBadgeClass(event)">{{ eventKindLabel(event.kind) }}</span>
                  <span v-if="event.group_id" class="muted">{{ displayGroupIdentity(event.group_id, event.group_name) }}</span>
                  <span v-if="displayChatIdentity(event.sender_name, event.user_id)" class="muted">{{ displayChatIdentity(event.sender_name, event.user_id) }}</span>
                  <span v-if="event.duration_ms" class="muted">{{ (event.duration_ms / 1000).toFixed(1) }}s</span>
                  <span v-if="event.decision" class="badge" :class="eventDecisionClass(event)">{{ eventDecisionLabel(event) }}</span>
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

    <!-- 所有卡片共用这一个明细弹窗：卡面一行只放得下一个数，分解、占比和口径说明都在这里。 -->
    <Modal v-if="activeDetail" :title="activeDetail.title" @close="openDetail = null">
      <div class="stack" style="gap: 14px">
        <div class="usage-detail-total">
          <span class="muted">{{ activeDetail.totalLabel }}</span>
          <strong>{{ activeDetail.total }}</strong>
        </div>
        <div v-if="activeDetail.rows.length" class="stack" style="gap: 10px; font-size: 13px">
          <div v-for="row in activeDetail.rows" :key="row.label" class="info-row">
            <span class="muted info-label">{{ row.label }}</span>
            <span class="info-value" :title="row.hint">{{ row.value }}</span>
          </div>
        </div>
        <p class="hint" style="margin: 0">{{ activeDetail.note }}</p>
      </div>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { useConfigurationRefresh } from "../configuration-sync";
import { computed, onMounted, ref, watch } from "vue";
import {
  Activity,
  ArrowRight,
  CheckCircle2,
  Coins,
  Gauge,
  MessageCircle,
  Sparkles,
  TriangleAlert,
  Zap
} from "@lucide/vue";
import {
  getConfig,
  getBotStatus,
  getStats,
  getStatsRanges,
  listBotGroups,
  type StatsHourBucket,
  type StatsRange,
  type StatsRangeID,
  type StatsRanges
} from "../api";
import { pushStatsSnapshot, pushStatusSnapshot, scopedStats, stream, type BotEvent } from "../stream";
import { navigate } from "../router";
import { botScope, matchesBotScope } from "../bot-scope";
import { formatBytes, formatClock, formatCompactNumber, formatNumber, formatRelative, formatUptime, truncate } from "../format";
import { displayMessageText, displayChatIdentity } from "../message-display";
import { toastError } from "../toast";
import StatCard from "../components/StatCard.vue";
import HourlyBars from "../components/HourlyBars.vue";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";

const pending = ref(true);
const setupNeeded = ref(false);
const groupNames = ref<Record<string, string>>({});

async function loadGroupNames(): Promise<void> {
  try {
    const response = await listBotGroups(false, botScope.value);
    groupNames.value = Object.fromEntries(response.groups.filter((group) => group.group_name).map((group) => [group.group_id, group.group_name ?? ""]));
  } catch {
    groupNames.value = {};
  }
}

function displayGroupIdentity(groupID?: string, eventName?: string): string {
  const id = (groupID ?? "").trim();
  const name = (eventName ?? groupNames.value[id] ?? "").trim();
  return name ? `${name}（${id}）` : `群 ${id}`;
}

const status = computed(() => stream.status);
// 总览跟着左上角的机器人开关走：切到哪台，收到、回复、错误的数字就是哪台的。
// 运行时长、服务器占用这类进程级指标不分机器人，本来就只有一份。
const stats = computed(() => scopedStats(botScope.value));
const initialLoading = computed(() => pending.value && !stats.value);

// 队列积压和后台子任务并发是排查「机器人怎么不理我」最直接的两个指标，后端一直在
// 算，前端此前没有读过。积压为 0 时不显示，免得平时多一串没信息量的文字。
const inboundFoot = computed(() => {
  const total = `累计 ${formatNumber(stats.value?.total_events ?? 0)}`;
  const pending = status.value?.pending_events ?? 0;
  return pending > 0 ? `${total} · 队列积压 ${formatNumber(pending)}` : total;
});
// 卡面只放得下一行，所以正文给峰值，再点出压力最集中的那个模型；完整分解走 hint。
const llmConcurrencyFoot = computed(() => {
  const concurrency = status.value?.llm_concurrency;
  // 邻座几张卡都跟着页头的时间选择走，这张不跟，卡面上要写明它是此刻的读数。
  const peak = `实时 · 峰值 ${formatNumber(concurrency?.peak ?? 0)}`;
  const busiest = concurrency?.models?.[0];
  return busiest ? `${peak} · ${busiest.model} ×${busiest.active}` : peak;
});

// 总览页只有一个时间选择，所有卡片跟着它走。「今日」读实时统计（进程内累加，重启
// 清零），其余几档读库里的窗口查询（跨重启仍然成立）——两个口径不混在一起算，
// 切到哪一档，页面上的数字就全部是那一档的。
type DashboardRange = "today" | StatsRangeID;

const rangeOptions: { value: DashboardRange; label: string }[] = [
  { value: "today", label: "今日" },
  { value: "1h", label: "最近 1 小时" },
  { value: "12h", label: "最近 12 小时" },
  { value: "24h", label: "最近 24 小时" }
];

const selectedRange = ref<DashboardRange>("today");
const ranges = ref<StatsRanges | null>(null);
const rangesLoading = ref(false);
const rangesError = ref("");

const rangeLabel = computed(() => rangeOptions.find((option) => option.value === selectedRange.value)?.label ?? "今日");
// 选中「今日」时为空，各处的取数逻辑据此退回实时统计。
const activeRange = computed<StatsRange | null>(() => {
  if (selectedRange.value === "today") return null;
  return ranges.value?.ranges.find((entry) => entry.id === selectedRange.value) ?? null;
});
// 已经选了窗口但还没拿到数，卡面要走骨架屏，不能先显示一版「今日」的数字。
const rangePending = computed(() => selectedRange.value !== "today" && !activeRange.value && !rangesError.value);
// 首屏还没数据、或刚切到某个窗口还没拿回来，卡面都走骨架屏。
const cardsLoading = computed(() => initialLoading.value || rangePending.value);

async function loadRanges(): Promise<void> {
  rangesLoading.value = true;
  rangesError.value = "";
  try {
    ranges.value = await getStatsRanges();
  } catch (error) {
    rangesError.value = error instanceof Error ? error.message : "读取时间窗统计失败";
    toastError(rangesError.value);
  } finally {
    rangesLoading.value = false;
  }
}

async function selectRange(value: DashboardRange): Promise<void> {
  selectedRange.value = value;
  // 每次切到窗口档都重拉：窗口是相对此刻算的，缓存住只会越看越旧。
  if (value !== "today") {
    await loadRanges();
  }
}

const messagesValue = computed(() => formatNumber(activeRange.value ? activeRange.value.messages : (stats.value?.today_events ?? 0)));
const handledValue = computed(() => formatNumber(activeRange.value ? activeRange.value.handled : (stats.value?.today_handled ?? 0)));
const errorsValue = computed(() => formatNumber(activeRange.value ? activeRange.value.errors : (stats.value?.today_errors ?? 0)));

// 平均响应没有样本时给「—」：显示 0 会被读成「回得飞快」，其实是这段时间没回过。
const avgReplyValue = computed(() => {
  const range = activeRange.value;
  if (range) {
    return range.replies_measured > 0 ? `${(range.avg_reply_ms / 1000).toFixed(1)}s` : "—";
  }
  return stats.value && stats.value.avg_reply_ms > 0 ? `${(stats.value.avg_reply_ms / 1000).toFixed(1)}s` : "—";
});

const avgReplyFoot = computed(() => {
  const range = activeRange.value;
  if (range) return `样本 ${formatNumber(range.replies_measured)} 次回复`;
  return `回复并发 ${status.value?.active_workers ?? 0} / 后台任务 ${status.value?.active_subagent_tasks ?? 0}`;
});

// 两个来源字段名不同（状态里叫 calls，用量日志里叫 recorded_calls），统一成一种形状再渲染。
interface UsageDetail {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
  total_tokens: number;
}

const selectedUsage = computed<UsageDetail | null>(() => {
  const range = activeRange.value;
  if (range) return { ...range.usage, calls: range.usage.recorded_calls };
  const today = status.value?.llm_usage?.today;
  return today ? { ...today } : null;
});

const usageRangeNote = computed(() => {
  if (selectedRange.value === "today") {
    return "今日合计由运行时累加，Diana 重启后从零开始计；换成下面几个时间窗读的就是用量日志，不受重启影响。";
  }
  return "统计窗口内已写入日志的全部调用，包括路由判断、后台子任务这些不直接产生回复的调用。";
});

// 每张卡都能点开看明细。卡面一行只放得下一个数，点开给的是这个数的分解、占比，
// 以及那些跟它一起看才有意义、但挤不进卡面的实时指标。
type DetailID = "messages" | "handled" | "errors" | "latency" | "concurrency" | "usage";

interface DetailRow {
  label: string;
  value: string;
  hint?: string;
}

interface DetailView {
  title: string;
  // 计数类的大数是「合计」，耗时是「平均」，并发是「当前」——都叫合计会把后两个说错。
  totalLabel: string;
  total: string;
  rows: DetailRow[];
  note: string;
}

const openDetail = ref<DetailID | null>(null);

// 占比的分母是同一范围内收到的消息数。没有消息、或者这个数本身就是 0 时不显示：
// 一行「占收到消息 0.0%」不带任何信息。
function shareOfMessages(value: number): string | undefined {
  const messages = activeRange.value ? activeRange.value.messages : (stats.value?.today_events ?? 0);
  if (messages <= 0 || value <= 0) return undefined;
  return `占收到消息 ${((value / messages) * 100).toFixed(1)}%`;
}

const rangeMessages = computed(() => (activeRange.value ? activeRange.value.messages : (stats.value?.today_events ?? 0)));
const rangeHandled = computed(() => (activeRange.value ? activeRange.value.handled : (stats.value?.today_handled ?? 0)));
const rangeErrors = computed(() => (activeRange.value ? activeRange.value.errors : (stats.value?.today_errors ?? 0)));

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
        { label: "已回复", value: formatNumber(rangeHandled.value) },
        { label: "错误", value: formatNumber(rangeErrors.value) },
        { label: "队列积压", value: formatNumber(status.value?.pending_events ?? 0), hint: "此刻还没处理完的消息，实时值，不随时间范围变化" },
        { label: "累计收到", value: formatNumber(stats.value?.total_events ?? 0), hint: "自有记录以来的总数" }
      ],
      note: "统计这段时间里进入处理队列的全部消息，按消息本身的发生时间计。"
    },
    handled: {
      title: `${rangeLabel.value}已回复`,
      totalLabel: "合计",
      total: formatNumber(rangeHandled.value),
      rows: [
        ...share(rangeHandled.value),
        { label: "平均响应", value: avgReplyValue.value },
        { label: "累计已回复", value: formatNumber(stats.value?.handled_events ?? 0) }
      ],
      note: "回复发出去了就算，包括那些内容是错误说明的回复；按处理完成的时间计。"
    },
    errors: {
      title: `${rangeLabel.value}错误`,
      totalLabel: "合计",
      total: formatNumber(rangeErrors.value),
      rows: [
        ...share(rangeErrors.value),
        { label: "累计错误", value: formatNumber(stats.value?.error_events ?? 0) }
      ],
      note: "处理过程中记下错误的消息。发送失败、上游拒收这些都算，不只是「报错并回复了」那一类；错误和已回复会同时计一条。"
    },
    latency: {
      title: "平均响应",
      totalLabel: "平均",
      total: avgReplyValue.value,
      rows: [
        // 「今日」这一档的平均值来自运行时那个累加器，它从本次启动算起、不分天，
        // 所以样本数也照这个口径写，不能拿今日回复数冒充。
        activeRange.value
          ? { label: "样本", value: `${formatNumber(activeRange.value.replies_measured)} 次回复` }
          : { label: "样本", value: "本次运行的全部回复", hint: "重启后从零开始计" },
        { label: "回复并发", value: formatNumber(status.value?.active_workers ?? 0), hint: "此刻正在生成回复的 worker 数，实时值" },
        { label: "后台任务", value: formatNumber(status.value?.active_subagent_tasks ?? 0), hint: "此刻在跑的后台子任务，实时值" }
      ],
      note: activeRange.value
        ? "从开始处理到这一轮回复结束的墙钟耗时，含模型调用、工具调用和发送；只统计真的回复了的消息。"
        : "从开始处理到这一轮回复结束的墙钟耗时。这一档是本次运行以来的平均，不只是今天的；换成时间窗才是那段时间自己的平均。"
    },
    concurrency: {
      title: "模型并发",
      totalLabel: "当前",
      total: formatNumber(concurrency?.active ?? 0),
      rows: [
        { label: "本次运行峰值", value: formatNumber(concurrency?.peak ?? 0), hint: "重启后从零开始计" },
        ...(concurrency?.models ?? []).map((entry) => ({
          label: `${entry.model}${entry.provider ? `（${entry.provider}）` : ""}`,
          value: `×${entry.active}`,
          hint: `最早一次开始于 ${formatRelative(entry.started_at)}`
        }))
      ],
      note: "此刻发出去还没回来的模型请求数。它从来没有被记成时间序列，所以这张卡不随上方的时间范围变化。"
    },
    usage: {
      title: `${rangeLabel.value} Token`,
      totalLabel: "合计",
      total: formatNumber(usage?.total_tokens ?? 0),
      rows: [
        { label: "输入", value: formatNumber(usage?.input_tokens ?? 0) },
        { label: "其中缓存命中", value: formatNumber(usage?.cached_input_tokens ?? 0), hint: "命中供应商前缀缓存的输入量，已经算在输入里，不要重复相加" },
        { label: "输出", value: formatNumber(usage?.output_tokens ?? 0) },
        { label: "调用次数", value: formatNumber(usage?.calls ?? 0) },
        ...(selectedRange.value === "today" && status.value?.llm_usage
          ? [
              {
                label: "本次运行累计",
                value: formatNumber(status.value.llm_usage.session.total_tokens),
                hint: `调用 ${formatNumber(status.value.llm_usage.session.calls)} 次，重启清零`
              }
            ]
          : []),
        // 上游没报用量的调用只有运行时那份累加器数得出来，所以只在「今日」这一档有。
        ...(selectedRange.value === "today" && (status.value?.llm_usage?.today.missing_usage_calls ?? 0) > 0
          ? [
              {
                label: "上游没报用量",
                value: `${formatNumber(status.value?.llm_usage?.today.missing_usage_calls ?? 0)} 次`,
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

const llmUsageFoot = computed(() => `调用 ${formatNumber(selectedUsage.value?.calls ?? 0)} 次`);

const hourlyBuckets = computed<StatsHourBucket[]>(() => (stats.value ? [...stats.value.hourly] : []));
// 进程指标可能因为权限或平台限制采集不到，那时整张卡片退回整机读数。
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
  if (event.error) {
    return "err";
  }
  if (event.handled) {
    return "ok";
  }
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

onMounted(() => {
  void loadGroupNames();
  // SSE 建连有初始快照；这里再兜底拉一次，保证直接打开页面就有数据。
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
});
useConfigurationRefresh(["bot", "llm"], refresh);

</script>
