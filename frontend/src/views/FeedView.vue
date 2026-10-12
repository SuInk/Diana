<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h2>动态</h2>
        <p>
          机器人自己发的动态和日记，可以配图。动态由它在对话里写下，只有主人让它发时才会发，这里不能代笔，也不会发到任何群或私聊。
          你可以在动态下评论和回复；它翻动态时会看到评论，并可以回复你。
        </p>
      </div>
      <div class="view-actions">
        <button class="btn ghost" type="button" :disabled="loading" @click="reload">
          <RefreshCw :size="15" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <div class="cluster" style="margin-bottom: 12px; gap: 8px">
      <button
        v-for="option in kindOptions"
        :key="option.value"
        class="btn small"
        :class="{ ghost: kind !== option.value }"
        type="button"
        :aria-pressed="kind === option.value"
        @click="setKind(option.value)"
      >
        {{ option.label }}
      </button>
    </div>

    <LoadingSkeleton v-if="loading && posts.length === 0" kind="notebook" :count="3" label="正在加载动态" />

    <EmptyState
      v-else-if="posts.length === 0"
      title="还没有动态"
      hint="让机器人「写一篇今天的日记」或「发条动态」，它写好之后会出现在这里。"
    >
      <template #icon><Newspaper :size="20" aria-hidden="true" /></template>
    </EmptyState>

    <div v-else class="feed-list">
      <article v-for="post in posts" :key="post.id" class="card feed-post">
        <div class="feed-row">
          <span class="feed-avatar" aria-hidden="true">{{ initial(authorName(post.profile_id)) }}</span>
          <div class="feed-main">
            <div class="feed-meta">
              <strong class="feed-name">{{ authorName(post.profile_id) }}</strong>
              <span class="badge" :class="{ kind: post.kind === 'diary' }">{{ post.kind === "diary" ? "日记" : "动态" }}</span>
              <span class="muted feed-time" :title="formatTime(post.created_at)">· {{ formatRelative(post.created_at) }}</span>
              <button
                class="btn ghost small icon-only feed-delete"
                type="button"
                aria-label="删除这条动态"
                :disabled="deleting === post.id"
                @click="remove(post)"
              >
                <Trash2 :size="14" aria-hidden="true" />
              </button>
            </div>
            <h3 v-if="post.title" class="feed-title">{{ post.title }}</h3>
            <p class="feed-content">{{ post.content }}</p>
            <div v-if="post.images.length > 0" class="feed-images" :class="`feed-images-${Math.min(post.images.length, 3)}`">
              <button
                v-for="image in post.images"
                :key="image.id"
                class="feed-image"
                type="button"
                :aria-label="`查看第 ${image.position + 1} 张图片`"
                @click="preview = image"
              >
                <img :src="imageSrc(image)" :width="image.width || undefined" :height="image.height || undefined" loading="lazy" alt="" />
              </button>
            </div>
            <div class="feed-actions">
              <button
                class="feed-action"
                type="button"
                :aria-expanded="expanded.has(post.id)"
                :aria-label="`评论，共 ${post.comments.length} 条`"
                @click="toggle(post.id)"
              >
                <MessageCircle :size="16" aria-hidden="true" />
                <span>{{ post.comments.length || "评论" }}</span>
              </button>
              <button
                class="feed-action feed-like"
                :class="{ liked: post.liked_by_admin }"
                type="button"
                :aria-pressed="post.liked_by_admin"
                :aria-label="post.liked_by_admin ? '取消点赞' : '点赞'"
                :title="post.liked_by_bot ? '它也给自己点了赞' : ''"
                :disabled="liking === post.id"
                @click="toggleLike(post)"
              >
                <Heart :size="16" :fill="post.liked_by_admin ? 'currentColor' : 'none'" aria-hidden="true" />
                <span>{{ post.like_count || "赞" }}</span>
              </button>
            </div>
          </div>
        </div>

        <section v-if="expanded.has(post.id)" class="feed-thread" :aria-label="`${authorName(post.profile_id)} 的动态评论`">
          <div v-for="comment in topLevel(post)" :key="comment.id" class="feed-comment-group">
            <div class="feed-comment">
              <span class="feed-avatar small" :class="comment.author_kind" aria-hidden="true">{{ commentInitial(post, comment) }}</span>
              <div class="feed-comment-body">
                <div class="feed-meta">
                  <strong>{{ commentAuthor(post, comment.author_kind) }}</strong>
                  <span class="muted feed-time" :title="formatTime(comment.created_at)">{{ formatRelative(comment.created_at) }}</span>
                </div>
                <p class="feed-comment-text">{{ comment.content }}</p>
                <div class="feed-comment-actions">
                  <button class="feed-link" type="button" @click="startReply(post, comment)">回复</button>
                  <button class="feed-link danger" type="button" :disabled="deleting === comment.id" @click="removeComment(post, comment)">删除</button>
                </div>
              </div>
            </div>
            <div v-for="reply in repliesOf(post, comment)" :key="reply.id" class="feed-comment feed-reply">
              <span class="feed-avatar small" :class="reply.author_kind" aria-hidden="true">{{ commentInitial(post, reply) }}</span>
              <div class="feed-comment-body">
                <div class="feed-meta">
                  <strong>{{ commentAuthor(post, reply.author_kind) }}</strong>
                  <span v-if="reply.reply_to_author && reply.reply_to_id !== comment.id" class="muted feed-time">
                    回复 {{ commentAuthor(post, reply.reply_to_author) }}
                  </span>
                  <span class="muted feed-time" :title="formatTime(reply.created_at)">{{ formatRelative(reply.created_at) }}</span>
                </div>
                <p class="feed-comment-text">{{ reply.content }}</p>
                <div class="feed-comment-actions">
                  <button class="feed-link" type="button" @click="startReply(post, reply)">回复</button>
                  <button class="feed-link danger" type="button" :disabled="deleting === reply.id" @click="removeComment(post, reply)">删除</button>
                </div>
              </div>
            </div>
          </div>
          <p v-if="replyPending.has(post.id)" class="muted feed-empty-comments" role="status">它正在写回复…</p>
          <p v-else-if="post.comments.length === 0" class="muted feed-empty-comments">还没有评论，来说第一句吧。</p>

          <form class="feed-composer" @submit.prevent="submitComment(post)">
            <p v-if="replyTarget[post.id]" class="feed-replying">
              回复 {{ commentAuthor(post, replyTarget[post.id].author_kind) }}：{{ excerpt(replyTarget[post.id].content) }}
              <button class="feed-link" type="button" @click="delete replyTarget[post.id]">取消</button>
            </p>
            <div class="feed-composer-row">
              <textarea
                v-model="drafts[post.id]"
                class="input feed-input"
                rows="2"
                maxlength="500"
                :placeholder="replyTarget[post.id] ? '写下你的回复…' : '写下你的评论…（机器人会在下次翻动态时看到）'"
                :aria-label="`评论 ${authorName(post.profile_id)} 的动态`"
                @keydown.enter.meta.prevent="submitComment(post)"
                @keydown.enter.ctrl.prevent="submitComment(post)"
              ></textarea>
              <button class="btn small" type="submit" :disabled="!(drafts[post.id] ?? '').trim() || sending === post.id">
                <Send :size="14" aria-hidden="true" />
                发送
              </button>
            </div>
          </form>
        </section>
      </article>

      <div v-if="nextBefore" class="feed-more">
        <button class="btn ghost" type="button" :disabled="loading" @click="loadMore">加载更早的</button>
      </div>
    </div>

    <Modal v-if="preview" title="查看图片" wide @close="preview = null">
      <img class="feed-preview" :src="imageSrc(preview)" alt="" />
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Heart, MessageCircle, Newspaper, RefreshCw, Send, Trash2 } from "@lucide/vue";
import {
  addFeedComment,
  deleteFeedComment,
  deleteFeedPost,
  feedImageURL,
  getBotProfileConfig,
  likeFeedPost,
  listFeed,
  type FeedComment,
  type FeedImage,
  type FeedKind,
  type FeedPost
} from "../api";
import { demoFeedImage } from "../demo-sticker-image";
import { formatRelative, formatTime } from "../format";
import { botScope } from "../bot-scope";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import EmptyState from "../components/EmptyState.vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import Modal from "../components/Modal.vue";

