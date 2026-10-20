<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  世界书：世界观设定库。树是全局一棵、所有机器人共用，所以这一页不跟侧栏的机器人
  切换走；每台机器人用不用它由「机器人 → 人设 → 世界书」的开关决定。
  以前整块编辑器嵌在机器人人设页里，条目一多就把配置页撑得很长，和笔记本一样是
  会长期攒起来的内容，放在档案里。
-->
<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <p>
          机器人所处世界的设定集，所有机器人共用一本，改动立即生效：常驻设定每轮都带上，触发式设定聊到才出现。
          每台机器人用不用在「机器人 → 人设 → 世界书」里开关。导入认三种文件：本机导出的 JSON、SillyTavern 世界书、角色卡（自动识别 character_book）。
        </p>
      </div>
      <div class="view-actions">
        <button class="btn ghost" type="button" :disabled="worldBookBusy || !worldBookNodes.length" @click="exportWorldBook">
          <Download :size="15" aria-hidden="true" />
          导出
        </button>
        <button class="btn ghost" type="button" :disabled="worldBookBusy" @click="worldBookFileInputClick">
          <Upload :size="15" aria-hidden="true" />
          导入
        </button>
        <button class="btn" type="button" :disabled="worldBookBusy" @click="openWorldBookEditor()">
          <Plus :size="15" aria-hidden="true" />
          新增设定
        </button>
      </div>
    </header>

    <section class="card">
        <div class="card-body form-grid">
          <input ref="worldBookFileInput" type="file" accept="application/json" style="display: none" @change="importWorldBookFile" />
          <div v-if="worldBookRows.length" class="field wide">
            <div class="world-book-list">
              <div v-for="row in worldBookRows" :key="row.node.id" class="world-book-row" :style="{ paddingLeft: `${row.depth * 18}px` }">
                <button type="button" class="world-book-title" :title="row.node.content || row.node.title" @click="openWorldBookEditor(row.node)">
                  <strong :class="{ muted: worldBookRowDisabled(row) }">{{ row.node.title }}</strong>
                  <small class="muted">{{ worldBookNodeSummary(row) }}</small>
                </button>
                <span class="cluster">
                  <button type="button" class="persona-chip-action danger" :disabled="worldBookBusy" :aria-label="`删除设定 ${row.node.title}`" :title="`删除设定 ${row.node.title}`" @click="removeWorldBookNode(row.node)">
                    <Trash2 :size="14" aria-hidden="true" />
                  </button>
                </span>
              </div>
            </div>
            <span class="hint">点标题编辑。删除一个节点时，它的子节点会接到它的父节点上，不会连坐。</span>
          </div>
          <span v-else class="hint">还没有条目。「常驻」的（酒馆里的蓝灯）写世界的骨架；带触发词的（绿灯）写细节，聊到相关话题才注入，不浪费上下文。手上有 SillyTavern 世界书或角色卡的话，直接点「导入」。</span>
          <template v-if="worldBookEditorOpen">
            <div class="field">
              <label for="world-book-title">标题</label>
              <input id="world-book-title" v-model.trim="worldBookDraft.title" class="input" placeholder="例如 枝江 / 港口" />
            </div>
            <div class="field">
              <label>父节点</label>
              <AppSelect
                :model-value="worldBookDraft.parent_id ?? ''"
                :options="worldBookParentOptions"
                @update:model-value="(value) => { worldBookDraft.parent_id = value; }"
              />
              <span class="hint">路径会作为语境一起注入，例如「枝江 / 港口：……」。</span>
            </div>
            <div class="field wide">
              <label for="world-book-content">设定内容</label>
              <textarea id="world-book-content" v-model="worldBookDraft.content" class="textarea" rows="3" placeholder="只有标题没有内容的节点当目录用，自身不注入。"></textarea>
            </div>
            <div class="field">
              <label for="world-book-keywords">触发词（逗号分隔）</label>
              <input id="world-book-keywords" v-model="worldBookKeywordsDraft" class="input" placeholder="港口,码头" />
              <span class="hint">最近对话里出现任意一个就注入本条；常驻节点不需要填。</span>
            </div>
            <div class="field">
              <label for="world-book-secondary">副触发词（可选，逗号分隔）</label>
              <input id="world-book-secondary" v-model="worldBookSecondaryDraft" class="input" placeholder="枝江" />
              <span class="hint">填了之后主词命中还要求任一副词也在场才注入，用来收窄太宽的主词（酒馆的 AND ANY）。</span>
            </div>
            <div class="field">
              <label class="switch">
                <input v-model="worldBookDraft.always_on" type="checkbox" />
                <span class="track" aria-hidden="true"></span>
                <span class="switch-label">常驻注入</span>
              </label>
              <label class="switch">
                <input v-model="worldBookDraftEnabled" type="checkbox" />
                <span class="track" aria-hidden="true"></span>
                <span class="switch-label">启用（关掉时整个子树都不注入）</span>
              </label>
            </div>
            <div class="field wide cluster">
              <button class="btn primary small" type="button" :disabled="worldBookBusy || !worldBookDraft.title" @click="storeWorldBookNode">
                {{ worldBookDraft.id ? "保存修改" : "添加设定" }}
              </button>
              <button class="btn small" type="button" :disabled="worldBookBusy" @click="worldBookEditorOpen = false">取消</button>
            </div>
          </template>
        </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Download, Plus, Trash2, Upload } from "@lucide/vue";
