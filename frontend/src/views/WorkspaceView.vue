<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  工作区是 Agent 读写文件、跑命令的目录，位置跟着数据库走（数据目录下的 workspace）。
  它藏在 Application Support 或 Docker 数据卷里，想看机器人写了什么、截了什么图，
  以前只能 SSH 上去翻。

  这一页以前拆成两处：这里只能逐层浏览，设置里的「工作目录」按分区列文件、能删。
  现在合在一起：上面是分区卡片，一眼看出哪块占得多、多久会被清掉，点卡片跳到对应
  目录；下面是目录浏览，能预览、下载，删除先进回收站。只有管理员进得来，所以什么都
  给看：凭据、运行时文件和指到工作区外面的链接照常能打开，只打标记；删凭据和运行时
  文件要多确认一次。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>文件</h2>
        <span class="card-sub">Agent 工作区里的文件：各分区的占用和清理规则，点一块进去逐层浏览、预览、下载；删除的先进回收站</span>
      </div>
      <div class="card-body stack">
        <div v-if="rootPath" class="workspace-root">
          <span class="muted">位置</span>
          <code class="mono workspace-root-path" :title="rootPath">{{ rootPath }}</code>
          <button class="btn ghost small" type="button" @click="copyRoot">
            <Copy :size="13" aria-hidden="true" />
            复制
          </button>
        </div>

        <p v-if="overviewError" class="workspace-error" role="alert">{{ overviewError }}</p>
        <LoadingSkeleton v-else-if="!overview" kind="users" :count="2" label="正在统计各分区" />
        <ul v-else-if="areas.length > 0" class="workspace-areas">
          <li v-for="area in areas" :key="`${area.key}:${area.bot_id ?? ''}`">
            <button
              type="button"
              class="workspace-area"
              :class="{ active: areaActive(area) }"
              :aria-current="areaActive(area) ? 'true' : undefined"
              @click="openArea(area)"
            >
              <span class="workspace-area-title">
                <strong>{{ area.label }}</strong>
                <span v-if="area.key === 'keep'" class="workspace-area-bot" :title="area.bot_id">{{ area.bot_name || area.bot_id || "未知机器人" }}</span>
              </span>
              <span class="workspace-area-size">{{ formatBytes(area.bytes) }}</span>
              <span class="workspace-area-meta">{{ formatNumber(area.files) }} 个文件 · {{ area.retention }}</span>
              <!-- 长期保存区有配额：写满之后 Agent 再存会被拒，提前看得到快满了。 -->
              <span v-if="quotaPercent(area) !== null" class="workspace-quota">
                <span
                  class="workspace-quota-bar"
                  role="img"
                  :aria-label="`已用 ${formatBytes(area.bytes)}，配额 ${formatBytes(area.quota_bytes)}`"
                >
                  <span :class="{ full: (quotaPercent(area) ?? 0) >= 90 }" :style="{ width: `${quotaPercent(area)}%` }"></span>
                </span>
                <span class="workspace-quota-text">配额 {{ formatBytes(area.quota_bytes) }}</span>
              </span>
            </button>
          </li>
        </ul>

        <!-- 散落文件和闲置的编码工作区只报告：是谁放的、还要不要，程序判断不了。 -->
        <div v-if="overview && overview.loose.length > 0" class="workspace-notice">
          <p>
            <strong>根目录下有 {{ formatNumber(overview.loose.length) }} 个散落文件</strong>，不属于任何分区、不会自动清理。
            用不上可以删掉，要留着的建议让 Agent 挪进 outputs/ 或 keep/。
            <button type="button" class="workspace-link" @click="open('')">去根目录看看</button>
          </p>
          <p class="workspace-notice-items mono">{{ looseSummary }}</p>
        </div>
        <div v-if="overview && overview.orphan_coding.length > 0" class="workspace-notice">
          <p>
            <strong>{{ formatNumber(overview.orphan_coding.length) }} 个编码工作区闲置</strong>：长期没动，也没有机器人配置引用。
            里面可能有没推上去的改动，只提示不自动删除，确认没用了再手动删。
          </p>
          <ul class="workspace-notice-list">
            <li v-for="entry in overview.orphan_coding" :key="entry.path">
              <button type="button" class="workspace-link mono" @click="open(entry.path)">{{ entry.path }}</button>
              <span class="muted">{{ formatBytes(entry.size) }} · 最后改动 {{ formatRelative(entry.modified) }}</span>
            </li>
          </ul>
        </div>
      </div>
    </div>

    <div ref="browserCard" class="card workspace-browser">
      <div class="card-body stack">
        <div class="workspace-toolbar">
          <nav class="workspace-crumbs" aria-label="当前位置">
            <button type="button" :disabled="!currentPath" @click="open('')">工作区</button>
            <template v-for="crumb in crumbs" :key="crumb.path">
              <ChevronRight :size="13" aria-hidden="true" class="muted" />
              <button type="button" :disabled="crumb.path === currentPath" @click="open(crumb.path)">{{ crumb.name }}</button>
            </template>
          </nav>
          <div class="workspace-toolbar-actions">
            <button
              v-if="inTrash && listing && listing.entries.length > 0"
              class="btn small danger"
              type="button"
              :disabled="busy !== ''"
              @click="emptyTrash"
            >
              <Trash2 :size="13" aria-hidden="true" />
              {{ busy === TRASH_BUSY ? "清空中…" : "清空回收站" }}
            </button>
            <button class="btn ghost small" type="button" :disabled="loading" @click="refresh">
              <RefreshCw :size="13" aria-hidden="true" />
              刷新
            </button>
          </div>
        </div>

        <p v-if="listing?.area" class="workspace-area-hint">
          <strong>{{ areaHintTitle }}</strong>
          <span>{{ listing.area.retention }}</span>
          <span v-if="inTrash" class="muted">回收站里的东西不单条删除，到期自动清理或整个清空。</span>
        </p>

        <p v-if="listing?.external" class="workspace-area-hint muted">这里经符号链接到了工作区外面：可以看和下载，不能删除。</p>

        <p v-if="error" class="workspace-error" role="alert">{{ error }}</p>
        <EmptyState
          v-else-if="listing?.missing"
          title="这里还没有文件"
          hint="这个分区的目录还没建出来，Agent 第一次往这里写文件时才会创建。"
        >
          <template #icon><FolderOpen :size="20" aria-hidden="true" /></template>
        </EmptyState>
        <EmptyState
          v-else-if="listing && !listing.exists"
          title="工作区还没建出来"
          hint="Agent 第一次写文件、截图或让编码代理克隆仓库时才会创建这个目录。"
        >
          <template #icon><FolderOpen :size="20" aria-hidden="true" /></template>
        </EmptyState>
        <EmptyState v-else-if="listing && listing.entries.length === 0" title="这个目录是空的">
          <template #icon><FolderOpen :size="20" aria-hidden="true" /></template>
        </EmptyState>
        <div v-else-if="listing" class="workspace-table-wrap">
          <table class="table workspace-table">
            <thead>
              <tr>
                <th>名称</th>
                <th class="workspace-size">大小</th>
                <th class="workspace-time">修改时间</th>
                <th class="workspace-actions" aria-label="操作"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in listing.entries" :key="entry.path">
                <td>
                  <button
                    type="button"
                    class="workspace-entry"
                    :class="{ disabled: entry.kind === 'link' }"
                    :disabled="entry.kind === 'link'"
                    :title="entryTitle(entry)"
                    @click="activate(entry)"
                  >
                    <component :is="entryIcon(entry)" :size="15" aria-hidden="true" class="workspace-entry-icon" />
                    <span class="workspace-entry-name">{{ entry.name }}</span>
                    <span v-if="workspaceProtectedLabel(entry)" class="badge warn">{{ workspaceProtectedLabel(entry) }}</span>
                    <span v-if="entry.kind === 'link'" class="badge err">失效</span>
                    <span v-else-if="entry.external" class="badge warn">外部链接</span>
                    <span v-else-if="entry.symlink" class="badge">链接</span>
                  </button>
                  <p v-if="entry.description || entry.saved_by" class="workspace-entry-desc">
                    <template v-if="entry.description">{{ entry.description }}</template>
                    <span v-if="entry.saved_by" class="muted">{{ entry.description ? " · " : "" }}{{ entry.saved_by }} 存的</span>
                  </p>
                </td>
                <td class="workspace-size muted">{{ entry.kind === "file" ? formatBytes(entry.size) : "" }}</td>
                <td class="workspace-time muted" :title="formatTime(entry.modified)">{{ formatRelative(entry.modified) }}</td>
                <td class="workspace-actions">
                  <a
                    v-if="entry.kind === 'file'"
                    class="btn ghost small icon-only"
                    :href="workspaceFileURL(entry.path, true)"
                    download
                    :title="`下载 ${entry.name}`"
                    :aria-label="`下载 ${entry.name}`"
                  >
                    <Download :size="14" aria-hidden="true" />
                  </a>
                  <button
                    v-if="canDelete(entry)"
                    class="btn ghost small icon-only danger"
                    type="button"
                    :disabled="busy !== ''"
                    :title="`删除 ${entry.name}（移到回收站）`"
                    :aria-label="`删除 ${entry.name}`"
                    @click="removeEntry(entry)"
                  >
                    <Trash2 :size="14" aria-hidden="true" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="listing?.truncated" class="muted workspace-note">目录里的条目太多，只列出前 1000 项。</p>
      </div>
    </div>

    <Modal v-if="preview" :title="preview.entry.name" wide @close="closePreview">
      <div class="workspace-preview">
        <p class="muted workspace-preview-meta">
          {{ formatBytes(preview.entry.size) }} · {{ formatTime(preview.entry.modified) }}
          <template v-if="preview.entry.saved_by"> · {{ preview.entry.saved_by }} 存的</template>
        </p>
        <p v-if="preview.entry.description" class="workspace-preview-desc">{{ preview.entry.description }}</p>
        <p v-if="workspaceProtectedLabel(preview.entry)" class="workspace-warning">
          这是 Diana 的{{ workspaceProtectedLabel(preview.entry) }}，里面可能有明文令牌或运行时开关，别截图外传；要改设置请到扩展页或编码代理设置。
        </p>
        <p v-else-if="preview.entry.external" class="workspace-warning">这个文件经链接指到了工作区外面。</p>
        <img v-if="preview.kind === 'image'" :src="workspaceFileURL(preview.entry.path)" :alt="preview.entry.name" />
        <video v-else-if="preview.kind === 'video'" :src="workspaceFileURL(preview.entry.path)" controls preload="metadata" />
        <audio v-else-if="preview.kind === 'audio'" :src="workspaceFileURL(preview.entry.path)" controls preload="metadata" />
        <iframe v-else-if="preview.kind === 'pdf'" :src="workspaceFileURL(preview.entry.path)" :title="preview.entry.name" />
        <template v-else-if="preview.kind === 'text'">
          <p v-if="preview.loading" class="muted">读取中…</p>
          <pre v-else class="workspace-preview-text">{{ preview.text }}</pre>
          <p v-if="preview.clipped" class="muted workspace-note">文件较大，只显示前 {{ formatBytes(TEXT_PREVIEW_LIMIT) }}，完整内容请下载。</p>
        </template>
        <p v-else class="muted">这种文件没法在页面里预览，可以下载后查看。</p>
      </div>
      <template #footer>
        <button
          v-if="canDelete(preview.entry)"
          class="btn ghost danger"
          type="button"
          :disabled="busy !== ''"
          @click="removeEntry(preview.entry)"
        >
          <Trash2 :size="14" aria-hidden="true" />
          移到回收站
        </button>
        <a class="btn primary" :href="workspaceFileURL(preview.entry.path, true)" download>
          <Download :size="14" aria-hidden="true" />
          下载
        </a>
      </template>
    </Modal>
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onMounted, ref } from "vue";
import type { Component } from "vue";
import {
  ChevronRight,
  Copy,
  Download,
  File,
  FileAudio,
  FileImage,
  FileText,
  FileType,
  FileVideo,
  Folder,
  FolderOpen,
  Link2Off,
  Lock,
  RefreshCw,
  Trash2
} from "@lucide/vue";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import Modal from "../components/Modal.vue";
import {
  deleteWorkspaceFile,
  emptyWorkspaceTrash,
  getWorkspaceOverview,
  listWorkspace,
  workspaceFileURL,
  type WorkspaceArea,
  type WorkspaceEntry,
  type WorkspaceListing,
  type WorkspaceOverview
} from "../api";
import { askConfirm } from "../confirm";
import { formatBytes, formatNumber, formatRelative, formatTime } from "../format";
import { toastError, toastSuccess } from "../toast";
import {
  sortWorkspaceAreas,
  workspaceAreaTarget,
  workspaceCanDelete,
  workspaceExtension,
  workspaceInTrash,
  workspacePreviewKind,
  workspaceProtectedLabel,
  workspaceQuotaPercent,
  type WorkspacePreviewKind
} from "../workspace-files";

