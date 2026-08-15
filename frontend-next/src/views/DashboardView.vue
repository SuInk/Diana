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

      <!-- 连接链路 -->
      <section class="card">
        <div class="card-body">
          <div class="checklist">
            <div class="checklist-item" :class="status?.running ? 'done' : 'todo'">
              <span class="check-icon">
                <CheckCircle2 v-if="status?.running" :size="15" aria-hidden="true" />
                <Power v-else :size="14" aria-hidden="true" />
              </span>
              <span class="check-main">
                机器人运行时
                <div class="check-hint">{{ status?.running ? "运行中" : "未启动 — 点击右上角「启动机器人」" }}</div>
              </span>
              <span v-if="status" class="badge" :class="status.running ? 'ok' : 'warn'">
                {{ status.running ? "运行中" : "已停止" }}
              </span>
            </div>

            <div class="checklist-item" :class="connectedChannelCount > 0 ? 'done' : 'todo'">
              <span class="check-icon">
                <CheckCircle2 v-if="connectedChannelCount > 0" :size="15" aria-hidden="true" />
                <Cable v-else :size="14" aria-hidden="true" />
              </span>
              <span class="check-main">
                机器人通道
                <div class="check-hint">{{ channelSummary }}</div>
                <div v-if="status?.channel.last_error" class="check-hint text-err">{{ status.channel.last_error }}</div>
              </span>
              <span v-if="status" class="badge" :class="connectedChannelCount > 0 ? 'ok' : 'err'">
                {{ connectedChannelCount > 0 ? `已连接 ${connectedChannelCount} / ${channelCount}` : "未连接" }}
              </span>
            </div>

            <div v-if="status?.nonebot_bridge.enabled" class="checklist-item" :class="status.nonebot_bridge.connected ? 'done' : 'todo'">
              <span class="check-icon">
                <CheckCircle2 v-if="status.nonebot_bridge.connected" :size="15" aria-hidden="true" />
                <SplitSquareHorizontal v-else :size="14" aria-hidden="true" />
              </span>
              <span class="check-main">
                NoneBot 插件桥
                <div class="check-hint mono">{{ status.nonebot_bridge.endpoint || "—" }}</div>
              </span>
              <span class="badge" :class="status.nonebot_bridge.connected ? 'ok' : 'warn'">
                {{ status.nonebot_bridge.connected ? "已连接" : "等待连接" }}
              </span>
            </div>
          </div>
        </div>
      </section>

      <!-- 统计卡片 -->
      <div class="stat-grid">
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

      <section class="card resource-monitor">
        <div class="card-header">
          <div>
            <h2>系统资源</h2>
            <div class="card-sub">{{ resourceHostLabel }}</div>
          </div>
          <span v-if="stats?.server?.collected_at" class="badge">更新于 {{ formatClock(stats.server.collected_at) }}</span>
        </div>
        <div class="resource-grid">
          <article class="resource-item">
            <div class="resource-heading">
              <span class="resource-icon"><Cpu :size="17" aria-hidden="true" /></span>
              <div>
                <div class="resource-label">CPU</div>
                <div class="resource-value">{{ formatPercent(stats?.server?.cpu_usage_percent) }}</div>
              </div>
            </div>
            <div class="resource-track" role="progressbar" aria-label="CPU 占用" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="resourcePercent(stats?.server?.cpu_usage_percent)">
              <span :class="usageClass(stats?.server?.cpu_usage_percent)" :style="{ width: `${resourcePercent(stats?.server?.cpu_usage_percent)}%` }" />
            </div>
            <div class="resource-detail">
              <span>{{ stats?.server ? `${stats.server.cpu_cores} 核` : "等待采样" }}</span>
              <span v-if="stats?.server?.process_cpu_percent !== undefined">Diana {{ formatPercent(stats.server.process_cpu_percent) }}</span>
            </div>
          </article>

          <article class="resource-item">
            <div class="resource-heading">
              <span class="resource-icon"><MemoryStick :size="17" aria-hidden="true" /></span>
              <div>
                <div class="resource-label">内存</div>
                <div class="resource-value">{{ formatPercent(stats?.server?.memory_usage_percent) }}</div>
              </div>
            </div>
            <div class="resource-track" role="progressbar" aria-label="内存占用" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="resourcePercent(stats?.server?.memory_usage_percent)">
              <span :class="usageClass(stats?.server?.memory_usage_percent)" :style="{ width: `${resourcePercent(stats?.server?.memory_usage_percent)}%` }" />
            </div>
            <div class="resource-detail">
              <span>{{ memoryUsageLabel }}</span>
              <span v-if="stats?.server?.process_memory_bytes !== undefined">Diana {{ formatBytes(stats.server.process_memory_bytes) }}</span>
            </div>
          </article>

          <article class="resource-item">
            <div class="resource-heading">
              <span class="resource-icon"><HardDrive :size="17" aria-hidden="true" /></span>
              <div>
                <div class="resource-label">存储空间</div>
                <div class="resource-value">{{ formatPercent(stats?.server?.storage_usage_percent) }}</div>
              </div>
            </div>
            <div class="resource-track" role="progressbar" aria-label="存储空间占用" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="resourcePercent(stats?.server?.storage_usage_percent)">
              <span :class="usageClass(stats?.server?.storage_usage_percent)" :style="{ width: `${resourcePercent(stats?.server?.storage_usage_percent)}%` }" />
            </div>
            <div class="resource-detail">
              <span>{{ storageUsageLabel }}</span>
              <span v-if="stats?.server?.storage_available_bytes !== undefined">可用 {{ formatBytes(stats.server.storage_available_bytes) }}</span>
            </div>
          </article>
        </div>
        <div v-if="resourceUnavailableReason" class="resource-unavailable">
          <TriangleAlert :size="14" aria-hidden="true" />
          {{ resourceUnavailableReason }}
        </div>
      </section>

      <div class="grid-main-side dashboard-insights">
        <!-- 24h 消息量 -->
        <section class="card">
          <div class="card-header">
            <h2>最近 24 小时消息量</h2>
            <span v-if="stats?.last_event_at" class="badge">最近事件 {{ formatRelative(stats.last_event_at) }}</span>
          </div>
          <div class="card-body">
            <HourlyBars v-if="stats" :buckets="hourlyBuckets" />
            <EmptyState v-else title="暂无统计数据" hint="机器人处理消息后这里会出现走势" />
          </div>
        </section>

        <!-- 运行信息 -->
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
                  <span v-if="event.user_id" class="muted mono">{{ event.user_id }}</span>
                  <span v-if="event.duration_ms" class="muted">{{ (event.duration_ms / 1000).toFixed(1) }}s</span>
                  <span v-if="event.decision" class="badge" :class="eventDecisionClass(event)">{{ eventDecisionLabel(event) }}</span>
                </div>
                <p v-if="event.text" class="event-text">{{ truncate(event.text, 140) }}</p>
                <p v-if="event.reply" class="event-reply">{{ truncate(event.reply, 200) }}</p>
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
  Cable,
  CheckCircle2,
  Cpu,
  HardDrive,
  MemoryStick,
  MessageCircle,
  Power,
  PowerOff,
  RefreshCw,
  Sparkles,
  SplitSquareHorizontal,
  TriangleAlert,
  Zap
} from "@lucide/vue";
import { getConfig, getQQBotStatus, getStats, startQQBot, stopQQBot, type StatsHourBucket } from "../api";
import { pushStatsSnapshot, pushStatusSnapshot, stream, type BotEvent } from "../stream";
import { navigate } from "../router";
import { formatBytes, formatClock, formatNumber, formatRelative, formatUptime, truncate } from "../format";
import { toastError, toastSuccess } from "../toast";
import StatCard from "../components/StatCard.vue";
import HourlyBars from "../components/HourlyBars.vue";
import EmptyState from "../components/EmptyState.vue";

