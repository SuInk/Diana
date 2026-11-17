<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 上一轮主回复实际发给模型的请求，从头到尾分块。数字来自真实请求，不是按配置估的：
       会话还没回复过就没有。上面一根总条按单次请求上限画，看得出离重开还有多远；下面
       每块一行，顺序就是请求里的顺序，头部在前、尾部在后。 -->
  <section class="rc">
    <header class="rc-head">
      <div>
        <h3>上一轮请求的构成</h3>
        <span class="rc-sub">某个会话上一轮主回复实际发给模型的内容，按块算 token。头部在会话里锁定不变，前缀缓存都能命中；尾部逐轮往后追加。</span>
      </div>
      <select v-if="sessions.length" v-model="session" class="input rc-select" aria-label="选择会话">
        <option v-for="item in sessions" :key="item.session" :value="item.session">{{ sessionLabel(item) }}</option>
      </select>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading && !breakdown" class="hint">正在读取…</p>
    <p v-else-if="!profile" class="hint">先保存这台机器人，再回来看。</p>
    <p v-else-if="!breakdown" class="hint">最近 24 小时还没有主回复请求。机器人在群里或私聊回复过一次之后，这里就能看到那一轮的请求构成。</p>
    <template v-else>
      <div class="rc-meter">
        <!-- 按这一轮请求的合计画：每块占多宽就是它占请求的比例。块多，靠两组色系里深浅
             交替、块间 2px 间隙和最小宽度保证每块都看得见；宽的块直接写百分比。 -->
        <div class="rc-total" role="img" :aria-label="`请求合计 ${breakdown.total_tokens} token，分 ${breakdown.segments.length} 块`">
          <span v-for="(segment, index) in breakdown.segments" :key="segment.name" class="rc-seg"
            :style="{flexGrow: segment.tokens, background: colors[index]}" :title="`${segment.name}：${formatNumber(segment.tokens)} token，${percentOf(segment.tokens, breakdown.total_tokens)}`">
            <em v-if="segment.tokens / breakdown.total_tokens >= 0.07">{{ percentOf(segment.tokens, breakdown.total_tokens) }}</em>
          </span>
        </div>
        <div class="rc-parts" aria-hidden="true">
          <span :style="{flexGrow: headTokens}">头部</span>
          <span :style="{flexGrow: breakdown.total_tokens - headTokens}">尾部</span>
        </div>
        <dl class="rc-stats">
          <div><dt>头部</dt><dd class="mono">{{ formatNumber(headTokens) }}</dd></div>
          <div><dt>尾部</dt><dd class="mono">{{ formatNumber(breakdown.total_tokens - headTokens) }}</dd></div>
          <div><dt>合计</dt><dd class="mono">{{ formatNumber(breakdown.total_tokens) }}<span v-if="limit"> / {{ formatNumber(limit) }}（{{ percentOf(breakdown.total_tokens, limit) }}）</span></dd></div>
        </dl>
        <p class="hint">
          <template v-if="limit">单次请求上限 {{ formatNumber(limit) }} token，请求超过它就重新组装一次。</template>
          <template v-if="breakdown.saved_at">这份请求记于 {{ formatTime(breakdown.saved_at) }}。</template>
        </p>
      </div>

      <table class="rc-rows">
        <caption class="sr-only">各块 token 数，按请求里的顺序</caption>
        <thead class="sr-only"><tr><th>块</th><th>占比</th><th>token</th></tr></thead>
        <template v-for="group in groups" :key="group.part">
          <tbody v-if="group.rows.length">
            <tr class="rc-group"><th colspan="3" scope="rowgroup">{{ group.label }}<small>{{ formatNumber(group.tokens) }} token · {{ percentOf(group.tokens, breakdown.total_tokens) }}</small></th></tr>
            <tr v-for="row in group.rows" :key="row.name" class="rc-row" :title="`${row.name}：${formatNumber(row.tokens)} token，占这一轮请求 ${percentOf(row.tokens, breakdown.total_tokens)}`">
              <th scope="row" class="rc-name"><i class="rc-dot" :style="{background: colorOf(row)}"></i>{{ row.name }}</th>
              <td class="rc-bar-cell"><span class="rc-bar" :style="{width: percentOf(row.tokens, largest), background: colorOf(row)}"></span></td>
              <td class="rc-num mono">{{ formatNumber(row.tokens) }}<small>{{ percentOf(row.tokens, breakdown.total_tokens) }}</small></td>
            </tr>
          </tbody>
        </template>
      </table>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getReplyContext, listBotGroups, type ReplyContextBreakdown, type ReplyContextSegment, type ReplyContextSession } from "../api";
import { formatNumber } from "../lib/format";

const props = defineProps<{ profile: string }>();

const sessions = ref<ReplyContextSession[]>([]);
const session = ref("");
const breakdown = ref<ReplyContextBreakdown | null>(null);
const loading = ref(false);
const loadError = ref("");
const groupNames = ref<Record<string, string>>({});

const limit = computed(() => breakdown.value?.compaction_threshold || breakdown.value?.context_window || 0);
// 每块一种颜色，按请求里的先后依次取八种分类色（相邻两块色觉障碍下也分得开）；超过八块
// 的从头再取一遍，换浅一档，和第一轮的隔得远，靠下面那行的块名对得上。
const palette = ["#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#0ca30c", "#9085e9", "#e66767"];
const colors = computed(() => (breakdown.value?.segments ?? []).map((_, index) =>
  index < palette.length ? palette[index] : `color-mix(in srgb,${palette[index % palette.length]} 55%,#ffffff)`));
