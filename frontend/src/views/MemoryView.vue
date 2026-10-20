<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  侧栏叫「档案」。世界书和小窝（钱包、自述、动态）也收在这里，各档仍是自己的地址（/space、/self-notes、/feed）。
  人员和笔记本是同一件事的两半：机器人记住的人（画像、长期记忆、好感度）和特意记下的
  话（梗、黑话、内部称呼）。两者都不是配置项，都是「它跟这个群相处下来学到了
  什么」，改词条和改画像常常是同一次翻查里做的事。这里合成一页两档，路由保持
  两个地址：#/users 与 #/notebook 各自直达对应的一档。
-->
<template>
  <div class="memory-view">
    <div ref="tabList" class="segmented memory-tabs" role="tablist" aria-label="记忆类型">
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

    <UsersView v-if="active === 'users'" />
    <NotebookView v-else-if="active === 'notebook'" />
    <WorldBookView v-else-if="active === 'world-book'" />
    <SpaceView v-else-if="active === 'space'" />
    <SelfNotesView v-else-if="active === 'self-notes'" />
    <FeedView v-else />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onActivated, ref, watch } from "vue";
import { BookMarked, Earth, Newspaper, NotebookPen, UserRound, Wallet } from "@lucide/vue";
import { currentView, navigate, type ViewID } from "../router";
import UsersView from "./UsersView.vue";
import NotebookView from "./NotebookView.vue";
import WorldBookView from "./WorldBookView.vue";
import SpaceView from "./SpaceView.vue";
import SelfNotesView from "./SelfNotesView.vue";
import FeedView from "./FeedView.vue";

const tabs = [
  { id: "users" as ViewID, label: "人员", icon: UserRound },
  { id: "notebook" as ViewID, label: "笔记本", icon: BookMarked },
  { id: "world-book" as ViewID, label: "世界书", icon: Earth },
  { id: "space" as ViewID, label: "钱包", icon: Wallet },
  { id: "self-notes" as ViewID, label: "自述", icon: NotebookPen },
  { id: "feed" as ViewID, label: "动态", icon: Newspaper }
];

// 档位直接读路由，不另存一份状态：浏览器前进后退、深链和侧边栏跳转都只有这一个
// 来源，不会出现「地址是笔记本、显示的是人员」。
const memoryTabs: ViewID[] = ["notebook", "world-book", "space", "self-notes", "feed"];
const active = computed<ViewID>(() => (memoryTabs.includes(currentView.value) ? currentView.value : "users"));

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
.memory-tabs {
  margin-bottom: 16px;
  /* 六档在手机宽度排不下一行，横向滚动，不让标签一字一行地折下去。 */
  overflow-x: auto;
  scrollbar-width: none;
}

.memory-tabs::-webkit-scrollbar {
  display: none;
}

.memory-tabs button {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
</style>
