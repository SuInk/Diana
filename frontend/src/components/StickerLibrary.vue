<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="sticker-library">
    <div class="sticker-toolbar">
      <input
        v-model="query"
        class="input sticker-library-search"
        type="search"
        placeholder="搜索表情包…"
        aria-label="搜索表情包"
      />
      <AppSelect v-model="sourceValue" class="sticker-toolbar-select" :options="sourceOptions" searchable search-placeholder="按群名或群号找…" />
      <AppSelect v-model="idleValue" class="sticker-toolbar-select" :options="idleOptions" />
      <AppSelect v-model="sortValue" class="sticker-toolbar-select" :options="sortOptions" />
    </div>

    <div class="sticker-bar">
      <div class="sticker-chips" role="group" aria-label="按画风筛选">
        <button class="sticker-category" :class="{ active: !filter.category }" type="button" @click="patch({ category: undefined })">全部</button>
        <button
          v-for="item in facets.categories"
          :key="item.value"
          class="sticker-category"
          :class="{ active: filter.category === item.value }"
          type="button"
          @click="toggle('category', item.value)"
        >
          {{ categoryLabel(item.value) }}<span>{{ item.count }}</span>
        </button>
        <button v-if="filter.tag" class="sticker-category active" type="button" :title="`取消关键词「${filter.tag}」`" @click="patch({ tag: undefined })">
          #{{ filter.tag }}<X :size="12" aria-hidden="true" />
        </button>
      </div>
      <div class="sticker-bar-end">
        <span class="sticker-count">{{ total }} 张</span>
        <button v-if="filtering" class="btn small ghost" type="button" @click="resetFilters">清空筛选</button>
        <button class="btn small ghost danger-text" type="button" :disabled="!total || cleaning" :title="filtering ? '把筛出来的这些移出表情包池' : '清空整个表情包池'" @click="cleanup">
          <Trash2 :size="14" aria-hidden="true" />{{ cleaning ? "清理中…" : filtering ? "清理这些" : "清空" }}
        </button>
      </div>
    </div>

    <p v-if="error" class="sticker-library-error">{{ error }}</p>
    <div v-else-if="loading && items.length === 0" class="sticker-library-grid" aria-hidden="true">
      <div v-for="n in 8" :key="n" class="sticker-tile"><SkeletonBlock width="100%" height="96px" /></div>
    </div>
    <EmptyState
      v-else-if="items.length === 0"
      :title="filtering ? '没有匹配的表情包' : '池子里还没有表情包'"
      :hint="filtering ? '换个条件试试。' : '群里有人发表情包后会自动收录。'"
    />
    <ul v-else class="sticker-library-grid">
      <li v-for="(item, index) in items" :key="item.hash" class="sticker-tile">
        <div class="sticker-tile-frame">
          <button
            class="sticker-tile-image"
            :class="{ unavailable: failed[item.hash] }"
            type="button"
            :title="item.description || item.summary"
            :aria-label="`${item.summary}，点击查看大图`"
            @click="activeIndex = index"
          >
            <template v-if="failed[item.hash]">
              <ImageOff :size="20" aria-hidden="true" />
              <span>文件已清理</span>
            </template>
            <img v-else :src="imageURL(item.hash, true)" :alt="item.summary" loading="lazy" decoding="async" @error="markFailed(item.hash)" />
          </button>
          <span v-if="item.category" class="sticker-tile-category">{{ item.category }}</span>
          <button
            class="sticker-tile-delete"
            type="button"
            title="移出表情包池"
            :aria-label="`把「${item.summary}」移出表情包池`"
            :disabled="deleting === item.hash"
            @click="remove(item)"
          >
            <Trash2 :size="14" aria-hidden="true" />
          </button>
        </div>
        <strong class="sticker-tile-name">{{ item.summary }}</strong>
        <div v-if="item.tags?.length" class="sticker-tile-tags">
          <button
            v-for="tag in item.tags.slice(0, 3)"
            :key="tag"
            class="sticker-tag"
            :class="{ active: filter.tag === tag }"
            type="button"
            :title="`只看「${tag}」`"
            @click="toggle('tag', tag)"
          >
            {{ tag }}
          </button>
        </div>
      </li>
    </ul>

    <button v-if="items.length < total && !error" class="btn small ghost sticker-library-more" type="button" :disabled="loading" @click="load(false)">
      {{ loading ? "加载中…" : `加载更多（还有 ${total - items.length} 张）` }}
    </button>

    <Teleport to="body">
      <div v-if="active" class="sticker-lightbox" role="presentation" @click.self="activeIndex = -1">
        <div class="sticker-lightbox-dialog" role="dialog" aria-modal="true" :aria-label="active.summary">
          <div class="sticker-lightbox-header">
            <span>{{ active.summary }}</span>
            <span class="sticker-lightbox-position">{{ activeIndex + 1 }} / {{ total }}</span>
            <button class="btn small ghost icon-only" type="button" title="关闭" aria-label="关闭" @click="activeIndex = -1"><X :size="16" aria-hidden="true" /></button>
          </div>
          <div class="sticker-lightbox-body">
            <div class="sticker-lightbox-stage">
              <button class="sticker-lightbox-nav" type="button" title="上一张（←）" aria-label="上一张" :disabled="activeIndex <= 0" @click="step(-1)">
                <ChevronLeft :size="20" aria-hidden="true" />
              </button>
              <div v-if="failed[active.hash]" class="sticker-lightbox-missing">
                <ImageOff :size="28" aria-hidden="true" />
                <span>图片文件已被清理，只剩标注信息</span>
              </div>
              <img v-else :src="imageURL(active.hash)" :alt="active.summary" @error="markFailed(active.hash)" />
              <button class="sticker-lightbox-nav" type="button" title="下一张（→）" aria-label="下一张" :disabled="activeIndex >= total - 1" @click="step(1)">
                <ChevronRight :size="20" aria-hidden="true" />
              </button>
            </div>
            <dl class="sticker-lightbox-facts">
              <dt>画风</dt>
              <dd>
                <button v-if="active.category" class="sticker-tag" type="button" @click="jump({ category: active.category })">{{ active.category }}</button>
                <span v-else class="sticker-tile-meta">还没判出来</span>
              </dd>
              <dt>关键词</dt>
              <dd>
                <div v-if="active.tags?.length" class="sticker-tile-tags">
                  <button v-for="tag in active.tags" :key="tag" class="sticker-tag" type="button" :title="`只看「${tag}」`" @click="jump({ tag })">{{ tag }}</button>
                </div>
                <span v-else class="sticker-tile-meta">未标注</span>
              </dd>
              <template v-if="active.description">
                <dt>简介</dt>
                <dd>{{ active.description }}</dd>
              </template>
              <dt>来源</dt>
              <dd>{{ origin(active) }}</dd>
              <dt>最近出现</dt>
              <dd>{{ formatTime(active.last_seen) }}</dd>
              <dt>机器人发过</dt>
              <dd>{{ active.sent_count ? `${active.sent_count} 次，最近 ${formatTime(active.last_sent ?? "")}` : "没发过" }}</dd>
            </dl>
            <button class="btn small danger" type="button" :disabled="deleting === active.hash" @click="remove(active)">
              <Trash2 :size="14" aria-hidden="true" />移出表情包池
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ChevronLeft, ChevronRight, ImageOff, Trash2, X } from "@lucide/vue";
import AppSelect, { type AppSelectOption } from "./AppSelect.vue";
import EmptyState from "./EmptyState.vue";
import SkeletonBlock from "./SkeletonBlock.vue";
import { askConfirm } from "../confirm";
import { demoStickerImage } from "../demo-sticker-image";
import { toastError, toastSuccess } from "../toast";
import {
  cleanupStickers,
  deleteSticker,
  listBotGroups,
  listStickerFacets,
  listStickerLibrary,
  stickerImageURL,
  stickerUncategorized,
  type StickerLibraryFacets,
  type StickerLibraryFilter,
  type StickerLibraryItem
} from "../api";

