<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="relation-chart">
    <p v-if="members.length > 0" class="relation-legend muted">
      {{ graph.messages }} 条发言 · {{ graph.participants }} 人发过言<template v-if="trimmed">，只画发言最多的 {{ members.length }} 人</template>
      <template v-if="graph.truncated">（只统计了最近这批消息）</template>
      · 圆点大小是发言量，连线粗细是互动次数
    </p>
    <svg
      v-if="members.length > 0"
      :viewBox="`0 0 ${width} ${height}`"
      class="relation-svg"
      role="img"
      :aria-label="`群聊关系图：${members.length} 人围绕机器人，连线粗细表示互动次数`"
    >
      <!-- 成员之间的边先画，压在中心边下面：中心边是这张图的主线。 -->
      <path
        v-for="edge in peerEdges"
        :key="`peer-${edge.source}-${edge.target}`"
        :d="edge.path"
        class="relation-peer-edge"
        :style="{ strokeWidth: edge.strokeWidth, opacity: edge.opacity }"
      >
        <title>{{ edge.label }}</title>
      </path>

      <!-- 垫一圈背景色：接近对径的弦中点正好落在圆心，会从中心节点上划过去。 -->
      <circle :cx="center.x" :cy="center.y" :r="centerRadius + 12" class="relation-center-halo" />

      <line
        v-for="edge in centerEdges"
        :key="`center-${edge.userID}`"
        :x1="center.x"
        :y1="center.y"
        :x2="edge.x"
        :y2="edge.y"
        class="relation-center-edge"
        :style="{ strokeWidth: edge.strokeWidth, opacity: edge.opacity }"
      >
        <title>{{ edge.label }}</title>
      </line>

      <g v-for="member in members" :key="member.userID">
        <circle
          :cx="member.x"
          :cy="member.y"
          :r="member.radius"
          class="relation-node"
          :class="{ 'relation-node-linked': member.weight > 0 }"
        >
          <title>{{ member.tooltip }}</title>
        </circle>
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

    <p v-else class="relation-empty muted">这段时间群里没有发言记录，先聊几句再看。</p>

  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { GroupRelationGraph } from "../api";

const props = defineProps<{ graph: GroupRelationGraph }>();

const width = 720;
const height = 620;
const centerRadius = 30;
const center = { x: width / 2, y: height / 2 };
// 环半径留出标签的位置：名字写在圆点外侧，贴着边缘会被 viewBox 切掉。
const ringRadius = Math.min(width, height) / 2 - 84;

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
  return ordered.map((node, index) => {
    // 从正上方起顺时针铺开。
    const angle = -Math.PI / 2 + (index * 2 * Math.PI) / ordered.length;
    const x = center.x + ringRadius * Math.cos(angle);
    const y = center.y + ringRadius * Math.sin(angle);
    // 面积随发言量走，所以半径开方——直接按半径线性缩放会把话痨画得夸张得多。
    const radius = 5 + 13 * Math.sqrt(node.messages / maxMessages);
    const onRight = Math.cos(angle) >= 0;
    const weight = weights.get(node.user_id) ?? 0;
    const name = node.display_name?.trim() || node.user_id;
    return {
      userID: node.user_id,
      x,
      y,
      radius,
      weight,
      label: name.length > 8 ? `${name.slice(0, 8)}…` : name,
      anchor: onRight ? "start" : "end",
      labelX: x + (onRight ? radius + 7 : -(radius + 7)),
      labelY: y + 4,
      tooltip: `${name} · 发言 ${node.messages} 条 · 与机器人互动 ${weight} 次 · 好感度 ${node.favorability}`
    };
  });
});

const trimmed = computed(() => props.graph.participants > props.graph.nodes.length);

const centerEdges = computed(() => {
  const maxWeight = Math.max(1, ...members.value.map((member) => member.weight));
  return members.value
    .filter((member) => member.weight > 0)
    .map((member) => ({
      userID: member.userID,
      x: member.x,
      y: member.y,
      strokeWidth: 1 + 5 * (member.weight / maxWeight),
      opacity: 0.3 + 0.55 * (member.weight / maxWeight),
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
      strokeWidth: 0.6 + 1.4 * (edge.weight / maxWeight),
      opacity: 0.12 + 0.2 * (edge.weight / maxWeight),
      label: `${from.label} ↔ ${to.label}：${edge.weight} 次`
    };
  });
});
</script>

<style scoped>
.relation-chart {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.relation-svg {
  width: 100%;
  height: auto;
  overflow: visible;
}

/* 中心边是主线，用主题色；成员之间的边是背景信息，用中性色压下去。 */
.relation-center-edge {
  stroke: var(--accent);
  stroke-linecap: round;
}

.relation-peer-edge {
  fill: none;
  stroke: var(--text-secondary);
  stroke-linecap: round;
}

.relation-node {
  fill: var(--surface-2);
  stroke: var(--border);
  stroke-width: 1;
}

/* 和机器人有过来往的人描一圈主题色，一眼能看出谁真的在跟它说话。 */
.relation-node-linked {
  stroke: var(--accent);
  stroke-width: 1.5;
}

.relation-center-halo {
  fill: var(--surface);
}

.relation-center {
  fill: var(--accent);
}

.relation-center-label {
  fill: var(--accent-contrast);
  font-size: 13px;
  font-weight: 600;
}

.relation-label {
  fill: var(--text-secondary);
  font-size: 11px;
}

.relation-empty,
.relation-legend {
  font-size: 12px;
  margin: 0;
}
</style>