function colorOf(row: ReplyContextSegment): string {
  const index = breakdown.value?.segments.indexOf(row) ?? -1;
  return colors.value[index] ?? "var(--accent)";
}
const headTokens = computed(() => (breakdown.value?.segments ?? []).filter((segment) => segment.part === "head").reduce((sum, segment) => sum + segment.tokens, 0));
const largest = computed(() => Math.max(1, ...(breakdown.value?.segments ?? []).map((segment) => segment.tokens)));
const groups = computed(() => {
  const segments = breakdown.value?.segments ?? [];
  return [
    { part: "head", label: "头部（锁定，每轮复用）" },
    { part: "tail", label: "尾部（历史、状态块、机器人发言，逐轮追加）" }
  ].map((group) => {
    const rows = segments.filter((segment) => segment.part === group.part);
    return { ...group, rows, tokens: rows.reduce((sum, row) => sum + row.tokens, 0) };
  });
});

function percentOf(value: number, total: number): string {
  if (!total) return "0%";
  const percent = (100 * value) / total;
  return `${percent < 10 ? percent.toFixed(1) : Math.round(percent)}%`;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function sessionLabel(item: ReplyContextSession): string {
  const name = item.group_id ? (groupNames.value[item.group_id] ? `${groupNames.value[item.group_id]}（${item.group_id}）` : `群 ${item.group_id}`) : item.user_id ? `私聊 ${item.user_id}` : item.session;
  return item.saved_at ? `${name} · ${formatTime(item.saved_at)}` : name;
}

async function load(): Promise<void> {
  if (!props.profile) return;
  loading.value = true;
  loadError.value = "";
  try {
    const response = await getReplyContext(props.profile, session.value);
    sessions.value = response.sessions;
    breakdown.value = response.breakdown ?? null;
    if (!session.value && response.breakdown) session.value = response.breakdown.session;
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : "读取失败";
  } finally {
    loading.value = false;
  }
}

async function loadGroupNames(): Promise<void> {
  try {
    const response = await listBotGroups(false, props.profile);
    groupNames.value = Object.fromEntries(response.groups.filter((group) => group.group_name).map((group) => [group.group_id, group.group_name as string]));
  } catch {
    // 群名只是标签，取不到就显示群号。
  }
}

watch(() => props.profile, () => {
  session.value = "";
  breakdown.value = null;
  void load();
  void loadGroupNames();
}, { immediate: true });
watch(session, (value, previous) => {
  if (previous && value !== previous) void load();
});

defineExpose({ reload: load });
</script>

<style scoped>
.rc{display:flex;flex-direction:column;gap:12px}
.rc .hint{color:var(--muted);font-size:12px;margin:0}
.rc-head{display:flex;align-items:flex-start;justify-content:space-between;gap:10px;flex-wrap:wrap}.rc-head>div{flex:1 1 260px;min-width:0}.rc-head h3{margin:0;font-size:15px}
.rc-sub{display:block;margin-top:4px;color:var(--muted);font-size:12.5px}
.rc-select{flex:0 1 280px;min-width:0;max-width:100%}
.rc-meter{display:flex;flex-direction:column;gap:8px;padding:12px 14px;border:1px solid var(--border);border-radius:var(--radius-sm)}
.rc-total{display:flex;gap:2px;height:22px;border-radius:4px;overflow:hidden}
.rc-seg{flex:1 1 0;min-width:4px;display:flex;align-items:center;justify-content:center;overflow:hidden}
.rc-seg em{font-style:normal;font-size:11px;font-weight:600;font-variant-numeric:tabular-nums;color:#fff;text-shadow:0 0 2px rgba(0,0,0,.55);white-space:nowrap}
.rc-parts{display:flex;gap:2px;margin-top:-4px}.rc-parts span{flex:1 1 0;min-width:24px;padding-top:3px;border-top:2px solid var(--border);color:var(--muted);font-size:11.5px;text-align:center;white-space:nowrap}
.rc-name .rc-dot{margin-right:6px;vertical-align:1px}
.rc-stats{display:flex;flex-wrap:wrap;gap:6px 20px;margin:0}
.rc-stats div{display:flex;align-items:baseline;gap:8px}.rc-stats dt{color:var(--muted);font-size:12px;display:flex;align-items:center;gap:6px}.rc-stats dd{margin:0;font-size:13px;color:var(--text)}.rc-stats dd span{color:var(--muted)}
.rc-dot{display:inline-block;width:8px;height:8px;border-radius:2px}
.rc-rows{width:100%;border-collapse:collapse;table-layout:fixed}
.rc-group th{padding:10px 2px 6px;border-bottom:1px solid var(--border);font-size:12.5px;font-weight:600;color:var(--text-secondary);text-align:left}
.rc-group small{margin-left:8px;font-weight:400;color:var(--muted);font-variant-numeric:tabular-nums}
.rc-row th,.rc-row td{padding:5px 4px;border-bottom:1px solid color-mix(in srgb,var(--border) 60%,transparent)}
.rc-row:hover{background:var(--surface-2,rgba(127,127,127,.06))}
.rc-name{width:44%;overflow-wrap:anywhere;line-height:1.4;font-size:12.5px;font-weight:400;color:var(--text);text-align:left}
.rc-bar-cell{width:auto}
.rc-bar{display:block;height:10px;min-width:2px;border-radius:0 4px 4px 0}
.rc-num{width:110px;font-size:12.5px;color:var(--text);text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}
.rc-num small{display:inline-block;min-width:42px;margin-left:6px;color:var(--muted)}
.error-text{color:var(--danger)}
.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
@media (max-width:560px){.rc-name{width:50%}.rc-num{width:92px}.rc-num small{display:none}}
</style>
