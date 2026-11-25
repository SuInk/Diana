<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="token-breakdown stack" style="gap: 12px" aria-label="Token 分类统计">
    <div class="token-breakdown-controls">
      <div class="segmented" role="group" aria-label="Token 统计维度">
        <button type="button" :class="{ active: dimension === 'purpose' }" :aria-pressed="dimension === 'purpose'" @click="dimension = 'purpose'">按使用方式</button>
        <button type="button" :class="{ active: dimension === 'model' }" :aria-pressed="dimension === 'model'" @click="dimension = 'model'">按模型</button>
        <button type="button" :class="{ active: dimension === 'provider' }" :aria-pressed="dimension === 'provider'" @click="dimension = 'provider'">按提供商</button>
        <button v-if="$slots.groups" type="button" :class="{ active: dimension === 'group' }" :aria-pressed="dimension === 'group'" @click="dimension = 'group'">按群聊</button>
      </div>
      <div v-if="dimension !== 'group'" class="token-breakdown-actions">
        <label class="token-breakdown-filter">
          <span class="muted">{{ dimension === 'purpose' ? '模型' : '使用方式' }}</span>
          <AppSelect v-model="filter" :options="filterOptions" :disabled="options.length === 0" :aria-label="dimension === 'purpose' ? '按模型筛选' : '按使用方式筛选'" />
        </label>
        <button v-if="rows.length > 0" class="btn ghost small" type="button" title="导出当前维度的 CSV 报表（UTF-8 BOM）" @click="exportCSV">
          导出 CSV
        </button>
      </div>
    </div>
    <slot v-if="dimension === 'group'" name="groups" />
    <p v-else-if="entries === undefined" class="hint" style="margin: 0">当前统计尚未提供分类明细。</p>
    <p v-else-if="rows.length === 0" class="hint" style="margin: 0">{{ filter ? '这个筛选条件下没有调用记录。' : '这个时间范围内还没有调用记录。' }}</p>
    <template v-else>
      <p class="hint" style="margin: 0">{{ filter ? '筛选合计' : '分类合计' }} {{ formatNumber(subtotal) }} Token · {{ formatNumber(calls) }} 次调用 · 缓存命中 {{ cacheUsageDisplay(cachedTokens, inputTokens) }} · 按总 Token 从高到低排列</p>
      <p class="hint token-breakdown-scroll-hint" style="margin: 0">左右滑动查看完整明细</p>
      <div class="token-breakdown-table-wrap" role="region" aria-label="Token 分类明细表，可横向滚动" tabindex="0">
        <table class="token-breakdown-table">
          <caption class="sr-only">{{ rangeLabel }}按{{ dimensionHeading }}统计的 Token 用量</caption>
          <thead>
            <tr>
              <th scope="col">{{ dimensionHeading }}</th>
              <th scope="col">总 Token</th>
              <th scope="col">占比</th>
              <th scope="col">调用</th>
              <th scope="col">输入</th>
              <th scope="col" title="同时显示缓存命中数量和利用率；利用率 = 缓存命中 ÷ 输入 Token">缓存命中</th>
              <th scope="col">输出</th>
              <th scope="col">吞吐 / 首字</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.key">
              <th scope="row">
                <span>{{ row.label }}</span>
                <small v-if="row.detail && row.detail !== row.label" class="muted">{{ row.detail }}</small>
              </th>
              <td class="token-breakdown-total">{{ formatNumber(row.total_tokens) }}</td>
              <td>{{ usageShare(row.total_tokens, subtotal) }}</td>
              <td>
                {{ formatNumber(row.recorded_calls) }}
                <small v-if="row.missing_usage_calls" class="text-warn">{{ formatNumber(row.missing_usage_calls) }} 次未报用量</small>
                <button
                  v-if="row.failed_calls"
                  type="button"
                  class="token-failed-link text-err"
                  title="点击查看这部分失败的模型调用日志"
                  @click="emit('inspectFailures', row)"
                >
                  失败 {{ formatNumber(row.failed_calls) }} 次 ↗
                </button>
              </td>
              <td>{{ formatNumber(row.input_tokens) }}</td>
              <td :title="cacheUsageDisplay(row.cached_input_tokens, row.input_tokens)">
                {{ formatNumber(row.cached_input_tokens) }}
                <small class="muted">{{ cacheUtilization(row.cached_input_tokens, row.input_tokens) }}</small>
              </td>
              <td>{{ formatNumber(row.output_tokens) }}</td>
              <td>
                <span>{{ row.output_tokens_per_second > 0 ? `${row.output_tokens_per_second.toFixed(1)} tok/s` : "—" }}</span>
                <small v-if="row.ttft_calls > 0 && row.avg_ttft_ms > 0" class="muted">首字 {{ formatDurationMS(row.avg_ttft_ms) }}</small>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="hint" style="margin: 0">使用方式指每次模型调用的用途；占比以{{ filter ? '筛选结果' : '当前范围' }}的总 Token 为分母。缓存命中同时显示数量和利用率，利用率 = 缓存命中 ÷ 输入 Token；缓存已包含在输入中。{{ missingCalls ? `有 ${formatNumber(missingCalls)} 次调用未报用量，Token 合计可能偏少。` : '' }}</p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { LLMUsageBreakdown } from "../api";
