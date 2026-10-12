<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="latency-detail stack" aria-label="响应耗时分布">
    <div class="latency-detail-head">
      <h3>按时间窗</h3>
      <div class="segmented" role="radiogroup" aria-label="耗时统计窗口">
        <button
          v-for="option in windowOptions"
          :key="option.id"
          type="button"
          role="radio"
          :class="{ active: selected === option.id }"
          :aria-checked="selected === option.id"
          @click="selected = option.id"
        >
          {{ option.label }}
        </button>
      </div>
    </div>

    <LoadingSkeleton v-if="loading && !data" kind="form" label="正在读取响应耗时" />
    <p v-else-if="error" class="hint text-err" style="margin: 0">{{ error }}</p>
    <p v-else-if="!current || current.total.samples === 0" class="hint" style="margin: 0">
      {{ windowLabel }}里没有回复出去的消息，没有耗时可以统计。
    </p>
    <template v-else>
      <div class="latency-tiles">
        <div v-for="tile in tiles" :key="tile.label" class="latency-tile">
          <span class="muted">{{ tile.label }}</span>
          <strong>{{ formatDurationMS(tile.value) }}</strong>
          <span v-if="tile.trend" class="latency-trend" :class="tile.trend.tone" :title="tile.trend.hint">{{ tile.trend.text }}</span>
          <span v-else class="latency-trend muted" title="前一段没有回复，无从对比">—</span>
        </div>
      </div>
      <p class="hint" style="margin: 0">
        样本 {{ formatNumber(current.total.samples) }} 次回复<template v-if="previous && previous.total.samples > 0">，前一段 {{ formatNumber(previous.total.samples) }} 次</template>。
        趋势和紧挨着的前一段等长时间比，变慢标红、变快标绿。
      </p>

      <div class="latency-table-wrap">
        <table class="table latency-table">
          <thead>
            <tr>
              <th scope="col">阶段</th>
              <th scope="col" class="num col-samples">样本</th>
              <th scope="col" class="num">平均</th>
              <th scope="col" class="num">P50</th>
              <th scope="col" class="num">P90</th>
              <th scope="col" class="num">P99</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="phase in phases" :key="phase.key">
              <th scope="row" :title="`${phase.hint}；样本 ${formatNumber(phase.dist.samples)}`">{{ phase.label }}</th>
              <template v-if="phase.dist.samples > 0">
                <td class="num muted col-samples">{{ formatNumber(phase.dist.samples) }}</td>
                <td class="num">{{ formatDurationMS(phase.dist.avg_ms) }}</td>
                <td class="num">{{ formatDurationMS(phase.dist.p50_ms) }}</td>
                <td class="num">{{ formatDurationMS(phase.dist.p90_ms) }}</td>
                <td class="num">{{ formatDurationMS(phase.dist.p99_ms) }}</td>
              </template>
              <td v-else colspan="5" class="muted latency-unavailable">{{ phase.unavailable }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="latency-extremes">
        <div v-for="list in extremeLists" :key="list.title" class="stack" style="gap: 6px">
          <h3>{{ list.title }}</h3>
          <p v-if="list.items.length === 0" class="hint" style="margin: 0">样本太少，都已列在另一侧。</p>
          <article v-for="item in list.items" :key="item.event_id" class="latency-sample">
            <div class="latency-sample-head">
              <strong>{{ formatDurationMS(item.total_ms) }}</strong>
              <span class="muted latency-sample-where">{{ where(item) }}</span>
              <span class="muted" :title="formatTime(item.completed_at)">{{ formatRelative(item.completed_at) }}</span>
            </div>
            <div class="latency-sample-phases">
              <span v-for="part in breakdown(item)" :key="part.label" class="badge">{{ part.label }} {{ formatDurationMS(part.value) }}</span>
              <span v-if="breakdown(item).length === 0" class="muted">没有可拆分的阶段数据</span>
            </div>
          </article>
        </div>
      </div>
    </template>

    <p class="hint" style="margin: 0">
      整轮耗时不含开始前的等待，和卡片上的平均响应同一口径。模型推理是这一轮所有模型调用的耗时之和，并行调用时可能超过整轮；
      首 token 取这一轮第一个流式调用，没开流式的回复不计入；工具只算 Agent 工具，插件解析等其余步骤不单列。
      升级前的记录没有等待和工具耗时，所以这两项的样本会比整轮少。
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { getStatsLatency, type LatencyDistribution, type LatencyWindowID, type ReplyLatencySample, type StatsLatency } from "../api";
import { formatDurationMS, formatNumber, formatRelative, formatTime } from "../format";
import LoadingSkeleton from "./LoadingSkeleton.vue";

const props = defineProps<{
  profileId: string;
  initialWindow: LatencyWindowID;
  groupLabel: (groupID: string) => string;
}>();

const windowOptions: { id: LatencyWindowID; label: string; previous: string }[] = [
  { id: "1h", label: "1 小时", previous: "前 1 小时" },
  { id: "24h", label: "24 小时", previous: "前 24 小时" },
  { id: "7d", label: "7 天", previous: "前 7 天" }
];

const selected = ref<LatencyWindowID>(props.initialWindow);
// 每档单独请求、单独缓存：只读当前这一档要的日志，切回看过的档位不再等。弹窗关掉
// 组件就卸了，缓存跟着清，重新打开拿的是新数。
const cache = ref<Partial<Record<LatencyWindowID, StatsLatency>>>({});
const data = computed(() => cache.value[selected.value] ?? null);
const loading = ref(false);
const error = ref("");

// 切换机器人或档位时先发的请求可能后返回，只认最后一次，免得标题是这台、数据是上一台。
let requestSeq = 0;

async function load(): Promise<void> {
  const window = selected.value;
  if (cache.value[window]) return;
  const seq = ++requestSeq;
  loading.value = true;
  error.value = "";
  try {
    const result = await getStatsLatency(props.profileId, window);
    if (seq === requestSeq) cache.value = { ...cache.value, [window]: result };
  } catch (err) {
    if (seq === requestSeq) error.value = err instanceof Error ? err.message : "读取响应耗时失败";
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

onMounted(load);
watch(selected, load);
watch(
  () => props.profileId,
  () => {
    cache.value = {};
    void load();
  }
);

const activeOption = computed(() => windowOptions.find((option) => option.id === selected.value) ?? windowOptions[1]);
const windowLabel = computed(() => `最近 ${activeOption.value.label}`);
const activeWindow = computed(() => data.value?.windows.find((window) => window.id === selected.value) ?? null);
const current = computed(() => activeWindow.value?.current ?? null);
const previous = computed(() => activeWindow.value?.previous ?? null);

interface Trend {
  text: string;
  tone: string;
  hint: string;
}

// 变化不到 5% 算持平：样本少的时候分位数本来就会晃，标红标绿只会让人追着噪声跑。
function trend(now: number, before: number | undefined): Trend | null {
  if (before === undefined || before <= 0) return null;
  const change = (now - before) / before;
  const hint = `${activeOption.value.previous}：${formatDurationMS(before)}`;
  if (Math.abs(change) < 0.05) return { text: "持平", tone: "muted", hint };
  const percent = `${Math.round(Math.abs(change) * 100)}%`;
  return change > 0 ? { text: `慢 ${percent}`, tone: "text-err", hint } : { text: `快 ${percent}`, tone: "text-ok", hint };
}

const tiles = computed(() => {
  const total = current.value?.total;
  if (!total) return [];
  const before = previous.value && previous.value.total.samples > 0 ? previous.value.total : undefined;
  return [
    { label: "P50", value: total.p50_ms, trend: trend(total.p50_ms, before?.p50_ms) },
    { label: "P90", value: total.p90_ms, trend: trend(total.p90_ms, before?.p90_ms) },
    { label: "P99", value: total.p99_ms, trend: trend(total.p99_ms, before?.p99_ms) },
    { label: "平均", value: total.avg_ms, trend: trend(total.avg_ms, before?.avg_ms) }
  ];
});

const phases = computed<{ key: string; label: string; hint: string; unavailable: string; dist: LatencyDistribution }[]>(() => {
  const summary = current.value;
  if (!summary) return [];
  return [
    { key: "total", label: "整轮", hint: "从回复轮次开始到结束的墙钟耗时", unavailable: "—", dist: summary.total },
    {
      key: "wait",
      label: "开始前等待",
      hint: "进队列到回复轮次开始：排队、冷却和回复判断",
      unavailable: "这段时间的记录还没有等待数据",
      dist: summary.wait
    },
    { key: "ttft", label: "首 token", hint: "这一轮第一个流式模型调用的首 token 时延", unavailable: "没有流式调用，量不到首 token", dist: summary.ttft },
    { key: "model", label: "模型推理", hint: "这一轮所有模型调用的耗时之和", unavailable: "没有对上号的模型调用记录", dist: summary.model },
    { key: "tool", label: "工具执行", hint: "Agent 执行工具的耗时之和", unavailable: "没有带工具耗时的 Agent 记录", dist: summary.tool }
  ];
});

const extremeLists = computed(() => [
  { title: "最慢", items: current.value?.slowest ?? [] },
  { title: "最快", items: current.value?.fastest ?? [] }
]);

function where(sample: ReplyLatencySample): string {
  if (sample.group_id) return props.groupLabel(sample.group_id);
  return sample.kind === "private" ? "私聊" : sample.kind;
}

function breakdown(sample: ReplyLatencySample): { label: string; value: number }[] {
  const parts: { label: string; value: number | undefined }[] = [
    { label: "等待", value: sample.wait_ms },
    { label: "首 token", value: sample.ttft_ms },
    { label: sample.model_calls ? `模型×${sample.model_calls}` : "模型", value: sample.model_ms },
    { label: "工具", value: sample.tool_ms }
  ];
  return parts.filter((part): part is { label: string; value: number } => part.value !== undefined && part.value !== null);
}
</script>

<style scoped>
.latency-detail {
  gap: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}

.latency-detail h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.latency-detail-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.latency-tiles {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
}

.latency-tile {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-size: 12px;
}

.latency-tile strong {
  font-size: 18px;
  font-variant-numeric: tabular-nums;
  color: var(--text);
}

.latency-trend {
  font-size: 11.5px;
}

.latency-table-wrap {
  overflow-x: auto;
}

.latency-table th,
.latency-table td {
  padding: 7px 8px;
}

.latency-table tbody th {
  font-weight: 500;
  color: var(--text-secondary);
  white-space: nowrap;
  border-bottom: 1px solid var(--border);
  text-align: left;
}

.latency-table tbody tr:last-child th {
  border-bottom: none;
}

.latency-table .num {
  text-align: right;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.latency-unavailable {
  font-size: 12px;
}

.latency-extremes {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.latency-sample {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  font-size: 12px;
}

.latency-sample-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}

.latency-sample-head strong {
  font-size: 14px;
  font-variant-numeric: tabular-nums;
}

.latency-sample-where {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.latency-sample-phases {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

@media (max-width: 640px) {
  .latency-tiles {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .latency-extremes {
    grid-template-columns: minmax(0, 1fr);
  }

  /* 窄屏放不下六列：样本数收进阶段名的悬停提示，页面下方的说明里也有整轮样本数。 */
  .latency-table .col-samples {
    display: none;
  }

  .latency-table th,
  .latency-table td {
    padding: 7px 5px;
  }
}
</style>
