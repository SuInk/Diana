<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 整页还没出来时的占位：页面代码首次下载、机器人配置还没读到这两段都用它。
       以前前者什么都不画，后者是居中转圈，页面出来后再换成页内骨架，一次加载
       连闪三种样子。这里按当前页套用它自己的列表骨架，接上页内骨架时不跳。 -->
  <div class="view-skeleton">
    <header class="view-header" aria-hidden="true">
      <div class="view-title"><SkeletonBlock width="220px" height="18px" /></div>
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
import { currentView, type ViewID } from "../router";

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
const bareKinds = new Set<SkeletonKind>(["bots", "groups", "plugins"]);

const kind = computed<SkeletonKind>(() => kinds[currentView.value] ?? "form");
const bare = computed(() => bareKinds.has(kind.value));
const count = computed(() => (kind.value === "bots" ? 2 : kind.value === "form" ? 4 : 3));
</script>
