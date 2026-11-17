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
        <div><dt>估算合计</dt><dd class="mono">{{ formatNumber(breakdown.total_tokens) }}<span v-if="limit"> / 上限 {{ formatNumber(limit) }}</span></dd></div>
        <div v-if="breakdown.input_tokens"><dt>供应商实计</dt><dd class="mono">{{ formatNumber(breakdown.input_tokens) }}<span>，命中缓存 {{ formatNumber(breakdown.cached_tokens ?? 0) }}（{{ percentOf(breakdown.cached_tokens ?? 0, breakdown.input_tokens) }}）</span></dd></div>
      </dl>
      <ol class="rc-stack" aria-label="请求构成，按请求顺序">
        <li v-for="segment in breakdown.segments" :key="segment.name" class="rc-block" :class="segment.kind"
          :style="{height: `${heightOf(segment)}px`}" :title="`${segment.name}：${formatNumber(segment.tokens)} token，${percentOf(segment.tokens, breakdown.total_tokens)}`">
          <strong>{{ segment.name }}</strong>
          <span>{{ formatNumber(segment.tokens) }} token · {{ percentOf(segment.tokens, breakdown.total_tokens) }} · 本轮新发 {{ formatNumber(segment.new_tokens) }}</span>
        </li>
      </ol>
      <p class="hint">
        方框高度按占比画，太小的块保底一行高度。「本轮新发」是和上一轮实际请求比，从头开始逐条相同的算沿用，其余算新发；沿用的部分才可能命中缓存。各块是本地估算，和供应商实计的差是分词器不同。<template v-if="limit">单次请求上限 {{ formatNumber(limit) }} token，超过就重新组装一次。</template>
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
// 每块保底 48px 写得下两行字，再按占比分 640px：占一半的块比保底高出 320px，比例一眼看得出。
function heightOf(segment: ReplyContextSegment): number {
  const total = breakdown.value?.total_tokens || 1;
  return Math.round(48 + (640 * segment.tokens) / total);
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
.rc-stack{display:flex;flex-direction:column;gap:6px;margin:0;padding:0;list-style:none}
.rc-block{flex:none;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:2px;padding:6px 12px;border:1px solid;border-radius:6px;text-align:center;overflow:hidden}
.rc-block strong{font-size:13.5px;font-weight:600;color:#f0efec;line-height:1.3}
.rc-block span{font-size:12px;color:rgba(240,239,236,.72);font-variant-numeric:tabular-nums;line-height:1.3}
.rc-block.history span{color:#5fd3b5}
.error-text{color:var(--danger)}
</style>