const props = defineProps<{ profile: string }>();

const pageSize = 48;
const items = ref<StickerLibraryItem[]>([]);
const total = ref(0);
const query = ref("");
const filter = ref<StickerLibraryFilter>({});
const facets = ref<StickerLibraryFacets>({ categories: [], sources: [], tags: [] });
const groupNames = ref<Record<string, string>>({});
const loading = ref(false);
const cleaning = ref(false);
const error = ref("");
const failed = ref<Record<string, boolean>>({});
const activeIndex = ref(-1);
const deleting = ref("");
let generation = 0;
let searchTimer: ReturnType<typeof setTimeout> | undefined;

const active = computed(() => items.value[activeIndex.value] ?? null);
const currentFilter = computed<StickerLibraryFilter>(() => ({ ...filter.value, q: query.value }));
const filtering = computed(() => {
  const value = filter.value;
  return Boolean(query.value.trim() || value.category || value.tag || value.source || value.idle_days || value.never_sent);
});
const sortOptions: AppSelectOption[] = [
  { value: "recent", label: "最近出现" },
  { value: "idle", label: "最久没用" },
  { value: "most_sent", label: "发得最多" }
];
const idleOptions: AppSelectOption[] = [
  { value: "0", label: "不限时间" },
  { value: "7", label: "7 天没用过" },
  { value: "30", label: "30 天没用过" },
  { value: "90", label: "90 天没用过" },
  { value: "180", label: "半年没用过" },
  { value: "never", label: "机器人没发过" }
];
const sourceOptions = computed<AppSelectOption[]>(() => {
  const sources = [...facets.value.sources];
  // 选中的来源清理完就不在统计里了，照样列出来，不然下拉框只能显示原始值。
  const selected = filter.value.source;
  if (selected && !sources.some((item) => item.value === selected)) sources.push({ value: selected, count: 0 });
  return [
    { value: "", label: "全部来源" },
    ...sources.map((item) => ({ value: item.value, label: sourceLabel(item.value), hint: `${item.count} 张` }))
  ];
});
const sortValue = computed({
  get: () => filter.value.sort ?? "recent",
  set: (value: string) => patch({ sort: value === "recent" ? undefined : (value as StickerLibraryFilter["sort"]) })
});
// 「机器人没发过」和「多久没用过」放在同一个下拉里，少占一格。
const idleValue = computed({
  get: () => (filter.value.never_sent ? "never" : String(filter.value.idle_days ?? 0)),
  set: (value: string) => patch(value === "never" ? { never_sent: true, idle_days: undefined } : { never_sent: undefined, idle_days: Number(value) || undefined })
});
const sourceValue = computed({
  get: () => filter.value.source ?? "",
  set: (value: string) => patch({ source: value || undefined })
});

