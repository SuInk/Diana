<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <!-- 刷新是整页的动作，和事件那一档的刷新落在同一个位置：两档共用一排页头，
         动作在档之间跳来跳去，人每次都得重新找一遍。 -->
    <Teleport v-if="actionsHost" :to="actionsHost">
      <label class="switch">
        <input v-model="autoRefresh" type="checkbox" />
        <span class="track" aria-hidden="true"></span>
        <span class="switch-label">自动刷新</span>
      </label>
      <button class="btn" type="button" :disabled="loading" @click="reload">
        <RefreshCw :size="15" aria-hidden="true" />
        刷新
      </button>
    </Teleport>

    <header class="view-header">
      <div class="view-title">
        <p>操作审计、接口错误与每次模型调用的记录</p>
      </div>
      <div class="view-actions">
        <div class="segmented" role="tablist" aria-label="日志类型">
          <button type="button" :class="{ active: kind === 'all' }" @click="switchKind('all')">全部</button>
          <button type="button" :class="{ active: kind === 'operation' }" @click="switchKind('operation')">操作日志</button>
          <button type="button" :class="{ active: kind === 'error' }" @click="switchKind('error')">错误日志</button>
          <button type="button" :class="{ active: kind === 'llm' }" @click="switchKind('llm')">LLM 调用</button>
        </div>
      </div>
    </header>

    <section class="card">
      <div class="card-body" style="padding-top: 8px">
        <div class="cluster" style="padding: 8px 0 12px">
          <div class="input-group" style="flex: 1; max-width: 360px">
            <input v-model="query" class="input" :placeholder="kind === 'llm' ? '按模型 / 用途 / 群 / 错误搜索…' : '按内容 / 操作人 / 群搜索…'" />
          </div>
          <SkeletonBlock v-if="loading && !logs.length" width="48px" height="18px" />
          <span v-else class="muted" style="font-size: 12.5px">{{ countLabel }}</span>
        </div>

        <div v-if="logs.length > 0">
          <article v-for="log in logs" :key="log.id" class="log-row" :class="{ 'log-row-failed': log.action === 'llm_call_failed' }">
            <span class="log-time">{{ formatTime(log.created_at) }}</span>
            <div class="log-main">
              <div v-if="log.actor || log.group_id || log.target" class="cluster" style="gap: 6px; margin-bottom: 2px">
                <span v-if="log.actor" class="muted" style="font-size: 11.5px" :title="actorTitle(log)">{{ actorLabel(log) }}</span>
                <span v-if="log.group_id" class="muted" style="font-size: 11.5px">@ {{ groupLabel(log) }}</span>
                <span v-if="log.target" class="muted mono" style="font-size: 11.5px">→ {{ log.target }}</span>
              </div>
              <template v-if="isLLMCall(log)">
                <p class="log-message">
                  <span class="badge" :class="log.action === 'llm_call_failed' ? 'err' : 'ok'">{{ log.action === 'llm_call_failed' ? (metaFlag(log, 'recovered') ? '失败·已转后备' : '失败') : '成功' }}</span>
                  <span class="mono">{{ metaText(log, 'model') || '未知模型' }}</span>
                  <span v-if="metaText(log, 'provider')" class="muted">{{ metaText(log, 'provider') }}</span>
                  <span class="muted">· {{ usagePurposeLabel(metaText(log, 'purpose')) }}</span>
                </p>
                <p class="log-detail">{{ llmCallSummary(log) }}</p>
                <p v-if="log.detail" class="log-detail log-detail-error">{{ log.detail }}</p>
              </template>
              <template v-else>
                <p class="log-message">{{ log.message }}</p>
                <p v-if="log.detail && log.detail !== log.message" class="log-detail">{{ log.detail }}</p>
              </template>
            </div>
          </article>
          <div v-if="nextCursor" class="log-more">
            <button class="btn" type="button" :disabled="loadingMore" @click="loadMore">{{ loadingMore ? '加载中…' : '加载更早的记录' }}</button>
          </div>
          <p v-else-if="logs.length >= pageSize" class="hint log-more">已经到底了</p>
        </div>
        <EmptyState v-else-if="!loading" :title="query ? '没有匹配的日志' : '暂无日志'" />
        <LoadingSkeleton v-else kind="logs" :count="6" label="正在加载日志" />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";
import { RefreshCw } from "@lucide/vue";
import { listAppLogs, type AppLogEntry, type AppLogTab } from "../api";
import { formatNumber, formatTime } from "../lib/format";
import { usagePurposeLabel } from "../lib/llm-usage";
import { displayChatIdentity } from "../lib/message-display";
import { toastError } from "../lib/toast";
import { recordsActionsHost } from "../lib/records-actions";
import { viewQuery } from "../router";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";

// 页头动作位由 RecordsView 提供；拿不到就说明这一档被单独用在别处，按钮不渲染。
const actionsHost = inject(recordsActionsHost, ref<HTMLElement | null>(null));

type LogTab = AppLogTab;
const kind = ref<LogTab>("all");
const logs = ref<AppLogEntry[]>([]);
const loading = ref(true);
const loadingMore = ref(false);
const nextCursor = ref("");
const query = ref("");
const autoRefresh = ref(false);
// 一页 100 条，往下翻按游标接着取；搜索交给后端，翻到多早都搜得到。
const pageSize = 100;

let timer: number | undefined;
let queryTimer: number | undefined;
// 只认最后一次请求的结果：切页签、改搜索词时上一次请求可能比这一次晚回来。
let requestSeq = 0;

