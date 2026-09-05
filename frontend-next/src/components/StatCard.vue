<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="card stat-card" :aria-busy="loading || undefined">
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
  </div>
</template>

<script setup lang="ts">
import SkeletonBlock from "./SkeletonBlock.vue";

defineProps<{ label: string; value: string; foot?: string; loading?: boolean }>();
</script>
