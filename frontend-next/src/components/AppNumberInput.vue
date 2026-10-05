<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="app-number-input" :class="{ compact, 'is-disabled': disabled }">
    <button type="button" :aria-label="`${label || '数值'}减少`" :disabled="disabled || numericValue <= min" @click="adjust(-step)"><Minus :size="13" aria-hidden="true" /></button>
    <input
      :id="id"
      type="text"
      inputmode="numeric"
      autocomplete="off"
      role="spinbutton"
      :value="displayValue"
      :aria-label="label"
      :aria-valuemin="min"
      :aria-valuemax="max"
      :aria-valuenow="numericValue"
      :disabled="disabled"
      @input="onInput"
      @blur="commit"
      @keydown="onKeydown"
    />
    <button type="button" :aria-label="`${label || '数值'}增加`" :disabled="disabled || numericValue >= max" @click="adjust(step)"><Plus :size="13" aria-hidden="true" /></button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Minus, Plus } from "@lucide/vue";

const props = withDefaults(defineProps<{
  modelValue?: number;
  min?: number;
  max?: number;
  step?: number;
  id?: string;
  label?: string;
  disabled?: boolean;
  compact?: boolean;
}>(), { min: 0, max: Number.MAX_SAFE_INTEGER, step: 1 });
const emit = defineEmits<{ "update:modelValue": [number] }>();

function bounded(value: number): number {
  return Math.max(props.min, Math.min(props.max, Math.round(Number.isNaN(value) ? props.min : value)));
}
const displayValue = ref(String(bounded(props.modelValue ?? props.min)));
const numericValue = computed(() => bounded(displayValue.value === "" ? (props.modelValue ?? props.min) : Number(displayValue.value)));
watch(() => props.modelValue, (value) => { displayValue.value = String(bounded(value ?? props.min)); });

function onInput(event: Event): void {
  const input = event.target as HTMLInputElement;
  if (!/^\d*$/.test(input.value)) {
    input.value = displayValue.value;
    return;
  }
  displayValue.value = input.value;
  // 允许先清空再输入；失焦时恢复原值或把越界值收回范围，避免输入半途跳动。
  if (input.value !== "" && Number(input.value) >= props.min && Number(input.value) <= props.max) emit("update:modelValue", Number(input.value));
}
function commit(): void {
  displayValue.value = String(numericValue.value);
  emit("update:modelValue", numericValue.value);
}
function adjust(delta: number): void {
  displayValue.value = String(bounded(numericValue.value + delta));
  emit("update:modelValue", Number(displayValue.value));
}
function onKeydown(event: KeyboardEvent): void {
  if (event.key === "ArrowUp" || event.key === "ArrowDown") {
    event.preventDefault();
    adjust(event.key === "ArrowUp" ? props.step : -props.step);
  } else if (event.key === "Home" || event.key === "End") {
    event.preventDefault();
    displayValue.value = String(event.key === "Home" ? props.min : props.max);
    emit("update:modelValue", Number(displayValue.value));
  } else if (event.key === "Enter") commit();
}
</script>

<style scoped>
.app-number-input {
  display: inline-flex;
  align-items: stretch;
  width: 144px;
  height: 40px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: var(--surface);
  overflow: hidden;
  transition: border-color 0.15s ease;
}
.app-number-input:hover:not(.is-disabled) { border-color: var(--border-strong); }
.app-number-input:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 12%, transparent); }
input {
  flex: 1;
  min-width: 0;
  width: 0;
  padding: 0 4px;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  text-align: center;
}
button {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 34px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
}
button:hover:not(:disabled) { background: var(--surface-2); color: var(--accent); }
button:focus-visible { outline: 2px solid var(--accent); outline-offset: -3px; }
button:disabled { opacity: 0.35; cursor: not-allowed; }
.is-disabled { opacity: 0.5; }
.compact { width: 96px; height: 34px; border-radius: var(--radius-sm); }
.compact input { font-size: 11px; }
.compact button { width: 26px; }
</style>
