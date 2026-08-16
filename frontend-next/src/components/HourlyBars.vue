<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <div class="spark-bars" role="img" :aria-label="`最近 24 小时消息量，共 ${total} 条`">
      <div
        v-for="bucket in buckets"
        :key="bucket.hour_unix"
        class="bar"
        :title="`${formatHourLabel(bucket.hour_unix)} — 共 ${bucket.total} 条 / 成功 ${bucket.handled} / 错误 ${bucket.errors}`"
      >
        <div class="bar-fill" :style="{ height: barHeight(bucket.total) }" />
        <div v-if="bucket.errors > 0" class="bar-err" :style="{ height: barHeight(bucket.errors) }" />
      </div>
    </div>
    <div class="spark-axis">
      <span>{{ firstLabel }}</span>
      <span>{{ lastLabel }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { StatsHourBucket } from "../api";
import { formatHourLabel } from "../format";

const props = defineProps<{ buckets: StatsHourBucket[] }>();

const max = computed(() => Math.max(1, ...props.buckets.map((bucket) => bucket.total)));
const total = computed(() => props.buckets.reduce((sum, bucket) => sum + bucket.total, 0));
const firstLabel = computed(() => (props.buckets.length > 0 ? formatHourLabel(props.buckets[0]!.hour_unix) : ""));
const lastLabel = computed(() =>
  props.buckets.length > 0 ? formatHourLabel(props.buckets[props.buckets.length - 1]!.hour_unix) : ""
);

function barHeight(value: number): string {
  if (value <= 0) {
    return "2px";
  }
  const ratio = value / max.value;
  return `${Math.max(6, Math.round(ratio * 88))}px`;
}
</script>
