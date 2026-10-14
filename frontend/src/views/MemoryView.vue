<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  侧栏叫「档案」。小窝（钱包、自述、动态）也收在这里，各档仍是自己的地址（/space、/self-notes、/feed）。
  人员和笔记本是同一件事的两半：机器人记住的人（画像、长期记忆、好感度）和特意记下的
  话（梗、黑话、内部称呼）。两者都不是配置项，都是「它跟这个群相处下来学到了
  什么」，改词条和改画像常常是同一次翻查里做的事。这里合成一页两档，路由保持
  两个地址：#/users 与 #/notebook 各自直达对应的一档。
-->
<template>
  <div class="memory-view">
    <div class="segmented memory-tabs" role="tablist" aria-label="记忆类型">
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
    <SpaceView v-else-if="active === 'space'" />
    <SelfNotesView v-else-if="active === 'self-notes'" />
    <FeedView v-else />
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { BookMarked, Newspaper, NotebookPen, UserRound, Wallet } from "@lucide/vue";
import { currentView, navigate, type ViewID } from "../router";
import UsersView from "./UsersView.vue";
import NotebookView from "./NotebookView.vue";
import SpaceView from "./SpaceView.vue";
import SelfNotesView from "./SelfNotesView.vue";
import FeedView from "./FeedView.vue";

const tabs = [
  { id: "users" as ViewID, label: "人员", icon: UserRound },
  { id: "notebook" as ViewID, label: "笔记本", icon: BookMarked },
  { id: "space" as ViewID, label: "钱包", icon: Wallet },
  { id: "self-notes" as ViewID, label: "自述", icon: NotebookPen },
  { id: "feed" as ViewID, label: "动态", icon: Newspaper }
];

// 档位直接读路由，不另存一份状态：浏览器前进后退、深链和侧边栏跳转都只有这一个
// 来源，不会出现「地址是笔记本、显示的是人员」。
const memoryTabs: ViewID[] = ["notebook", "space", "self-notes", "feed"];
const active = computed<ViewID>(() => (memoryTabs.includes(currentView.value) ? currentView.value : "users"));

function select(view: ViewID): void {
  if (active.value !== view) {
    navigate(view);
  }
}
</script>

<style scoped>
.memory-tabs {
  margin-bottom: 16px;
}

.memory-tabs button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
</style>
