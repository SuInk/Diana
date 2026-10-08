<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  小窝收着属于她自己的东西：她拥有的（零花钱、愿望单、房间）、她对自己的认识
  （自述）和她写下的（动态、日记）。合成一页多档，做法同记忆页；每档仍是各自的
  路由地址（#/space、#/self-notes、#/feed），旧的动态书签照样能用。
-->
<template>
  <div class="own-space-view">
    <div class="segmented own-space-tabs" role="tablist" aria-label="小窝分区">
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
import { computed } from "vue";
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

// 档位直接读路由，不另存一份状态，理由同记忆页。
const active = computed<ViewID>(() =>
  currentView.value === "feed" || currentView.value === "self-notes" ? currentView.value : "space"
);

function select(view: ViewID): void {
  if (active.value !== view) {
    navigate(view);
  }
}
</script>

<style scoped>
.own-space-tabs {
  margin-bottom: 16px;
}

.own-space-tabs button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
</style>