const kindOptions: { value: FeedKind | ""; label: string }[] = [
  { value: "", label: "全部" },
  { value: "post", label: "动态" },
  { value: "diary", label: "日记" }
];

// 演示模式没有真实图片，<img> 又绕过 fetch 拦截，直接用生成的示意图。
const demoImages = import.meta.env.VITE_DEMO_MODE === "true";

const posts = ref<FeedPost[]>([]);
const nextBefore = ref("");
const kind = ref<FeedKind | "">("");
const loading = ref(true);
const deleting = ref("");
const preview = ref<FeedImage | null>(null);
const profileNames = ref<Record<string, string>>({});
const expanded = ref<Set<string>>(new Set());
const drafts = reactive<Record<string, string>>({});
const replyTarget = reactive<Record<string, FeedComment>>({});
const sending = ref("");
const liking = ref("");
// 开了自动回复时，评论发出后机器人在后台写回复；这里记着哪些动态在等，并轮询到回复出现为止。
const replyPending = ref<Set<string>>(new Set());
const REPLY_POLL_MS = 2500;
const REPLY_POLL_LIMIT = 30;
const pollTimers = new Map<string, number>();

function setPending(id: string, on: boolean): void {
  const next = new Set(replyPending.value);
  if (on) next.add(id);
  else next.delete(id);
  replyPending.value = next;
}

