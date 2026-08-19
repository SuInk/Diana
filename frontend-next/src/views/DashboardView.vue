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
        <button class="btn" type="button" :disabled="busy" @click="refresh">
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
          <p>配置模型渠道和机器人接入后，即可开始处理 QQ 消息。</p>
        </div>
        <button class="btn primary" type="button" @click="navigate('setup')">
          开始配置
          <ArrowRight :size="15" aria-hidden="true" />
        </button>
      </section>


      <!-- 统计卡片 -->
      <div class="stat-grid dashboard-stats">
        <StatCard label="今日消息" :value="formatNumber(stats?.today_events ?? 0)" :foot="`累计 ${formatNumber(stats?.total_events ?? 0)}`">
          <template #icon><MessageCircle :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="今日已回复" :value="formatNumber(stats?.today_handled ?? 0)" :foot="`累计 ${formatNumber(stats?.handled_events ?? 0)}`">
          <template #icon><CheckCircle2 :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="今日错误" :value="formatNumber(stats?.today_errors ?? 0)" :foot="`累计 ${formatNumber(stats?.error_events ?? 0)}`">
          <template #icon><TriangleAlert :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="平均响应" :value="stats && stats.avg_reply_ms > 0 ? `${(stats.avg_reply_ms / 1000).toFixed(1)}s` : '—'" :foot="`并发 ${status?.active_workers ?? 0}`">
          <template #icon><Zap :size="14" aria-hidden="true" /></template>
        </StatCard>
      </div>

      <div class="dashboard-insights">
        <!-- 24h 消息量：与第一行统计卡同一四列网格，占左两格。 -->
        <section class="card dashboard-chart">
          <div class="card-header">
            <h2>最近 24 小时消息量</h2>
            <span v-if="stats?.last_event_at" class="badge">最近事件 {{ formatRelative(stats.last_event_at) }}</span>
          </div>
          <div class="card-body">
            <HourlyBars v-if="stats" :buckets="hourlyBuckets" />
            <EmptyState v-else title="暂无统计数据" hint="机器人处理消息后这里会出现走势" />
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2>运行信息</h2>
          </div>
          <div class="card-body stack" style="gap: 10px; font-size: 13px">
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">服务运行时长</span>
              <span>{{ stats ? formatUptime(stats.uptime_seconds) : "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">启用插件</span>
              <span>{{ stats ? `${stats.bot.plugins_enabled} / ${stats.bot.plugins_total}` : "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">实时通道</span>
              <span :class="stream.connected ? 'text-ok' : 'text-err'">{{ stream.connected ? "SSE 已连接" : "重连中…" }}</span>
            </div>
            <div v-if="status?.last_error" class="cluster" style="justify-content: space-between">
              <span class="muted">最近错误</span>
              <span class="text-err" style="max-width: 60%; overflow-wrap: anywhere">{{ status.last_error }}</span>
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
              <span>{{ processMetricsReady ? formatPercent(stats?.server?.process_cpu_percent) : "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">内存</span>
              <span>{{ processMetricsReady ? formatBytes(stats?.server?.process_memory_bytes) : "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">存储</span>
              <span>{{ stats?.server?.process_storage_bytes ? formatBytes(stats.server.process_storage_bytes) : "统计中…" }}</span>
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
                  <span v-if="event.group_id" class="muted mono">群 {{ event.group_id }}</span>
                  <span v-if="displayQQIdentity(event.sender_name, event.user_id)" class="muted">{{ displayQQIdentity(event.sender_name, event.user_id) }}</span>
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
          <EmptyState v-else title="还没有事件" hint="机器人收到消息后会实时显示在这里">
            <template #icon><Activity :size="20" aria-hidden="true" /></template>
          </EmptyState>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import {
  Activity,
  ArrowRight,
  CheckCircle2,
  MessageCircle,
  Power,
  PowerOff,
  RefreshCw,
  Sparkles,
  TriangleAlert,
  Zap
} from "@lucide/vue";
import { getConfig, getQQBotStatus, getStats, startQQBot, stopQQBot, type StatsHourBucket } from "../api";
import { pushStatsSnapshot, pushStatusSnapshot, stream, type BotEvent } from "../stream";
import { navigate } from "../router";
import { formatBytes, formatClock, formatNumber, formatRelative, formatUptime, truncate } from "../format";
import { displayMessageText, displayQQIdentity } from "../message-display";
import { toastError, toastSuccess } from "../toast";
import StatCard from "../components/StatCard.vue";
import HourlyBars from "../components/HourlyBars.vue";
import EmptyState from "../components/EmptyState.vue";

const busy = ref(false);
const setupNeeded = ref(false);

const status = computed(() => stream.status);
const stats = computed(() => stream.stats);
const hourlyBuckets = computed<StatsHourBucket[]>(() => (stream.stats ? [...stream.stats.hourly] : []));
// 进程指标可能因为权限或平台限制采集不到，那时整张卡片退回整机读数。
const processMetricsReady = computed(() => {
  const server = stats.value?.server;
  return !!server && !server.process_metrics_unavailable && server.process_cpu_percent !== undefined;
});

const feed = computed<BotEvent[]>(() => {
  if (stream.events.length > 0) {
    return [...stream.events];
  }
  return stream.status?.recent_events ? [...stream.status.recent_events] : [];
});

function eventKindLabel(kind: string): string {
  const labels: Record<string, string> = { private: "私聊", group: "群聊", notice: "通知", meta: "元事件" };
  return labels[kind] ?? kind;
}

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (["onebot-v11", "onebot", "napcat", "lagrange", "go-cqhttp"].includes(platform)) return "QQ";
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
    const [statusResult, statsResult, llmConfig] = await Promise.all([getQQBotStatus(), getStats(), getConfig()]);
    pushStatusSnapshot(statusResult);
    pushStatsSnapshot(statsResult);
    setupNeeded.value = !llmConfig.model || !llmConfig.api_key_configured;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "刷新失败");
  }
}

async function toggleBot(start: boolean): Promise<void> {
  busy.value = true;
  try {
    const result = start ? await startQQBot() : await stopQQBot();
    pushStatusSnapshot(result);
    toastSuccess(start ? "机器人已启动" : "机器人已停止");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  // SSE 建连有初始快照；这里再兜底拉一次，保证直接打开页面就有数据。
  if (stream.status && stream.stats) {
    void getConfig()
      .then((llmConfig) => {
        setupNeeded.value = !llmConfig.model || !llmConfig.api_key_configured;
      })
      .catch(() => undefined);
    return;
  }
  void refresh();
});
</script>
