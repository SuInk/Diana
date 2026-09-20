<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="draft-section">
    <header class="draft-heading">
      <div>
        <h3>Issue 草稿</h3>
        <p>待审批草稿 7 天后自动过期，群里的确认码随之失效，再过 30 天自动删除。后台可以直接提交、修改内容、还原过期或已取消的草稿（还原会换一个新确认码），也可以删除记录——删除只删这里的记录，已经写进 GitHub 的 Issue 不受影响。</p>
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
    <p v-if="notice" class="draft-notice">{{ notice }}</p>
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
          <div class="draft-item-actions">
            <span class="badge" :class="badgeClass(draft)">{{ draftStatusLabel(draft) }}</span>
            <button v-if="draft.status === 'pending'" class="btn small primary" type="button" :disabled="busyID === draft.id" @click="publish(draft)">
              提交
            </button>
            <button v-if="draft.status !== 'created'" class="btn small ghost" type="button" :disabled="busyID === draft.id" @click="startEdit(draft)">
              修改
            </button>
            <button v-if="isExpired(draft) || draft.status === 'cancelled'" class="btn small ghost" type="button" :disabled="busyID === draft.id" @click="restore(draft)">
              还原
            </button>
            <button class="btn small ghost danger" type="button" :disabled="busyID === draft.id" @click="remove(draft)">
              删除
            </button>
          </div>
        </div>
        <div class="draft-meta">
          <span>提出人：{{ draft.requester_name || draft.requester_id }}</span>
          <span v-if="draft.status === 'pending' && draft.expires_at">
            {{ isExpired(draft) ? "已于" : "有效期至" }} {{ formatDate(draft.expires_at) }}{{ isExpired(draft) ? " 过期，30 天后删除" : "" }}
          </span>
          <span v-if="draft.requester_name" class="mono">{{ draft.requester_id }}</span>
          <span>群：<span class="mono">{{ draft.group_id }}</span></span>
          <time :datetime="draft.created_at">{{ formatDate(draft.created_at) }}</time>
        </div>
        <form v-if="editingID === draft.id" class="draft-edit" @submit.prevent="saveEdit(draft)">
          <label :for="`draft-title-${draft.id}`">标题</label>
          <input :id="`draft-title-${draft.id}`" v-model="editForm.title" class="input" />
          <label :for="`draft-body-${draft.id}`">正文</label>
          <textarea :id="`draft-body-${draft.id}`" v-model="editForm.body" class="input" rows="6"></textarea>
          <label :for="`draft-labels-${draft.id}`">标签（逗号分隔）</label>
          <input :id="`draft-labels-${draft.id}`" v-model="editForm.labels" class="input" placeholder="enhancement, webui" />
          <div class="draft-edit-actions">
            <button class="btn small primary" type="submit" :disabled="busyID === draft.id">保存</button>
            <button class="btn small ghost" type="button" @click="editingID = ''">取消</button>
          </div>
        </form>
        <p v-else-if="draft.input.body" class="draft-body">{{ draft.input.body }}</p>
        <div v-if="draft.input.labels?.length" class="draft-labels">
          <span v-for="label in draft.input.labels" :key="label" class="badge">{{ label }}</span>
        </div>
        <a v-if="draft.issue_url" class="draft-issue-link" :href="draft.issue_url" target="_blank" rel="noreferrer">#{{ draft.issue_number }} 查看 Issue</a>
        <span class="draft-id mono">
          草稿 {{ draft.id }}
          <template v-if="draft.status === 'pending' && !isExpired(draft)"> · 确认码 {{ confirmationCode(draft) }}</template>
        </span>
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { RefreshCw } from "@lucide/vue";
import {
  deleteRepositoryIssueDraft,
  editRepositoryIssueDraft,
  listRepositoryIssueDrafts,
  publishRepositoryIssueDraft,
  restoreRepositoryIssueDraft,
  type RepositoryIssueDraft
} from "../api";

