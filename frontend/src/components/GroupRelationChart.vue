<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="relation-chart">
    <p v-if="placed.length > 0" class="relation-legend muted">
      {{ graph.messages }} 条发言 · {{ graph.participants }} 人发过言<template v-if="trimmed">，只画发言最多的 {{ graph.nodes.filter((node) => !node.is_bot).length }} 人</template><template v-if="overflow > 0">，其中 {{ overflow }} 人圈里放不下</template>
      <template v-if="graph.truncated">（只统计了最近这批消息）</template>
      · 越靠里和机器人互动越多，悬停看单人
    </p>
    <svg
      v-if="placed.length > 0"
      :viewBox="`0 0 ${size} ${size}`"
      class="relation-svg"
      :class="{ 'relation-focusing': focused }"
      role="img"
      :aria-label="`群聊互动圈：${placed.length} 人按和机器人的互动次数分成内圈、中圈、外圈`"
      @mouseleave="focusedID = ''"
    >
      <!-- 圈层参考线：只是背景，让人一眼看出谁在第几圈。 -->
      <g v-for="ring in usedRings" :key="ring.name">
        <circle :cx="center" :cy="center" :r="ring.radius" class="relation-ring" />
        <text :x="center" :y="center - ring.radius - ring.node - 6" class="relation-ring-label" text-anchor="middle">{{ ring.name }}</text>
      </g>

      <!-- 连线只在悬停时画：平时全画出来就又挤成一团了。 -->
      <template v-if="focused">
        <path
          v-for="edge in focusedPeerEdges"
          :key="`peer-${edge.target}`"
          :d="edge.path"
          class="relation-peer-edge"
          :style="{ strokeWidth: edge.strokeWidth }"
        />
        <line
          v-if="focused.weight > 0"
          :x1="center"
          :y1="center"
          :x2="focused.x"
          :y2="focused.y"
          class="relation-center-edge"
        />
      </template>

      <g
        v-for="member in placed"
        :key="member.userID"
        class="relation-member"
        :class="{ 'relation-active': focusedID === member.userID || focusedNeighbors.has(member.userID) }"
        @mouseenter="focusedID = member.userID"
      >
        <circle :cx="member.x" :cy="member.y" :r="member.radius" class="relation-node" :style="{ fill: member.fill }">
          <title>{{ member.tooltip }}</title>
        </circle>
        <text
          :x="member.x"
          :y="member.y + member.radius * 0.36"
          class="relation-initial"
          :class="{ 'relation-initial-strong': member.ring < 2 && member.weight > 0 }"
          :style="{ fontSize: `${Math.round(member.radius * 0.95)}px` }"
          text-anchor="middle"
        >{{ member.initial }}</text>
        <text
          v-if="member.ring === 0"
          :x="member.x"
          :y="member.y + member.radius + 14"
          class="relation-label"
          text-anchor="middle"
        >{{ member.label }}</text>
      </g>

      <circle :cx="center" :cy="center" :r="centerRadius + 6" class="relation-center-halo" />
      <circle :cx="center" :cy="center" :r="centerRadius" class="relation-center" />
      <text :x="center" :y="center + 5" class="relation-center-label" text-anchor="middle">{{ centerLabel }}</text>

      <!-- 悬停的人不在内圈时名字不常驻，这里补一个浮签。 -->
      <g v-if="focused && focused.ring > 0" class="relation-tag">
        <rect :x="focused.x - tagWidth / 2" :y="focused.y + focused.radius + 4" :width="tagWidth" height="20" rx="6" />
        <text :x="focused.x" :y="focused.y + focused.radius + 18" text-anchor="middle">{{ focused.label }}</text>
      </g>
    </svg>

    <ol v-if="ranking.length > 0" class="relation-ranking">
      <li
        v-for="(member, index) in ranking"
        :key="member.userID"
        :class="{ active: focusedID === member.userID }"
        @mouseenter="focusedID = member.userID"
        @mouseleave="focusedID = ''"
      >
        <span class="relation-rank">{{ index + 1 }}</span>
        <span class="relation-rank-name" :title="member.name">{{ member.name }}</span>
        <span class="relation-rank-bar"><span :style="{ width: `${Math.max(6, member.strength * 100)}%` }" /></span>
        <span class="relation-rank-count muted">{{ member.weight }} 次</span>
      </li>
    </ol>

    <p v-if="placed.length === 0" class="relation-empty muted">这段时间群里没有发言记录，先聊几句再看。</p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { GroupRelationGraph } from "../api";