const busy = ref(false);
const setupNeeded = ref(false);

const status = computed(() => stream.status);
const stats = computed(() => stream.stats);
const channelCount = computed(() => status.value?.channels?.length ?? (status.value?.channel ? 1 : 0));
const connectedChannelCount = computed(() => status.value?.channels?.filter((channel) => channel.connected).length ?? (status.value?.channel?.connected ? 1 : 0));
const channelSummary = computed(() => {
  const channels = status.value?.channels ?? [];
  if (channels.length === 0) return status.value?.channel?.endpoint || "—";
  return channels.map((channel) => `${channel.name || channel.platform || "通道"} · ${channel.connected ? "在线" : "离线"}`).join("  /  ");
});
const hourlyBuckets = computed<StatsHourBucket[]>(() => (stream.stats ? [...stream.stats.hourly] : []));
const resourceHostLabel = computed(() => {
  const server = stats.value?.server;
  if (!server) return "等待服务器资源采样";
  const platform = [server.os, server.arch].filter(Boolean).join(" / ");
  return [server.hostname, platform].filter(Boolean).join(" · ");
});
const memoryUsageLabel = computed(() => {
  const server = stats.value?.server;
  if (!server?.memory_total_bytes) return "暂不可用";
  return `${formatBytes(server.memory_used_bytes)} / ${formatBytes(server.memory_total_bytes)}`;
});
const storageUsageLabel = computed(() => {
  const server = stats.value?.server;
  if (!server?.storage_total_bytes) return "暂不可用";
  return `${formatBytes(server.storage_used_bytes)} / ${formatBytes(server.storage_total_bytes)}`;
});
const resourceUnavailableReason = computed(() => {
  const server = stats.value?.server;
  return server?.metrics_unavailable_reason || server?.storage_metrics_unavailable || "";
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

function resourcePercent(value: number | undefined): number {
  if (value === undefined || !Number.isFinite(value)) return 0;
  return Math.min(100, Math.max(0, value));
}

function formatPercent(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return "—";
  return `${value.toFixed(value >= 10 ? 1 : 2)}%`;
}

function usageClass(value: number | undefined): string {
  const percent = resourcePercent(value);
  if (percent >= 90) return "critical";
  if (percent >= 75) return "warning";
  return "normal";
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
  void refresh();
});
</script>
