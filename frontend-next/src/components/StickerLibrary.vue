<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="sticker-library">
    <div class="sticker-library-head">
      <div>
        <h3>表情包池</h3>
        <p>群友发过、已经收进池子的表情包。同一张图在多个会话出现只列一次；简介是模型第一次搜到它时写的。</p>
      </div>
      <span class="badge">{{ total }} 张</span>
    </div>

    <input
      v-model="query"
      class="input sticker-library-search"
      type="search"
      placeholder="按名称或简介搜索…"
      aria-label="搜索表情包"
    />

    <p v-if="error" class="sticker-library-error">{{ error }}</p>
    <div v-else-if="loading && items.length === 0" class="sticker-library-grid" aria-hidden="true">
      <div v-for="n in 8" :key="n" class="sticker-tile"><SkeletonBlock width="100%" height="96px" /></div>
    </div>
    <EmptyState
      v-else-if="items.length === 0"
      :title="query.trim() ? '没有匹配的表情包' : '池子里还没有表情包'"
      :hint="query.trim() ? '换个关键词试试。' : '群里有人发表情包后会自动收录。'"
    />
    <ul v-else class="sticker-library-grid">
      <li v-for="item in items" :key="item.hash" class="sticker-tile">
        <div v-if="failed[item.hash]" class="sticker-tile-image unavailable" :title="item.summary">
          <ImageOff :size="20" aria-hidden="true" />
          <span>文件已清理</span>
        </div>
        <button
          v-else
          class="sticker-tile-image"
          type="button"
          :title="item.description || item.summary"
          :aria-label="`${item.summary}，点击查看原图`"
          @click="active = item"
        >
          <img :src="stickerImageURL(item.hash, profile, true)" :alt="item.summary" loading="lazy" decoding="async" @error="failed = { ...failed, [item.hash]: true }" />
        </button>
        <strong class="sticker-tile-name">{{ item.summary }}</strong>
        <p v-if="item.description" class="sticker-tile-desc">{{ item.description }}</p>
        <span class="sticker-tile-meta">{{ origin(item) }} · {{ formatSeen(item.last_seen) }}</span>
      </li>
    </ul>

    <button v-if="items.length < total && !error" class="btn small ghost sticker-library-more" type="button" :disabled="loading" @click="load(false)">
      {{ loading ? "加载中…" : `加载更多（还有 ${total - items.length} 张）` }}
    </button>

    <Teleport to="body">
      <div v-if="active" class="sticker-lightbox" role="presentation" @click.self="active = null">
        <div class="sticker-lightbox-dialog" role="dialog" aria-modal="true" :aria-label="active.summary">
          <div class="sticker-lightbox-header">
            <span>{{ active.summary }}</span>
            <button class="btn small ghost icon-only" type="button" title="关闭" aria-label="关闭" @click="active = null"><X :size="16" aria-hidden="true" /></button>
          </div>
          <div class="sticker-lightbox-body">
            <img :src="stickerImageURL(active.hash, profile)" :alt="active.summary" />
            <p v-if="active.description">{{ active.description }}</p>
            <span class="sticker-tile-meta">{{ origin(active) }} · 最近出现 {{ formatSeen(active.last_seen) }}</span>
          </div>
        </div>
      </div>
    </Teleport>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ImageOff, X } from "@lucide/vue";
import EmptyState from "./EmptyState.vue";
import SkeletonBlock from "./SkeletonBlock.vue";
import { listStickerLibrary, stickerImageURL, type StickerLibraryItem } from "../api";

const props = defineProps<{ profile: string }>();

const pageSize = 48;
const items = ref<StickerLibraryItem[]>([]);
const total = ref(0);
const query = ref("");
const loading = ref(false);
const error = ref("");
const failed = ref<Record<string, boolean>>({});
const active = ref<StickerLibraryItem | null>(null);
let generation = 0;
let searchTimer: ReturnType<typeof setTimeout> | undefined;

async function load(reset: boolean): Promise<void> {
  const current = ++generation;
  loading.value = true;
  error.value = "";
  try {
    const page = await listStickerLibrary(props.profile, query.value, reset ? 0 : items.value.length, pageSize);
    if (current !== generation) return;
    items.value = reset ? page.items : [...items.value, ...page.items];
    total.value = page.total;
  } catch (err) {
    if (current !== generation) return;
    error.value = err instanceof Error ? err.message : "读取表情包池失败";
  } finally {
    if (current === generation) loading.value = false;
  }
}

function origin(item: StickerLibraryItem): string {
  const where = item.kind === "private" ? `私聊 ${item.user_id || ""}`.trim() : `群 ${item.group_id || ""}`.trim();
  return item.sessions > 1 ? `${where} 等 ${item.sessions} 个会话` : where;
}

function formatSeen(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

// 大图叠在插件设置弹窗上面。捕获阶段先拦下 Esc，只关大图，不让弹窗跟着一起关。
function onKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape" || !active.value) return;
  event.stopPropagation();
  active.value = null;
}

watch(query, () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void load(true), 250);
});
watch(() => props.profile, () => void load(true));

onMounted(() => {
  document.addEventListener("keydown", onKeydown, true);
  void load(true);
});
onBeforeUnmount(() => {
  clearTimeout(searchTimer);
  document.removeEventListener("keydown", onKeydown, true);
});
</script>

<style scoped>
.sticker-library {
  display: grid;
  gap: 12px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}

.sticker-library-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.sticker-library-head h3 {
  margin: 0 0 4px;
  font-size: 14px;
}

.sticker-library-head p {
  margin: 0;
  color: var(--muted);
  font-size: 12.5px;
}

.sticker-library-error {
  margin: 0;
  color: var(--err);
  font-size: 13px;
}

.sticker-library-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.sticker-tile {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 4px;
}

.sticker-tile-image {
  display: grid;
  height: 96px;
  place-items: center;
  overflow: hidden;
  padding: 4px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-raised);
  cursor: zoom-in;
}

.sticker-tile-image img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}

.sticker-tile-image.unavailable {
  gap: 4px;
  color: var(--muted);
  font-size: 12px;
  cursor: default;
}

.sticker-tile-name {
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sticker-tile-desc {
  display: -webkit-box;
  margin: 0;
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 12px;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.sticker-tile-meta {
  color: var(--muted);
  font-size: 11.5px;
}

.sticker-library-more {
  justify-self: center;
}

.sticker-lightbox {
  position: fixed;
  inset: 0;
  z-index: 240;
  display: grid;
  place-items: center;
  padding: 20px;
  background: rgba(10, 8, 11, 0.82);
  backdrop-filter: blur(4px);
}

.sticker-lightbox-dialog {
  display: flex;
  width: min(560px, 100%);
  max-height: calc(100dvh - 40px);
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--surface);
  box-shadow: var(--shadow-lg);
}

.sticker-lightbox-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 10px 8px 16px;
  border-bottom: 1px solid var(--border);
}

.sticker-lightbox-header > span {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sticker-lightbox-body {
  display: grid;
  justify-items: center;
  gap: 10px;
  overflow: auto;
  padding: 16px;
}

.sticker-lightbox-body img {
  max-width: 100%;
  max-height: 60dvh;
  object-fit: contain;
}

.sticker-lightbox-body p {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13px;
}
</style>
