<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="wordcloud-chart">
    <p v-if="placed.length > 0" class="wordcloud-legend muted">
      {{ cloud.messages }} 条发言 · {{ cloud.speakers }} 人 · 字越大说得越多
      <template v-if="cloud.truncated">（只统计了最近这批消息）</template>
      <template v-if="cloud.segmenter === 'ngram'"> · 未开启词典分词，按字组粗切</template>
    </p>
    <svg
      v-if="placed.length > 0"
      :viewBox="`0 0 ${width} ${height}`"
      class="wordcloud-svg"
      role="img"
      :aria-label="`群聊词云：${placed.length} 个高频词`"
    >
      <text
        v-for="word in placed"
        :key="word.text"
        :x="word.x"
        :y="word.y"
        :font-size="word.size"
        :class="`wordcloud-word tone-${word.tone}`"
        text-anchor="middle"
        dominant-baseline="central"
        :transform="word.vertical ? `rotate(-90 ${word.x} ${word.y})` : undefined"
      >
        {{ word.text }}
        <title>{{ word.text }}：{{ word.count }} 条消息提到</title>
      </text>
    </svg>
    <p v-else class="wordcloud-empty muted">这段时间群里没有足够的发言，先聊几句再看。</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { GroupWordCloud } from "../api";

const props = defineProps<{ cloud: GroupWordCloud }>();

const width = 900;
const height = 520;
const minSize = 15;
const maxSize = 88;

interface PlacedWord {
  text: string;
  count: number;
  x: number;
  y: number;
  size: number;
  tone: number;
  vertical: boolean;
}

let measureContext: CanvasRenderingContext2D | null | undefined;

// 量字宽用 canvas，量不到（测试环境）就按中文全角、拉丁半角估。
function textWidth(text: string, size: number): number {
  if (measureContext === undefined) {
    measureContext = typeof document === "undefined" ? null : document.createElement("canvas").getContext("2d");
  }
  if (measureContext) {
    measureContext.font = `600 ${size}px system-ui, sans-serif`;
    return measureContext.measureText(text).width;
  }
  return [...text].reduce((sum, char) => sum + (char.charCodeAt(0) > 0xff ? size : size * 0.6), 0);
}

// 阿基米德螺线放词：从中心往外找第一个不和已放的词重叠的位置。词按频次从大到小
// 放，大词自然聚在中间。放不下的小词直接丢掉，不硬塞到画布外面。
const placed = computed<PlacedWord[]>(() => {
  const words = props.cloud.words.slice(0, 120);
  if (words.length === 0) return [];
  const maxCount = words[0].count;
  const minCount = words[words.length - 1].count;
  const boxes: Array<[number, number, number, number]> = [];
  const result: PlacedWord[] = [];
  words.forEach((word, index) => {
    const ratio = maxCount === minCount ? 1 : Math.sqrt((word.count - minCount) / (maxCount - minCount));
    const size = Math.round(minSize + (maxSize - minSize) * ratio);
    // 中等大小的词偶尔竖排，填缝用；最大的几个保持横排好认。
    const vertical = index > 3 && index % 5 === 2 && [...word.text].length <= 4;
    const w = textWidth(word.text, size) + 6;
    const h = size * 1.05;
    const [boxW, boxH] = vertical ? [h, w] : [w, h];
    for (let step = 0; step < 2400; step += 1) {
      const angle = step * 0.21;
      const radius = 2.2 * angle;
      const x = width / 2 + radius * Math.cos(angle) * 1.6;
      const y = height / 2 + radius * Math.sin(angle);
      const box: [number, number, number, number] = [x - boxW / 2, y - boxH / 2, x + boxW / 2, y + boxH / 2];
      if (box[0] < 4 || box[1] < 4 || box[2] > width - 4 || box[3] > height - 4) continue;
      if (boxes.some((other) => box[0] < other[2] && box[2] > other[0] && box[1] < other[3] && box[3] > other[1])) continue;
      boxes.push(box);
      result.push({ text: word.text, count: word.count, x, y, size, tone: index % 5, vertical });
      return;
    }
  });
  return result;
});
</script>

<style scoped>
.wordcloud-svg {
  width: 100%;
  height: auto;
  display: block;
}

.wordcloud-word {
  font-family: system-ui, sans-serif;
  font-weight: 600;
  cursor: default;
}

.tone-0 { fill: var(--accent); }
.tone-1 { fill: var(--text); }
.tone-2 { fill: var(--text-secondary); }
.tone-3 { fill: color-mix(in srgb, var(--accent) 65%, var(--text)); }
.tone-4 { fill: color-mix(in srgb, var(--accent) 45%, var(--text-secondary)); }

.wordcloud-legend,
.wordcloud-empty {
  margin: 0;
  font-size: 13px;
}
</style>