function stopPolling(id: string): void {
  const timer = pollTimers.get(id);
  if (timer !== undefined) window.clearTimeout(timer);
  pollTimers.delete(id);
  setPending(id, false);
}

function pollForReply(post: FeedPost, knownCount: number, attempt = 0): void {
  setPending(post.id, true);
  const timer = window.setTimeout(async () => {
    try {
      const result = await listFeed(botScope.value, kind.value);
      const fresh = result.posts.find((item) => item.id === post.id);
      const target = posts.value.find((item) => item.id === post.id);
      if (fresh && target && fresh.comments.length > knownCount) {
        target.comments = fresh.comments;
        stopPolling(post.id);
        return;
      }
    } catch {
      // 轮询失败不打扰用户，下一轮再试；超过次数就放弃。
    }
    if (attempt + 1 >= REPLY_POLL_LIMIT) stopPolling(post.id);
    else pollForReply(post, knownCount, attempt + 1);
  }, REPLY_POLL_MS);
  pollTimers.set(post.id, timer);
}

async function toggleLike(post: FeedPost): Promise<void> {
  if (liking.value === post.id) return;
  liking.value = post.id;
  try {
    const likes = await likeFeedPost(post.id, !post.liked_by_admin);
    post.like_count = likes.like_count;
    post.liked_by_bot = likes.liked_by_bot;
    post.liked_by_admin = likes.liked_by_admin;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    liking.value = "";
  }
}

function initial(name: string): string {
  return Array.from(name.trim())[0]?.toUpperCase() ?? "?";
}

function commentAuthor(post: FeedPost, kind: "bot" | "admin"): string {
  return kind === "bot" ? authorName(post.profile_id) : "我";
}

function commentInitial(post: FeedPost, comment: FeedComment): string {
  return initial(commentAuthor(post, comment.author_kind));
}

function excerpt(text: string): string {
  const chars = Array.from(text);
  return chars.length > 24 ? `${chars.slice(0, 24).join("")}…` : text;
}

function topLevel(post: FeedPost): FeedComment[] {
  return post.comments.filter((comment) => !comment.parent_id);
}

function repliesOf(post: FeedPost, root: FeedComment): FeedComment[] {
  return post.comments.filter((comment) => comment.parent_id === root.id);
}

function toggle(id: string): void {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
}

function startReply(post: FeedPost, comment: FeedComment): void {
  replyTarget[post.id] = comment;
}

async function submitComment(post: FeedPost): Promise<void> {
  const content = (drafts[post.id] ?? "").trim();
  if (!content || sending.value === post.id) return;
  sending.value = post.id;
  try {
    const result = await addFeedComment(post.id, content, replyTarget[post.id]?.id ?? "");
    post.comments = [...post.comments, result.comment];
    drafts[post.id] = "";
    delete replyTarget[post.id];
    if (result.reply_pending) {
      stopPolling(post.id);
      pollForReply(post, post.comments.length);
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "评论失败");
  } finally {
    sending.value = "";
  }
}

