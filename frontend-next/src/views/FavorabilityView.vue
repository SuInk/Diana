<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  后台好感度评估的时间线。人员详情里只有某一个人「分数真的变了」的几条；这里是
  所有人的每一次评估，默认只看分数或画像变了的；结果选「全部评估」或具体某一类，
  才能回答「这句话为什么没加分」：判了 0、把握不够、评估失败、排满跳过。
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
        <h2>好感与画像</h2>
        <p>后台每一次好感度与画像评估的结果</p>
      </div>
    </header>

    <section class="card">
      <div class="card-body" style="padding-top: 8px">
        <!-- 「全部评估」回答的是「这句话为什么没加分」：判了 0、把握不够、失败、排满跳过。 -->
        <div class="cluster" style="padding: 8px 0 12px">
          <AppSelect
            class="result-filter"
            :model-value="resultFilter"
            :options="RESULT_OPTIONS"
            aria-label="评估结果"
            @update:model-value="(value) => (resultFilter = value as ResultFilter)"
          />
          <div class="input-group" style="flex: 1; min-width: 160px; max-width: 240px">
            <input v-model="personFilter" class="input" placeholder="QQ 号或昵称" aria-label="按人筛选" />
          </div>
          <div class="input-group" style="flex: 1; min-width: 120px; max-width: 180px">
            <input v-model="groupFilter" class="input" inputmode="numeric" placeholder="群号" aria-label="按群筛选" />
          </div>
          <button v-if="filtersActive" class="btn ghost small" type="button" @click="resetFilters">清除筛选</button>
        </div>
        <div v-if="userFilter" class="cluster" style="padding: 0 0 4px">
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
              <p v-if="item.portrait?.length" class="log-detail">
                记下画像：<template v-for="(trait, index) in item.portrait" :key="trait.field + index"><template v-if="index > 0">；</template>{{ trait.label }} {{ trait.value }}<span v-if="trait.source === 'inferred'" class="muted">（推断）</span></template>
              </p>
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
          :title="filtersActive ? '没有符合筛选条件的评估' : '最近没有好感度或画像变化'"
          :hint="filtersActive ? '换个条件试试，或者清除筛选。' : '结果选「全部评估」可以看到判了 0、把握不够或评估失败的记录。'"
        />
        <LoadingSkeleton v-else kind="logs" :count="6" label="正在加载好感与画像" />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { RefreshCw } from "@lucide/vue";
import { listRelationshipEvaluations, type RelationshipEvaluation, type RelationshipEvaluationStatus } from "../api";
import { botScope } from "../bot-scope";
import { formatTime } from "../format";
import { navigate, viewQuery } from "../router";
import { recordsActionsHost } from "../records-actions";
import { toastError } from "../toast";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";

const PAGE_SIZE = 50;

// 结果筛选。「有变化」包括顶到上下限的（模型要加分却没加上，也是分数这件事上发生的
// 事）和只记下了画像的；后面几项对应单一结果，用来回答「这句话为什么没加分」。
type ResultFilter = "changed" | "all" | "portrait" | RelationshipEvaluationStatus;
const RESULT_OPTIONS: AppSelectOption[] = [
  { value: "changed", label: "有变化", hint: "分数变了或记下了画像", group: "范围" },
  { value: "all", label: "全部评估", hint: "每一次评估都列出来", group: "范围" },
  { value: "portrait", label: "记下画像", hint: "只看记下了画像的", group: "范围" },
  { value: "capped", label: "到上限", hint: "要加减分，但分数已到头", group: "没加分的原因" },
  { value: "unchanged", label: "不变", hint: "模型判断不影响关系", group: "没加分的原因" },
  { value: "low_confidence", label: "把握不够", hint: "想加减分，置信度不到 75%", group: "没加分的原因" },
  { value: "failed", label: "评估失败", hint: "调用出错或返回格式不对", group: "没加分的原因" },
  { value: "skipped", label: "排满跳过", hint: "后台评估排满，这一轮没评", group: "没加分的原因" }
];

const actionsHost = inject(recordsActionsHost, ref<HTMLElement | null>(null));

const evaluations = ref<RelationshipEvaluation[]>([]);
const nextBeforeID = ref(0);
const loading = ref(true);
const loadingMore = ref(false);
const resultFilter = ref<ResultFilter>("changed");
const personFilter = ref("");
const groupFilter = ref("");
const filtersActive = computed(() => resultFilter.value !== "changed" || personFilter.value.trim() !== "" || groupFilter.value.trim() !== "");
// 从人员详情跳过来时只看这一个人。
const userFilter = ref(typeof window === "undefined" ? "" : viewQuery().get("user_id") ?? "");

function query(beforeID = 0) {
  const result = resultFilter.value;
  return {
    profile: botScope.value,
    userID: userFilter.value,
    search: personFilter.value.trim(),
    groupID: groupFilter.value.trim(),
    changedOnly: result === "changed",
    portraitOnly: result === "portrait",
    statuses: result === "changed" || result === "all" || result === "portrait" ? undefined : [result],
    beforeID,
    limit: PAGE_SIZE
  };
}

function resetFilters(): void {
  resultFilter.value = "changed";
  personFilter.value = "";
  groupFilter.value = "";
}

async function reload(): Promise<void> {
  loading.value = true;
  try {
    const response = await listRelationshipEvaluations(query());
    evaluations.value = response.evaluations;
    nextBeforeID.value = response.next_before_id ?? 0;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载好感与画像失败");
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
      return item.portrait?.length ? "记下画像" : "不变";
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
    case "unchanged":
      return item.portrait?.length ? "accent" : "";
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

// 打字时不要每敲一个字就查一次：停手 300ms 再查。结果下拉和机器人切换立刻生效。
let typingTimer: number | undefined;
watch([personFilter, groupFilter], () => {
  window.clearTimeout(typingTimer);
  typingTimer = window.setTimeout(() => void reload(), 300);
});
watch([resultFilter, botScope], () => void reload());
onBeforeUnmount(() => window.clearTimeout(typingTimer));

onMounted(() => {
  void reload();
});
</script>

<style scoped>
.result-filter {
  width: 148px;
  flex: none;
}

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