const filters = [
  { value: "pending", label: "待审批" },
  { value: "expired", label: "已过期" },
  { value: "created", label: "已创建" },
  { value: "cancelled", label: "已取消" },
  { value: "all", label: "全部" }
] as const;
const status = ref<(typeof filters)[number]["value"]>("pending");
const drafts = ref<RepositoryIssueDraft[]>([]);
const loading = ref(false);
const error = ref("");
const busyID = ref("");
const notice = ref("");
const editingID = ref("");
const editForm = ref({ title: "", body: "", labels: "" });

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
// 过期不是后端存的状态，而是待审批草稿过了 expires_at；列表按「全部」查时两种都在。
function isExpired(draft: RepositoryIssueDraft): boolean {
  if (draft.status !== "pending" || !draft.expires_at) return false;
  const expiry = new Date(draft.expires_at).getTime();
  return !Number.isNaN(expiry) && expiry < Date.now();
}
function draftStatusLabel(draft: RepositoryIssueDraft): string {
  return isExpired(draft) ? "已过期" : statusLabel(draft.status);
}
// 历史草稿没有 confirmation_code，确认码就是草稿 ID 的前六位。
function confirmationCode(draft: RepositoryIssueDraft): string {
  return draft.confirmation_code || draft.id.slice(0, 6);
}
function badgeClass(draft: RepositoryIssueDraft): string {
  return draft.status === "pending" && !isExpired(draft) ? "accent" : "";
}
// 后台提交不走确认码：调用者已经登录过控制台，本身就是审批人。
async function publish(draft: RepositoryIssueDraft): Promise<void> {
  if (!window.confirm(`确认直接写入 GitHub？\n\n仓库：${draft.repository}\n标题：${draft.input.title || "未命名草稿"}`)) return;
  await run(draft, "提交草稿失败", async () => {
    const result = await publishRepositoryIssueDraft(draft.id);
    notice.value = result.issue?.url ? `已提交：#${result.issue.number} ${result.issue.url}` : result.message || "已提交。";
  });
}
async function restore(draft: RepositoryIssueDraft): Promise<void> {
  await run(draft, "还原草稿失败", async () => {
    const restored = (await restoreRepositoryIssueDraft(draft.id)).draft;
    notice.value = `已还原，新的确认码是 ${confirmationCode(restored)}；旧码已失效。`;
  });
}
async function remove(draft: RepositoryIssueDraft): Promise<void> {
  const extra = draft.issue_url ? "\n\n只删这里的记录，GitHub 上的 Issue 不受影响。" : "";
  if (!window.confirm(`删除这条草稿记录？\n\n${draft.input.title || "未命名草稿"}${extra}`)) return;
  await run(draft, "删除草稿失败", async () => {
    await deleteRepositoryIssueDraft(draft.id);
    notice.value = "草稿记录已删除。";
  });
}
function startEdit(draft: RepositoryIssueDraft): void {
  editingID.value = draft.id;
  error.value = "";
  notice.value = "";
  editForm.value = {
    title: draft.input.title ?? "",
    body: draft.input.body ?? "",
    labels: (draft.input.labels ?? []).join(", ")
  };
}
async function saveEdit(draft: RepositoryIssueDraft): Promise<void> {
  const labels = editForm.value.labels.split(",").map((item) => item.trim()).filter(Boolean);
  await run(draft, "修改草稿失败", async () => {
    await editRepositoryIssueDraft(draft.id, { title: editForm.value.title, body: editForm.value.body, labels });
    editingID.value = "";
    notice.value = "草稿已修改。";
  });
}
async function run(draft: RepositoryIssueDraft, failure: string, task: () => Promise<void>): Promise<void> {
  busyID.value = draft.id;
  error.value = "";
  notice.value = "";
  try {
    await task();
    await load();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : failure;
  } finally {
    busyID.value = "";
  }
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
.draft-item-actions { display: flex; flex-direction: row; align-items: center; gap: 6px; flex-shrink: 0; flex-wrap: wrap; justify-content: flex-end; }
.draft-item-actions .danger { color: var(--err); }
.draft-edit { display: flex; flex-direction: column; gap: 6px; }
.draft-edit label { color: var(--muted); font-size: 12px; }
.draft-edit textarea { resize: vertical; font-family: inherit; line-height: 1.6; }
.draft-edit-actions { display: flex; gap: 8px; margin-top: 4px; }
.draft-item-head strong { overflow-wrap: anywhere; }
.draft-item-head .mono { color: var(--muted); font-size: 12px; }
.draft-meta { display: flex; flex-wrap: wrap; gap: 5px 14px; color: var(--muted); font-size: 12px; }
.draft-body { margin: 0; padding: 10px; white-space: pre-wrap; overflow-wrap: anywhere; border-left: 2px solid var(--border-strong); background: var(--surface); font-size: 13px; line-height: 1.6; }
.draft-labels { display: flex; flex-wrap: wrap; gap: 6px; }
.draft-issue-link { align-self: flex-start; color: var(--accent); font-size: 12px; }
.draft-id { color: var(--muted); font-size: 11px; }
.draft-empty, .draft-error, .draft-notice { margin: 0; color: var(--muted); font-size: 12px; }
.draft-notice { color: var(--accent); }
.draft-error { color: var(--err); }
.draft-skeleton { height: 92px; }
.spinning { animation: spin .8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
