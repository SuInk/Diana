<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  后台好感度与画像评估的时间线。人员详情里只有某一个人「分数真的变了」的几条；
  这里是所有人的每一次评估。「全部」里也有没加分的那些（判了 0、把握不够、评估
  失败、排满跳过），标签上写着原因，用来回答「这句话为什么没加分」。
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
        <div class="cluster" style="padding: 8px 0 12px">
          <div class="segmented" role="tablist" aria-label="评估类型">
            <button
              v-for="option in KIND_OPTIONS"
              :key="option.value"
              type="button"
              role="tab"
              :aria-selected="kindFilter === option.value"
              :class="{ active: kindFilter === option.value }"
              @click="kindFilter = option.value"
            >
              {{ option.label }}
            </button>
          </div>
          <div class="evaluation-search">
            <Search :size="14" class="evaluation-search-icon" aria-hidden="true" />
            <input
              v-model="searchFilter"
              type="search"
              class="input evaluation-search-input"
              placeholder="搜索人、群、原话、画像…"
              aria-label="搜索评估记录"
            />
          </div>
          <button
            class="btn advanced-toggle"
            :class="{ active: advancedCount > 0 }"
            type="button"
            aria-haspopup="dialog"
            @click="openAdvanced"
          >
            <SlidersHorizontal :size="15" aria-hidden="true" />
            高级筛选<template v-if="advancedCount > 0">（{{ advancedCount }}）</template>
          </button>
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
          :title="filtersActive ? '没有符合筛选条件的评估' : '还没有好感度或画像评估'"
          :hint="filtersActive ? '换个关键词，或者在高级筛选里放宽条件。' : '机器人回复之后才会在后台评估一次。'"
        />
        <LoadingSkeleton v-else kind="logs" :count="6" label="正在加载好感与画像" />
      </div>
    </section>

    <!-- 高级筛选是精确条件：搜索框什么都搜，要限定「就是这个人、就是这个群、就这几天」时
         才用得上。弹窗里改的是草稿，点「应用」才生效，取消不动当前的筛选。 -->
    <Modal v-if="advancedOpen" title="高级筛选" @close="advancedOpen = false">
      <form class="evaluation-advanced" @submit.prevent="applyAdvanced">
        <div class="field">
          <label for="evaluation-person">人</label>
          <input id="evaluation-person" v-model="draft.person" class="input" placeholder="QQ 号或昵称" />
        </div>
        <div class="field">
          <label for="evaluation-group">群号</label>
          <input id="evaluation-group" v-model="draft.group" class="input" inputmode="numeric" placeholder="完整群号" />
        </div>
        <div class="field">
          <label>时间</label>
          <div class="segmented evaluation-range" role="radiogroup" aria-label="时间范围">
            <button
              v-for="option in RANGE_OPTIONS"
              :key="option.value"
              type="button"
              role="radio"
              :aria-checked="draft.range === option.value"
              :class="{ active: draft.range === option.value }"
              @click="draft.range = option.value"
            >
              {{ option.label }}
            </button>
          </div>
        </div>
        <!-- 回车直接应用：表单里没有这个按钮，浏览器不会把回车当提交。 -->
        <button type="submit" hidden aria-hidden="true" tabindex="-1"></button>
      </form>
      <template #footer>
        <button class="btn ghost evaluation-reset" type="button" :disabled="!draftActive" @click="resetDraft">重置</button>
        <button class="btn ghost" type="button" @click="advancedOpen = false">取消</button>
        <button class="btn primary" type="button" @click="applyAdvanced">应用</button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { RefreshCw, Search, SlidersHorizontal } from "@lucide/vue";
import { listRelationshipEvaluations, type RelationshipEvaluation, type RelationshipEvaluationStatus } from "../api";
import { botScope } from "../bot-scope";
import { formatTime } from "../format";
import { navigate, viewQuery } from "../router";
import { recordsActionsHost } from "../records-actions";
import { toastError } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import Modal from "../components/Modal.vue";

const PAGE_SIZE = 50;

// 类型筛选。好感度变化只算分数真的动了的；到上限、把握不够这些没动的留在「全部」里，
// 标签上写着原因。
type KindFilter = "all" | "favorability" | "portrait";
const KIND_OPTIONS: { value: KindFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "favorability", label: "好感度变化" },
  { value: "portrait", label: "画像生成" }
];

