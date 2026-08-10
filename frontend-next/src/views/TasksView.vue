<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>提醒与订阅</h1>
        <p>查看所有一次性提醒和周期订阅的执行状态</p>
      </div>
      <div class="view-actions">
        <button class="btn" type="button" :disabled="loading" @click="load">
          <RefreshCw :size="15" :class="{ spin: loading }" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <div class="stack">
      <div class="stat-grid task-stats">
        <StatCard label="全部任务" :value="formatNumber(tasks.length)" :foot="`${reminderCount} 个提醒 / ${scheduleCount} 个订阅`">
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
              <Repeat2 v-if="task.kind === 'schedule'" :size="17" aria-hidden="true" />
              <Bell v-else :size="17" aria-hidden="true" />
            </span>

            <div class="task-main">
              <div class="task-meta">
                <span class="badge">{{ task.kind === "schedule" ? "周期订阅" : "一次性提醒" }}</span>
                <span class="badge" :class="statusTone(task.status)">{{ statusLabel(task.status) }}</span>
                <span v-if="task.platform" class="badge">{{ platformLabel(task.platform) }}</span>
                <span v-if="task.consumes_quota" class="badge warn">占用额度</span>
                <span class="mono muted">{{ task.id }}</span>
              </div>

              <p class="task-message">{{ task.message || "[无任务内容]" }}</p>

              <div class="task-facts">
                <span>
                  <UserRound :size="13" aria-hidden="true" />
                  用户 <strong class="mono">{{ task.owner_id || task.user_id || "—" }}</strong>
                </span>
                <span v-if="task.group_id">
                  <UsersRound :size="13" aria-hidden="true" />
                  群 <strong class="mono">{{ task.group_id }}</strong>
                </span>
                <span v-if="task.kind === 'schedule' && task.interval_seconds">
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
                <span>{{ task.last_error }}</span>
                <span v-if="task.consecutive_failures">连续失败 {{ task.consecutive_failures }} 次</span>
              </div>
            </div>
          </article>
        </div>

        <div v-else-if="loading" class="task-loading">
          <LoaderCircle :size="20" class="spin" aria-hidden="true" />
          正在加载任务
        </div>
        <EmptyState v-else :title="tasks.length === 0 ? '还没有提醒或订阅' : '没有匹配的任务'" :hint="tasks.length === 0 ? '机器人创建提醒或周期订阅后会显示在这里' : '调整类型、状态或搜索条件'">
          <template #icon><CalendarClock :size="20" aria-hidden="true" /></template>
        </EmptyState>
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
  Gauge,
  History,
  ListTodo,
  LoaderCircle,
  Plus,
  RefreshCw,
  Repeat2,
  Search,
  TriangleAlert,
  UserRound,
  UsersRound
} from "@lucide/vue";
import { getAssistantTasks, type AssistantTask, type AssistantTaskKind, type AssistantTaskStatus } from "../api";
import { formatClock, formatNumber, formatTime } from "../format";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import StatCard from "../components/StatCard.vue";

type KindFilter = "all" | AssistantTaskKind;
type StatusFilter = "all" | AssistantTaskStatus;

const kindOptions: Array<{ value: KindFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "reminder", label: "一次性提醒" },
  { value: "schedule", label: "周期订阅" }
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
    return [task.id, task.message, task.owner_id, task.user_id, task.group_id, task.platform, task.profile_id]
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

function statusTone(value: AssistantTaskStatus): string {
  if (value === "active") return "ok";
  if (value === "retrying") return "warn";
  if (value === "cancelled") return "err";
  return "";
}

function platformLabel(platform: string): string {
  if (platform === "telegram") return "Telegram";
  if (platform === "napcat" || platform === "onebot") return "QQ";
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

onMounted(() => {
  void load();
  refreshTimer = window.setInterval(() => void load(), 15_000);
});

onBeforeUnmount(() => {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer);
});
</script>
