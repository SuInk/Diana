<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 按分区列：每个区的清理规则不一样（长期保存区不清、下载 7 天、回收站再过几天真删），
       混成一张大表就看不出「这个文件还能活多久」。 -->
  <section class="card">
    <div class="card-header" style="justify-content: flex-end">
      <button class="btn small ghost" type="button" :disabled="loading" title="重新读取工作目录" aria-label="重新读取工作目录" @click="load">
        <RefreshCw :size="14" aria-hidden="true" />
      </button>
    </div>
    <div class="card-body">
      <p v-if="loadError" class="error" role="alert">{{ loadError }}</p>
      <LoadingSkeleton v-if="!files && !loadError" kind="users" :count="4" label="正在读取工作目录" />
      <template v-else-if="files">
        <EmptyState v-if="empty" title="工作目录还是空的" hint="Agent 下载、生成或长期保存文件后会出现在这里">
          <template #icon><FolderOpen :size="20" aria-hidden="true" /></template>
        </EmptyState>
        <template v-else>
          <div v-for="area in areas" :key="`${area.key}:${area.bot_id ?? ''}`" class="ws-group">
            <div class="ws-group-head">
              <div class="ws-group-title">
                <h3>{{ area.label }}</h3>
                <template v-if="area.key === 'keep'">
                  <span class="ws-bot">{{ area.bot_name || area.bot_id || "未知机器人" }}</span>
                  <code v-if="area.bot_id && area.bot_name" class="mono muted ws-bot-id">{{ area.bot_id }}</code>
                </template>
              </div>
              <span class="ws-group-meta muted">{{ formatBytes(area.bytes) }} · {{ formatNumber(area.files) }} 个文件</span>
              <button
                v-if="area.key === 'trash' && area.files > 0"
                class="btn small danger"
                type="button"
                :disabled="busy !== ''"
                @click="emptyTrash(area)"
              >
                <Trash2 :size="14" aria-hidden="true" />
                {{ busy === trashBusyKey ? "清空中…" : "清空回收站" }}
              </button>
            </div>
            <p class="hint ws-retention">
              {{ area.retention }}<template v-if="area.path">　<code class="mono">{{ area.path }}</code></template>
            </p>
            <!-- 长期保存区有配额：写满之后 Agent 再存会被拒，提前看得到快满了。 -->
            <div v-if="quotaPercent(area) !== null" class="ws-quota">
              <div class="ws-quota-bar" role="img" :aria-label="`已用 ${formatBytes(area.bytes)}，配额 ${formatBytes(area.quota_bytes)}`">
                <span :class="{ full: (quotaPercent(area) ?? 0) >= 90 }" :style="{ width: `${quotaPercent(area)}%` }"></span>
              </div>
              <span class="muted">{{ formatBytes(area.bytes) }} / {{ formatBytes(area.quota_bytes) }}</span>
            </div>
            <ul v-if="area.entries.length > 0" class="ws-list">
              <li v-for="entry in area.entries" :key="entry.path" class="ws-entry">
                <div class="ws-entry-main">
                  <span class="ws-entry-path mono" :title="entry.path">{{ workspaceEntryLabel(area, entry) }}</span>
                  <span v-if="entry.description" class="ws-entry-desc">{{ entry.description }}</span>
                </div>
                <span class="ws-entry-size">{{ formatBytes(entry.size) }}</span>
                <span class="ws-entry-time muted">{{ formatTime(entry.modified) }}</span>
                <div class="ws-entry-actions">
                  <a v-if="!entry.is_dir" class="btn small ghost" :href="workspaceDownloadURL(entry.path)" download>
                    <Download :size="14" aria-hidden="true" />
                    下载
                  </a>
                  <button
                    v-if="workspaceCanDelete(area)"
                    class="btn small ghost danger"
                    type="button"
                    :disabled="busy !== ''"
                    @click="removeEntry(entry)"
                  >
                    {{ busy === entry.path ? "删除中…" : "删除" }}
                  </button>
                </div>
              </li>
            </ul>
            <p v-else class="hint ws-empty">这里没有文件。</p>
            <p v-if="area.truncated" class="hint">只列出最近的 {{ area.entries.length }} 个，大小和数量是全部文件的合计。</p>
          </div>

          <div v-if="files.loose.length > 0" class="ws-group">
            <div class="ws-group-head">
              <div class="ws-group-title"><h3>散落文件</h3></div>
              <span class="ws-group-meta muted">{{ formatNumber(files.loose.length) }} 个</span>
            </div>
            <p class="hint ws-retention">工作目录根下的散落文件不会自动清理，建议挪进 downloads/ 或 outputs/。</p>
            <ul class="ws-list">
              <li v-for="entry in files.loose" :key="entry.path" class="ws-entry">
                <div class="ws-entry-main">
                  <span class="ws-entry-path mono" :title="entry.path">{{ entry.path }}</span>
                </div>
                <span class="ws-entry-size">{{ formatBytes(entry.size) }}</span>
                <span class="ws-entry-time muted">{{ formatTime(entry.modified) }}</span>
                <div class="ws-entry-actions">
                  <a v-if="!entry.is_dir" class="btn small ghost" :href="workspaceDownloadURL(entry.path)" download>
                    <Download :size="14" aria-hidden="true" />
                    下载
                  </a>
                  <button class="btn small ghost danger" type="button" :disabled="busy !== ''" @click="removeEntry(entry)">
                    {{ busy === entry.path ? "删除中…" : "删除" }}
                  </button>
                </div>
              </li>
            </ul>
          </div>

          <!-- 编码工作区里可能是还没推上去的改动，这里只提示，删不删交给人去看过再决定。 -->
          <div v-if="files.orphan_coding.length > 0" class="ws-group">
            <div class="ws-group-head">
              <div class="ws-group-title"><h3>闲置的编码工作区</h3></div>
              <span class="ws-group-meta muted">{{ formatNumber(files.orphan_coding.length) }} 个</span>
            </div>
            <p class="hint ws-retention">长期未动、也没有机器人配置引用的编码工作区，只提示不自动删除。</p>
            <ul class="ws-list">
              <li v-for="entry in files.orphan_coding" :key="entry.path" class="ws-entry">
                <div class="ws-entry-main">
                  <span class="ws-entry-path mono" :title="entry.path">{{ entry.path }}</span>
                </div>
                <span class="ws-entry-size">{{ formatBytes(entry.size) }}</span>
                <span class="ws-entry-time muted">{{ formatTime(entry.modified) }}</span>
                <div class="ws-entry-actions"></div>
              </li>
            </ul>
          </div>
        </template>

        <p class="hint ws-foot">
          工作目录 <code class="mono">{{ files.root }}</code>。
          <template v-if="files.collected_at">读取于 {{ formatTime(files.collected_at) }}。</template>
          删除的文件先进回收站，回收站到期或手动清空后才真正删掉。
        </p>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Download, FolderOpen, RefreshCw, Trash2 } from "@lucide/vue";
