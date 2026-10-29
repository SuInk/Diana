<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="relation-chart">
    <p v-if="members.length > 0" class="relation-legend muted">
      {{ graph.messages }} 条发言 · {{ graph.participants }} 人发过言<template v-if="trimmed">，只画发言最多的 {{ members.length }} 人</template>
      <template v-if="graph.truncated">（只统计了最近这批消息）</template>
      · 圆点大小是发言量，颜色越深和机器人互动越多，悬停看单人
    </p>
    <svg
      v-if="members.length > 0"
      :viewBox="`0 0 ${width} ${height}`"
      class="relation-svg"
      :class="{ 'relation-focusing': focusedID }"
      role="img"
      :aria-label="`群聊关系图：${members.length} 人围绕机器人，连线粗细表示互动次数`"
      @mouseleave="focusedID = ''"
    >
      <!-- 成员之间的边先画，压在中心边下面：中心边是这张图的主线。 -->
      <path
        v-for="edge in peerEdges"
        :key="`peer-${edge.source}-${edge.target}`"
        :d="edge.path"
        class="relation-peer-edge"
        :class="{ 'relation-active': isActive(edge.source) || isActive(edge.target) }"
        :style="{ strokeWidth: edge.strokeWidth, opacity: edge.opacity }"
      >
        <title>{{ edge.label }}</title>
      </path>

      <!-- 垫一圈背景色：接近对径的弦中点正好落在圆心，会从中心节点上划过去。 -->
      <circle :cx="center.x" :cy="center.y" :r="centerRadius + 10" class="relation-center-halo" />

      <line
        v-for="edge in centerEdges"
        :key="`center-${edge.userID}`"
        :x1="center.x"
        :y1="center.y"
        :x2="edge.x"
        :y2="edge.y"
        class="relation-center-edge"
        :class="{ 'relation-active': isActive(edge.userID) }"
        :style="{ strokeWidth: edge.strokeWidth, opacity: edge.opacity }"
      >
        <title>{{ edge.label }}</title>
      </line>

      <g
        v-for="member in members"
        :key="member.userID"
        class="relation-member"
        :class="{ 'relation-active': isActive(member.userID) }"
        @mouseenter="focusedID = member.userID"
      >
        <circle
          :cx="member.x"
          :cy="member.y"
          :r="member.radius"
          class="relation-node"
          :style="{ fill: member.fill }"
        >
          <title>{{ member.tooltip }}</title>
        </circle>
        <text
          v-if="member.radius >= 11"
          :x="member.x"
          :y="member.y + 4"
          class="relation-initial"
          :class="{ 'relation-initial-strong': member.strength > 0.45 }"
          text-anchor="middle"
        >{{ member.initial }}</text>
        <text
          :x="member.labelX"
          :y="member.labelY"
          class="relation-label"
          :text-anchor="member.anchor"
        >{{ member.label }}</text>
      </g>

      <circle :cx="center.x" :cy="center.y" :r="centerRadius" class="relation-center" />
      <text :x="center.x" :y="center.y + 5" class="relation-center-label" text-anchor="middle">{{ centerLabel }}</text>
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

    <p v-else-if="members.length === 0" class="relation-empty muted">这段时间群里没有发言记录，先聊几句再看。</p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { GroupRelationGraph } from "../api";

const props = defineProps<{ graph: GroupRelationGraph }>();

const width = 760;
const height = 460;
const centerRadius = 32;
const center = { x: width / 2, y: height / 2 };
// 弹窗比较矮，环压成椭圆；两侧留出标签位置，名字写在圆点外侧，贴边会被 viewBox 切掉。
const ringRadiusX = width / 2 - 130;
const ringRadiusY = height / 2 - 36;

// 悬停的成员：只亮他自己的连线，其余压暗。
const focusedID = ref("");

function isActive(userID: string): boolean {
  return focusedID.value === userID;
}

const botNode = computed(() => props.graph.nodes.find((node) => node.is_bot) ?? null);
const centerLabel = computed(() => botNode.value?.display_name?.slice(0, 4) || "Diana");

// 每个成员和机器人的互动次数，决定他在环上的位置：强的排在正上方，依次顺时针。
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

