<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!-- 小窝展示机器人的钱包、自述和动态，各档保留独立地址。 -->
<template>
  <div class="home-view">
    <div ref="tabList" class="segmented home-tabs" role="tablist" aria-label="小窝内容">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        role="tab"
        :aria-selected="active === tab.id"
        :class="{ active: active === tab.id }"
        @click="select(tab.id)"
      >
        <component :is="tab.icon" :size="15" aria-hidden="true" />
        {{ tab.label }}
      </button>
    </div>

    <SpaceView v-if="active === 'space'" />
    <SelfNotesView v-else-if="active === 'self-notes'" />
    <FeedView v-else />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onActivated, ref, watch } from "vue";
import { Newspaper, NotebookPen, Wallet } from "@lucide/vue";
import { currentView, navigate, type ViewID } from "../router";
import SpaceView from "./SpaceView.vue";
import SelfNotesView from "./SelfNotesView.vue";
import FeedView from "./FeedView.vue";

const tabs = [
  { id: "space" as ViewID, label: "钱包", icon: Wallet },
  { id: "self-notes" as ViewID, label: "自述", icon: NotebookPen },
  { id: "feed" as ViewID, label: "动态", icon: Newspaper }
];

// 档位直接读路由，不另存一份状态：浏览器前进后退、深链和侧边栏跳转都只有这一个
// 来源，不会出现「地址是自述、显示的是钱包」。
const homeTabs: ViewID[] = ["space", "self-notes", "feed"];
const active = computed<ViewID>(() => (homeTabs.includes(currentView.value) ? currentView.value : "space"));

// 窄屏下标签栏横向滚动，从别处直达后面几档（动态、自述）时，选中的那一档可能在
// 视野外，看不出当前在哪。切档和切回来时把它滚进来。
const tabList = ref<HTMLElement | null>(null);
function revealActiveTab(): void {
  void nextTick(() => {
    const tab = tabList.value?.querySelector<HTMLElement>('[aria-selected="true"]');
    tab?.scrollIntoView({ block: "nearest", inline: "nearest" });
  });
}
watch(active, revealActiveTab);
onActivated(revealActiveTab);

function select(view: ViewID): void {
  if (active.value !== view) {
    navigate(view);
  }
}
</script>

<style scoped>
.home-tabs {
  margin-bottom: 16px;
  /* 标签在窄屏排不下一行，横向滚动，不让标签一字一行地折下去。 */
  overflow-x: auto;
  scrollbar-width: none;
}

.home-tabs::-webkit-scrollbar {
  display: none;
}

.home-tabs button {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
</style>
