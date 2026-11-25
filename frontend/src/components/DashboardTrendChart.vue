<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="trend-chart">
    <div class="trend-summary">
      <div v-for="stat in summaryItems" :key="stat.label" class="trend-summary-item">
        <span class="muted">{{ stat.label }}</span>
        <strong :class="stat.tone">{{ stat.value }}</strong>
      </div>
      <div class="trend-legend muted" aria-hidden="true">
        <span v-for="item in legendItems" :key="item.label" class="trend-legend-item">
          <i :class="item.dotClass" />
          {{ item.label }}
        </span>
      </div>
    </div>

    <div class="spark-plot trend-plot" @mouseleave="pointed = null">
      <div class="spark-y" aria-hidden="true">
        <span>{{ maxLabel }}</span>
        <span>{{ midLabel }}</span>
        <span>0</span>
      </div>
      <div class="spark-plot-main">
        <div
          class="spark-bars trend-bars"
          role="group"
          :aria-label="`${metricTitle}趋势，共 ${buckets.length} 个${granularity === 'day' ? '日' : '小时'}分桶`"
        >
          <button
            v-for="bucket in buckets"
            :key="bucket.start_unix"
            type="button"
            class="bar trend-bar-btn"
            :class="{ 'has-err': bucketErrorValue(bucket) > 0 }"
            :aria-label="bucketTooltip(bucket)"
            @mouseenter="pointBucket($event, bucket)"
            @focus="pointBucket($event, bucket)"
            @blur="pointed = null"
            @click="emit('selectBucket', bucket)"
          >
            <div class="bar-fill" :style="{ height: barHeight(bucketPrimaryValue(bucket)) }" />
            <div
              v-if="bucketSecondaryValue(bucket) > 0"
              class="bar-sub"
              :style="{ height: barHeight(bucketSecondaryValue(bucket)) }"
            />
            <div
              v-if="bucketErrorValue(bucket) > 0"
              class="bar-err"
              :style="{ height: barHeight(bucketErrorValue(bucket), true) }"
            />
          </button>
        </div>
        <div class="spark-axis">
          <span>{{ firstLabel }}</span>
          <span v-if="midBucketLabel" class="muted">{{ midBucketLabel }}</span>
          <span>{{ lastLabel }}</span>
        </div>
      </div>
    </div>

    <Teleport to="body">
      <div
        v-if="pointed"
        class="trend-tooltip"
        role="tooltip"
        :style="{ left: `${pointed.x}px`, top: `${pointed.y}px` }"
      >
        <strong>{{ pointed.heading }}</strong>
        <span>{{ pointed.body }}</span>
        <small class="trend-tooltip-hint">{{ pointed.actionHint }}</small>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import type { StatsTrendBucket } from "../api";
import { cacheUtilization } from "../lib/llm-usage";
import { formatCompactNumber, formatHourLabel, formatNumber, niceAxisMax } from "../lib/format";

export type TrendMetric = "messages" | "tokens" | "calls";

const props = defineProps<{
  buckets: StatsTrendBucket[];
  granularity: "hour" | "day";
  metric: TrendMetric;
}>();

const emit = defineEmits<{
  selectBucket: [bucket: StatsTrendBucket];
}>();

const pointed = ref<{ heading: string; body: string; actionHint: string; x: number; y: number } | null>(null);
onBeforeUnmount(() => {
  pointed.value = null;
});

const metricTitle = computed(() => {
  if (props.metric === "tokens") return "Token 用量";
  if (props.metric === "calls") return "模型调用与错误";
  return "消息与回复";
});

function bucketPrimaryValue(bucket: StatsTrendBucket): number {
  if (props.metric === "tokens") return bucket.total_tokens;
  if (props.metric === "calls") return bucket.calls + bucket.failed_calls + bucket.app_errors;
  return bucket.messages;
}

function bucketSecondaryValue(bucket: StatsTrendBucket): number {
  if (props.metric === "tokens") return bucket.cached_input_tokens;
  if (props.metric === "calls") return bucket.calls;
  return bucket.handled;
}

function bucketErrorValue(bucket: StatsTrendBucket): number {
  if (props.metric === "tokens") return 0;
  if (props.metric === "calls") return bucket.failed_calls + bucket.app_errors;
  return bucket.errors;
}