async function load(reset: boolean): Promise<void> {
  const current = ++generation;
  loading.value = true;
  error.value = "";
  if (reset) void loadFacets();
  try {
    const page = await listStickerLibrary(props.profile, currentFilter.value, reset ? 0 : items.value.length, pageSize);
    if (current !== generation) return;
    items.value = reset ? page.items : [...items.value, ...page.items];
    total.value = page.total;
    if (reset) activeIndex.value = -1;
  } catch (err) {
    if (current !== generation) return;
    error.value = err instanceof Error ? err.message : "读取表情包池失败";
  } finally {
    if (current === generation) loading.value = false;
  }
}

// 分类栏读失败不挡浏览，只是不显示。
async function loadFacets(): Promise<void> {
  try {
    facets.value = await listStickerFacets(props.profile, currentFilter.value);
  } catch {
    facets.value = { categories: [], sources: [], tags: [] };
  }
}

// 来源栏显示群名；取不到就显示群号。
async function loadGroupNames(): Promise<void> {
  try {
    const groups = (await listBotGroups(false, props.profile)).groups ?? [];
    groupNames.value = Object.fromEntries(groups.filter((group) => group.group_name).map((group) => [group.group_id, group.group_name as string]));
  } catch {
    groupNames.value = {};
  }
}

function patch(next: Partial<StickerLibraryFilter>): void {
  const merged: StickerLibraryFilter = { ...filter.value, ...next };
  for (const key of Object.keys(merged) as (keyof StickerLibraryFilter)[]) {
    if (merged[key] === undefined) delete merged[key];
  }
  filter.value = merged;
  void load(true);
}

// 再点一次当前选项等于取消这一栏的筛选。
function toggle(key: "category" | "tag", value: string): void {
  patch({ [key]: filter.value[key] === value ? undefined : value });
}

function jump(next: Partial<StickerLibraryFilter>): void {
  activeIndex.value = -1;
  patch(next);
}

function resetFilters(): void {
  query.value = "";
  clearTimeout(searchTimer);
  filter.value = filter.value.sort ? { sort: filter.value.sort } : {};
  void load(true);
}

function step(delta: number): void {
  const next = activeIndex.value + delta;
  if (next < 0 || next >= total.value) return;
  // 翻到已加载的末尾时顺手加载下一页。
  if (next >= items.value.length) {
    void load(false).then(() => {
      if (next < items.value.length) activeIndex.value = next;
    });
    return;
  }
  activeIndex.value = next;
}

const demoImages = import.meta.env.VITE_DEMO_MODE === "true";

function imageURL(hash: string, thumbnail = false): string {
  return demoImages ? demoStickerImage(hash) : stickerImageURL(hash, props.profile, thumbnail);
}