// 文本预览只取开头一段：日志、克隆下来的大文件整份塞进 <pre> 会把页面卡住。
const TEXT_PREVIEW_LIMIT = 256 * 1024;
// 同时只跑一个删除或清空：做完要重读目录，连点两条时第二条可能已经指向回收站里的路径。
const TRASH_BUSY = "\u0000trash";

interface Preview {
  entry: WorkspaceEntry;
  kind: WorkspacePreviewKind | "other";
  loading: boolean;
  text: string;
  clipped: boolean;
}

const overview = ref<WorkspaceOverview | null>(null);
const overviewError = ref("");
const listing = ref<WorkspaceListing | null>(null);
const currentPath = ref("");
const loading = ref(false);
const error = ref("");
const preview = ref<Preview | null>(null);
const busy = ref("");
const browserCard = ref<HTMLElement | null>(null);

const areas = computed(() => sortWorkspaceAreas(overview.value?.areas));
const rootPath = computed(() => listing.value?.root || overview.value?.root || "");
const inTrash = computed(() => workspaceInTrash(currentPath.value));
const crumbs = computed(() => {
  const parts = currentPath.value ? currentPath.value.split("/") : [];
  return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join("/") }));
});
const areaHintTitle = computed(() => {
  const area = listing.value?.area;
  if (!area) return "";
  if (area.key !== "keep" || !area.bot_id) return area.label;
  return `${area.label} · ${area.bot_name || area.bot_id}`;
});
const looseSummary = computed(() => {
  const loose = overview.value?.loose ?? [];
  const names = loose.slice(0, 5).map((entry) => entry.name);
  return loose.length > names.length ? `${names.join("、")} 等` : names.join("、");
});