const props = defineProps<{ graph: GroupRelationGraph }>();

const size = 620;
const center = size / 2;
const centerRadius = 34;

// 三圈从里到外：圈越外半径越大、头像越小，所以外圈能放的人反而多。
// 每圈的人数上限按周长和头像直径算，再压一点，保证相邻头像不贴在一起。
const rings = [
  { name: "内圈", radius: 108, node: 25 },
  { name: "中圈", radius: 190, node: 18 },
  { name: "外圈", radius: 268, node: 13 }
].map((ring) => ({
  ...ring,
  capacity: Math.floor((2 * Math.PI * ring.radius) / (ring.node * 2 + 12))
}));
// 内圈只留最亲近的几个人，挤满了就不算「内圈」了。
rings[0].capacity = Math.min(rings[0].capacity, 8);

const focusedID = ref("");

const botNode = computed(() => props.graph.nodes.find((node) => node.is_bot) ?? null);
const centerLabel = computed(() => botNode.value?.display_name?.slice(0, 4) || "Diana");

const weightToBot = computed(() => {
  const botID = props.graph.bot_id;
  const weights = new Map<string, number>();
  if (!botID) return weights;
  for (const edge of props.graph.edges) {
    if (edge.source === botID) weights.set(edge.target, edge.weight);
    else if (edge.target === botID) weights.set(edge.source, edge.weight);
  }
  return weights;
});

const ordered = computed(() => {
  const weights = weightToBot.value;
  return props.graph.nodes
    .filter((node) => !node.is_bot)
    .map((node) => ({ node, weight: weights.get(node.user_id) ?? 0 }))
    .sort((left, right) => right.weight - left.weight || right.node.messages - left.node.messages);
});

interface PlacedMember {
  userID: string;
  ring: number;
  x: number;
  y: number;
  radius: number;
  weight: number;
  strength: number;
  name: string;
  initial: string;
  label: string;
  fill: string;
  tooltip: string;
}

const placed = computed<PlacedMember[]>(() => {
  const list = ordered.value;
  const maxWeight = Math.max(1, ...list.map((item) => item.weight));
  const out: PlacedMember[] = [];
  let cursor = 0;
  for (let ringIndex = 0; ringIndex < rings.length && cursor < list.length; ringIndex++) {
    const ring = rings[ringIndex];
    const slice = list.slice(cursor, cursor + ring.capacity);
    cursor += slice.length;
    // 每圈错开半格起点，相邻两圈的头像不会正好排成一条射线。
    const offset = ringIndex % 2 === 0 ? 0 : Math.PI / Math.max(slice.length, 1);
    slice.forEach(({ node, weight }, index) => {
      const angle = -Math.PI / 2 + offset + (index * 2 * Math.PI) / slice.length;
      const name = node.display_name?.trim() || node.user_id;
      const strength = weight / maxWeight;
      out.push({
        userID: node.user_id,
        ring: ringIndex,
        x: center + ring.radius * Math.cos(angle),
        y: center + ring.radius * Math.sin(angle),
        radius: ring.node,
        weight,
        strength,
        name,
        initial: Array.from(name)[0] ?? "",
        label: name.length > 6 ? `${name.slice(0, 6)}…` : name,
        // 没跟机器人说过话的人用中性底色，有来往的越往里颜色越深。
        fill: weight > 0
          ? `color-mix(in srgb, var(--accent) ${[100, 62, 34][ringIndex]}%, var(--surface-2))`
          : "var(--surface-2)",
        tooltip: `${name} · ${ring.name} · 发言 ${node.messages} 条 · 与机器人互动 ${weight} 次 · 好感度 ${node.favorability}`
      });
    });
  }
  return out;
});

const trimmed = computed(() => props.graph.participants > props.graph.nodes.length);
// 三圈都排满后剩下的人：按互动排在最后，不画。
const overflow = computed(() => ordered.value.length - placed.value.length);

const usedRings = computed(() => {
  const deepest = Math.max(-1, ...placed.value.map((member) => member.ring));
  return rings.slice(0, deepest + 1);
});

const focused = computed(() => placed.value.find((member) => member.userID === focusedID.value) ?? null);

