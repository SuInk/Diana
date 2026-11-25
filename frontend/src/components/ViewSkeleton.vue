<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 聊天页是左右双栏占满一屏的布局（280px 会话列表 + 右侧对话窗），单独画同尺寸双栏骨架，
       加载完切进真实聊天界面时边框和分栏丝毫不跳。 -->
  <div v-if="isChatView" class="chat-view-skeleton" role="status" :aria-label="label">
    <aside class="chat-skel-list" aria-hidden="true">
      <div class="chat-skel-list-head">
        <SkeletonBlock width="48px" height="20px" />
        <div class="chat-skel-icons">
          <SkeletonBlock width="28px" height="28px" />
          <SkeletonBlock width="28px" height="28px" />
        </div>
      </div>
      <div class="chat-skel-contacts">
        <div v-for="n in 4" :key="n" class="chat-skel-contact">
          <SkeletonBlock class="chat-skel-avatar" width="44px" height="44px" />
          <div class="chat-skel-contact-lines">
            <SkeletonBlock :width="n % 2 === 1 ? '62%' : '48%'" height="14px" />
            <SkeletonBlock :width="n % 2 === 1 ? '82%' : '70%'" height="12px" />
          </div>
        </div>
      </div>
    </aside>
    <section class="chat-skel-room" aria-hidden="true">
      <div class="chat-skel-room-head">
        <div class="chat-skel-contact-lines" style="gap: 6px">
          <SkeletonBlock width="116px" height="17px" />
          <SkeletonBlock width="84px" height="12px" />
        </div>
        <SkeletonBlock width="30px" height="30px" />
      </div>
      <div class="chat-skel-transcript">
        <div class="chat-skel-msg">
          <SkeletonBlock class="chat-skel-avatar" width="38px" height="38px" />
          <SkeletonBlock width="42%" height="44px" />
        </div>
        <div class="chat-skel-msg self">
          <SkeletonBlock width="34%" height="40px" />
          <SkeletonBlock class="chat-skel-avatar" width="38px" height="38px" />
        </div>
        <div class="chat-skel-msg">
          <SkeletonBlock class="chat-skel-avatar" width="38px" height="38px" />
          <SkeletonBlock width="56%" height="58px" />
        </div>
      </div>
      <div class="chat-skel-composer">
        <SkeletonBlock width="100%" height="38px" />
        <SkeletonBlock width="68px" height="38px" />
      </div>
    </section>
  </div>

  <!-- 其余页面的整页占位：页面代码首次下载、机器人配置还没读到这两段都用它。 -->
  <div v-else class="view-skeleton">
    <header class="view-header" aria-hidden="true">
      <div v-if="kind === 'bot-editor'" class="view-title">
        <SkeletonBlock class="back-link" width="104px" height="29px" />
        <div class="skeleton-copy" style="gap: 3px"><SkeletonBlock width="168px" height="26px" /><SkeletonBlock width="150px" height="20px" /></div>
      </div>
      <div v-else class="view-title"><SkeletonBlock width="220px" height="18px" /></div>
      <div v-if="kind === 'bots' || kind === 'bot-editor'" class="view-actions">
        <SkeletonBlock v-for="n in kind === 'bot-editor' ? 2 : 1" :key="n" width="108px" height="39px" />
      </div>
    </header>
    <LoadingSkeleton v-if="bare" :kind="kind" :count="count" :label="label" />
    <section v-else class="card">
      <div class="card-body"><LoadingSkeleton :kind="kind" :count="count" :label="label" /></div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import LoadingSkeleton from "./LoadingSkeleton.vue";
import SkeletonBlock from "./SkeletonBlock.vue";
import { currentView, viewQuery, type ViewID } from "../router";
import { ALL_PROFILES, botScope } from "../lib/bot-scope";

type SkeletonKind = InstanceType<typeof LoadingSkeleton>["$props"]["kind"];

withDefaults(defineProps<{ label?: string }>(), { label: "正在加载页面" });

const isChatView = computed(() => currentView.value === "chat" || currentView.value === "admin-chat");

const kinds: Partial<Record<ViewID, SkeletonKind>> = {
  dashboard: "chart",
  bot: "bots",
  groups: "groups",
  plugins: "plugins",
  provider: "providers",
  events: "logs",
  logs: "logs",
  favorability: "logs",
  users: "users",
  notebook: "notebook",
  tasks: "tasks",
  chat: "sessions",
  "admin-chat": "sessions",
  feed: "feed",
  space: "feed",
  "self-notes": "feed"
};

// 卡片网格本身就是页面的外框，不再套一层 card。
const bareKinds = new Set<SkeletonKind>(["bots", "bot-editor", "groups", "plugins"]);

// 机器人页地址点名了某台、或侧栏选着某台时会直达它的配置，占位跟着画编辑页。
const kind = computed<SkeletonKind>(() => {
  if (currentView.value === "bot" && (viewQuery().has("profile") || botScope.value !== ALL_PROFILES)) return "bot-editor";
  return kinds[currentView.value] ?? "form";
});
const bare = computed(() => bareKinds.has(kind.value));
const count = computed(() => (kind.value === "bots" ? 2 : kind.value === "form" || kind.value === "bot-editor" ? 4 : 3));
</script>

<style scoped>
.chat-view-skeleton {
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  height: calc(100dvh - 86px);
  min-height: 420px;
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--surface);
}

.chat-skel-list {
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--border);
  background: color-mix(in srgb, var(--surface-2) 60%, var(--surface));
}

.chat-skel-list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 12px 10px 16px;
}

.chat-skel-icons {
  display: inline-flex;
  gap: 6px;
}

.chat-skel-contacts {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 6px 10px;
}

.chat-skel-contact {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 6px;
}

.chat-skel-avatar {
  flex-shrink: 0;
  border-radius: 999px;
}

.chat-skel-contact-lines {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 7px;
}

.chat-skel-room {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.chat-skel-room-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 18px;
  border-bottom: 1px solid var(--border);
}

.chat-skel-transcript {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 20px 18px;
}

.chat-skel-msg {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}

.chat-skel-msg.self {
  justify-content: flex-end;
}

.chat-skel-composer {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 10px;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
}

@media (max-width: 760px) {
  .chat-view-skeleton {
    grid-template-columns: minmax(0, 1fr);
  }

  .chat-skel-room {
    display: none;
  }
}
</style>