function quotaPercent(area: WorkspaceArea): number | null {
  return area.key === "keep" ? workspaceQuotaPercent(area.bytes, area.quota_bytes) : null;
}

// 当前目录落在哪张卡片里就把它标出来。「其他目录」点进去是根目录，根目录什么都有，
// 标它反而误导，就不标。
function areaActive(area: WorkspaceArea): boolean {
  const target = workspaceAreaTarget(area);
  if (!target) return false;
  return currentPath.value === target || currentPath.value.startsWith(`${target}/`);
}

async function loadOverview(): Promise<void> {
  overviewError.value = "";
  try {
    const result = await getWorkspaceOverview();
    // 旧后端或空目录可能把列表省成 null，模板里直接 .length 会炸。
    overview.value = {
      ...result,
      areas: result.areas ?? [],
      loose: result.loose ?? [],
      orphan_coding: result.orphan_coding ?? []
    };
  } catch (err) {
    overviewError.value = err instanceof Error ? err.message : "统计各分区失败";
  }
}

async function open(path: string): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    const result = await listWorkspace(path);
    listing.value = { ...result, entries: result.entries ?? [] };
    currentPath.value = result.path;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}

// 点分区卡片时目录浏览在卡片下面，窄屏上往往在屏幕外，跳过去才看得到变化。
async function openArea(area: WorkspaceArea): Promise<void> {
  await open(workspaceAreaTarget(area));
  browserCard.value?.scrollIntoView({ behavior: "smooth", block: "start" });
}