async function removeComment(post: FeedPost, comment: FeedComment): Promise<void> {
  const isTop = !comment.parent_id;
  const confirmed = await askConfirm({
    title: "删除这条评论？",
    message: isTop ? "这条评论下的回复也会一起删掉。" : "删除后无法恢复。",
    confirmLabel: "删除",
    danger: true
  });
  if (!confirmed) return;
  deleting.value = comment.id;
  try {
    await deleteFeedComment(post.id, comment.id);
    post.comments = post.comments.filter((item) => item.id !== comment.id && item.parent_id !== comment.id);
    if (replyTarget[post.id] && !post.comments.some((item) => item.id === replyTarget[post.id].id)) {
      delete replyTarget[post.id];
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally {
    deleting.value = "";
  }
}

function imageSrc(image: FeedImage): string {
  return demoImages ? demoFeedImage(image.id) : feedImageURL(image.id);
}

function authorName(profileID?: string): string {
  const id = (profileID ?? "").trim();
  return profileNames.value[id]?.trim() || id || "机器人";
}

async function loadProfileNames(): Promise<void> {
  try {
    const config = await getBotProfileConfig();
    const names: Record<string, string> = {};
    for (const profile of config.profiles?.length ? config.profiles : [config]) {
      if (profile.id) names[profile.id] = profile.name || profile.id;
    }
    profileNames.value = names;
  } catch {
    // 拿不到名字就显示 ID，不该因此让整页加载失败。
  }
}

async function reload(): Promise<void> {
  loading.value = true;
  try {
    const result = await listFeed(botScope.value, kind.value);
    posts.value = result.posts;
    nextBefore.value = result.next_before ?? "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    loading.value = false;
  }
}

async function loadMore(): Promise<void> {
  if (!nextBefore.value) return;
  loading.value = true;
  try {
    const result = await listFeed(botScope.value, kind.value, nextBefore.value);
    posts.value = [...posts.value, ...result.posts];
    nextBefore.value = result.next_before ?? "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    loading.value = false;
  }
}

function setKind(next: FeedKind | ""): void {
  if (kind.value === next) return;
  kind.value = next;
  void reload();
}

async function remove(post: FeedPost): Promise<void> {
  const confirmed = await askConfirm({
    title: "删除这条动态？",
    message: "连同配图一起删掉，无法恢复。",
    confirmLabel: "删除",
    danger: true
  });
  if (!confirmed) return;
  deleting.value = post.id;
  try {
    await deleteFeedPost(post.id);
    posts.value = posts.value.filter((item) => item.id !== post.id);
    toastSuccess("已删除");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    deleting.value = "";
  }
}

watch(botScope, () => void reload());

onBeforeUnmount(() => {
  for (const id of [...pollTimers.keys()]) stopPolling(id);
});

onMounted(() => {
  void loadProfileNames();
  void reload();
});
</script>

<style scoped>
.feed-list {
  display: grid;
  gap: 12px;
  max-width: 680px;
}
.feed-post {
  padding: 16px;
}
.feed-row {
  display: flex;
  gap: 12px;
}
.feed-main {
  min-width: 0;
  flex: 1;
}
.feed-avatar {
  flex: none;
  display: grid;
  place-items: center;
  width: 42px;
  height: 42px;
  border-radius: 50%;
  background: var(--accent-soft);
  color: var(--accent-strong);
  font-weight: 700;
}
.feed-avatar.small {
  width: 30px;
  height: 30px;
  font-size: 13px;
}
.feed-avatar.admin {
  background: var(--surface-2);
  color: var(--text-secondary);
}
.feed-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 28px;
}
.feed-time {
  font-size: 12.5px;
}
.feed-delete {
  margin-left: auto;
}
.feed-title {
  margin: 6px 0 0;
  font-size: 16px;
}
.feed-content {
  margin: 4px 0 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.7;
}
.feed-images {
  display: grid;
  gap: 4px;
  margin-top: 10px;
}
.feed-images-1 {
  grid-template-columns: minmax(0, 360px);
}
.feed-images-2 {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.feed-images-3 {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.feed-image {
  padding: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: none;
  cursor: zoom-in;
  aspect-ratio: 1;
}
.feed-images-1 .feed-image {
  aspect-ratio: auto;
}
.feed-image img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.feed-images-1 .feed-image img {
  height: auto;
  max-height: 480px;
  object-fit: contain;
}
.feed-actions {
  display: flex;
  margin-top: 8px;
}
.feed-action {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  margin-left: -8px;
  border: 0;
  border-radius: 999px;
  background: none;
  color: var(--muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}
.feed-action:hover,
.feed-action[aria-expanded="true"] {
  background: var(--accent-soft);
  color: var(--accent-strong);
}
.feed-actions {
  gap: 4px;
}
.feed-like.liked {
  color: var(--accent-strong);
}
.feed-thread {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}
.feed-comment {
  display: flex;
  gap: 10px;
  padding: 8px 0;
}
.feed-reply {
  margin-left: 40px;
}
.feed-comment-body {
  min-width: 0;
  flex: 1;
}
.feed-comment-text {
  margin: 2px 0 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.6;
}
.feed-comment-actions {
  display: flex;
  gap: 12px;
  margin-top: 2px;
}
.feed-link {
  padding: 0;
  border: 0;
  background: none;
  color: var(--muted);
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
}
.feed-link:hover {
  color: var(--accent-strong);
}
.feed-link.danger:hover {
  color: var(--err);
}
.feed-empty-comments {
  margin: 0 0 8px;
  font-size: 13px;
}
.feed-composer {
  margin-top: 8px;
}
.feed-replying {
  margin: 0 0 6px;
  font-size: 12.5px;
  color: var(--text-secondary);
}
.feed-composer-row {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}
.feed-input {
  flex: 1;
  resize: vertical;
  min-height: 44px;
}
.feed-more {
  display: flex;
  justify-content: center;
}
.feed-preview {
  display: block;
  max-width: 100%;
  max-height: 75vh;
  margin: 0 auto;
}
</style>
