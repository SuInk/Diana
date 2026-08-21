<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>人员</h1>
        <p>机器人记住的人员画像、长期记忆与好感度</p>
      </div>
      <div class="view-actions">
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
            <input v-model="query" class="input" placeholder="按账号 / 昵称搜索…" @keydown.enter="search" />
          </div>
          <button class="btn ghost small" type="button" :disabled="loading" @click="search">搜索</button>
          <span class="muted" style="font-size: 12.5px">共 {{ total }} 人</span>
        </div>

        <div v-if="users.length > 0">
          <article
            v-for="user in users"
            :key="user.user_id"
            class="log-row user-row"
            role="button"
            tabindex="0"
            :aria-label="`查看 ${user.display_name || user.user_id} 的长期记忆`"
            @click="openDetail(user)"
            @keydown.enter="openDetail(user)"
          >
            <div class="log-main">
              <div class="cluster" style="gap: 6px; margin-bottom: 2px">
                <strong>{{ user.display_name || "（未记录昵称）" }}</strong>
                <span class="muted mono" style="font-size: 11.5px">{{ user.user_id }}</span>
                <span class="badge" :class="favorabilityClass(user.favorability)">好感 {{ user.favorability }}</span>
              </div>
              <p class="log-detail">
                记忆 {{ user.memory_count ?? 0 }} 条 · 消息 {{ formatNumber(user.message_count) }} 条
                <template v-if="user.last_seen_at"> · 最近活跃 {{ formatRelative(user.last_seen_at) }}</template>
              </p>
            </div>
            <ChevronRight :size="16" class="muted" aria-hidden="true" />
          </article>

          <div v-if="hasMore" class="cluster" style="justify-content: center; padding-top: 12px">
            <button class="btn ghost small" type="button" :disabled="loading" @click="loadMore">加载更多</button>
          </div>
        </div>
        <EmptyState
          v-else-if="!loading"
          :title="query ? '没有匹配的人员' : '暂无人员画像'"
          :description="query ? '换个关键词试试。' : '机器人开始和大家聊天后，会在这里沉淀每个人的长期记忆。'"
        />
        <div v-else class="stack">
          <div class="skeleton" style="height: 48px"></div>
          <div class="skeleton" style="height: 48px"></div>
          <div class="skeleton" style="height: 48px"></div>
        </div>
      </div>
    </section>

    <Modal v-if="selected" :title="detailTitle" wide @close="closeDetail">
      <div v-if="detailLoading" class="stack">
        <div class="skeleton" style="height: 48px"></div>
        <div class="skeleton" style="height: 120px"></div>
      </div>
      <div v-else-if="detail" class="stack" style="gap: 16px">
        <div class="cluster" style="gap: 8px">
          <span class="badge" :class="favorabilityClass(detail.profile.favorability)">好感度 {{ detail.profile.favorability }}</span>
          <span class="badge">消息 {{ formatNumber(detail.profile.message_count) }} 条</span>
          <span v-if="detail.profile.last_seen_at" class="muted" style="font-size: 12.5px">
            最近活跃 {{ formatTime(detail.profile.last_seen_at) }}
          </span>
        </div>

        <section>
          <h3 class="detail-section-title">长期记忆（{{ detail.profile.memories?.length ?? 0 }} 条）</h3>
          <div v-if="detail.profile.memories && detail.profile.memories.length > 0" class="stack" style="gap: 8px">
            <article v-for="(memory, index) in detail.profile.memories" :key="index" class="memory-item">
              <p class="memory-text">{{ memory.text }}</p>
              <p class="log-detail">
                <template v-if="memory.at">{{ formatTime(memory.at) }}</template>
                <template v-if="memory.group_id"> · 群 {{ memory.group_id }}</template>
                <template v-if="memory.source"> · {{ memorySourceLabel(memory.source) }}</template>
              </p>
            </article>
          </div>
          <EmptyState v-else title="还没有记忆条目" description="有实质内容的发言会被摘录成长期记忆。" />
        </section>

        <section>
          <h3 class="detail-section-title">好感度变更（最近 {{ detail.favorability_changes.length }} 条）</h3>
          <div v-if="detail.favorability_changes.length > 0" class="stack" style="gap: 8px">
            <article v-for="change in detail.favorability_changes" :key="change.id" class="memory-item">
              <div class="cluster" style="gap: 6px">
                <span class="badge" :class="change.delta >= 0 ? 'ok' : 'err'">
                  {{ change.delta >= 0 ? "+" : "" }}{{ change.delta }}
                </span>
                <span class="mono" style="font-size: 12.5px">{{ change.before_score }} → {{ change.after_score }}</span>
                <span class="muted" style="font-size: 12px">{{ changeSourceLabel(change.source) }}</span>
              </div>
              <p v-if="change.reason" class="memory-text" style="margin-top: 4px">{{ change.reason }}</p>
              <p class="log-detail">
                {{ formatTime(change.created_at) }}
                <template v-if="change.group_id"> · 群 {{ change.group_id }}</template>
                <template v-if="change.operator_id"> · 操作人 {{ change.operator_id }}</template>
              </p>
            </article>
          </div>
          <EmptyState v-else title="暂无好感度变更记录" />
        </section>
      </div>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { ChevronRight, RefreshCw } from "@lucide/vue";
