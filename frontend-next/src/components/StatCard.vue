<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 可点的卡片渲染成 button：键盘能聚焦、回车能触发，读屏也会报成按钮。
       套一层 div 加 @click 看着一样，但这些都没有。 -->
  <component
    :is="clickable ? 'button' : 'div'"
    class="card stat-card"
    :class="{ 'stat-card-clickable': clickable }"
    :type="clickable ? 'button' : undefined"
    :aria-busy="loading || undefined"
    :title="hint || undefined"
    @click="clickable ? emit('activate') : undefined"
  >
    <span class="stat-label">
      <slot name="icon" />
      {{ label }}
    </span>
    <span class="stat-value">
      <SkeletonBlock v-if="loading" width="3ch" height="24px" inline />
      <template v-else>{{ value }}</template>
    </span>
    <span v-if="foot || loading" class="stat-foot">
      <span v-if="loading" class="skeleton skeleton-text" aria-hidden="true">{{ foot || label }}</span>
      <template v-else>{{ foot }}</template>
    </span>
  </component>
</template>

<script setup lang="ts">
import SkeletonBlock from "./SkeletonBlock.vue";

// hint 是悬停才展开的明细，给那些一行放不下、又不值得单开一张卡的分解数据。
defineProps<{ label: string; value: string; foot?: string; hint?: string; loading?: boolean; clickable?: boolean }>();
const emit = defineEmits<{ activate: [] }>();
</script>
