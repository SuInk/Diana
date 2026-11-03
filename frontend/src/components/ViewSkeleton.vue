<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 整页还没出来时的占位：页面代码首次下载、机器人配置还没读到这两段都用它。
       以前前者什么都不画，后者是居中转圈，页面出来后再换成页内骨架，一次加载
       连闪三种样子。这里按当前页套用它自己的列表骨架，接上页内骨架时不跳。 -->
  <div class="view-skeleton">
    <!-- 机器人页两种样子的页头都有右侧按钮，编辑页还多一行返回和名字，这里照着画，
         页面出来时页头不跳。 -->
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