import {
  listWorldBook,
  saveWorldBookNode,
  deleteWorldBookNode,
  importWorldBook,
  importWorldBookSillyTavern,
  WORLD_BOOK_EXPORT_VERSION,
  type WorldBookNode,
  type WorldBookImportResult
} from "../api";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import { askConfirm } from "../lib/confirm";
import { toastError, toastSuccess } from "../lib/toast";

function downloadJSONFile(fileName: string, content: string): void {
  const url = URL.createObjectURL(new Blob([content], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  link.click();
  URL.revokeObjectURL(url);
}

// ---- 世界书（世界观设定库） ----
// 树是全局一棵，节点的增删改立即落库；每台机器人用不用由机器人配置里的 world_book_enabled 决定。

const worldBookNodes = ref<WorldBookNode[]>([]);
const worldBookBusy = ref(false);
const worldBookEditorOpen = ref(false);
const worldBookDraft = ref<WorldBookNode>({ id: "", title: "" });
const worldBookKeywordsDraft = ref("");
const worldBookSecondaryDraft = ref("");
const worldBookDraftEnabled = ref(true);
const worldBookFileInput = ref<HTMLInputElement | null>(null);

interface WorldBookRow {
  node: WorldBookNode;
  depth: number;
  path: string[];
}

// 深度优先展开，和后端注入顺序一致；悬空父节点当根处理，后端下次保存会修正。
const worldBookRows = computed<WorldBookRow[]>(() => {
  const byParent = new Map<string, WorldBookNode[]>();
  const ids = new Set(worldBookNodes.value.map((node) => node.id));
  for (const node of worldBookNodes.value) {
    const parent = node.parent_id && ids.has(node.parent_id) ? node.parent_id : "";
    byParent.set(parent, [...(byParent.get(parent) ?? []), node]);
  }
  const rows: WorldBookRow[] = [];
  const walk = (parent: string, depth: number, path: string[]): void => {
    for (const node of byParent.get(parent) ?? []) {
      rows.push({ node, depth, path });
      walk(node.id, depth + 1, [...path, node.title]);
    }
  };
  walk("", 0, []);
  return rows;
});

// 父节点候选不能选自己和自己的后代，不然一保存就成了环。
const worldBookParentOptions = computed<AppSelectOption[]>(() => {
  const editingID = worldBookDraft.value.id;
  const excluded = new Set<string>();
  if (editingID) {
    excluded.add(editingID);
    for (const row of worldBookRows.value) {
      if (row.node.parent_id && excluded.has(row.node.parent_id)) excluded.add(row.node.id);
    }
  }
  return [
    { value: "", label: "（根）" },
    ...worldBookRows.value
      .filter((row) => !excluded.has(row.node.id))
      .map((row) => ({ value: row.node.id, label: `${"　".repeat(row.depth)}${row.node.title}` }))
  ];
});

function worldBookRowDisabled(row: WorldBookRow): boolean {
  if (row.node.enabled === false) return true;
  const byID = new Map(worldBookNodes.value.map((node) => [node.id, node]));
  let parent = row.node.parent_id;
  while (parent) {
    const node = byID.get(parent);
    if (!node) break;
    if (node.enabled === false) return true;
    parent = node.parent_id;
  }
  return false;
}

function worldBookNodeSummary(row: WorldBookRow): string {
  const parts: string[] = [];
  if (row.node.enabled === false) parts.push("已停用");
  else if (worldBookRowDisabled(row)) parts.push("随父级停用");
  if (row.node.always_on) parts.push("常驻");
  else if (row.node.keywords?.length) {
    parts.push(`触发：${row.node.keywords.join("、")}`);
    if (row.node.secondary_keywords?.length) parts.push(`且需：${row.node.secondary_keywords.join("、")}`);
  }
  else if (row.node.content?.trim()) parts.push("未设注入方式，仅作目录");
  else parts.push("目录");
  return parts.join(" · ");
}

async function loadWorldBook(): Promise<void> {
  try {
    worldBookNodes.value = (await listWorldBook()).nodes ?? [];
  } catch {
    worldBookNodes.value = [];
    toastError("世界书读取失败");
  }
}

function openWorldBookEditor(node?: WorldBookNode): void {
  worldBookDraft.value = node
    ? { ...node }
    : { id: "", title: "", parent_id: "", content: "", always_on: false };
  worldBookKeywordsDraft.value = (node?.keywords ?? []).join(",");
  worldBookSecondaryDraft.value = (node?.secondary_keywords ?? []).join(",");
  worldBookDraftEnabled.value = node?.enabled !== false;
  worldBookEditorOpen.value = true;
}

async function storeWorldBookNode(): Promise<void> {
  const draft = worldBookDraft.value;
  if (!draft.title.trim()) return;
  worldBookBusy.value = true;
  try {
    const response = await saveWorldBookNode({
      ...(draft.id ? { id: draft.id } : {}),
      parent_id: draft.parent_id || "",
      title: draft.title,
      content: draft.content ?? "",
      keywords: worldBookKeywordsDraft.value.split(/[,，]/).map((keyword) => keyword.trim()).filter(Boolean),
      secondary_keywords: worldBookSecondaryDraft.value.split(/[,，]/).map((keyword) => keyword.trim()).filter(Boolean),
      always_on: draft.always_on ?? false,
      enabled: worldBookDraftEnabled.value
    });
    worldBookNodes.value = response.nodes ?? [];
    worldBookEditorOpen.value = false;
    toastSuccess(draft.id ? `已更新设定「${response.node.title}」` : `已添加设定「${response.node.title}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "世界书保存失败");
  } finally {
    worldBookBusy.value = false;
  }
}

async function removeWorldBookNode(node: WorldBookNode): Promise<void> {
  if (!(await askConfirm({ title: `删除设定「${node.title}」？`, message: "它的子节点会接到它的父节点上，不会一起删掉。", danger: true, confirmLabel: "删除" }))) {
    return;
  }
  worldBookBusy.value = true;
  try {
    worldBookNodes.value = (await deleteWorldBookNode(node.id)).nodes ?? [];
    toastSuccess(`已删除设定「${node.title}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "世界书删除失败");
  } finally {
    worldBookBusy.value = false;
  }
}

function worldBookFileInputClick(): void {
  worldBookFileInput.value?.click();
}

// 不导 id 和 updated_at 的原值给人看，但父子引用要靠 id 重建，所以原样带上；
// 后端导入时会统一换新 ID 并按文件内的对照重连。
function exportWorldBook(): void {
  const content = JSON.stringify(
    {
      version: WORLD_BOOK_EXPORT_VERSION,
      exported_at: new Date().toISOString(),
      nodes: worldBookNodes.value.map((node) => ({
        id: node.id,
        parent_id: node.parent_id ?? "",
        title: node.title,
        content: node.content ?? "",
        keywords: node.keywords ?? [],
        secondary_keywords: node.secondary_keywords ?? [],
        always_on: node.always_on ?? false,
        enabled: node.enabled !== false
      }))
    },
    null,
    2
  );
  downloadJSONFile(`diana-world-book-${new Date().toISOString().slice(0, 10)}.json`, content);
}

async function importWorldBookFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;

  worldBookBusy.value = true;
  try {
    const parsed = JSON.parse(await file.text()) as unknown;
    // SillyTavern 的三种文件都认：世界书文件顶层是 entries 对象，角色卡把
    // 条目埋在 data.character_book.entries 或 character_book.entries 里。
    // 认出来就把 entries 原样交给后端转换，规则只维护一份。
    const record = (parsed ?? {}) as Record<string, any>;
    const stEntries = !Array.isArray(parsed)
      ? record.entries ?? record.data?.character_book?.entries ?? record.character_book?.entries
      : undefined;
    let result: WorldBookImportResult;
    if (stEntries && typeof stEntries === "object") {
      result = await importWorldBookSillyTavern(stEntries);
    } else {
      const list = Array.isArray(parsed)
        ? parsed
        : Array.isArray(record.nodes)
          ? (record.nodes as unknown[])
          : [parsed];
      result = await importWorldBook(list as WorldBookNode[]);
    }
    worldBookNodes.value = result.nodes ?? [];
    const notes = [`导入 ${result.imported} 条设定`];
    if (result.dropped) notes.push(`${result.dropped} 条无效已忽略`);
    toastSuccess(notes.join("，"));
  } catch (error) {
    toastError(error instanceof SyntaxError ? "这个文件不是有效的 JSON" : error instanceof Error ? error.message : "世界书导入失败");
  } finally {
    worldBookBusy.value = false;
  }
}

onMounted(() => {
  void loadWorldBook();
});
</script>
