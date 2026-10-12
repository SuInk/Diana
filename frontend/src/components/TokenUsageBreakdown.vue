<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="token-breakdown stack" style="gap: 12px" aria-label="Token 分类统计">
    <div class="token-breakdown-controls">
      <div class="segmented" role="group" aria-label="Token 统计维度">
        <button type="button" :class="{ active: dimension === 'purpose' }" :aria-pressed="dimension === 'purpose'" @click="dimension = 'purpose'">按使用方式</button>
        <button type="button" :class="{ active: dimension === 'model' }" :aria-pressed="dimension === 'model'" @click="dimension = 'model'">按模型</button>
      </div>
      <label class="token-breakdown-filter">
        <span class="muted">{{ dimension === 'purpose' ? '模型' : '使用方式' }}</span>
        <select v-model="filter" class="input" :disabled="options.length === 0">
          <option value="">{{ dimension === 'purpose' ? '全部模型' : '全部使用方式' }}</option>
          <option v-for="option in options" :key="option.key" :value="option.key">{{ option.label }}{{ option.detail ? `（${option.detail}）` : '' }}</option>
        </select>
      </label>
    </div>
    <p v-if="entries === undefined" class="hint" style="margin: 0">当前统计尚未提供分类明细。</p>
    <p v-else-if="rows.length === 0" class="hint" style="margin: 0">{{ filter ? '这个筛选条件下没有调用记录。' : '这个时间范围内还没有调用记录。' }}</p>
    <template v-else>
      <p class="hint" style="margin: 0">{{ filter ? '筛选合计' : '分类合计' }} {{ formatNumber(subtotal) }} Token · {{ formatNumber(calls) }} 次调用 · 缓存命中 {{ cacheUsageDisplay(cachedTokens, inputTokens) }} · 按总 Token 从高到低排列</p>
      <p class="hint token-breakdown-scroll-hint" style="margin: 0">左右滑动查看完整明细</p>
      <div class="token-breakdown-table-wrap" role="region" aria-label="Token 分类明细表，可横向滚动" tabindex="0">
        <table class="token-breakdown-table">
          <caption class="sr-only">{{ rangeLabel }}按{{ dimension === 'purpose' ? '使用方式' : '模型' }}统计的 Token 用量</caption>
          <thead>
            <tr>
              <th scope="col">{{ dimension === 'purpose' ? '使用方式' : '模型' }}</th>
              <th scope="col">总 Token</th>
              <th scope="col">占比</th>
              <th scope="col">调用</th>
              <th scope="col">输入</th>
              <th scope="col" title="同时显示缓存命中数量和利用率；利用率 = 缓存命中 ÷ 输入 Token">缓存命中</th>
              <th scope="col">输出</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.key">
              <th scope="row">
                <span>{{ row.label }}</span>
                <small v-if="row.detail" class="muted">{{ row.detail }}</small>
              </th>
              <td class="token-breakdown-total">{{ formatNumber(row.total_tokens) }}</td>
              <td>{{ usageShare(row.total_tokens, subtotal) }}</td>
              <td>
                {{ formatNumber(row.recorded_calls) }}
                <small v-if="row.missing_usage_calls" class="text-warn">{{ formatNumber(row.missing_usage_calls) }} 次未报用量</small>
              </td>
              <td>{{ formatNumber(row.input_tokens) }}</td>
              <td :title="cacheUsageDisplay(row.cached_input_tokens, row.input_tokens)">
                {{ formatNumber(row.cached_input_tokens) }}
                <small class="muted">{{ cacheUtilization(row.cached_input_tokens, row.input_tokens) }}</small>
              </td>
              <td>{{ formatNumber(row.output_tokens) }}</td>
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
import { formatNumber } from "../format";
import { cacheUsageDisplay, cacheUtilization, groupLLMUsage, usageShare, type UsageDimension } from "../llm-usage";

const props = defineProps<{ entries?: readonly LLMUsageBreakdown[]; rangeLabel: string }>();
const dimension = ref<UsageDimension>("purpose");
const filter = ref("");
const options = computed(() => groupLLMUsage(props.entries ?? [], dimension.value === "purpose" ? "model" : "purpose"));
const rows = computed(() => groupLLMUsage(props.entries ?? [], dimension.value, filter.value));
const subtotal = computed(() => rows.value.reduce((sum, row) => sum + row.total_tokens, 0));
const calls = computed(() => rows.value.reduce((sum, row) => sum + row.recorded_calls, 0));
const inputTokens = computed(() => rows.value.reduce((sum, row) => sum + row.input_tokens, 0));
const cachedTokens = computed(() => rows.value.reduce((sum, row) => sum + row.cached_input_tokens, 0));
const missingCalls = computed(() => rows.value.reduce((sum, row) => sum + row.missing_usage_calls, 0));

watch([dimension, () => props.rangeLabel], () => { filter.value = ""; });
watch(options, (values) => {
  if (filter.value && !values.some((option) => option.key === filter.value)) filter.value = "";
});
</script>

<style scoped>
.token-breakdown {
  border-top: 1px solid var(--border);
  padding-top: 14px;
}
.token-breakdown-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; justify-content: space-between; }
.token-breakdown-filter { display: flex; align-items: center; gap: 8px; font-size: 12px; min-width: 0; }
.token-breakdown-filter > span { flex: none; white-space: nowrap; }
.token-breakdown-filter select { width: min(280px, 100%); min-width: 0; }
.token-breakdown-table-wrap { overflow-x: auto; }
.token-breakdown-scroll-hint { display: none; }
.token-breakdown-table { width: 100%; border-collapse: collapse; font-size: 12px; font-variant-numeric: tabular-nums; }
.token-breakdown-table th, .token-breakdown-table td { padding: 10px 8px; text-align: right; white-space: nowrap; border-bottom: 1px solid var(--border); }
.token-breakdown-table thead th { color: var(--muted); font-weight: 500; }
.token-breakdown-table th:first-child { text-align: left; min-width: 120px; max-width: 220px; white-space: normal; overflow-wrap: anywhere; }
.token-breakdown-table tbody th { font-weight: 500; }
.token-breakdown-table small { display: block; margin-top: 3px; font-size: 11px; font-weight: 400; }
.token-breakdown-total { font-weight: 600; }
.text-warn { color: var(--warn); }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 600px) {
  .token-breakdown-scroll-hint { display: block; }
  .token-breakdown-controls > .segmented { width: 100%; }
  .token-breakdown-controls > .segmented button { flex: 1; }
  .token-breakdown-filter { width: 100%; }
  .token-breakdown-filter select { flex: 1; width: auto; }
}
</style>
