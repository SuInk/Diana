<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  自述：只读加删除。写入只有她自己能做，人代笔想加的内容属于 SOUL.md。
  开关留在机器人配置里，和其他行为开关放在一起；这里只看她写了什么。
-->
<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h2>自述</h2>
        <p>她自己记下的关于自己的观察：说话习惯、偏好和毛病，跨群生效。人只能看和删，写只有她自己能写。</p>
      </div>
      <div class="view-actions">
        <button class="btn ghost" type="button" :disabled="busy" @click="reload">
          <RefreshCw :size="15" aria-hidden="true" />
          刷新
        </button>
        <button class="btn ghost danger" type="button" :disabled="busy || !notes.length" @click="clearAll">
          <Trash2 :size="15" aria-hidden="true" />
          清空
        </button>
      </div>
    </header>

    <LoadingSkeleton v-if="busy && !loaded" kind="notebook" :count="3" label="正在加载自述" />

    <div v-else-if="!enabled" class="card">
      <div class="card-body self-notes-off">
        <strong>自述还没开</strong>
        <span class="hint">到「机器人 → 人设」里打开「允许它写自述」并保存。</span>
        <button class="btn small" type="button" @click="navigate('bot')">去打开</button>
      </div>
    </div>

    <EmptyState v-else-if="!notes.length" title="还没有写过自述" hint="她在相处中注意到关于自己的事时会自己记一条。">
      <template #icon><NotebookPen :size="20" aria-hidden="true" /></template>
    </EmptyState>

    <section v-else class="card">
      <div class="card-body">
        <ul class="self-note-rows">
          <li v-for="note in notes" :key="note.id">
            <div class="self-note-row-main">
              <span>{{ note.content }}</span>
              <span class="muted">{{ note.topic }} · {{ source(note) }}</span>
            </div>
            <button class="btn small ghost" type="button" :disabled="busy" aria-label="删除这条自述" @click="remove(note.id)">
              <X :size="14" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { NotebookPen, RefreshCw, Trash2, X } from "@lucide/vue";
import { deleteSelfNote, listSelfNotes, purgeSelfNotes, type SelfNote } from "../api";
import { botScope } from "../bot-scope";
import { formatClock } from "../format";
import { navigate } from "../router";
import { toastError, toastSuccess } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";

const notes = ref<SelfNote[]>([]);
const enabled = ref(false);
const busy = ref(false);
const loaded = ref(false);

async function run(action: () => Promise<{ notes?: SelfNote[]; enabled: boolean }>, success = ""): Promise<void> {
  busy.value = true;
  try {
    const result = await action();
    notes.value = result.notes ?? [];
    enabled.value = result.enabled;
    if (success) toastSuccess(success);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    busy.value = false;
    loaded.value = true;
  }
}

function reload(): Promise<void> {
  return run(() => listSelfNotes(botScope.value));
}

function remove(id: string): Promise<void> {
  return run(() => deleteSelfNote(botScope.value, id), "已删除这条自述");
}

async function clearAll(): Promise<void> {
  if (!window.confirm("清空她写下的全部自述？删掉之后她要重新观察才会再记。")) return;
  await run(() => purgeSelfNotes(botScope.value), "自述已清空");
}

// source 说明这条是在哪、谁在场时记下的。自述跨群生效，来源是主人事后判断
// 「这句话是谁哄着她写的」的唯一线索。
function source(note: SelfNote): string {
  const parts: string[] = [];
  if (note.source_group_id) parts.push(`群 ${note.source_group_id}`);
  else parts.push("私聊");
  if (note.source_user_name || note.source_user_id) parts.push(note.source_user_name || note.source_user_id || "");
  if (note.created_at) parts.push(formatClock(note.created_at));
  return parts.filter(Boolean).join(" · ");
}

onMounted(() => void reload());
// 自述按机器人隔离，换一台时上一台的列表留在屏幕上会让人以为这台也写过。
watch(botScope, () => void reload());
</script>

<style scoped>
.self-notes-off {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}

/* 和钱包档的账目同一种行：正文在上，类别和来源一行小字在下，分隔线隔开。 */
.self-note-rows {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
}

.self-note-rows li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--border, rgba(0, 0, 0, 0.08));
}

.self-note-rows li:first-child,
.self-note-rows li:first-child {
  border-top: none;
}

.self-note-row-main {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.self-note-row-main .muted {
  font-size: 12px;
}
</style>
