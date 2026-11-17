<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 这台机器人各会话上一轮主回复第一步实际发给模型的请求，平均成一次。从上到下就是
       请求里的顺序，每块一个方框，高度按占比。颜色按这条消息在真实请求里的身份分四类，
       由后端按消息标记判定，不是按块名猜的。后端会拿请求和续接前缀逐条对账，对不上直接
       报错，这里只显示错误。 -->
  <section class="rc">
    <header class="rc-head">
      <h3>请求构成</h3>
      <span class="rc-sub">各个群和私聊上一轮主回复第一步实际发给模型的请求，平均成一次，按块估算 token。从上到下是请求里的顺序。</span>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading && !breakdown" class="hint">正在读取…</p>
    <p v-else-if="!profile" class="hint">先保存这台机器人，再回来看。</p>
    <p v-else-if="!breakdown" class="hint">最近 24 小时还没有主回复请求。机器人在群里或私聊回复过一次之后，这里就能看到请求构成。</p>
    <template v-else>
      <dl class="rc-stats">
        <div v-for="kind in kinds" :key="kind.key"><dt><i class="rc-swatch" :class="kind.key"></i>{{ kind.label }}</dt><dd class="mono">{{ formatNumber(kind.tokens) }}<span>（{{ percentOf(kind.tokens, breakdown.total_tokens) }}）</span></dd></div>
        <div><dt>本轮新发</dt><dd class="mono">{{ formatNumber(breakdown.new_tokens) }}<span>（{{ percentOf(breakdown.new_tokens, breakdown.total_tokens) }}，其余沿用上一轮请求）</span></dd></div>
        <div><dt>估算合计</dt><dd class="mono">{{ formatNumber(breakdown.total_tokens) }}<span> / 窗口 {{ formatNumber(windowSize) }}（{{ percentOf(breakdown.total_tokens, windowSize) }}）</span></dd></div>
        <div v-if="breakdown.input_tokens"><dt>供应商实计</dt><dd class="mono">{{ formatNumber(breakdown.input_tokens) }}<span>，命中缓存 {{ formatNumber(breakdown.cached_tokens ?? 0) }}（{{ percentOf(breakdown.cached_tokens ?? 0, breakdown.input_tokens) }}）</span></dd></div>
      </dl>
      <!-- 左边竖条：整列就是模型的上下文窗口，每块高度严格等于它占窗口的比例，不设保底，
           小块就是一条细线。右边清单按请求顺序列出每块的数字，两种占比都写。 -->
      <div class="rc-chart">
        <figure class="rc-col">
          <div class="rc-column" :style="{height: `${columnHeight}px`}" role="img" :aria-label="`实际：窗口 ${windowSize} token，请求 ${breakdown.total_tokens}`">
            <span v-for="segment in breakdown.segments" :key="segment.kind + segment.name" class="rc-fill" :class="segment.kind"
              :style="{height: `${(columnHeight * segment.tokens) / windowSize}px`}" :title="`${segment.name}：${formatNumber(segment.tokens)} token`"></span>
          </div>
          <figcaption>实际</figcaption>
        </figure>
        <figure class="rc-col">
          <div class="rc-column" :style="{height: `${columnHeight}px`}" role="img" :aria-label="`历史涨满时（推算）：请求 ${fullTotal} token`">
            <span v-for="segment in breakdown.segments" :key="segment.kind + segment.name" class="rc-fill" :class="segment.kind"
              :style="{height: `${(columnHeight * fullTokensOf(segment)) / windowSize}px`}" :title="`${segment.name}：${formatNumber(fullTokensOf(segment))} token（推算）`"></span>
          </div>
          <figcaption>历史涨满时<br />（推算）</figcaption>
        </figure>
        <div class="rc-list-wrap"><table class="rc-list">
          <thead>
            <tr><th scope="col" rowspan="2">块（按请求顺序）</th><th scope="colgroup" colspan="4">实际</th><th scope="colgroup" colspan="2">历史涨满时（推算）</th></tr>
            <tr><th scope="col">token</th><th scope="col">占请求</th><th scope="col">占窗口</th><th scope="col">本轮新发</th><th scope="col">token</th><th scope="col">占请求</th></tr>
          </thead>
          <tbody>
            <tr v-for="segment in breakdown.segments" :key="segment.kind + segment.name">
              <th scope="row"><i class="rc-swatch" :class="segment.kind"></i>{{ segment.name }}</th>
              <td class="mono">{{ formatNumber(segment.tokens) }}</td>
              <td class="mono">{{ percentOf(segment.tokens, breakdown.total_tokens) }}</td>
              <td class="mono">{{ percentOf(segment.tokens, windowSize) }}</td>
              <td class="mono">{{ formatNumber(segment.new_tokens) }}</td>
              <td class="mono">{{ formatNumber(fullTokensOf(segment)) }}</td>
              <td class="mono">{{ percentOf(fullTokensOf(segment), fullTotal) }}</td>
            </tr>
            <tr class="rc-free-row">
              <th scope="row"><i class="rc-swatch free"></i>窗口剩余</th>
              <td class="mono">{{ formatNumber(Math.max(0, windowSize - breakdown.total_tokens)) }}</td>
              <td class="mono">—</td>
              <td class="mono">{{ percentOf(Math.max(0, windowSize - breakdown.total_tokens), windowSize) }}</td>
              <td class="mono">—</td>
              <td class="mono">{{ formatNumber(Math.max(0, windowSize - fullTotal)) }}</td>
              <td class="mono">—</td>
            </tr>
          </tbody>
        </table></div>
      </div>
      <p class="hint">
        两根竖条整列都是模型的上下文窗口，每块高度严格按占窗口的比例画，不设保底。「历史涨满时」是推算：其余各块按实际大小，聊天历史涨到压缩阈值为止（再多就压缩）。「本轮新发」是和上一轮实际请求比，从头开始逐条相同的算沿用，其余算新发；沿用的部分才可能命中缓存。各块是本地估算，和供应商实计的差是分词器不同。<template v-if="limit">单次请求上限 {{ formatNumber(limit) }} token，超过就重新组装一次。</template>
        平均了最近 24 小时 {{ breakdown.sessions }} 个会话<template v-if="breakdown.saved_at">，最新一份记于 {{ formatTime(breakdown.saved_at) }}</template>。
      </p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getReplyContext, type ReplyContextBreakdown, type ReplyContextSegment } from "../api";
