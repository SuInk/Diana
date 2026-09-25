<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  工作区是 Agent 读写文件、跑命令的目录，位置跟着数据库走（数据目录下的 workspace）。
  它藏在 Application Support 或 Docker 数据卷里，想看机器人写了什么、截了什么图，
  以前只能 SSH 上去翻。这一页只读：能进目录、预览图片和文本、下载，不能改。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>文件</h2>
        <span class="card-sub">Agent 工作区里的文件：它写的笔记、截的图，编码代理的仓库在 coding 下。这里只能看，不能改</span>
      </div>
      <div class="card-body stack">
        <div v-if="listing" class="workspace-root">
          <span class="muted">位置</span>
          <code class="mono workspace-root-path" :title="listing.root">{{ listing.root }}</code>
          <button class="btn ghost small" type="button" @click="copyRoot">
            <Copy :size="13" aria-hidden="true" />
            复制
          </button>
        </div>

        <div class="workspace-toolbar">
          <nav class="workspace-crumbs" aria-label="当前位置">
            <button type="button" :disabled="!currentPath" @click="open('')">工作区</button>
            <template v-for="crumb in crumbs" :key="crumb.path">
              <ChevronRight :size="13" aria-hidden="true" class="muted" />
              <button type="button" :disabled="crumb.path === currentPath" @click="open(crumb.path)">{{ crumb.name }}</button>
            </template>
          </nav>
          <button class="btn ghost small" type="button" :disabled="loading" @click="open(currentPath)">
            <RefreshCw :size="13" aria-hidden="true" />
            刷新
          </button>
        </div>

        <p v-if="error" class="workspace-error">{{ error }}</p>
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
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in listing.entries" :key="entry.path">
                <td>
                  <button
                    type="button"
                    class="workspace-entry"
                    :class="{ disabled: !openable(entry) }"
                    :disabled="!openable(entry)"
                    :title="entryTitle(entry)"
                    @click="activate(entry)"
                  >
                    <component :is="entryIcon(entry)" :size="15" aria-hidden="true" class="workspace-entry-icon" />
                    <span class="workspace-entry-name">{{ entry.name }}</span>
                    <span v-if="entry.protected" class="badge warn">凭据</span>
                    <span v-else-if="entry.symlink" class="badge">链接</span>
                  </button>
                </td>
                <td class="workspace-size muted">{{ entry.kind === "file" ? formatBytes(entry.size) : "" }}</td>
                <td class="workspace-time muted" :title="formatTime(entry.modified)">{{ formatRelative(entry.modified) }}</td>
              </tr>
            </tbody>
          </table>
          <p v-if="listing.truncated" class="muted workspace-note">目录里的条目太多，只列出前 1000 项。</p>
        </div>
      </div>
    </div>

    <Modal v-if="preview" :title="preview.entry.name" wide @close="closePreview">
      <div class="workspace-preview">
        <p class="muted workspace-preview-meta">
          {{ formatBytes(preview.entry.size) }} · {{ formatTime(preview.entry.modified) }}
        </p>
        <img v-if="preview.kind === 'image'" :src="workspaceFileURL(preview.entry.path)" :alt="preview.entry.name" />
        <template v-else-if="preview.kind === 'text'">
          <p v-if="preview.loading" class="muted">读取中…</p>
          <pre v-else class="workspace-preview-text">{{ preview.text }}</pre>
          <p v-if="preview.clipped" class="muted workspace-note">文件较大，只显示前 {{ formatBytes(TEXT_PREVIEW_LIMIT) }}，完整内容请下载。</p>
        </template>
        <p v-else class="muted">这种文件没法在页面里预览，可以下载后查看。</p>
      </div>
      <template #footer>
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
import { ChevronRight, Copy, Download, File, FileImage, FileText, Folder, FolderOpen, Link2Off, Lock, RefreshCw } from "@lucide/vue";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";
import { listWorkspace, workspaceFileURL, type WorkspaceEntry, type WorkspaceListing } from "../api";
import { formatBytes, formatRelative, formatTime } from "../format";
import { toastError, toastSuccess } from "../toast";

