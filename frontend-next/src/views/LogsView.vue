<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>日志</h1>
        <p>操作审计与接口错误记录</p>
      </div>
      <div class="view-actions">
        <div class="segmented" role="tablist" aria-label="日志类型">
          <button type="button" :class="{ active: kind === 'operation' }" @click="switchKind('operation')">操作日志</button>
          <button type="button" :class="{ active: kind === 'error' }" @click="switchKind('error')">错误日志</button>
        </div>
        <label class="switch" style="margin-left: 4px">
          <input v-model="autoRefresh" type="checkbox" />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">自动刷新</span>
        </label>
        <button class="btn" type="button" :disabled="loading" @click="reload">
          <RefreshCw :size="15" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <section class="card">
      <div class="card-body" style="padding-top: 8px">
        <div class="cluster" style="padding: 8px 0 12px">
          <div class="input-group" style="flex: 1; max-width: 360px">
            <input v-model="query" class="input" placeholder="按动作 / 内容 / 操作人过滤…" />
          </div>
          <span class="muted" style="font-size: 12.5px">{{ filteredLogs.length }} 条</span>
        </div>

        <div v-if="filteredLogs.length > 0">
          <article v-for="log in filteredLogs" :key="log.id" class="log-row">
            <span class="log-time">{{ formatTime(log.created_at) }}</span>
            <div class="log-main">
              <div class="cluster" style="gap: 6px; margin-bottom: 2px">
                <span class="badge" :class="log.level === 'error' ? 'err' : 'ok'">{{ log.action }}</span>
                <span v-if="log.actor" class="muted mono" style="font-size: 11.5px">{{ log.actor }}</span>
                <span v-if="log.target" class="muted mono" style="font-size: 11.5px">→ {{ log.target }}</span>
              </div>
              <p class="log-message">{{ log.message }}</p>
              <p v-if="log.detail && log.detail !== log.message" class="log-detail">{{ log.detail }}</p>
            </div>
          </article>
        </div>
        <EmptyState v-else-if="!loading" :title="query ? '没有匹配的日志' : '暂无日志'" />
        <div v-else class="stack">
          <div class="skeleton" style="height: 48px"></div>
          <div class="skeleton" style="height: 48px"></div>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { RefreshCw } from "@lucide/vue";
import { listAppLogs, type AppLogEntry, type AppLogKind } from "../api";
import { formatTime } from "../format";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";

const kind = ref<AppLogKind>("operation");
const logs = ref<AppLogEntry[]>([]);
const loading = ref(false);
const query = ref("");
const autoRefresh = ref(false);

let timer: number | undefined;

const filteredLogs = computed<AppLogEntry[]>(() => {
  const keyword = query.value.trim().toLowerCase();
  if (!keyword) {
    return logs.value;
  }
  return logs.value.filter((log) => {
    return [log.action, log.message, log.detail, log.actor, log.target]
      .filter((field): field is string => Boolean(field))
      .some((field) => field.toLowerCase().includes(keyword));
  });
});

async function reload(): Promise<void> {
  loading.value = true;
  try {
    const response = await listAppLogs(kind.value, 200);
    logs.value = response.logs;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载日志失败");
  } finally {
    loading.value = false;
  }
}

function switchKind(next: AppLogKind): void {
  if (kind.value !== next) {
    kind.value = next;
    void reload();
  }
}

function applyAutoRefresh(): void {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
  if (autoRefresh.value) {
    timer = window.setInterval(() => {
      void reload();
    }, 5000);
  }
}

watch(autoRefresh, applyAutoRefresh);

onMounted(() => {
  void reload();
});

onBeforeUnmount(() => {
  if (timer !== undefined) {
    window.clearInterval(timer);
  }
});
</script>
