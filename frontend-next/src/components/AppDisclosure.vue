<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="app-disclosure">
    <button type="button" class="app-disclosure-trigger" :aria-expanded="open" :aria-controls="`${id}-content`" @click="open = !open">
      <ChevronDown :size="14" :class="{ open }" aria-hidden="true" />
      <span>{{ title }}</span>
    </button>
    <div v-show="open" :id="`${id}-content`" class="app-disclosure-content"><slot /></div>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { ChevronDown } from "@lucide/vue";
defineProps<{ id: string; title: string }>();
const open = ref(false);
</script>

<style scoped>
.app-disclosure-trigger {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-height: 34px;
  padding: 0 6px;
  margin-left: -6px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--muted);
  font: inherit;
  cursor: pointer;
}
.app-disclosure-trigger:hover { background: var(--surface-2); color: var(--text); }
.app-disclosure-trigger:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.app-disclosure-trigger svg { transform: rotate(-90deg); transition: transform 0.18s ease; }
.app-disclosure-trigger svg.open { transform: rotate(0); }
.app-disclosure-content { padding-left: 21px; }
@media (prefers-reduced-motion: reduce) { .app-disclosure-trigger svg { transition: none; } }
</style>