// 悬停某人时，他和其他成员之间的往来也画出来，只画他的。
const focusedPeerEdges = computed(() => {
  const current = focused.value;
  if (!current) return [];
  const botID = props.graph.bot_id;
  const positions = new Map(placed.value.map((member) => [member.userID, member]));
  const edges = props.graph.edges.filter((edge) =>
    edge.source !== botID && edge.target !== botID && (edge.source === current.userID || edge.target === current.userID)
  );
  const maxWeight = Math.max(1, ...edges.map((edge) => edge.weight));
  return edges.flatMap((edge) => {
    const other = positions.get(edge.source === current.userID ? edge.target : edge.source);
    if (!other) return [];
    // 往圆心方向弯一点，不和中心那条线叠在一起。
    const midX = (current.x + other.x) / 2;
    const midY = (current.y + other.y) / 2;
    const controlX = midX + (center - midX) * 0.4;
    const controlY = midY + (center - midY) * 0.4;
    return [{
      target: other.userID,
      path: `M ${current.x} ${current.y} Q ${controlX} ${controlY} ${other.x} ${other.y}`,
      strokeWidth: 1 + 2 * (edge.weight / maxWeight)
    }];
  });
});

const focusedNeighbors = computed(() => new Set(focusedPeerEdges.value.map((edge) => edge.target)));

const tagWidth = computed(() => {
  const label = focused.value?.label ?? "";
  // 中文按一个字 12px、其余按 7px 估宽，够放就行。
  const width = Array.from(label).reduce((sum: number, char: string) => sum + (char.charCodeAt(0) > 0x2e7f ? 12 : 7), 0);
  return width + 16;
});

const ranking = computed(() => placed.value.filter((member) => member.weight > 0).slice(0, 8));
</script>

<style scoped>
.relation-chart {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.relation-svg {
  width: 100%;
  max-height: 62vh;
  height: auto;
  overflow: visible;
}

.relation-ring {
  fill: none;
  stroke: var(--border);
  stroke-dasharray: 3 5;
}

.relation-ring-label {
  fill: var(--text-secondary);
  font-size: 11px;
  opacity: 0.8;
}

.relation-center-edge {
  stroke: var(--accent);
  stroke-width: 2.5;
  stroke-linecap: round;
  opacity: 0.85;
}

.relation-peer-edge {
  fill: none;
  stroke: var(--text-secondary);
  stroke-linecap: round;
  opacity: 0.55;
}

.relation-member {
  cursor: default;
  transition: opacity 0.15s;
}

.relation-node {
  stroke: var(--surface);
  stroke-width: 2.5;
}

/* 悬停时其余人压暗，只留他本人和跟他有来往的人。 */
.relation-focusing .relation-member {
  opacity: 0.3;
}

.relation-focusing .relation-member.relation-active {
  opacity: 1;
}

.relation-center-halo {
  fill: color-mix(in srgb, var(--accent) 16%, var(--surface));
}

.relation-center {
  fill: var(--accent);
}

.relation-center-label {
  fill: var(--accent-contrast);
  font-size: 13px;
  font-weight: 600;
}

.relation-initial {
  fill: var(--text);
  font-weight: 600;
  pointer-events: none;
}

.relation-initial-strong {
  fill: var(--accent-contrast);
}

.relation-label {
  fill: var(--text-secondary);
  font-size: 12px;
  pointer-events: none;
}

.relation-tag {
  pointer-events: none;
}

.relation-tag rect {
  fill: var(--text);
  opacity: 0.9;
}

.relation-tag text {
  fill: var(--surface);
  font-size: 12px;
}

.relation-ranking {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 6px 18px;
  margin: 0;
  padding: 10px 0 0;
  list-style: none;
  border-top: 1px solid var(--border);
}

.relation-ranking li {
  display: grid;
  grid-template-columns: 16px minmax(0, 5em) 1fr auto;
  align-items: center;
  gap: 8px;
  padding: 3px 6px;
  border-radius: 6px;
  font-size: 12px;
}

.relation-ranking li.active {
  background: var(--surface-2);
}

.relation-rank {
  color: var(--text-secondary);
  font-variant-numeric: tabular-nums;
}

.relation-rank-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.relation-rank-bar {
  height: 6px;
  border-radius: 3px;
  background: var(--surface-2);
  overflow: hidden;
}

.relation-rank-bar span {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--accent);
}

.relation-rank-count {
  font-variant-numeric: tabular-nums;
}

.relation-empty,
.relation-legend {
  font-size: 12px;
  margin: 0;
}
</style>
