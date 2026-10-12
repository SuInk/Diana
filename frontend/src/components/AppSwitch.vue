<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <button
    type="button"
    class="app-switch"
    :class="{ 'is-on': modelValue, compact }"
    role="switch"
    :aria-checked="!!modelValue"
    :disabled="disabled"
    @click="emit('update:modelValue', !modelValue)"
  >
    <span class="app-switch-track" aria-hidden="true"></span>
    <span v-if="$slots.default" class="app-switch-label"><slot /></span>
  </button>
</template>

<script setup lang="ts">
defineProps<{ modelValue?: boolean; disabled?: boolean; compact?: boolean }>();
const emit = defineEmits<{ "update:modelValue": [boolean] }>();
</script>

<style scoped>
.app-switch {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  min-height: 34px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.app-switch-track {
  flex: none;
  width: 38px;
  height: 22px;
  border-radius: 999px;
  background: var(--border-strong);
  transition: background 0.18s ease;
}
.app-switch-track::after {
  content: "";
  display: block;
  width: 16px;
  height: 16px;
  margin: 3px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.25);
  transition: transform 0.18s ease;
}
.is-on .app-switch-track { background: var(--accent); }
.is-on .app-switch-track::after { transform: translateX(16px); }
.app-switch:hover:not(:disabled) .app-switch-track { filter: brightness(1.08); }
.app-switch:focus-visible { outline: none; }
.app-switch:focus-visible .app-switch-track {
  outline: 2px solid var(--accent);
  outline-offset: 3px;
}
.app-switch:disabled { opacity: 0.45; cursor: not-allowed; }
.app-switch-label { font-size: 13.5px; line-height: 1.6; }
.compact .app-switch-track { width: 30px; height: 18px; }
.compact .app-switch-track::after { width: 14px; height: 14px; margin: 2px; }
.compact.is-on .app-switch-track::after { transform: translateX(12px); }
@media (prefers-reduced-motion: reduce) {
  .app-switch-track, .app-switch-track::after { transition: none; }
}
</style>