const peak = computed(() => Math.max(0, ...props.buckets.map((bucket) => bucketPrimaryValue(bucket))));
const max = computed(() => niceAxisMax(peak.value));
const maxLabel = computed(() => (props.metric === "tokens" ? formatCompactNumber(max.value) : formatNumber(max.value)));
const midLabel = computed(() => (props.metric === "tokens" ? formatCompactNumber(max.value / 2) : formatNumber(max.value / 2)));

function formatBucketAxisLabel(unix: number): string {
  const date = new Date(unix * 1000);
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  if (props.granularity === "day") {
    return `${m}-${d}`;
  }
  if (props.buckets.length > 26) {
    return `${m}-${d} ${formatHourLabel(unix)}`;
  }
  return formatHourLabel(unix);
}

function formatBucketFullLabel(unix: number): string {
  const date = new Date(unix * 1000);
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  if (props.granularity === "day") {
    return `${y}-${m}-${d}`;
  }
  const endHour = `${String((date.getHours() + 1) % 24).padStart(2, "0")}:00`;
  return `${m}-${d} ${formatHourLabel(unix)}–${endHour}`;
}

const firstLabel = computed(() => (props.buckets.length > 0 ? formatBucketAxisLabel(props.buckets[0]!.start_unix) : ""));
const midBucketLabel = computed(() => {
  if (props.buckets.length < 7) return "";
  const mid = props.buckets[Math.floor(props.buckets.length / 2)];
  return mid ? formatBucketAxisLabel(mid.start_unix) : "";
});
const lastLabel = computed(() =>
  props.buckets.length > 0 ? formatBucketAxisLabel(props.buckets[props.buckets.length - 1]!.start_unix) : ""
);

function barHeight(value: number, isError = false): string {
  if (value <= 0) {
    return isError ? "0px" : "3px";
  }
  const minPct = isError ? 12 : 6;
  return `${Math.min(100, Math.max(minPct, Math.round((value / max.value) * 100)))}%`;
}

const summaryItems = computed(() => {
  if (props.metric === "tokens") {
    const total = props.buckets.reduce((s, b) => s + b.total_tokens, 0);
    const input = props.buckets.reduce((s, b) => s + b.input_tokens, 0);
    const cached = props.buckets.reduce((s, b) => s + b.cached_input_tokens, 0);
    const output = props.buckets.reduce((s, b) => s + b.output_tokens, 0);
    return [
      { label: "合计 Token", value: formatCompactNumber(total), tone: "" },
      { label: "输入 / 输出", value: `${formatCompactNumber(input)} / ${formatCompactNumber(output)}`, tone: "" },
      { label: "缓存利用率", value: cacheUtilization(cached, input), tone: "" }
    ];
  }
  if (props.metric === "calls") {
    const ok = props.buckets.reduce((s, b) => s + b.calls, 0);
    const failed = props.buckets.reduce((s, b) => s + b.failed_calls, 0);
    const appErr = props.buckets.reduce((s, b) => s + b.app_errors, 0);
    const rate = ok + failed > 0 ? `${((ok / (ok + failed)) * 100).toFixed(1)}%` : "—";
    return [
      { label: "成功调用", value: formatNumber(ok), tone: "" },
      { label: "模型成功率", value: rate, tone: failed > 0 ? "text-warn" : "text-ok" },
      { label: "失败 / 错误日志", value: `${formatNumber(failed)} / ${formatNumber(appErr)}`, tone: failed + appErr > 0 ? "text-err" : "" }
    ];
  }
  const messages = props.buckets.reduce((s, b) => s + b.messages, 0);
  const handled = props.buckets.reduce((s, b) => s + b.handled, 0);
  const errors = props.buckets.reduce((s, b) => s + b.errors, 0);
  const replyRate = messages > 0 ? `${((handled / messages) * 100).toFixed(1)}%` : "—";
  return [
    { label: "收到 / 已回复", value: `${formatNumber(messages)} / ${formatNumber(handled)}`, tone: "" },
    { label: "回复率", value: replyRate, tone: "" },
    { label: "异常事件", value: formatNumber(errors), tone: errors > 0 ? "text-err" : "" }
  ];
});