import {
  getAssistantUser,
  listAssistantUsers,
  type AssistantUserDetailResponse,
  type UserMemoryProfile
} from "../api";
import { formatNumber, formatRelative, formatTime } from "../format";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";

const PAGE_SIZE = 50;

const users = ref<UserMemoryProfile[]>([]);
const total = ref(0);
const query = ref("");
const activeQuery = ref("");
const loading = ref(false);
const selected = ref<UserMemoryProfile | null>(null);
const detail = ref<AssistantUserDetailResponse | null>(null);
const detailLoading = ref(false);

const hasMore = computed(() => users.value.length < total.value);
const detailTitle = computed(() => {
  if (!selected.value) return "";
  const name = selected.value.display_name || selected.value.user_id;
  return `${name} 的长期记忆`;
});

async function fetchUsers(offset: number): Promise<void> {
  loading.value = true;
  try {
    const response = await listAssistantUsers(activeQuery.value, PAGE_SIZE, offset);
    users.value = offset === 0 ? response.users : [...users.value, ...response.users];
    total.value = response.total;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载人员列表失败");
  } finally {
    loading.value = false;
  }
}

function reload(): void {
  void fetchUsers(0);
}

function search(): void {
  activeQuery.value = query.value.trim();
  void fetchUsers(0);
}

function loadMore(): void {
  void fetchUsers(users.value.length);
}

async function openDetail(user: UserMemoryProfile): Promise<void> {
  selected.value = user;
  detail.value = null;
  detailLoading.value = true;
  try {
    detail.value = await getAssistantUser(user.user_id);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载人员详情失败");
    selected.value = null;
  } finally {
    detailLoading.value = false;
  }
}

function closeDetail(): void {
  selected.value = null;
  detail.value = null;
}

function favorabilityClass(score: number): string {
  if (score >= 50) return "ok";
  if (score < 0) return "err";
  return "";
}

function memorySourceLabel(source: string): string {
  const labels: Record<string, string> = { group: "群聊", private: "私聊" };
  return labels[source] ?? source;
}

function changeSourceLabel(source: string): string {
  const labels: Record<string, string> = {
    interaction: "日常互动",
    manual: "手动调整",
    owner_set: "主人设置"
  };
  return labels[source] ?? source;
}

onMounted(() => {
  reload();
});
</script>

<style scoped>
.user-row {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.user-row .log-main {
  flex: 1;
}

.detail-section-title {
  font-size: 13px;
  margin: 0 0 8px;
  color: var(--text-secondary);
}

.memory-item {
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 8px 10px;
}

.memory-text {
  margin: 0;
  font-size: 13px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}
</style>