function refresh(): void {
  void open(currentPath.value);
  void loadOverview();
}

function canDelete(entry: WorkspaceEntry): boolean {
  return workspaceCanDelete(entry, Boolean(listing.value?.external));
}

function entryIcon(entry: WorkspaceEntry): Component {
  if (entry.protected) return Lock;
  if (entry.kind === "link") return Link2Off;
  if (entry.kind === "dir") return Folder;
  const icons: Partial<Record<WorkspacePreviewKind, Component>> = { image: FileImage, video: FileVideo, audio: FileAudio, pdf: FileType };
  return icons[workspacePreviewKind(entry.name)] ?? (workspaceExtension(entry.name) ? FileText : File);
}

function entryTitle(entry: WorkspaceEntry): string {
  const label = workspaceProtectedLabel(entry);
  if (label) return `${entry.path}：Diana 的${label}，里面可能有明文令牌或运行时开关`;
  if (entry.kind === "link") return "链接的目标已经不在了";
  if (entry.external) return `${entry.path}：链接指到了工作区外面`;
  return entry.path;
}

function activate(entry: WorkspaceEntry): void {
  if (entry.kind === "link") return;
  if (entry.kind === "dir") {
    void open(entry.path);
    return;
  }
  const kind = workspacePreviewKind(entry.name);
  preview.value = { entry, kind, loading: kind === "text", text: "", clipped: false };
  if (kind === "text") void loadText(entry);
}