function markFailed(hash: string): void {
  failed.value = { ...failed.value, [hash]: true };
}

async function remove(item: StickerLibraryItem): Promise<void> {
  // 确认框在大图下面一层，先关大图。
  activeIndex.value = -1;
  const ok = await askConfirm({
    title: "移出表情包池？",
    message: `「${item.summary}」会从${props.profile ? "这个机器人" : "全部机器人"}的表情包池里删掉，以后再有人发同一张也不再收录。聊天记录里的图片不受影响。`,
    confirmLabel: "移出",
    danger: true
  });
  if (!ok) return;
  deleting.value = item.hash;
  try {
    await deleteSticker(item.hash, props.profile);
    items.value = items.value.filter((entry) => entry.hash !== item.hash);
    total.value = Math.max(0, total.value - 1);
    toastSuccess(`已把「${item.summary}」移出表情包池`);
    void loadFacets();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "删除表情包失败");
  } finally {
    deleting.value = "";
  }
}

// 先试算一遍拿到真实张数再确认，免得界面上的数字和实际删的对不上。
async function cleanup(): Promise<void> {
  if (cleaning.value) return;
  cleaning.value = true;
  try {
    const target = currentFilter.value;
    const preview = await cleanupStickers(props.profile, target, { block: false, dryRun: true });
    if (!preview.stickers) {
      toastSuccess("没有符合条件的表情包");
      return;
    }
    const scope = target.source ? `（只移出「${sourceLabel(target.source)}」里的记录，同一张图在别处的还留着）` : "";
    const ok = await askConfirm({
      title: filtering.value ? `清理筛出的 ${preview.stickers} 张表情包？` : `清空全部 ${preview.stickers} 张表情包？`,
      message: `${describeFilter(target)}会从${props.profile ? "这个机器人" : "全部机器人"}的表情包池里移出${scope}。以后再有人发还会重新收录，想彻底不要某张请单独删除。聊天记录里的图片不受影响。`,
      confirmLabel: "清理",
      danger: true
    });
    if (!ok) return;
    const result = await cleanupStickers(props.profile, target, { block: false, dryRun: false });
    toastSuccess(`已清理 ${result.stickers} 张表情包`);
    await load(true);
  } catch (err) {
    toastError(err instanceof Error ? err.message : "清理表情包失败");
  } finally {
    cleaning.value = false;
  }
}

function describeFilter(value: StickerLibraryFilter): string {
  const parts: string[] = [];
  if (value.category) parts.push(`画风「${categoryLabel(value.category)}」`);
  if (value.tag) parts.push(`关键词「${value.tag}」`);
  if (value.source) parts.push(`来源「${sourceLabel(value.source)}」`);
  if (value.idle_days) parts.push(`${value.idle_days} 天没用过`);
  if (value.never_sent) parts.push("机器人没发过");
  if (value.q?.trim()) parts.push(`搜索「${value.q.trim()}」`);
  return parts.length ? `符合${parts.join("、")}的表情包` : "池子里的全部表情包";
}

function categoryLabel(value: string): string {
  return value === stickerUncategorized ? "未分类" : value;
}

function sourceLabel(value: string): string {
  if (value === "private") return "私聊";
  const groupID = value.replace(/^group:/, "");
  return groupNames.value[groupID] ? `${groupNames.value[groupID]}（${groupID}）` : `群 ${groupID}`;
}

function origin(item: StickerLibraryItem): string {
  const where = item.kind === "private" ? "私聊" : sourceLabel(`group:${item.group_id || ""}`);
  return item.sessions > 1 ? `${where} 等 ${item.sessions} 个会话` : where;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return value && !Number.isNaN(date.getTime()) ? date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }) : "";
}

// 大图叠在插件设置弹窗上面。捕获阶段先拦下 Esc 和方向键，只作用于大图，不让弹窗跟着一起关。
function onKeydown(event: KeyboardEvent): void {
  if (!active.value) return;
  if (event.key === "Escape") activeIndex.value = -1;
  else if (event.key === "ArrowLeft") step(-1);
  else if (event.key === "ArrowRight") step(1);
  else return;
  event.stopPropagation();
  event.preventDefault();
}

watch(query, () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void load(true), 250);
});
watch(() => props.profile, () => {
  filter.value = {};
  void load(true);
  void loadGroupNames();
});

