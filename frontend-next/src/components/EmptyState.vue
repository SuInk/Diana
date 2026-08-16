<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="empty-state">
    <span class="empty-icon">
      <slot name="icon">
        <Sparkles :size="20" aria-hidden="true" />
      </slot>
    </span>
    <p>{{ title }}</p>
    <p v-if="hint" class="muted" style="font-size: 12.5px">{{ hint }}</p>
    <slot />
  </div>
</template>

<script setup lang="ts">
import { Sparkles } from "@lucide/vue";

defineProps<{ title: string; hint?: string }>();
</script>