async function loadText(entry: WorkspaceEntry): Promise<void> {
  const current = preview.value;
  try {
    const response = await fetch(workspaceFileURL(entry.path), {
      headers: entry.size > TEXT_PREVIEW_LIMIT ? { Range: `bytes=0-${TEXT_PREVIEW_LIMIT - 1}` } : undefined
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({}) as { error?: string });
      throw new Error(body.error || `读取失败（${response.status}）`);
    }
    const contentType = response.headers.get("Content-Type") ?? "";
    if (preview.value !== current || !current) return;
    // 后端按内容判断：不是文本（压缩包、Office 文档之类）就改成只给下载。
    if (!contentType.startsWith("text/")) {
      current.kind = "other";
      return;
    }
    current.text = await response.text();
    current.clipped = entry.size > TEXT_PREVIEW_LIMIT;
  } catch (err) {
    toastError(err instanceof Error ? err.message : String(err));
    if (preview.value === current) preview.value = null;
  } finally {
    if (current) current.loading = false;
  }
}

function closePreview(): void {
  preview.value = null;
}

async function removeEntry(entry: WorkspaceEntry): Promise<void> {
  if (busy.value) return;
  const what = entry.kind === "dir" ? "目录" : "文件";
  const linkNote = entry.symlink ? "挪走的是链接本身，指向的东西不动。" : "";
  const ok = await askConfirm({
    title: `删除${what}`,
    message: `「${entry.path}」${entry.kind === "dir" && !entry.symlink ? "连同里面的东西" : ""}会挪进工作区的回收站，在回收站被清理之前还能找回。${linkNote}`,
    confirmLabel: "移到回收站",
    danger: true
  });
  if (!ok) return;
  // 凭据和运行时文件再确认一次：挪走之后对应的功能会失效，回收站里那份也不再在
  // Agent 的凭据名单里（名单按原路径认），命令和文件工具可能读得到。
  const label = workspaceProtectedLabel(entry);
  if (label) {
    const sure = await askConfirm({
      title: `这是 Diana 的${label}`,
      message: `「${entry.path}」是运行时自己在用的${label}：挪走之后，依赖它的扩展开关、MCP 连接、编码代理登录或长期区索引会失效；挪进回收站的那份不再受 Agent 凭据名单保护。确定仍要删除？`,
      confirmLabel: "仍然移到回收站",
      danger: true
    });
    if (!sure) return;
  }
  busy.value = entry.path;
  try {
    await deleteWorkspaceFile(entry.path);
    toastSuccess("已移到回收站");
    if (preview.value?.entry.path === entry.path) preview.value = null;
    await Promise.all([open(currentPath.value), loadOverview()]);
  } catch (err) {
    toastError(err instanceof Error ? err.message : "删除失败");
  } finally {
    busy.value = "";
  }
}