// 文本预览只取开头一段：日志、克隆下来的大文件整份塞进 <pre> 会把页面卡住。
const TEXT_PREVIEW_LIMIT = 256 * 1024;
const IMAGE_EXTENSIONS = new Set(["png", "jpg", "jpeg", "gif", "webp", "bmp"]);
const TEXT_EXTENSIONS = new Set([
  "txt", "md", "json", "jsonl", "yaml", "yml", "toml", "ini", "csv", "tsv", "log",
  "go", "py", "js", "mjs", "ts", "vue", "sh", "html", "htm", "css", "xml", "svg", "sql"
]);

interface Preview {
  entry: WorkspaceEntry;
  kind: "image" | "text" | "other";
  loading: boolean;
  text: string;
  clipped: boolean;
}

const listing = ref<WorkspaceListing | null>(null);
const currentPath = ref("");
const loading = ref(false);
const error = ref("");
const preview = ref<Preview | null>(null);

const crumbs = computed(() => {
  const parts = currentPath.value ? currentPath.value.split("/") : [];
  return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join("/") }));
});

async function open(path: string): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    const result = await listWorkspace(path);
    listing.value = result;
    currentPath.value = result.path;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}

function extension(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

function openable(entry: WorkspaceEntry): boolean {
  return entry.kind !== "link" && !entry.protected;
}

function entryIcon(entry: WorkspaceEntry): Component {
  if (entry.protected) return Lock;
  if (entry.kind === "link") return Link2Off;
  if (entry.kind === "dir") return Folder;
  const ext = extension(entry.name);
  if (IMAGE_EXTENSIONS.has(ext)) return FileImage;
  if (TEXT_EXTENSIONS.has(ext)) return FileText;
  return File;
}

function entryTitle(entry: WorkspaceEntry): string {
  if (entry.protected) return "运行时的凭据配置，不在 WebUI 里显示";
  if (entry.kind === "link") return "这个链接指到了工作区外面，或者目标已经不在了";
  return entry.path;
}

function activate(entry: WorkspaceEntry): void {
  if (!openable(entry)) return;
  if (entry.kind === "dir") {
    void open(entry.path);
    return;
  }
  const ext = extension(entry.name);
  const kind = IMAGE_EXTENSIONS.has(ext) ? "image" : TEXT_EXTENSIONS.has(ext) || ext === "" ? "text" : "other";
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
    // 没有扩展名的文件可能是二进制：后端按内容判断，不是文本就改成只给下载。
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

async function copyRoot(): Promise<void> {
  if (!listing.value) return;
  try {
    await navigator.clipboard.writeText(listing.value.root);
    toastSuccess("已复制工作区路径");
  } catch {
    toastError("复制失败，请手动选中路径");
  }
}

onMounted(() => void open(""));
// 切回这一页时 Agent 可能又写了文件，重新列一次当前目录。
let mounted = false;
onActivated(() => {
  if (mounted) void open(currentPath.value);
  mounted = true;
});
</script>

<style scoped>
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

.workspace-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
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

.workspace-error {
  margin: 0;
  color: var(--err);
  font-size: 13px;
}

.workspace-table-wrap {
  overflow-x: auto;
}

.workspace-table td {
  vertical-align: middle;
}

.workspace-size,
.workspace-time {
  white-space: nowrap;
  width: 1%;
}

.workspace-entry {
  display: inline-flex;
  align-items: center;
  gap: 8px;
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

.workspace-entry-icon {
  flex-shrink: 0;
  color: var(--muted);
}

.workspace-entry-name {
  overflow-wrap: anywhere;
}

.workspace-note {
  margin: 8px 0 0;
  font-size: 12.5px;
}

.workspace-preview {
  display: grid;
  gap: 10px;
}

.workspace-preview-meta {
  margin: 0;
  font-size: 12.5px;
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
  .workspace-time {
    display: none;
  }
}
</style>
