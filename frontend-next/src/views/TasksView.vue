<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>提醒与订阅</h1>
        <p>查看一次性提醒、周期查询和仓库更新订阅的执行状态</p>
      </div>
      <div class="view-actions">
        <button class="btn ghost" type="button" @click="navigate('logs')">
          <FileClock :size="15" aria-hidden="true" />
          执行日志
        </button>
        <button class="btn" type="button" :disabled="loading" @click="load">
          <RefreshCw :size="15" :class="{ spin: loading }" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <div class="stack">
      <div class="stat-grid task-stats">
        <StatCard label="全部任务" :value="formatNumber(tasks.length)" :foot="`${reminderCount} 个提醒 / ${scheduleCount} 个周期查询 / ${repositoryWatchCount} 个仓库订阅 / ${rssWatchCount} 个 RSS 订阅`">
          <template #icon><ListTodo :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="运行中" :value="formatNumber(activeCount)" :foot="`${retryingCount} 个正在重试`">
          <template #icon><Clock3 :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="占用额度" :value="formatNumber(quotaCount)" foot="已完成或取消后释放">
          <template #icon><Gauge :size="14" aria-hidden="true" /></template>
        </StatCard>
        <StatCard label="已结束" :value="formatNumber(finishedCount)" :foot="`${usedCount} 个已执行 / ${cancelledCount} 个已取消`">
          <template #icon><CircleCheck :size="14" aria-hidden="true" /></template>
        </StatCard>
      </div>

      <section class="task-filter-band" aria-label="任务筛选">
        <div class="task-filter-group">
          <span class="task-filter-label">类型</span>
          <div class="segmented task-segments" role="radiogroup" aria-label="按任务类型筛选">
            <button v-for="option in kindOptions" :key="option.value" type="button" role="radio" :aria-checked="kind === option.value" :class="{ active: kind === option.value }" @click="kind = option.value">
              {{ option.label }}
            </button>
          </div>
        </div>
        <div class="task-filter-group">
          <span class="task-filter-label">状态</span>
          <div class="segmented task-segments" role="radiogroup" aria-label="按任务状态筛选">
            <button v-for="option in statusOptions" :key="option.value" type="button" role="radio" :aria-checked="status === option.value" :class="{ active: status === option.value }" @click="status = option.value">
              {{ option.label }}
            </button>
          </div>
        </div>
        <label class="task-search">
          <Search :size="15" aria-hidden="true" />
          <input v-model="query" type="search" placeholder="搜索内容、用户或群号" />
        </label>
      </section>

      <section class="card task-list-card">
        <div class="card-header">
          <div class="cluster">
            <h2>任务明细</h2>
            <span class="badge">{{ filteredTasks.length }} 条</span>
          </div>
          <span v-if="lastLoadedAt" class="muted task-updated">更新于 {{ formatClock(lastLoadedAt) }}</span>
        </div>

        <div v-if="filteredTasks.length > 0" class="task-list">
          <article v-for="task in filteredTasks" :key="task.id" class="task-row">
            <span class="task-kind-icon" :class="task.kind">
              <GitBranch v-if="task.kind === 'repository_watch'" :size="17" aria-hidden="true" />
              <Rss v-if="task.kind === 'rss_watch'" :size="17" aria-hidden="true" />
              <Repeat2 v-if="task.kind === 'schedule'" :size="17" aria-hidden="true" />
              <Bell v-if="task.kind === 'reminder'" :size="17" aria-hidden="true" />
            </span>

            <div class="task-main">
              <div class="task-meta">
                <span class="badge">{{ taskKindLabel(task.kind) }}</span>
                <span class="badge" :class="statusTone(task.status)">{{ statusLabel(task.status) }}</span>
                <span v-if="task.platform" class="badge">{{ platformLabel(task.platform) }}</span>
                <span v-if="task.consumes_quota" class="badge warn">占用额度</span>
                <span class="mono muted">{{ task.id }}</span>
              </div>

              <p class="task-message">{{ task.message || "[无任务内容]" }}</p>

              <div v-if="task.kind === 'repository_watch' && task.repository" class="task-facts">
                <span>
                  <GitBranch :size="13" aria-hidden="true" />
                  仓库 <strong class="mono">{{ task.repository }}</strong>
                </span>
                <span v-if="task.repository_branch">
                  分支 <strong class="mono">{{ task.repository_branch }}</strong>
                </span>
                <span>
                  监控 {{ [task.watch_commits ? "Commit" : "", task.watch_pull_requests ? "PR" : "", task.watch_releases ? "Release" : "", task.watch_stars ? "Star" : ""].filter(Boolean).join(" + ") }}
                </span>
                <span v-if="task.last_commit_sha">
                  Commit <strong class="mono">{{ task.last_commit_sha.slice(0, 8) }}</strong>
                </span>
                <span v-if="task.last_release_tag && task.last_release_tag !== '__none__'">
                  Release <strong class="mono">{{ task.last_release_tag }}</strong>
                </span>
                <span v-if="task.watch_stars">
                  Star <strong class="mono">{{ task.last_star_count ?? 0 }}</strong>
                </span>
              </div>

              <div v-if="task.kind === 'rss_watch'" class="task-facts">
                <span><Rss :size="13" aria-hidden="true" />来源 <strong>{{ task.feed_source === "twitter" ? `@${task.feed_handle}` : task.message }}</strong></span>
                <span v-if="task.last_feed_item_id">游标 <strong class="mono">{{ task.last_feed_item_id.slice(0, 28) }}</strong></span>
                <a v-if="task.feed_url" :href="task.feed_url" target="_blank" rel="noreferrer">打开 Feed</a>
              </div>
              <p v-if="task.kind === 'rss_watch' && task.feed_judge_prompt" class="task-message">判断规则：{{ task.feed_judge_prompt }}</p>

              <div class="task-facts">
                <span v-if="task.kind !== 'repository_watch' && task.kind !== 'rss_watch'">
                  <UserRound :size="13" aria-hidden="true" />
                  用户 <strong class="mono">{{ task.owner_id || task.user_id || "—" }}</strong>
                </span>
                <span v-else-if="task.user_id && !task.group_id">
                  <UserRound :size="13" aria-hidden="true" />
                  私聊对象 <strong class="mono">{{ task.user_id }}</strong>
                </span>
                <span v-if="task.group_id">
                  <UsersRound :size="13" aria-hidden="true" />
                  群 <strong class="mono">{{ task.group_id }}</strong>
                </span>
                <span v-if="task.kind !== 'reminder' && task.interval_seconds">
                  <Repeat2 :size="13" aria-hidden="true" />
                  每 {{ formatInterval(task.interval_seconds) }}
                </span>
                <span v-if="nextRunLabel(task)">
                  <CalendarClock :size="13" aria-hidden="true" />
                  {{ nextRunLabel(task) }}
                </span>
                <span v-if="validTimestamp(task.last_run_at)">
                  <History :size="13" aria-hidden="true" />
                  最近执行 {{ formatTime(task.last_run_at) }}
                </span>
                <span v-if="validTimestamp(task.cancelled_at)">
                  <CircleX :size="13" aria-hidden="true" />
                  取消于 {{ formatTime(task.cancelled_at) }}
                </span>
                <span>
                  <Plus :size="13" aria-hidden="true" />
                  创建于 {{ formatTime(task.created_at) }}
                </span>
              </div>

              <div v-if="task.pending_delivery" class="task-notice warn">
                <LoaderCircle :size="14" class="spin" aria-hidden="true" />
                已生成结果，正在等待投递<span v-if="validTimestamp(task.pending_since)">，始于 {{ formatTime(task.pending_since) }}</span>
              </div>
              <div v-if="task.last_error" class="task-notice err">
                <TriangleAlert :size="14" aria-hidden="true" />
                <span class="task-notice-message">{{ task.last_error }}</span>
                <span v-if="task.consecutive_failures">连续失败 {{ task.consecutive_failures }} 次</span>
                <button
                  v-if="showRepositorySettingsGuide(task)"
                  class="btn small task-notice-action"
                  type="button"
                  @click="openRepositoryWatchSettings"
                >
                  <SlidersHorizontal :size="14" aria-hidden="true" />
                  配置 Token
                </button>
              </div>
            </div>
          </article>
        </div>

        <div v-else-if="loading" class="task-loading">
          <LoaderCircle :size="20" class="spin" aria-hidden="true" />
          正在加载任务
        </div>
        <EmptyState v-else :title="tasks.length === 0 ? '还没有提醒或订阅' : '没有匹配的任务'" :hint="tasks.length === 0 ? '仓库订阅可在插件设置中创建，聊天中的提醒和周期查询也会显示在这里' : '调整类型、状态或搜索条件'">
          <template #icon><CalendarClock :size="20" aria-hidden="true" /></template>
        </EmptyState>
      </section>

      <section class="card issue-drafts-card">
        <div class="card-body">
          <RepositoryIssueDraftList />
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import {
  Bell,
  CalendarClock,
  CircleCheck,
  CircleX,
  Clock3,
  FileClock,
  Gauge,
  GitBranch,
  History,
  ListTodo,
  LoaderCircle,
  Plus,
  RefreshCw,
  Repeat2,
  Rss,
  Search,
  SlidersHorizontal,
  TriangleAlert,
  UserRound,
  UsersRound
} from "@lucide/vue";
import { getAssistantTasks, type AssistantTask, type AssistantTaskKind, type AssistantTaskStatus } from "../api";
import { formatClock, formatNumber, formatTime } from "../format";
import { navigate } from "../router";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import RepositoryIssueDraftList from "../components/RepositoryIssueDraftList.vue";
import StatCard from "../components/StatCard.vue";