async function emptyTrash(): Promise<void> {
  if (busy.value) return;
  const trash = overview.value?.areas.find((area) => area.key === "trash");
  const size = trash ? `里的 ${formatNumber(trash.files)} 个文件（${formatBytes(trash.bytes)}）` : "里的全部文件";
  const ok = await askConfirm({
    title: "清空回收站",
    message: `回收站${size}会被永久删除，无法恢复。`,
    confirmLabel: "永久删除",
    danger: true
  });
  if (!ok) return;
  busy.value = TRASH_BUSY;
  try {
    const result = await emptyWorkspaceTrash();
    toastSuccess(`已清空回收站，释放 ${formatBytes(result.deleted_bytes)}`);
    // 在 .trash/<时间>/ 里面点的清空：那层目录已经没了，回到回收站根目录。
    await Promise.all([open(inTrash.value ? ".trash" : currentPath.value), loadOverview()]);
  } catch (err) {
    toastError(err instanceof Error ? err.message : "清空回收站失败");
  } finally {
    busy.value = "";
  }
}

async function copyRoot(): Promise<void> {
  if (!rootPath.value) return;
  try {
    await navigator.clipboard.writeText(rootPath.value);
    toastSuccess("已复制工作区路径");
  } catch {
    toastError("复制失败，请手动选中路径");
  }
}

onMounted(() => {
  void open("");
  void loadOverview();
});
// 切回这一页时 Agent 可能又写了文件，重新列一次当前目录和分区合计。
let mounted = false;
onActivated(() => {
  if (mounted) refresh();
  mounted = true;
});
</script>

<style scoped>
.workspace-browser {
  scroll-margin-top: calc(var(--topbar-height) + 12px);
}

.workspace-root {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 13px;
}

.workspace-root-path {
  min-width: 0;
  overflow-wrap: anywhere;
  padding: 3px 8px;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-size: 12.5px;
}