const actionsHost = inject(recordsActionsHost, ref<HTMLElement | null>(null));

const evaluations = ref<RelationshipEvaluation[]>([]);
const nextBeforeID = ref(0);
const loading = ref(true);
const loadingMore = ref(false);
const kindFilter = ref<KindFilter>("all");
// 时间范围按天数算起点，0 表示不限。
type RangeFilter = 0 | 1 | 7 | 30;
const RANGE_OPTIONS: { value: RangeFilter; label: string }[] = [
  { value: 0, label: "全部时间" },
  { value: 1, label: "今天" },
  { value: 7, label: "近 7 天" },
  { value: 30, label: "近 30 天" }
];

const searchFilter = ref("");
const advancedOpen = ref(false);
const draft = reactive<{ person: string; group: string; range: RangeFilter }>({ person: "", group: "", range: 0 });
const draftActive = computed(() => draft.person.trim() !== "" || draft.group.trim() !== "" || draft.range !== 0);
const personFilter = ref("");
const groupFilter = ref("");
const rangeFilter = ref<RangeFilter>(0);
const advancedCount = computed(
  () => [personFilter.value.trim() !== "", groupFilter.value.trim() !== "", rangeFilter.value !== 0].filter(Boolean).length
);
const filtersActive = computed(() => kindFilter.value !== "all" || searchFilter.value.trim() !== "" || advancedCount.value > 0);

// 「今天」从本地零点算，其余按整天往前推。
function rangeSince(days: RangeFilter): number {
  if (days === 0) return 0;
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  start.setDate(start.getDate() - (days - 1));
  return Math.floor(start.getTime() / 1000);
}
// 从人员详情跳过来时只看这一个人。
const userFilter = ref(typeof window === "undefined" ? "" : viewQuery().get("user_id") ?? "");

function query(beforeID = 0) {
  const kind = kindFilter.value;
  return {
    profile: botScope.value,
    userID: userFilter.value,
    search: searchFilter.value.trim(),
    person: personFilter.value.trim(),
    groupID: groupFilter.value.trim(),
    since: rangeSince(rangeFilter.value),
    statuses: kind === "favorability" ? (["changed"] as RelationshipEvaluationStatus[]) : undefined,
    portraitOnly: kind === "portrait",
    beforeID,
    limit: PAGE_SIZE
  };
}

function openAdvanced(): void {
  draft.person = personFilter.value;
  draft.group = groupFilter.value;
  draft.range = rangeFilter.value;
  advancedOpen.value = true;
}

function resetDraft(): void {
  draft.person = "";
  draft.group = "";
  draft.range = 0;
}

// 三个条件一次性生效，只查一次。
function applyAdvanced(): void {
  const changed = personFilter.value !== draft.person.trim() || groupFilter.value !== draft.group.trim() || rangeFilter.value !== draft.range;
  personFilter.value = draft.person.trim();
  groupFilter.value = draft.group.trim();
  rangeFilter.value = draft.range;
  advancedOpen.value = false;
  if (changed) void reload();
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
watch(searchFilter, () => {
  window.clearTimeout(typingTimer);
  typingTimer = window.setTimeout(() => void reload(), 300);
});
watch([kindFilter, botScope], () => void reload());
onBeforeUnmount(() => window.clearTimeout(typingTimer));

onMounted(() => {
  void reload();
});
</script>

<style scoped>
.evaluation-search {
  position: relative;
  flex: 1 1 200px;
  min-width: 0;
}

.evaluation-search-icon {
  position: absolute;
  top: 50%;
  left: 10px;
  color: var(--muted);
  transform: translateY(-50%);
  pointer-events: none;
}

.evaluation-search-input {
  width: 100%;
  padding-left: 30px;
}

/* 展开着或者有条件生效时高亮，收起后也看得出还有筛选挂着。 */
.advanced-toggle.active {
  border-color: var(--accent);
  color: var(--accent);
}

.evaluation-advanced {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.evaluation-range button {
  white-space: nowrap;
}

/* 重置靠左，和取消、应用分开：它清的是弹窗里的草稿，不是关掉弹窗。 */
.evaluation-reset {
  margin-right: auto;
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