type KindFilter = "all" | AssistantTaskKind;
type StatusFilter = "all" | AssistantTaskStatus;

const kindOptions: Array<{ value: KindFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "reminder", label: "一次性提醒" },
  { value: "schedule", label: "周期查询" },
  { value: "repository_watch", label: "仓库订阅" },
  { value: "rss_watch", label: "RSS 订阅" }
];
const statusOptions: Array<{ value: StatusFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "active", label: "运行中" },
  { value: "retrying", label: "重试中" },
  { value: "used", label: "已执行" },
  { value: "cancelled", label: "已取消" }
];

const tasks = ref<AssistantTask[]>([]);
const kind = ref<KindFilter>("all");
const status = ref<StatusFilter>("all");
const query = ref("");
const loading = ref(false);
const lastLoadedAt = ref<string | null>(null);
let refreshTimer: number | undefined;

const reminderCount = computed(() => tasks.value.filter((task) => task.kind === "reminder").length);
const scheduleCount = computed(() => tasks.value.filter((task) => task.kind === "schedule").length);
const repositoryWatchCount = computed(() => tasks.value.filter((task) => task.kind === "repository_watch").length);
const rssWatchCount = computed(() => tasks.value.filter((task) => task.kind === "rss_watch").length);
const activeCount = computed(() => tasks.value.filter((task) => task.status === "active" || task.status === "retrying").length);
const retryingCount = computed(() => tasks.value.filter((task) => task.status === "retrying").length);
const quotaCount = computed(() => tasks.value.filter((task) => task.consumes_quota).length);
const usedCount = computed(() => tasks.value.filter((task) => task.status === "used").length);
const cancelledCount = computed(() => tasks.value.filter((task) => task.status === "cancelled").length);
const finishedCount = computed(() => usedCount.value + cancelledCount.value);

