<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  小窝和动态是同一件事的两面：她拥有的东西（零花钱、愿望单、房间）和她自己写下的
  东西（动态、日记）。合成一页两档，做法同记忆页；路由保持两个地址，#/space 与
  #/feed 各自直达对应的一档，旧的动态书签照样能用。
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
    <FeedView v-else />
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Newspaper, Wallet } from "@lucide/vue";
import { currentView, navigate, type ViewID } from "../router";
import SpaceView from "./SpaceView.vue";
import FeedView from "./FeedView.vue";

const tabs = [
  { id: "space" as ViewID, label: "钱包", icon: Wallet },
  { id: "feed" as ViewID, label: "动态", icon: Newspaper }
];

// 档位直接读路由，不另存一份状态，理由同记忆页。
const active = computed<ViewID>(() => (currentView.value === "feed" ? "feed" : "space"));

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