// 日志里的 actor 形如 qq:30001，光一串号码认不出是谁。有群名片先用群名片，
// 没有再用昵称，显示成「名字（账号）」；原样的 actor 和昵称退到悬浮提示里。
function actorLabel(log: AppLogEntry): string {
  const actor = (log.actor ?? "").trim();
  const name = (log.actor_card ?? "").trim() || (log.actor_name ?? "").trim();
  if (!name) return actor;
  return displayChatIdentity(name, actorAccount(actor)) || actor;
}

function actorTitle(log: AppLogEntry): string {
  const nickname = (log.actor_name ?? "").trim();
  const card = (log.actor_card ?? "").trim();
  return nickname && card && nickname !== card ? `${log.actor} · 昵称：${nickname}` : (log.actor ?? "");
}

function groupLabel(log: AppLogEntry): string {
  const groupID = (log.group_id ?? "").trim();
  const name = (log.group_name ?? "").trim();
  return name ? displayChatIdentity(name, groupID) || groupID : `群 ${groupID}`;
}

function actorAccount(actor: string): string {
  const separator = actor.indexOf(":");
  return separator < 0 ? actor : actor.slice(separator + 1).trim();
}

const countLabel = computed(() => `${nextCursor.value ? "已加载 " : ""}${formatNumber(logs.value.length)} 条${nextCursor.value ? "，还有更早的" : ""}`);

function isLLMCall(log: AppLogEntry): boolean {
  return log.action === "llm_usage" || log.action === "llm_call_failed";
}

function metaText(log: AppLogEntry, key: string): string {
  const value = log.metadata?.[key];
  return typeof value === "string" ? value.trim() : "";
}

function metaNumber(log: AppLogEntry, key: string): number {
  const value = log.metadata?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function metaFlag(log: AppLogEntry, key: string): boolean {
  return log.metadata?.[key] === true;
}

// 一行写清这次调用花了多少、多久：成功的给 token 和耗时，失败的只有耗时。
function llmCallSummary(log: AppLogEntry): string {
  const parts: string[] = [];
  if (log.action === "llm_usage") {
    const input = metaNumber(log, "input_tokens");
    const output = metaNumber(log, "output_tokens");
    const cached = metaNumber(log, "cached_input_tokens");
    if (input || output) {
      parts.push(`输入 ${formatNumber(input)}${cached ? `（缓存 ${formatNumber(cached)}）` : ""} · 输出 ${formatNumber(output)} Token`);
    } else {
      parts.push("上游没报用量");
    }
  }
  const duration = metaNumber(log, "duration_ms");
  if (duration > 0) parts.push(`耗时 ${(duration / 1000).toFixed(1)}s`);
  const ttft = metaNumber(log, "ttft_ms");
  if (ttft > 0) parts.push(`首 Token ${(ttft / 1000).toFixed(1)}s`);
  return parts.join(" · ");
}

async function reload(): Promise<void> {
  const seq = ++requestSeq;
  loading.value = true;
  try {
    const response = await listAppLogs(kind.value, pageSize, { query: query.value });
    if (seq !== requestSeq) return;
    logs.value = response.logs;
    nextCursor.value = response.next_cursor ?? "";
  } catch (error) {
    if (seq === requestSeq) toastError(error instanceof Error ? error.message : "加载日志失败");
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

async function loadMore(): Promise<void> {
  if (!nextCursor.value || loadingMore.value) return;
  const seq = requestSeq;
  loadingMore.value = true;
  try {
    const response = await listAppLogs(kind.value, pageSize, { query: query.value, before: nextCursor.value });
    if (seq !== requestSeq) return;
    const seen = new Set(logs.value.map((log) => log.id));
    logs.value = [...logs.value, ...response.logs.filter((log) => !seen.has(log.id))];
    nextCursor.value = response.next_cursor ?? "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载日志失败");
  } finally {
    loadingMore.value = false;
  }
}

// 自动刷新只取最新一页、把新来的插到最前面：已经往下翻过的记录不能被刷新冲掉。
async function refreshHead(): Promise<void> {
  if (logs.value.length <= pageSize) {
    await reload();
    return;
  }
  const seq = requestSeq;
  try {
    const response = await listAppLogs(kind.value, pageSize, { query: query.value });
    if (seq !== requestSeq) return;
    const seen = new Set(logs.value.map((log) => log.id));
    const fresh = response.logs.filter((log) => !seen.has(log.id));
    if (fresh.length) logs.value = [...fresh, ...logs.value];
  } catch {
    // 后台刷新失败不打扰人，下一轮再试。
  }
}

function switchKind(next: LogTab): void {
  if (kind.value !== next) {
    kind.value = next;
    void reload();
  }
}

watch(query, () => {
  if (queryTimer !== undefined) window.clearTimeout(queryTimer);
  queryTimer = window.setTimeout(() => {
    queryTimer = undefined;
    void reload();
  }, 300);
});

function applyAutoRefresh(): void {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
  if (autoRefresh.value) {
    timer = window.setInterval(() => {
      void refreshHead();
    }, 5000);
  }
}

watch(autoRefresh, applyAutoRefresh);

// 别的页面可以带着 ?q= 跳过来（比如浏览器页的「查看全部」），进来就按它过滤。
function seedQueryFromLocation(): void {
  const seeded = viewQuery().get("q");
  if (seeded !== null) query.value = seeded;
}

onMounted(() => {
  seedQueryFromLocation();
  void reload();
});

onActivated(() => {
  seedQueryFromLocation();
  applyAutoRefresh();
});

onDeactivated(() => {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
});

onBeforeUnmount(() => {
  if (timer !== undefined) {
    window.clearInterval(timer);
  }
  if (queryTimer !== undefined) window.clearTimeout(queryTimer);
});
</script>