onMounted(() => {
  document.addEventListener("keydown", onKeydown, true);
  void load(true);
  void loadGroupNames();
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
}

.sticker-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.sticker-toolbar .sticker-library-search {
  min-width: 180px;
  flex: 1;
}

.sticker-toolbar-select {
  width: 124px;
  flex-shrink: 0;
}

.sticker-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.sticker-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.sticker-bar-end {
  display: flex;
  align-items: center;
  gap: 4px;
}

.sticker-count {
  margin-right: 4px;
  color: var(--muted);
  font-size: 12.5px;
}

.danger-text {
  color: var(--err);
}

.sticker-category,
.sticker-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 100%;
  overflow: hidden;
  border: 1px solid var(--border);
  background: var(--bg-raised);
  color: var(--text-secondary);
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.sticker-category {
  padding: 3px 10px;
  border-radius: 999px;
  font-size: 12.5px;
}

.sticker-category span {
  color: var(--muted);
  font-size: 11.5px;
}

.sticker-tag {
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11.5px;
}

.sticker-category:hover,
.sticker-tag:hover {
  border-color: var(--accent);
  color: var(--accent-strong);
}

.sticker-category.active,
.sticker-tag.active {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.sticker-category.active span {
  color: inherit;
}

.sticker-library-error {
  margin: 0;
  color: var(--err);
  font-size: 13px;
}

.sticker-library-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(132px, 1fr));
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

.sticker-tile-frame {
  position: relative;
}

.sticker-tile-image {
  display: grid;
  width: 100%;
  height: 96px;
  place-items: center;
  overflow: hidden;
  padding: 4px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-raised);
  cursor: zoom-in;
}

.sticker-tile-image:hover {
  border-color: var(--accent);
}

.sticker-tile-image img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}

.sticker-tile-image.unavailable {
  align-content: center;
  gap: 4px;
  color: var(--muted);
  font-size: 12px;
}

.sticker-tile-category {
  position: absolute;
  top: 4px;
  left: 4px;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--accent-soft);
  color: var(--accent-strong);
  font-size: 11px;
  line-height: 18px;
  pointer-events: none;
}

.sticker-tile-delete {
  position: absolute;
  top: 4px;
  right: 4px;
  display: grid;
  width: 24px;
  height: 24px;
  place-items: center;
  padding: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--muted);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.12s ease;
}

.sticker-tile:hover .sticker-tile-delete,
.sticker-tile-delete:focus-visible {
  opacity: 1;
}

/* 触屏没有悬停，删除按钮常驻。 */
@media (hover: none) {
  .sticker-tile-delete {
    opacity: 1;
  }
}

.sticker-tile-delete:hover {
  border-color: var(--err);
  color: var(--err);
}

.sticker-tile-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.sticker-tile-name {
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  width: min(720px, 100%);
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

.sticker-lightbox-header > span:first-child {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sticker-lightbox-position {
  color: var(--muted);
  font-size: 12px;
}

.sticker-lightbox-body {
  display: grid;
  justify-items: center;
  gap: 14px;
  overflow: auto;
  padding: 16px;
}

.sticker-lightbox-stage {
  display: grid;
  width: 100%;
  grid-template-columns: 36px minmax(0, 1fr) 36px;
  align-items: center;
  justify-items: center;
  gap: 8px;
}

.sticker-lightbox-stage img {
  max-width: 100%;
  max-height: 60dvh;
  min-height: 160px;
  object-fit: contain;
}

.sticker-lightbox-missing {
  display: grid;
  min-height: 160px;
  place-items: center;
  align-content: center;
  gap: 8px;
  color: var(--muted);
  font-size: 13px;
}

.sticker-lightbox-nav {
  display: grid;
  width: 36px;
  height: 36px;
  place-items: center;
  padding: 0;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-raised);
  color: var(--text-secondary);
  cursor: pointer;
}

.sticker-lightbox-nav:disabled {
  opacity: 0.35;
  cursor: default;
}

.sticker-lightbox-facts {
  display: grid;
  width: 100%;
  grid-template-columns: 72px minmax(0, 1fr);
  gap: 6px 12px;
  margin: 0;
  font-size: 13px;
}

.sticker-lightbox-facts dt {
  color: var(--muted);
}

.sticker-lightbox-facts dd {
  margin: 0;
  color: var(--text-secondary);
}

@media (max-width: 560px) {
  .sticker-toolbar .sticker-library-search {
    flex-basis: 100%;
  }

  .sticker-toolbar-select {
    width: auto;
    min-width: 0;
    flex: 1;
  }

  .sticker-lightbox-stage {
    grid-template-columns: 28px minmax(0, 1fr) 28px;
  }
}
</style>
