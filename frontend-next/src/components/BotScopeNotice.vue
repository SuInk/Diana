<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<!--
  给「数据还没有机器人维度」的页面用。
  选了某一台机器人却看到和「全部」一模一样的内容，人会以为这就是那台机器人的数据；
  与其让切换器在这里悄悄失效，不如把话说明白。等对应的存储补上 profile 维度，这个
  组件就该从那个页面上撤掉。
-->
<template>
  <p v-if="scoped" class="bot-scope-notice">
    <Info :size="14" aria-hidden="true" />
    <span>{{ subject }}目前不区分机器人，这里显示的是所有机器人共用的数据。</span>
  </p>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Info } from "@lucide/vue";
import { ALL_PROFILES, botScope } from "../bot-scope";

defineProps<{ subject: string }>();

const scoped = computed(() => botScope.value !== ALL_PROFILES);
</script>

<style scoped>
.bot-scope-notice {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 12px;
  padding: 8px 12px;
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius-md);
  color: var(--muted);
  font-size: 12.5px;
}
</style>
