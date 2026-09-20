<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>总览</h1>
        <p>机器人运行状态、消息量和实时事件</p>
      </div>
      <div class="view-actions">
        <button v-if="status && !status.running" class="btn primary" type="button" :disabled="busy" @click="toggleBot(true)">
          <Power :size="15" aria-hidden="true" />
          启动机器人
        </button>
        <button v-else-if="status" class="btn danger" type="button" :disabled="busy" @click="toggleBot(false)">
          <PowerOff :size="15" aria-hidden="true" />
          停止机器人
        </button>
        <button class="btn" type="button" :disabled="busy || refreshing" @click="refresh">
          <RefreshCw :size="15" aria-hidden="true" />
          刷新
        </button>
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
        <StatCard label="今日消息" :loading="initialLoading" :value="formatNumber(stats?.today_events ?? 0)" :foot="inboundFoot">
          <template #icon><MessageCircle :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="今日已回复" :loading="initialLoading" :value="formatNumber(stats?.today_handled ?? 0)" :foot="`累计 ${formatNumber(stats?.handled_events ?? 0)}`">
          <template #icon><CheckCircle2 :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="今日错误" :loading="initialLoading" :value="formatNumber(stats?.today_errors ?? 0)" :foot="`累计 ${formatNumber(stats?.error_events ?? 0)}`">
          <template #icon><TriangleAlert :size="14" aria-hidden="true" /></template>
        </StatCard>
        <!-- 队列积压和后台子任务并发是排查「机器人怎么不理我」最直接的两个指标，
             后端一直在算，前端此前没有读。 -->
        <StatCard label="平均响应" :loading="initialLoading" :value="stats && stats.avg_reply_ms > 0 ? `${(stats.avg_reply_ms / 1000).toFixed(1)}s` : '—'" :foot="`回复并发 ${status?.active_workers ?? 0} / 后台任务 ${status?.active_subagent_tasks ?? 0}`">
          <template #icon><Zap :size="14" aria-hidden="true" /></template>
        </StatCard>
        <!-- 回复并发数的是消息 worker；一个 worker 一轮要打好几次模型，撞限流和涨账单
             的是这一个数，所以它单独占一张卡。 -->
        <StatCard label="模型并发" :loading="initialLoading" :value="formatNumber(status?.llm_concurrency?.active ?? 0)" :foot="llmConcurrencyFoot" :hint="llmConcurrencyHint">
          <template #icon><Gauge :size="14" aria-hidden="true" /></template>
        </StatCard>
        <!-- 并发说明此刻压力有多大，token 说明这些调用花掉了什么，两个数出自同一批调用。 -->
        <StatCard label="今日 Token" :loading="initialLoading" :value="formatNumber(status?.llm_usage?.today?.total_tokens ?? 0)" :foot="llmUsageFoot" :hint="llmUsageHint">
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
  Power,
  PowerOff,
  RefreshCw,
  Sparkles,
  TriangleAlert,
  Zap
} from "@lucide/vue";
import { getConfig, getBotStatus, getStats, listBotGroups, startBot, stopBot, type StatsHourBucket } from "../api";
import { pushStatsSnapshot, pushStatusSnapshot, scopedStats, stream, type BotEvent } from "../stream";
import { navigate } from "../router";
import { botScope, matchesBotScope } from "../bot-scope";
import { formatBytes, formatClock, formatNumber, formatRelative, formatUptime, truncate } from "../format";
import { displayMessageText, displayChatIdentity } from "../message-display";
import { toastError, toastSuccess } from "../toast";
import StatCard from "../components/StatCard.vue";
import HourlyBars from "../components/HourlyBars.vue";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";

const busy = ref(false);
const refreshing = ref(false);
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
  const peak = `峰值 ${formatNumber(concurrency?.peak ?? 0)}`;
  const busiest = concurrency?.models?.[0];
  return busiest ? `${peak} · ${busiest.model} ×${busiest.active}` : peak;
});

const llmConcurrencyHint = computed(() => {
  const models = status.value?.llm_concurrency?.models ?? [];
  if (models.length === 0) {
    return "当前没有正在进行的模型调用";
  }
  return models
    .map((entry) => `${entry.model}${entry.provider ? `（${entry.provider}）` : ""} ×${entry.active}，最早 ${formatRelative(entry.started_at)}`)
    .join("\n");
});

const llmUsageFoot = computed(() => `调用 ${formatNumber(status.value?.llm_usage?.today?.calls ?? 0)} 次`);

// 输入输出的分法、缓存命中和「上游没报用量」都影响这个数怎么读，但一行放不下，走 hint。
const llmUsageHint = computed(() => {
  const usage = status.value?.llm_usage;
  if (!usage) {
    return "本次启动后还没有模型调用";
  }
  const lines = [
    `今日 输入 ${formatNumber(usage.today.input_tokens)} / 输出 ${formatNumber(usage.today.output_tokens)}`,
    `本次运行累计 ${formatNumber(usage.session.total_tokens)}，调用 ${formatNumber(usage.session.calls)} 次（重启清零）`
  ];
  if (usage.today.cached_input_tokens > 0) {
    lines.splice(1, 0, `其中缓存命中输入 ${formatNumber(usage.today.cached_input_tokens)}`);
  }
  if (usage.today.missing_usage_calls > 0) {
    lines.push(`有 ${formatNumber(usage.today.missing_usage_calls)} 次调用上游没报用量，合计偏少`);
  }
  return lines.join("\n");
});

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
  if (["onebot-v11", "onebot", "napcat", "lagrange", "go-cqhttp"].includes(platform)) return "OneBot v11";
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
  refreshing.value = true;
  try {
    const [statusResult, statsResult, llmConfig] = await Promise.all([getBotStatus(), getStats(), getConfig()]);
    pushStatusSnapshot(statusResult);
    pushStatsSnapshot(statsResult);
    setupNeeded.value = !llmConfig.model || !llmConfig.api_key_configured;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "刷新失败");
  } finally {
    pending.value = false;
    refreshing.value = false;
  }
}

async function toggleBot(start: boolean): Promise<void> {
  busy.value = true;
  try {
    const result = start ? await startBot() : await stopBot();
    pushStatusSnapshot(result);
    toastSuccess(start ? "机器人已启动" : "机器人已停止");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    busy.value = false;
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
