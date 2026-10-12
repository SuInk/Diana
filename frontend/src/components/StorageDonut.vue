<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="storage-donut">
    <svg class="storage-donut-ring" viewBox="0 0 160 160" role="img" :aria-label="ariaLabel">
      <circle class="storage-donut-track" cx="80" cy="80" r="62" fill="none" stroke-width="16" />
      <circle
        v-for="arc in arcs"
        :key="arc.key"
        class="storage-donut-arc"
        cx="80"
        cy="80"
        r="62"
        fill="none"
        stroke-width="16"
        stroke-linecap="butt"
        :stroke="arc.color"
        :stroke-dasharray="`${arc.length} ${circumference - arc.length}`"
        :stroke-dashoffset="-arc.offset"
      >
        <title>{{ arc.label }} {{ formatBytes(arc.bytes) }}（{{ arc.percentLabel }}）</title>
      </circle>
    </svg>
    <div class="storage-donut-center">
      <strong>{{ centerValue }}</strong>
      <span class="muted">{{ centerLabel }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { formatBytes } from "../format";
import type { StorageSegment } from "../storage-usage";

const props = defineProps<{
  segments: StorageSegment[];
  total: number;
  centerValue: string;
  centerLabel: string;
}>();

const circumference = 2 * Math.PI * 62;
// 扇区之间留一点缝：相邻两段颜色接近时（比如「其它程序」和「可用空间」都是灰），
// 没有缝就看不出边界在哪。缝只吃掉画出来的长度，偏移仍按真实占比累加。
const arcGap = 3;

// 分母用整块盘的容量，扇区依次首尾相接：可用空间也是一段，所以环一定是满的，
// 缺口只会出现在数据本身对不上的时候。
const arcs = computed(() => {
  const total = props.total > 0 ? props.total : props.segments.reduce((sum, segment) => sum + segment.bytes, 0);
  if (total <= 0) {
    return [];
  }
  const drawn = props.segments.filter((segment) => segment.bytes > 0);
  let offset = 0;
  return drawn.map((segment) => {
    const ratio = Math.min(1, segment.bytes / total);
    const length = ratio * circumference;
    const arc = {
      ...segment,
      // 只有长到留得下缝的扇区才让缝；再短就只剩一条看不见的线了。
      length: drawn.length > 1 && length > arcGap * 2 ? length - arcGap : length,
      offset,
      percentLabel: `${(ratio * 100).toFixed(ratio < 0.1 ? 1 : 0)}%`
    };
    offset += length;
    return arc;
  });
});

const ariaLabel = computed(() => {
  const parts = arcs.value.map((arc) => `${arc.label} ${formatBytes(arc.bytes)}`);
  return `存储占用饼图：${parts.join("，")}`;
});
</script>

<style scoped>
.storage-donut {
  position: relative;
  width: 160px;
  height: 160px;
  flex: none;
}

.storage-donut-ring {
  width: 100%;
  height: 100%;
  transform: rotate(-90deg);
}

.storage-donut-track {
  stroke: var(--surface-2);
}

.storage-donut-arc {
  transition: stroke-dasharray 0.3s ease;
}

.storage-donut-center {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  text-align: center;
  pointer-events: none;
}

.storage-donut-center strong {
  font-size: 20px;
  line-height: 1.1;
}

.storage-donut-center span {
  font-size: 12px;
}
</style>
