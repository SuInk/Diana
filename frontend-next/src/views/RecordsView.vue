<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  事件和日志本来就是同一件事的两面：出了问题先看这条消息怎么处理的，再看那一刻
  运行时报了什么。分成两个侧边栏入口，等于让人在「查一次故障」的过程中来回换页，
  也把 11 个平级入口撑得更长。这里合成一页两档，路由保持两个地址：#/events 与
  #/logs 各自直达对应的一档，插件页「查看执行日志」这类深链不受影响。
-->
<template>
  <div class="records-view">
    <div class="segmented records-tabs" role="tablist" aria-label="运行记录类型">
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

    <EventsView v-if="active === 'events'" />
    <LogsView v-else />
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Activity, FileClock } from "@lucide/vue";
import { currentView, navigate, type ViewID } from "../router";
import EventsView from "./EventsView.vue";
import LogsView from "./LogsView.vue";

const tabs = [
  { id: "events" as ViewID, label: "事件明细", icon: Activity },
  { id: "logs" as ViewID, label: "运行日志", icon: FileClock }
];

// 当前档位直接读路由，不另存一份状态：浏览器前进后退、深链和侧边栏跳转都只有
// 这一个来源，不会出现「地址是日志、显示的是事件」。
const active = computed<ViewID>(() => (currentView.value === "logs" ? "logs" : "events"));

function select(view: ViewID): void {
  if (active.value !== view) {
    navigate(view);
  }
}
</script>

<style scoped>
.records-tabs {
  margin-bottom: 16px;
}

.records-tabs button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
</style>