const filteredTasks = computed(() => {
  const keyword = query.value.trim().toLowerCase();
  return tasks.value.filter((task) => {
    if (kind.value !== "all" && task.kind !== kind.value) return false;
    if (status.value !== "all" && task.status !== status.value) return false;
    if (!keyword) return true;
    return [task.id, task.message, task.repository, task.repository_branch, task.feed_url, task.feed_handle, task.feed_judge_prompt, task.owner_id, task.user_id, task.group_id, task.platform, task.profile_id]
      .filter((value): value is string => Boolean(value))
      .some((value) => value.toLowerCase().includes(keyword));
  });
});

async function load(): Promise<void> {
  if (loading.value) return;
  loading.value = true;
  try {
    const response = await getAssistantTasks();
    tasks.value = response.items;
    lastLoadedAt.value = new Date().toISOString();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "任务加载失败");
  } finally {
    loading.value = false;
  }
}

function statusLabel(value: AssistantTaskStatus): string {
  return { active: "运行中", retrying: "重试中", used: "已执行", cancelled: "已取消" }[value] ?? value;
}

function taskKindLabel(value: AssistantTaskKind): string {
  if (value === "repository_watch") return "仓库更新订阅";
  if (value === "rss_watch") return "RSS 条件订阅";
  if (value === "schedule") return "周期查询";
  return "一次性提醒";
}

function statusTone(value: AssistantTaskStatus): string {
  if (value === "active") return "ok";
  if (value === "retrying") return "warn";
  if (value === "cancelled") return "err";
  return "";
}

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (["onebot-v11", "onebot", "napcat", "lagrange", "go-cqhttp"].includes(platform)) return "QQ";
  return platform;
}

function validTimestamp(value?: string): boolean {
  if (!value) return false;
  const parsed = new Date(value);
  return !Number.isNaN(parsed.getTime()) && parsed.getFullYear() >= 2000;
}

function nextRunLabel(task: AssistantTask): string {
  if ((task.status !== "active" && task.status !== "retrying") || !validTimestamp(task.trigger_at)) return "";
  return `${task.status === "retrying" ? "下次重试" : "下次执行"} ${formatTime(task.trigger_at)}`;
}

function formatInterval(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400} 天`;
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`;
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

function showRepositorySettingsGuide(task: AssistantTask): boolean {
  return task.kind === "repository_watch" && /GitHub|Token|请求额度|限流/i.test(task.last_error ?? "");
}

function openRepositoryWatchSettings(): void {
  navigate("plugins", { settings: "official.repository-watch" });
}

onMounted(() => {
  void load();
  refreshTimer = window.setInterval(() => void load(), 15_000);
});

onBeforeUnmount(() => {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer);
});
</script>
