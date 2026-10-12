<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="plugin-setting-size">
    <input
      :id="id"
      class="input"
      type="number"
      :value="amount"
      :min="minAmount"
      :max="maxAmount"
      step="any"
      @input="onAmountInput"
    />
    <AppSelect class="plugin-setting-size-unit" :model-value="unit" :options="unitOptions" @update:model-value="onUnitChange" />
    <span class="plugin-setting-size-hint">{{ rangeHint }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import AppSelect from "./AppSelect.vue";

// 值统一是字节数，界面只负责在字节和 KB/MB/GB 之间换算，
// 避免用户在纯数字输入框里少写或多写一个 0。
const KB = 1024;
const MB = 1024 * 1024;
const GB = 1024 * 1024 * 1024;
const FACTORS: Record<string, number> = { KB, MB, GB };

const props = defineProps<{
  id?: string;
  modelValue: number;
  min?: number;
  max?: number;
}>();
const emit = defineEmits<{ "update:modelValue": [number] }>();

const unitOptions = [
  { value: "KB", label: "KB" },
  { value: "MB", label: "MB" },
  { value: "GB", label: "GB" }
];

const bytes = computed(() => (Number.isFinite(props.modelValue) ? Math.max(0, Number(props.modelValue)) : 0));

// 选一个能把数值显示得最短的单位：1 MB 显示成 1 MB 而不是 1024 KB。
const unit = computed(() => {
  const value = bytes.value;
  if (value > 0 && value % GB === 0) return "GB";
  if (value > 0 && value % MB === 0) return "MB";
  if (value > 0 && value % KB === 0) return "KB";
  return value >= MB ? "MB" : "KB";
});

const factor = computed(() => FACTORS[unit.value] ?? KB);
const amount = computed(() => round(bytes.value / factor.value));
const minAmount = computed(() => (props.min === undefined ? undefined : round(props.min / factor.value)));
const maxAmount = computed(() => (props.max === undefined ? undefined : round(props.max / factor.value)));

const rangeHint = computed(() => {
  if (props.min === undefined && props.max === undefined) return "";
  if (props.min !== undefined && props.max !== undefined) return `${formatBytes(props.min)} ~ ${formatBytes(props.max)}`;
  if (props.min !== undefined) return `≥ ${formatBytes(props.min)}`;
  return `≤ ${formatBytes(props.max as number)}`;
});

function round(value: number): number {
  return Math.round(value * 100) / 100;
}

function formatBytes(value: number): string {
  if (value >= GB) return `${round(value / GB)} GB`;
  if (value >= MB) return `${round(value / MB)} MB`;
  return `${round(value / KB)} KB`;
}

function commit(nextBytes: number): void {
  let next = Math.round(nextBytes);
  if (props.min !== undefined) next = Math.max(next, props.min);
  if (props.max !== undefined) next = Math.min(next, props.max);
  emit("update:modelValue", next);
}

function onAmountInput(event: Event): void {
  const raw = (event.target as HTMLInputElement).value;
  if (raw === "" || !Number.isFinite(Number(raw))) return;
  commit(Number(raw) * factor.value);
}

function onUnitChange(nextUnit: string): void {
  const nextFactor = FACTORS[nextUnit] ?? KB;
  // 换单位保持数字不变，符合「32 KB 改成 32 MB」的直觉。
  commit(amount.value * nextFactor);
}
</script>