.workspace-areas {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
  gap: 10px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.workspace-area {
  display: grid;
  gap: 4px;
  width: 100%;
  height: 100%;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: var(--bg-raised);
  color: var(--text);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s ease, background 0.15s ease;
}

.workspace-area:hover {
  border-color: var(--border-strong);
  background: var(--surface-2);
}

.workspace-area.active {
  border-color: var(--accent);
  background: var(--accent-soft);
}

.workspace-area-title {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  font-size: 13px;
}

.workspace-area-title strong {
  flex: none;
  white-space: nowrap;
}

.workspace-area-bot {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
  font-size: 12.5px;
}

.workspace-area-size {
  font-size: 18px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.workspace-area-meta {
  color: var(--muted);
  font-size: 12px;
}

.workspace-quota {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
  font-size: 11.5px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

.workspace-quota-bar {
  flex: 1 1 auto;
  height: 6px;
  border-radius: 999px;
  overflow: hidden;
  background: var(--surface-2);
}

.workspace-quota-bar span {
  display: block;
  height: 100%;
  min-width: 2px;
  background: var(--accent);
}

.workspace-quota-bar span.full {
  background: var(--warn);
}

.workspace-quota-text {
  white-space: nowrap;
}

.workspace-notice {
  display: grid;
  gap: 6px;
  padding: 10px 14px;
  border-radius: var(--radius-sm);
  background: var(--warn-soft);
  font-size: 13px;
  line-height: 1.55;
}

.workspace-notice p {
  margin: 0;
}

.workspace-notice-items {
  color: var(--text-secondary);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.workspace-notice-list {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: 12.5px;
}

.workspace-notice-list li {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
}

.workspace-link {
  padding: 0;
  border: none;
  background: none;
  color: var(--accent);
  font: inherit;
  cursor: pointer;
  text-decoration: underline;
  overflow-wrap: anywhere;
}

.workspace-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.workspace-toolbar-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
}

.workspace-crumbs {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  min-width: 0;
  font-size: 13.5px;
}

.workspace-crumbs button {
  padding: 2px 4px;
  border: none;
  border-radius: 6px;
  background: none;
  color: var(--accent);
  font: inherit;
  cursor: pointer;
  overflow-wrap: anywhere;
}

.workspace-crumbs button:hover:not(:disabled) {
  background: var(--accent-soft);
}

.workspace-crumbs button:disabled {
  color: var(--text);
  font-weight: 600;
  cursor: default;
}

.workspace-area-hint {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  margin: 0;
  padding: 8px 12px;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-size: 12.5px;
}

.workspace-error {
  margin: 0;
  color: var(--err);
  font-size: 13px;
}

.workspace-table-wrap {
  overflow-x: auto;
}

/* 行高按带操作按钮的行定：有的行能删、有的不能，高度不齐看着像错位。 */
.workspace-table tbody td {
  height: 44px;
  vertical-align: middle;
}

.workspace-size,
.workspace-time,
.workspace-actions {
  white-space: nowrap;
  width: 1%;
}

.workspace-actions {
  text-align: right;
}

.workspace-actions .btn {
  text-decoration: none;
}

.workspace-actions .btn + .btn {
  margin-left: 2px;
}

.workspace-entry {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 2px 8px;
  max-width: 100%;
  padding: 0;
  border: none;
  background: none;
  color: var(--text);
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.workspace-entry:hover:not(:disabled) .workspace-entry-name {
  color: var(--accent);
  text-decoration: underline;
}

.workspace-entry.disabled {
  color: var(--muted);
  cursor: default;
}

.workspace-entry .badge {
  flex: none;
  white-space: nowrap;
}

.workspace-entry-icon {
  flex-shrink: 0;
  color: var(--muted);
}

.workspace-entry-name {
  overflow-wrap: anywhere;
}

.workspace-entry-desc {
  margin: 3px 0 0 23px;
  color: var(--text-secondary);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.workspace-note {
  margin: 0;
  font-size: 12.5px;
}

.workspace-preview {
  display: grid;
  gap: 10px;
}

.workspace-preview-meta,
.workspace-preview-desc {
  margin: 0;
  font-size: 12.5px;
}

.workspace-preview-desc {
  color: var(--text-secondary);
}

.workspace-warning {
  margin: 0;
  padding: 8px 12px;
  border-radius: var(--radius-sm);
  background: var(--warn-soft);
  color: var(--warn);
  font-size: 13px;
}

.workspace-preview video,
.workspace-preview audio {
  display: block;
  width: 100%;
  max-height: 70vh;
  border-radius: var(--radius-sm);
}

.workspace-preview iframe {
  display: block;
  width: 100%;
  height: 70vh;
  border: none;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
}

.workspace-preview img {
  display: block;
  max-width: 100%;
  max-height: 70vh;
  margin: 0 auto;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
}

.workspace-preview-text {
  margin: 0;
  max-height: 65vh;
  overflow: auto;
  padding: 12px;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

@media (max-width: 640px) {
  .workspace-areas {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }

  .workspace-area {
    padding: 10px 12px;
  }

  .workspace-area-size {
    font-size: 16px;
  }

  .workspace-time {
    display: none;
  }
}
</style>