import { formatNumber } from "../lib/format";

const props = defineProps<{ profile: string }>();

const breakdown = ref<ReplyContextBreakdown | null>(null);
const loading = ref(false);
const loadError = ref("");

const limit = computed(() => breakdown.value?.compaction_threshold || breakdown.value?.context_window || 0);
const kinds = computed(() => {
  const segments = breakdown.value?.segments ?? [];
  const sum = (key: string) => segments.filter((segment) => segment.kind === key).reduce((total, segment) => total + segment.tokens, 0);
  return [
    { key: "prefix", label: "锁定的头部", tokens: sum("prefix") },
    { key: "history", label: "只追加的聊天历史", tokens: sum("history") },
    { key: "state", label: "进前缀的状态段", tokens: sum("state") },
    { key: "turn_only", label: "只用于本轮（每轮重发）", tokens: sum("turn_only") }
  ];
});
const columnHeight = 640;
const windowSize = computed(() => Math.max(breakdown.value?.context_window || 0, breakdown.value?.total_tokens || 1));
// 历史涨满时（推算）：头部、状态段、只用于本轮的段按实际大小不动，聊天历史一直涨到
// 压缩阈值——再多就会被压缩，所以这就是聊天历史占用最大的情况。实际已经超过阈值时
// 就按实际。
const historyTokens = computed(() => (breakdown.value?.segments ?? []).filter((segment) => segment.kind === "history").reduce((sum, segment) => sum + segment.tokens, 0));
const fullHistory = computed(() => {
  const total = breakdown.value?.total_tokens ?? 0;
  return Math.max(historyTokens.value, limit.value - (total - historyTokens.value));
});
const fullTotal = computed(() => (breakdown.value?.total_tokens ?? 0) - historyTokens.value + fullHistory.value);
function fullTokensOf(segment: ReplyContextSegment): number {
  if (segment.kind !== "history" || !historyTokens.value) return segment.tokens;
  return Math.round((segment.tokens * fullHistory.value) / historyTokens.value);
}

