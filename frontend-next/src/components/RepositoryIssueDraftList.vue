<template>
  <section class="draft-section">
    <header class="draft-heading">
      <div>
        <h3>Issue 草稿</h3>
        <p>草稿不会自动过期，创建或取消后仍保留记录。</p>
      </div>
      <button class="btn small ghost icon-only" type="button" title="刷新草稿" aria-label="刷新草稿" :disabled="loading" @click="load">
        <RefreshCw :size="15" :class="{ spinning: loading }" aria-hidden="true" />
      </button>
    </header>
    <div class="segmented draft-filters" role="tablist" aria-label="草稿状态">
      <button v-for="option in filters" :key="option.value" type="button" role="tab" :aria-selected="status === option.value" :class="{ active: status === option.value }" @click="status = option.value; load()">
        {{ option.label }}
      </button>
    </div>
    <div v-if="loading && !drafts.length" class="skeleton draft-skeleton"></div>
    <p v-else-if="error" class="draft-error">{{ error }}</p>
    <p v-else-if="!drafts.length" class="draft-empty">没有符合条件的草稿</p>
    <div v-else class="draft-list">
      <article v-for="draft in drafts" :key="draft.id" class="draft-item">
        <div class="draft-item-head">
          <div>
            <strong>{{ draft.input.title || "未命名草稿" }}</strong>
            <span class="mono">{{ draft.repository }}</span>
          </div>
          <span class="badge" :class="draft.status === 'pending' ? 'accent' : ''">{{ statusLabel(draft.status) }}</span>
        </div>
        <div class="draft-meta">
          <span>提出人：{{ draft.requester_name || draft.requester_id }}</span>
          <span v-if="draft.requester_name" class="mono">{{ draft.requester_id }}</span>
          <span>群：<span class="mono">{{ draft.group_id }}</span></span>
          <time :datetime="draft.created_at">{{ formatDate(draft.created_at) }}</time>
        </div>
        <p v-if="draft.input.body" class="draft-body">{{ draft.input.body }}</p>
        <div v-if="draft.input.labels?.length" class="draft-labels">
          <span v-for="label in draft.input.labels" :key="label" class="badge">{{ label }}</span>
        </div>
        <a v-if="draft.issue_url" class="draft-issue-link" :href="draft.issue_url" target="_blank" rel="noreferrer">#{{ draft.issue_number }} 查看 Issue</a>
        <span class="draft-id mono">草稿 {{ draft.id }}</span>
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { RefreshCw } from "@lucide/vue";
import { listRepositoryIssueDrafts, type RepositoryIssueDraft } from "../api";

const filters = [
  { value: "pending", label: "待审批" },
  { value: "created", label: "已创建" },
  { value: "cancelled", label: "已取消" },
  { value: "all", label: "全部" }
] as const;
const status = ref<(typeof filters)[number]["value"]>("pending");
const drafts = ref<RepositoryIssueDraft[]>([]);
const loading = ref(false);
const error = ref("");

async function load(): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    drafts.value = (await listRepositoryIssueDrafts(status.value)).drafts ?? [];
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : "读取草稿失败";
  } finally {
    loading.value = false;
  }
}
function statusLabel(value: RepositoryIssueDraft["status"]): string {
  return value === "pending" ? "待审批" : value === "created" ? "已创建" : "已取消";
}
function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN", { hour12: false });
}
onMounted(load);
</script>

<style scoped>
.draft-section { display: flex; flex-direction: column; gap: 12px; padding-top: 18px; border-top: 1px solid var(--border); }
.draft-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.draft-heading h3 { margin: 0; font-size: 15px; }
.draft-heading p { margin: 4px 0 0; color: var(--muted); font-size: 12px; }
.draft-filters { align-self: flex-start; }
.draft-list { display: flex; flex-direction: column; gap: 10px; }
.draft-item { display: flex; flex-direction: column; gap: 9px; padding: 14px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-2); }
.draft-item-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.draft-item-head > div { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.draft-item-head strong { overflow-wrap: anywhere; }
.draft-item-head .mono { color: var(--muted); font-size: 12px; }
.draft-meta { display: flex; flex-wrap: wrap; gap: 5px 14px; color: var(--muted); font-size: 12px; }
.draft-body { margin: 0; padding: 10px; white-space: pre-wrap; overflow-wrap: anywhere; border-left: 2px solid var(--border-strong); background: var(--surface); font-size: 13px; line-height: 1.6; }
.draft-labels { display: flex; flex-wrap: wrap; gap: 6px; }
.draft-issue-link { align-self: flex-start; color: var(--accent); font-size: 12px; }
.draft-id { color: var(--muted); font-size: 11px; }
.draft-empty, .draft-error { margin: 0; color: var(--muted); font-size: 12px; }
.draft-error { color: var(--err); }
.draft-skeleton { height: 92px; }
.spinning { animation: spin .8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