const members = computed(() => {
  const list = props.graph.nodes.filter((node) => !node.is_bot);
  const weights = weightToBot.value;
  const ordered = [...list].sort((left, right) => {
    const delta = (weights.get(right.user_id) ?? 0) - (weights.get(left.user_id) ?? 0);
    return delta !== 0 ? delta : right.messages - left.messages;
  });
  const maxMessages = Math.max(1, ...ordered.map((node) => node.messages));
  const maxWeight = Math.max(1, ...ordered.map((node) => weights.get(node.user_id) ?? 0));
  return ordered.map((node, index) => {
    // 从正上方起顺时针铺开。
    const angle = -Math.PI / 2 + (index * 2 * Math.PI) / ordered.length;
    const x = center.x + ringRadiusX * Math.cos(angle);
    const y = center.y + ringRadiusY * Math.sin(angle);
    // 面积随发言量走，所以半径开方——直接按半径线性缩放会把话痨画得夸张得多。
    const radius = 7 + 13 * Math.sqrt(node.messages / maxMessages);
    const onRight = Math.cos(angle) >= 0;
    const weight = weights.get(node.user_id) ?? 0;
    const name = node.display_name?.trim() || node.user_id;
    const strength = weight / maxWeight;
    return {
      userID: node.user_id,
      x,
      y,
      radius,
      weight,
      strength,
      name,
      initial: Array.from(name)[0] ?? "",
      // 没跟机器人说过话的人用中性底色，有来往的按强度往主题色里调。
      fill: weight > 0
        ? `color-mix(in srgb, var(--accent) ${Math.round(28 + 72 * strength)}%, var(--surface-2))`
        : "var(--surface-2)",
      label: name.length > 7 ? `${name.slice(0, 7)}…` : name,
      anchor: onRight ? "start" : "end",
      labelX: x + (onRight ? radius + 8 : -(radius + 8)),
      labelY: y + 4,
      tooltip: `${name} · 发言 ${node.messages} 条 · 与机器人互动 ${weight} 次 · 好感度 ${node.favorability}`
    };
  });
});

// 和机器人互动最多的几个人，图上圆点挤在一起时这里能直接读数。
const ranking = computed(() => members.value.filter((member) => member.weight > 0).slice(0, 8));

const trimmed = computed(() => props.graph.participants > props.graph.nodes.length);

const centerEdges = computed(() => {
  const maxWeight = Math.max(1, ...members.value.map((member) => member.weight));
  return members.value
    .filter((member) => member.weight > 0)
    .map((member) => ({
      userID: member.userID,
      x: member.x,
      y: member.y,
      strokeWidth: 0.8 + 3 * (member.weight / maxWeight),
      opacity: 0.25 + 0.45 * (member.weight / maxWeight),
      label: `${member.label} ↔ ${centerLabel.value}：${member.weight} 次`
    }));
});

// 成员之间的边只画最强的一批。四十个人两两之间可以有几百条线；画三十条时
// 灰线已经糊成一片，把中心那圈主线压住了——这张图的主语是机器人，成员之间
// 的往来是背景信息，宁可少画。
const maxPeerEdges = 12;

const peerEdges = computed(() => {
  const botID = props.graph.bot_id;
  const positions = new Map(members.value.map((member) => [member.userID, member]));
  const candidates = props.graph.edges
    .filter((edge) => edge.source !== botID && edge.target !== botID)
    .filter((edge) => positions.has(edge.source) && positions.has(edge.target))
    .slice(0, maxPeerEdges);
  const maxWeight = Math.max(1, ...candidates.map((edge) => edge.weight));
  return candidates.map((edge) => {
    const from = positions.get(edge.source)!;
    const to = positions.get(edge.target)!;
    // 控制点往圆心方向拉，边成为弦而不是直穿中心的直线——直线会和中心边混在一起。
    const midX = (from.x + to.x) / 2;
    const midY = (from.y + to.y) / 2;
    const controlX = midX + (center.x - midX) * 0.55;
    const controlY = midY + (center.y - midY) * 0.55;
    return {
      source: edge.source,
      target: edge.target,
      path: `M ${from.x} ${from.y} Q ${controlX} ${controlY} ${to.x} ${to.y}`,
      strokeWidth: 0.6 + 1.2 * (edge.weight / maxWeight),
      opacity: 0.1 + 0.18 * (edge.weight / maxWeight),
      label: `${from.label} ↔ ${to.label}：${edge.weight} 次`
    };
  });
});
</script>

<style scoped>
.relation-chart {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.relation-svg {
  width: 100%;
  max-height: 60vh;
  height: auto;
  overflow: visible;
}

/* 中心边是主线，用主题色；成员之间的边是背景信息，用中性色压下去。 */
.relation-center-edge {
  stroke: var(--accent);
  stroke-linecap: round;
  transition: opacity 0.15s;
}

.relation-peer-edge {
  fill: none;
  stroke: var(--text-secondary);
  stroke-linecap: round;
  transition: opacity 0.15s;
}

.relation-member {
  cursor: default;
  transition: opacity 0.15s;
}

.relation-node {
  stroke: var(--surface);
  stroke-width: 2;
}

/* 悬停某人时其余压暗，只留他的连线。用 !important 盖过按权重算的内联透明度。 */
.relation-focusing .relation-center-edge,
.relation-focusing .relation-peer-edge {
  opacity: 0.05 !important;
}

.relation-focusing .relation-member {
  opacity: 0.35;
}

.relation-focusing .relation-center-edge.relation-active {
  opacity: 0.9 !important;
}

.relation-focusing .relation-peer-edge.relation-active {
  opacity: 0.6 !important;
}

.relation-focusing .relation-member.relation-active {
  opacity: 1;
}

.relation-center-halo {
  fill: color-mix(in srgb, var(--accent) 14%, var(--surface));
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
  font-size: 11px;
  font-weight: 600;
  pointer-events: none;
}

.relation-initial-strong {
  fill: var(--accent-contrast);
}

.relation-label {
  fill: var(--text-secondary);
  font-size: 12px;
}

.relation-member.relation-active .relation-label {
  fill: var(--text);
  font-weight: 600;
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