function percentOf(value: number, total: number): string {
  if (!total) return "0%";
  const percent = (100 * value) / total;
  return `${percent < 10 ? percent.toFixed(1) : Math.round(percent)}%`;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

async function load(): Promise<void> {
  if (!props.profile) return;
  loading.value = true;
  loadError.value = "";
  try {
    breakdown.value = (await getReplyContext(props.profile)).breakdown ?? null;
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : "读取失败";
  } finally {
    loading.value = false;
  }
}

watch(() => props.profile, () => {
  breakdown.value = null;
  void load();
}, { immediate: true });

defineExpose({ reload: load });
</script>

<style scoped>
.rc{display:flex;flex-direction:column;gap:12px}
.rc .hint{color:var(--muted);font-size:12px;margin:0}
.rc-head h3{margin:0;font-size:15px}
.rc-sub{display:block;margin-top:4px;color:var(--muted);font-size:12.5px}
.rc-stats{display:flex;flex-wrap:wrap;gap:6px 20px;margin:0}
.rc-stats div{display:flex;align-items:baseline;gap:8px}.rc-stats dt{color:var(--muted);font-size:12px;display:flex;align-items:center;gap:6px}.rc-stats dd{margin:0;font-size:13px;color:var(--text)}.rc-stats dd span{color:var(--muted)}
.rc-swatch{display:inline-block;width:10px;height:10px;border-radius:3px;border:1px solid}
/* 四类：灰是锁定的头部，青是只追加的历史，蓝紫是进前缀的状态段，赭是每轮重发的。 */
.rc-swatch.prefix,.rc-block.prefix{background:#4a4a4a;border-color:#6a6a6a}
.rc-swatch.history,.rc-block.history{background:#0b4f43;border-color:#1f8a73}
.rc-swatch.state,.rc-block.state{background:#2f3a6b;border-color:#5566b0}
.rc-swatch.turn_only,.rc-block.turn_only{background:#7a2d17;border-color:#b0482a}
.rc-chart{display:flex;gap:16px;align-items:flex-start}
.rc-col{flex:none;margin:0;display:flex;flex-direction:column;align-items:center;gap:6px}.rc-col figcaption{font-size:11.5px;color:var(--muted);text-align:center;line-height:1.3}
.rc-column{flex:none;width:56px;display:flex;flex-direction:column;border:1px dashed var(--border);border-radius:4px;overflow:hidden;background:transparent}
.rc-fill{display:block;flex:none;width:100%}
.rc-fill.prefix{background:#5a5a5a}.rc-fill.history{background:#1f8a73}.rc-fill.state{background:#5566b0}.rc-fill.turn_only{background:#b0482a}
.rc-list{width:100%;border-collapse:collapse;font-size:12.5px}
.rc-list th,.rc-list td{padding:5px 6px;border-bottom:1px solid color-mix(in srgb,var(--border) 60%,transparent);text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}
.rc-list thead th{color:var(--muted);font-weight:500;font-size:12px}
.rc-list th[scope=row],.rc-list thead th:first-child{text-align:left;white-space:normal;font-weight:400;color:var(--text)}
.rc-list .rc-swatch{margin-right:6px;vertical-align:-1px}
.rc-swatch.free{background:transparent;border-style:dashed;border-color:var(--muted)}
.rc-free-row th,.rc-free-row td{color:var(--muted)}
.rc-list-wrap{flex:1 1 auto;min-width:0;overflow-x:auto}
@media (max-width:640px){.rc-chart{flex-wrap:wrap}.rc-list-wrap{flex-basis:100%}}
.error-text{color:var(--danger)}
</style>
