<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 这台机器人各会话上一轮主回复第一步实际发给模型的请求，平均成一次。从上到下就是
       请求里的顺序，每块一个整宽方框，颜色分三类，由后端按消息身份判定。 -->
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
        <div><dt>平均一次</dt><dd class="mono">{{ formatNumber(breakdown.total_tokens) }}<span> / 上限 {{ formatNumber(breakdown.context_window) }}（{{ percentOf(breakdown.total_tokens, breakdown.context_window) }}）</span></dd></div>
        <div><dt>本轮新发</dt><dd class="mono">{{ formatNumber(breakdown.new_tokens) }}<span>（{{ percentOf(breakdown.new_tokens, breakdown.total_tokens) }}，其余和上一轮相同）</span></dd></div>
        <div v-if="breakdown.input_tokens"><dt>供应商实计</dt><dd class="mono">{{ formatNumber(breakdown.input_tokens) }}<span>，命中缓存 {{ formatNumber(breakdown.cached_tokens ?? 0) }}（{{ percentOf(breakdown.cached_tokens ?? 0, breakdown.input_tokens) }}）</span></dd></div>
      </dl>
      <div class="rc-stack" role="list">
        <div v-for="segment in breakdown.segments" :key="segment.kind + segment.name" role="listitem" class="rc-block" :class="segment.kind"
          :style="{height: `${blockHeight(segment)}px`}">
          <strong>{{ segment.name }}</strong>
          <span>{{ formatNumber(segment.tokens) }} token · {{ percentOf(segment.tokens, breakdown.total_tokens) }} · 本轮新发 {{ formatNumber(segment.new_tokens) }}</span>
        </div>
      </div>
      <ul class="rc-legend">
        <li><i class="rc-swatch prefix"></i>工具定义和头部</li>
        <li><i class="rc-swatch history"></i>聊天历史</li>
        <li><i class="rc-swatch changing"></i>每轮或按人变化</li>
      </ul>
      <p class="hint">
        块高大致按比例，小块保底一行，数字以块里写的为准。「本轮新发」是和同一会话上一轮请求比，从头开始逐条相同的算沿用，沿用的部分才可能命中缓存。各块是本地估算，和供应商实计的差是分词器不同。
        平均了最近 24 小时 {{ breakdown.sessions }} 个会话<template v-if="breakdown.saved_at">，最新一份记于 {{ formatTime(breakdown.saved_at) }}</template>。
      </p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { getReplyContext, type ReplyContextBreakdown, type ReplyContextSegment } from "../api";
import { formatNumber } from "../lib/format";

const props = defineProps<{ profile: string }>();

const breakdown = ref<ReplyContextBreakdown | null>(null);
const loading = ref(false);
const loadError = ref("");

const stackHeight = 640;
const rowHeight = 52;
function blockHeight(segment: ReplyContextSegment): number {
  return Math.max(rowHeight, Math.round((stackHeight * segment.tokens) / (breakdown.value?.total_tokens || 1)));
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
</script>

<style scoped>
.rc{display:flex;flex-direction:column;gap:12px}
.rc .hint{color:var(--muted);font-size:12px;margin:0}
.rc-head h3{margin:0;font-size:15px}
.rc-sub{display:block;margin-top:4px;color:var(--muted);font-size:12.5px}
.rc-stats{display:flex;flex-wrap:wrap;gap:6px 20px;margin:0}
.rc-stats div{display:flex;align-items:baseline;gap:8px}.rc-stats dt{color:var(--muted);font-size:12px}.rc-stats dd{margin:0;font-size:13px;color:var(--text)}.rc-stats dd span{color:var(--muted)}
.rc-stack{display:flex;flex-direction:column;gap:8px}
.rc-block{box-sizing:border-box;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:2px;padding:6px 12px;border:1px solid;border-radius:6px;text-align:center;overflow:hidden}
.rc-block strong{font-size:14px;font-weight:600;color:#f2f2f2}
.rc-block span{font-size:12.5px}
.rc-block.prefix{background:#4a4a4a;border-color:#6a6a6a}.rc-block.prefix span{color:#cfcfcf}
.rc-block.history{background:#0b4f43;border-color:#1f8a73}.rc-block.history span{color:#5fd0b0}
.rc-block.changing{background:#7a2d17;border-color:#b0482a}.rc-block.changing span{color:#f0a080}
.rc-legend{display:flex;flex-wrap:wrap;gap:6px 28px;margin:0;padding:0;list-style:none;font-size:12.5px;color:var(--text)}
.rc-legend li{display:flex;align-items:center;gap:8px}
.rc-swatch{display:inline-block;width:14px;height:14px;border-radius:3px;border:1px solid}
.rc-swatch.prefix{background:#4a4a4a;border-color:#6a6a6a}.rc-swatch.history{background:#0b4f43;border-color:#1f8a73}.rc-swatch.changing{background:#7a2d17;border-color:#b0482a}
.error-text{color:var(--danger)}
</style>
