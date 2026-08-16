<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="card stat-card">
    <span class="stat-label">
      <slot name="icon" />
      {{ label }}
    </span>
    <span class="stat-value">{{ value }}</span>
    <span v-if="foot" class="stat-foot">{{ foot }}</span>
  </div>
</template>

<script setup lang="ts">
defineProps<{ label: string; value: string; foot?: string }>();
</script>