import { formatDurationMS, formatNumber } from "../lib/format";
import AppSelect from "./AppSelect.vue";
import { cacheUsageDisplay, cacheUtilization, groupLLMUsage, usageShare, type UsageDimension, type UsageGroup } from "../lib/llm-usage";

const props = defineProps<{ entries?: readonly LLMUsageBreakdown[]; rangeLabel: string }>();
const emit = defineEmits<{ inspectFailures: [row: UsageGroup] }>();
const dimension = ref<UsageDimension | "group">("purpose");
const dimensionHeading = computed(() => (dimension.value === "purpose" ? "使用方式" : dimension.value === "provider" ? "提供商" : "模型"));
const filter = ref("");
const filterOptions = computed(() => [{ value: "", label: dimension.value === "purpose" ? "全部模型" : "全部使用方式" }, ...options.value.map((option) => ({ value: option.key, label: option.label, hint: option.detail || undefined }))]);
const options = computed(() => groupLLMUsage(props.entries ?? [], dimension.value === "purpose" ? "model" : "purpose"));
const rows = computed(() => dimension.value === "group" ? [] : groupLLMUsage(props.entries ?? [], dimension.value, filter.value));
const subtotal = computed(() => rows.value.reduce((sum, row) => sum + row.total_tokens, 0));
const calls = computed(() => rows.value.reduce((sum, row) => sum + row.recorded_calls, 0));
const inputTokens = computed(() => rows.value.reduce((sum, row) => sum + row.input_tokens, 0));
const cachedTokens = computed(() => rows.value.reduce((sum, row) => sum + row.cached_input_tokens, 0));
const missingCalls = computed(() => rows.value.reduce((sum, row) => sum + row.missing_usage_calls, 0));

watch([dimension, () => props.rangeLabel], () => { filter.value = ""; });
watch(options, (values) => {
  if (filter.value && !values.some((option) => option.key === filter.value)) filter.value = "";
});

function csvCell(value: string | number): string {
  const text = String(value ?? "");
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

function exportCSV(): void {
  const lines = [
    [dimensionHeading.value, "附加信息", "总Token", "占比", "成功调用", "失败请求", "输入Token", "缓存命中Token", "缓存利用率", "输出Token", "输出吞吐(tok/s)", "首字均值(ms)"]
      .map(csvCell)
      .join(",")
  ];
  for (const row of rows.value) {
    lines.push(
      [
        row.label,
        row.detail,
        row.total_tokens,
        usageShare(row.total_tokens, subtotal.value),
        row.recorded_calls,
        row.failed_calls,
        row.input_tokens,
        row.cached_input_tokens,
        cacheUtilization(row.cached_input_tokens, row.input_tokens),
        row.output_tokens,
        row.output_tokens_per_second > 0 ? row.output_tokens_per_second.toFixed(1) : "",
        row.ttft_calls > 0 ? Math.round(row.avg_ttft_ms) : ""
      ]
        .map(csvCell)
        .join(",")
    );
  }
  const blob = new Blob([`\uFEFF${lines.join("\r\n")}`], { type: "text/csv;charset=utf-8;" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `diana-usage-${dimension.value}.csv`;
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  URL.revokeObjectURL(url);
}
</script>

<style scoped>
.token-breakdown {
  border-top: 1px solid var(--border);
  padding-top: 14px;
}
.token-breakdown-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; justify-content: space-between; }
.token-breakdown-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.token-breakdown-filter { display: flex; align-items: center; gap: 8px; font-size: 12px; min-width: 0; }
.token-breakdown-filter > span { flex: none; white-space: nowrap; }
.token-breakdown-filter > :last-child { width: min(280px, 100%); min-width: 0; }
.token-breakdown-table-wrap { overflow-x: auto; }
.token-breakdown-scroll-hint { display: none; }
.token-breakdown-table { width: 100%; border-collapse: collapse; font-size: 12px; font-variant-numeric: tabular-nums; }
.token-breakdown-table th, .token-breakdown-table td { padding: 10px 8px; text-align: right; white-space: nowrap; border-bottom: 1px solid var(--border); }
.token-breakdown-table thead th { color: var(--muted); font-weight: 500; }
.token-breakdown-table th:first-child { text-align: left; min-width: 120px; max-width: 220px; white-space: normal; overflow-wrap: anywhere; }
.token-breakdown-table tbody th { font-weight: 500; }
.token-breakdown-table small { display: block; margin-top: 3px; font-size: 11px; font-weight: 400; }
.token-breakdown-total { font-weight: 600; }
.token-failed-link { display: block; margin-top: 3px; margin-left: auto; padding: 0; border: 0; background: none; font: inherit; font-size: 11px; cursor: pointer; text-decoration: underline; text-underline-offset: 2px; }
.text-warn { color: var(--warn); }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 600px) {
  .token-breakdown-scroll-hint { display: block; }
  .token-breakdown-controls > .segmented { width: 100%; }
  .token-breakdown-controls > .segmented button { flex: 1; }
  .token-breakdown-filter { width: 100%; }
  .token-breakdown-filter > :last-child { flex: 1; width: auto; }
}
</style>
