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
            :aria-label="`查看 ${user.display_name || user.user_id} 的画像与长期记忆`"
            @click="openDetail(user)"
            @keydown.enter="openDetail(user)"
          >
            <div class="log-main">
              <div class="cluster" style="gap: 6px; margin-bottom: 2px">
                <strong>{{ user.display_name || "（未记录昵称）" }}</strong>
                <span class="muted mono" style="font-size: 11.5px">{{ user.user_id }}</span>
                <span class="badge" :class="favorabilityClass(user.favorability)">好感 {{ user.favorability }}</span>
                <span v-if="user.romance?.active" class="badge accent">恋人</span>
              </div>
              <p class="log-detail">
                画像 {{ user.portrait_count ?? 0 }} 条 · 长期记忆 {{ user.structured_memory_count ?? 0 }} 条 · 消息 {{ formatNumber(user.message_count) }} 条
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
          <span v-if="detail.profile.romance?.active" class="badge accent" :title="detail.profile.romance.since ? `确立于 ${formatTime(detail.profile.romance.since)}` : undefined">恋人</span>
          <span class="badge">消息 {{ formatNumber(detail.profile.message_count) }} 条</span>
          <span v-if="detail.profile.last_seen_at" class="muted" style="font-size: 12.5px">
            最近活跃 {{ formatTime(detail.profile.last_seen_at) }}
          </span>
        </div>

        <section>
          <h3 class="detail-section-title">人员画像（{{ detail.profile.portrait?.length ?? 0 }} 条）</h3>
          <div v-if="portraitGroups.length > 0" class="portrait-table">
            <div v-for="group in portraitGroups" :key="group.field" class="portrait-row">
              <span class="portrait-label">{{ group.label }}</span>
              <div class="portrait-values">
                <span v-for="(trait, index) in group.traits" :key="index" class="portrait-value">
                  {{ trait.value }}
                  <span v-if="trait.source === 'inferred'" class="badge" title="机器人根据聊天推断，不是本人明说">推断</span>
                </span>
              </div>
            </div>
          </div>
          <EmptyState
            v-else
            title="还没有画像"
            description="聊天里提到居住地点、职业、作息、生活习惯这类长期情况时，会被记进这里。"
          />
        </section>

        <section>
          <h3 class="detail-section-title">长期记忆（{{ structuredMemories.length }} 条）</h3>
          <div v-if="structuredMemories.length > 0" class="stack" style="gap: 8px">
            <article v-for="memory in structuredMemories" :key="memory.id" class="memory-item">
              <div class="cluster" style="gap: 6px">
                <span class="badge">{{ memoryKindLabel(memory.kind) }}</span>
                <strong style="font-size: 13px">{{ memory.topic || memory.entity || "未命名记忆" }}</strong>
                <span v-if="memory.sensitive" class="badge warn">敏感</span>
                <span v-if="memory.source_type === 'inferred'" class="badge" title="机器人根据聊天推断，不是本人明说">推断</span>
              </div>
              <p class="memory-text" style="margin-top: 4px">{{ memory.content }}</p>
              <p class="log-detail">
                置信 {{ formatScore(memory.confidence) }} · 重要度 {{ formatScore(memory.importance) }}
                <template v-if="memory.source_group_id"> · 群 {{ memory.source_group_id }}</template>
                <template v-if="memory.source_event_time"> · {{ formatTime(memory.source_event_time) }}</template>
                <template v-if="memory.expires_at"> · {{ formatTime(memory.expires_at) }} 过期</template>
              </p>
            </article>
          </div>
          <EmptyState
            v-else
            title="还没有长期记忆"
            description="聊天里出现值得长期记住的事实、偏好或长期要求时，才会被单独提炼成一条记进这里；普通问答和寒暄不记。"
          />
        </section>

        <section v-if="detail.profile.memories && detail.profile.memories.length > 0">
          <h3 class="detail-section-title">
            <button
              class="recent-toggle"
              type="button"
              :aria-expanded="recentOpen ? 'true' : 'false'"
              @click="recentOpen = !recentOpen"
            >
              <ChevronDown :size="13" :class="{ 'recent-chevron-open': recentOpen }" aria-hidden="true" />
              最近发言（{{ detail.profile.memories.length }} 条）
            </button>
          </h3>
          <template v-if="recentOpen">
            <p class="muted" style="font-size: 12px; margin: 0 0 8px">
              这个人最近说过的话，只留最近 20 条，@ 和引用已还原成昵称；不进模型上下文，只用于排查。
            </p>
            <div class="stack" style="gap: 8px">
              <article v-for="(memory, index) in detail.profile.memories" :key="index" class="memory-item">
                <p class="memory-text">{{ memory.text }}</p>
                <p class="log-detail">
                  <template v-if="memory.at">{{ formatTime(memory.at) }}</template>
                  <template v-if="memory.group_id"> · 群 {{ memory.group_id }}</template>
                  <template v-if="memory.source"> · {{ memorySourceLabel(memory.source) }}</template>
                </p>
              </article>
            </div>
          </template>
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
import { computed, onMounted, ref, watch } from "vue";
import { botScope } from "../bot-scope";
import { ChevronDown, ChevronRight, RefreshCw } from "@lucide/vue";
import {
  getAssistantUser,
  listAssistantUsers,
  type AssistantUserDetailResponse,
  type UserMemoryProfile,
  type UserPortraitTrait,
  type UserStructuredMemory
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
// 最近发言默认收起：它是排查用的原始缓冲，展开后会把画像和长期记忆挤出屏幕。
const recentOpen = ref(false);

const structuredMemories = computed<UserStructuredMemory[]>(() => detail.value?.structured_memories ?? []);

const hasMore = computed(() => users.value.length < total.value);

// 画像按后端给的栏目顺序排，空栏不显示：一整列「暂无」比没有更难读。
const portraitGroups = computed<{ field: string; label: string; traits: UserPortraitTrait[] }[]>(() => {
  const traits = detail.value?.profile.portrait ?? [];
  if (traits.length === 0) return [];
  const specs = detail.value?.portrait_fields ?? [];
  const order = specs.length > 0 ? specs : traits.map((trait) => ({ field: trait.field, label: trait.label }));
  const groups: { field: string; label: string; traits: UserPortraitTrait[] }[] = [];
  for (const spec of order) {
    const matched = traits.filter((trait) => trait.field === spec.field);
    if (matched.length > 0) {
      groups.push({ field: spec.field, label: spec.label, traits: matched });
    }
  }
  return groups;
});
const detailTitle = computed(() => {
  if (!selected.value) return "";
  const name = selected.value.display_name || selected.value.user_id;
  return `${name} 的画像与记忆`;
});

async function fetchUsers(offset: number): Promise<void> {
  loading.value = true;
  try {
    const response = await listAssistantUsers(activeQuery.value, PAGE_SIZE, offset, botScope.value);
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
  recentOpen.value = false;
  detailLoading.value = true;
  try {
    detail.value = await getAssistantUser(user.user_id, botScope.value);
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

function memoryKindLabel(kind: string): string {
  const labels: Record<string, string> = {
    fact: "事实",
    preference: "偏好",
    episode: "情景",
    instruction: "长期要求",
    summary: "摘要",
    thread: "会话状态"
  };
  return labels[kind] ?? (kind || "记忆");
}

function formatScore(value: number): string {
  return `${Math.round((value ?? 0) * 100)}%`;
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

// 换了机器人，画像和好感度都是另一份，列表和详情一起重置。
watch(botScope, () => {
  detail.value = null;
  reload();
});

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

.portrait-table {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.portrait-row {
  display: flex;
  gap: 12px;
  align-items: baseline;
}

.portrait-label {
  flex: 0 0 76px;
  font-size: 12.5px;
  color: var(--text-secondary);
}

.portrait-values {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  font-size: 13px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.portrait-value {
  display: inline-flex;
  align-items: center;
  gap: 4px;
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

.recent-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  cursor: pointer;
}

.recent-chevron-open {
  transform: rotate(180deg);
}
</style>