const legendItems = computed(() => {
  if (props.metric === "tokens") {
    return [
      { label: "总 Token", dotClass: "dot-primary" },
      { label: "缓存命中", dotClass: "dot-sub" }
    ];
  }
  if (props.metric === "calls") {
    return [
      { label: "模型成功", dotClass: "dot-sub" },
      { label: "失败与错误", dotClass: "dot-err" }
    ];
  }
  return [
    { label: "收到", dotClass: "dot-primary" },
    { label: "已回复", dotClass: "dot-sub" },
    { label: "异常", dotClass: "dot-err" }
  ];
});

function bucketBodyText(bucket: StatsTrendBucket): string {
  if (props.metric === "tokens") {
    return `总计 ${formatNumber(bucket.total_tokens)} · 输入 ${formatNumber(bucket.input_tokens)}（缓存 ${formatNumber(bucket.cached_input_tokens)}）· 输出 ${formatNumber(bucket.output_tokens)}`;
  }
  if (props.metric === "calls") {
    return `成功调用 ${formatNumber(bucket.calls)} · 模型失败 ${formatNumber(bucket.failed_calls)} · 错误日志 ${formatNumber(bucket.app_errors)}`;
  }
  return `收到 ${formatNumber(bucket.messages)} · 已回复 ${formatNumber(bucket.handled)} · 异常 ${formatNumber(bucket.errors)}`;
}

function bucketTooltip(bucket: StatsTrendBucket): string {
  return `${formatBucketFullLabel(bucket.start_unix)} — ${bucketBodyText(bucket)}`;
}

function pointBucket(event: Event, bucket: StatsTrendBucket): void {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  const hasErr = bucketErrorValue(bucket) > 0;
  pointed.value = {
    heading: formatBucketFullLabel(bucket.start_unix),
    body: bucketBodyText(bucket),
    actionHint:
      props.metric === "messages"
        ? hasErr
          ? "点击直达该时段的异常事件记录"
          : "点击查看该时段的事件明细"
        : props.metric === "calls" && hasErr
          ? "点击直达该时段的失败与错误日志"
          : "点击查看该时段的 LLM 调用日志",
    x: rect.left + rect.width / 2,
    y: rect.top
  };
}
</script>

<style scoped>
.trend-chart {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.trend-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 18px;
  font-size: 12px;
}

.trend-summary-item {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  font-variant-numeric: tabular-nums;
}

.trend-summary-item strong {
  font-size: 13.5px;
}

.trend-legend {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 10px;
  font-size: 11.5px;
}

.trend-legend-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.trend-legend-item i {
  width: 8px;
  height: 8px;
  border-radius: 2px;
  display: inline-block;
}

.dot-primary {
  background: color-mix(in srgb, var(--accent) 42%, var(--surface-2));
}

.dot-sub {
  background: var(--accent);
}

.dot-err {
  background: var(--err);
}

.trend-bars {
  gap: 3px;
}

.trend-bar-btn {
  padding: 0;
  border: 0;
  background: transparent;
  cursor: pointer;
  position: relative;
}

.trend-bar-btn:hover .bar-fill {
  filter: brightness(1.08);
}

.trend-bar-btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
  border-radius: 3px;
}

.trend-bar-btn .bar-fill {
  background: color-mix(in srgb, var(--accent) 36%, var(--surface-2));
}

.bar-sub {
  position: absolute;
  inset: auto 0 0 0;
  border-radius: 3px 3px 0 0;
  background: var(--accent);
  pointer-events: none;
}

.trend-tooltip {
  position: fixed;
  z-index: 1000;
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-width: min(360px, calc(100vw - 24px));
  padding: 6px 10px;
  border-radius: 8px;
  background: var(--text);
  color: var(--surface);
  font-size: 12px;
  line-height: 1.45;
  pointer-events: none;
  transform: translate(-50%, calc(-100% - 8px));
  box-shadow: 0 6px 18px rgba(0, 0, 0, 0.18);
}

.trend-tooltip-hint {
  opacity: 0.78;
  font-size: 11px;
}

@media (max-width: 640px) {
  .trend-legend {
    margin-left: 0;
  }
}
</style>
