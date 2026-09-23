<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  后台好感度评估的时间线。人员详情里只有某一个人「分数真的变了」的几条；这里是
  所有人的每一次评估，默认也只看变了的，打开「显示未变化」才能回答「这句话为什么
  没加分」：判了 0、把握不够、评估失败、排满跳过。
-->
<template>
  <div>
    <Teleport v-if="actionsHost" :to="actionsHost">
      <button class="btn" type="button" :disabled="loading" @click="reload">
        <RefreshCw :size="15" aria-hidden="true" />
        刷新
      </button>
    </Teleport>

    <header class="view-header">
      <div class="view-title">
        <h2>好感变化</h2>
        <p>后台每一次好感度评估的结果</p>
      </div>
      <div class="view-actions">
        <label class="switch">
          <input v-model="showAll" type="checkbox" />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">显示未变化</span>
        </label>
      </div>
    </header>

    <section class="card">
      <div class="card-body" style="padding-top: 8px">
        <div v-if="userFilter" class="cluster" style="padding: 8px 0 4px">
          <span class="badge">只看 {{ userFilter }}</span>
          <button class="btn ghost small" type="button" @click="clearUserFilter">看全部人</button>
        </div>

        <div v-if="evaluations.length > 0">
          <article v-for="item in evaluations" :key="item.id" class="log-row">
            <span class="log-time">{{ formatTime(item.created_at) }}</span>
            <div class="log-main">
              <div class="cluster" style="gap: 8px; margin-bottom: 2px">
                <button type="button" class="link-button" :title="`查看 ${item.user_id} 的人员详情`" @click="openUser(item)">
                  {{ personLabel(item) }}
                </button>
                <span class="badge" :class="statusClass(item)">{{ statusLabel(item) }}</span>
                <span v-if="showScores(item)" class="muted mono" style="font-size: 11.5px">
                  {{ item.before_score }} → {{ item.after_score }}
                </span>
                <span v-if="item.group_id" class="muted" style="font-size: 11.5px">群 {{ item.group_id }}</span>
              </div>
              <p v-if="item.message_text" class="log-message">「{{ item.message_text }}」</p>
              <p v-if="item.reason" class="log-detail">{{ item.reason }}</p>
              <p v-if="item.error" class="log-detail">{{ item.error }}</p>
              <p v-if="metaLine(item)" class="log-detail">{{ metaLine(item) }}</p>
            </div>
          </article>
          <div v-if="nextBeforeID" class="cluster" style="justify-content: center; padding-top: 12px">
            <button class="btn" type="button" :disabled="loadingMore" @click="loadMore">
              {{ loadingMore ? "加载中…" : "加载更早的记录" }}
            </button>
          </div>
        </div>
        <EmptyState
          v-else-if="!loading"
          :title="showAll ? '还没有好感度评估记录' : '最近没有好感度变化'"
          :hint="showAll ? '机器人回复之后才会在后台评估一次。' : '打开「显示未变化」可以看到判了 0、把握不够或评估失败的记录。'"
        />
        <LoadingSkeleton v-else kind="logs" :count="6" label="正在加载好感变化" />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { inject, onMounted, ref, watch } from "vue";
import { RefreshCw } from "@lucide/vue";
import { listRelationshipEvaluations, type RelationshipEvaluation, type RelationshipEvaluationStatus } from "../api";
import { botScope } from "../bot-scope";
import { formatTime } from "../format";
import { navigate, viewQuery } from "../router";
import { recordsActionsHost } from "../records-actions";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";

const PAGE_SIZE = 50;
// 「有变化」包括顶到上下限的：模型要加分却没加上，也是分数这件事上发生的事。
const CHANGED_STATUSES: RelationshipEvaluationStatus[] = ["changed", "capped"];

const actionsHost = inject(recordsActionsHost, ref<HTMLElement | null>(null));

const evaluations = ref<RelationshipEvaluation[]>([]);
const nextBeforeID = ref(0);
const loading = ref(true);
const loadingMore = ref(false);
const showAll = ref(false);
// 从人员详情跳过来时只看这一个人。
const userFilter = ref(typeof window === "undefined" ? "" : viewQuery().get("user_id") ?? "");

function query(beforeID = 0) {
  return {
    profile: botScope.value,
    userID: userFilter.value,
    statuses: showAll.value ? undefined : CHANGED_STATUSES,
    beforeID,
    limit: PAGE_SIZE
  };
}

async function reload(): Promise<void> {
  loading.value = true;
  try {
    const response = await listRelationshipEvaluations(query());
    evaluations.value = response.evaluations;
    nextBeforeID.value = response.next_before_id ?? 0;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载好感变化失败");
  } finally {
    loading.value = false;
  }
}

async function loadMore(): Promise<void> {
  if (!nextBeforeID.value || loadingMore.value) return;
  loadingMore.value = true;
  try {
    const response = await listRelationshipEvaluations(query(nextBeforeID.value));
    evaluations.value = [...evaluations.value, ...response.evaluations];
    nextBeforeID.value = response.next_before_id ?? 0;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载更早的记录失败");
  } finally {
    loadingMore.value = false;
  }
}

function clearUserFilter(): void {
  userFilter.value = "";
  navigate("favorability");
  void reload();
}

function openUser(item: RelationshipEvaluation): void {
  navigate("users", { user: item.user_id, profile: item.bot_profile_id ?? "" });
}

function personLabel(item: RelationshipEvaluation): string {
  const name = (item.sender_name ?? "").trim();
  return name && name !== item.user_id ? `${name}（${item.user_id}）` : item.user_id;
}

function signed(value: number): string {
  return value > 0 ? `+${value}` : String(value);
}

function statusLabel(item: RelationshipEvaluation): string {
  switch (item.status) {
    case "changed":
      return signed(item.applied_delta);
    case "capped":
      return item.applied_delta === 0 ? "已到上限" : `${signed(item.applied_delta)}（到上限）`;
    case "unchanged":
      return "不变";
    case "low_confidence":
      return "把握不够";
    case "failed":
      return "评估失败";
    case "skipped":
      return "排满跳过";
    default:
      return item.status;
  }
}

function statusClass(item: RelationshipEvaluation): string {
  switch (item.status) {
    case "changed":
      return item.applied_delta < 0 ? "err" : "ok";
    case "capped":
    case "low_confidence":
    case "skipped":
      return "warn";
    case "failed":
      return "err";
    default:
      return "";
  }
}

// 排满跳过时根本没读档案，分数是空的，不显示。
function showScores(item: RelationshipEvaluation): boolean {
  return item.status !== "skipped";
}

// 置信度、模型原本给的幅度和用的模型放在最后一行，排查打分偏松偏紧时看。
function metaLine(item: RelationshipEvaluation): string {
  if (item.status === "skipped") return "";
  const parts: string[] = [];
  if (item.status !== "failed") {
    if (item.proposed_delta !== 0 && item.proposed_delta !== item.applied_delta) {
      parts.push(`模型给 ${signed(item.proposed_delta)}`);
    }
    parts.push(`置信度 ${Math.round(item.confidence * 100)}%`);
  }
  if (item.model) parts.push(item.model);
  return parts.join(" · ");
}

watch(showAll, () => void reload());
watch(botScope, () => void reload());

onMounted(() => {
  void reload();
});
</script>

<style scoped>
.link-button {
  padding: 0;
  border: none;
  background: none;
  color: var(--text);
  font: inherit;
  font-weight: 600;
  cursor: pointer;
}

.link-button:hover {
  color: var(--accent);
  text-decoration: underline;
}
</style>