import EmptyState from "./EmptyState.vue";
import LoadingSkeleton from "./LoadingSkeleton.vue";
import {
  deleteWorkspaceFile,
  emptyWorkspaceTrash,
  getWorkspaceFiles,
  type WorkspaceArea,
  type WorkspaceFileEntry,
  type WorkspaceFilesResponse
} from "../api";
import { askConfirm } from "../confirm";
import { formatBytes, formatNumber, formatTime } from "../format";
import { toastError, toastSuccess } from "../toast";
import {
  sortWorkspaceAreas,
  workspaceCanDelete,
  workspaceDownloadURL,
  workspaceEntryLabel,
  workspaceIsEmpty,
  workspaceQuotaPercent
} from "../workspace-files";

const files = ref<WorkspaceFilesResponse | null>(null);
const loading = ref(false);
const loadError = ref("");
// 同时只跑一个删除：删完要整份重读，连点两条时第二条的按钮可能已经指向回收站里的路径。
const busy = ref("");
const trashBusyKey = "\u0000trash";

const areas = computed(() => sortWorkspaceAreas(files.value?.areas));
const empty = computed(() => workspaceIsEmpty(files.value));

function quotaPercent(area: WorkspaceArea): number | null {
  return area.key === "keep" ? workspaceQuotaPercent(area.bytes, area.quota_bytes) : null;
}

async function load() {
  loading.value = true;
  loadError.value = "";
  try {
    const result = await getWorkspaceFiles();
    // 旧后端或空目录可能把列表省成 null，模板里直接 .length 会炸。
    files.value = {
      ...result,
      areas: (result.areas ?? []).map((area) => ({ ...area, entries: area.entries ?? [] })),
      loose: result.loose ?? [],
      orphan_coding: result.orphan_coding ?? []
    };
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : "读取工作目录失败";
  } finally {
    loading.value = false;
  }
}

async function removeEntry(entry: WorkspaceFileEntry) {
  if (busy.value) return;
  const ok = await askConfirm({
    title: "删除文件",
    message: `「${entry.path}」会挪进工作目录的回收站，在回收站被清理之前还能找回。`,
    confirmLabel: "移到回收站",
    danger: true
  });
  if (!ok) return;
  busy.value = entry.path;
  try {
    await deleteWorkspaceFile(entry.path);
    toastSuccess("已移到回收站");
    await load();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally {
    busy.value = "";
  }
}

async function emptyTrash(area: WorkspaceArea) {
  if (busy.value) return;
  const ok = await askConfirm({
    title: "清空回收站",
    message: `回收站里的 ${formatNumber(area.files)} 个文件（${formatBytes(area.bytes)}）会被永久删除，无法恢复。`,
    confirmLabel: "永久删除",
    danger: true
  });
  if (!ok) return;
  busy.value = trashBusyKey;
  try {
    const result = await emptyWorkspaceTrash();
    toastSuccess(`已清空回收站，释放 ${formatBytes(result.deleted_bytes)}`);
    await load();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "清空回收站失败");
  } finally {
    busy.value = "";
  }
}

onMounted(() => void load());
</script>

<style scoped>
.ws-group {
  display: grid;
  gap: 8px;
  padding: 16px 0;
  border-bottom: 1px solid var(--border);
}

.ws-group:first-child {
  padding-top: 0;
}

.ws-group-head {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.ws-group-title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex: 1 1 auto;
  min-width: 0;
  flex-wrap: wrap;
}

.ws-group-title h3 {
  margin: 0;
  font-size: 14px;
}

.ws-bot {
  font-size: 13px;
  font-weight: 550;
}

.ws-bot-id {
  font-size: 12px;
}

.ws-group-meta {
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}

.ws-retention,
.ws-empty {
  margin: 0;
  font-size: 12.5px;
  color: var(--muted);
}

.ws-foot {
  margin: 14px 0 0;
}

.ws-quota {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
}

.ws-quota-bar {
  flex: 0 1 240px;
  height: 8px;
  border-radius: 999px;
  overflow: hidden;
  background: var(--surface-2);
}

.ws-quota-bar span {
  display: block;
  height: 100%;
  min-width: 2px;
  background: var(--accent);
}

.ws-quota-bar span.full {
  background: var(--warn);
}

.ws-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  container-type: inline-size;
}

.ws-entry {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 76px 130px auto;
  align-items: center;
  gap: 12px;
  padding: 7px 0;
  font-size: 13px;
  border-top: 1px solid var(--border);
}

.ws-entry:first-child {
  border-top: none;
}

.ws-entry-main {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.ws-entry-path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ws-entry-desc {
  font-size: 12px;
  color: var(--text-secondary);
}

.ws-entry-size {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.ws-entry-time {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.ws-entry-actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
  min-width: 128px;
}

.ws-entry-actions a.btn {
  text-decoration: none;
}

/* 按列表自己的宽度换行而不是按屏幕：设置页左边还有一栏菜单，桌面上卡片也可能
   只有四百来宽。窄了就让路径独占一行，大小、时间、操作挤到第二行。 */
@container (max-width: 560px) {
  .ws-entry {
    grid-template-columns: auto 1fr auto;
    row-gap: 4px;
  }

  .ws-entry-main {
    grid-column: 1 / -1;
  }

  .ws-entry-size {
    text-align: left;
  }

  .ws-entry-actions {
    min-width: 0;
  }
}
</style>
