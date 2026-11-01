<template>
  <component :is="embedded ? 'section' : 'details'" class="token-usage-panel" :class="{ embedded }" :open="embedded ? undefined : defaultOpen" @toggle="onToggle">
    <summary v-if="!embedded">群聊 Token 用量与排行</summary>
    <template v-if="opened">
      <div class="token-toolbar">
        <label for="group-token-hours">时间范围</label>
        <AppSelect id="group-token-hours" v-model="hours" :options="ranges" />
        <button class="btn" :disabled="loading" @click="load">刷新用量</button>
      </div>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-else-if="loading" role="status">正在读取用量…</p>
      <template v-else-if="report">
        <p>所选范围合计 {{ number(report.usage.total_tokens) }} Token · {{ number(report.usage.recorded_calls) }} 次调用<span v-if="report.usage.usage_missing_calls"> · {{ number(report.usage.usage_missing_calls) }} 次未报告用量</span></p>
        <p class="cache-summary">整体缓存利用率 <strong>{{ cacheUtilization(report.usage.cached_input_tokens, report.usage.input_tokens) }}</strong> · 命中 {{ number(report.usage.cached_input_tokens) }} / 输入 {{ number(report.usage.input_tokens) }} Token</p>
        <div class="token-table-wrap">
          <table class="token-table">
            <thead><tr><th>群聊</th><th>输入</th><th>输出</th><th>总量</th><th>缓存命中</th><th>缓存利用率</th><th>调用</th></tr></thead>
            <tbody>
              <tr v-for="row in report.groups" :key="`${row.profile_id}/${row.platform}/${row.group_id}`">
                <td><strong>{{ groupName(row.group_id,row.profile_id) }}</strong><small>{{ row.group_id }} · {{ row.platform || '历史记录未标记平台' }}<template v-if="!profile"> · {{ row.profile_id || '未标记机器人' }}</template></small></td>
                <td>{{ number(row.usage.input_tokens) }}</td><td>{{ number(row.usage.output_tokens) }}</td><td><strong>{{ number(row.usage.total_tokens) }}</strong></td><td>{{ number(row.usage.cached_input_tokens) }}</td>
                <td>{{ cacheUtilization(row.usage.cached_input_tokens, row.usage.input_tokens) }}</td>
                <td>{{ number(row.usage.recorded_calls) }}<small v-if="row.usage.usage_missing_calls">{{ row.usage.usage_missing_calls }} 次未报告用量</small></td>
              </tr>
              <tr v-if="!report.groups.length"><td colspan="7">此范围没有已归属群聊的用量记录。</td></tr>
            </tbody>
          </table>
        </div>
        <p class="hint">按所选滚动时间范围统计，群排行按总 Token 降序。包含回复、记忆提取、摘要等已记录调用。私聊和未归属后台调用计入合计，不进入群排行；旧日志不能补推归属。缓存利用率 = 缓存命中 / 输入 Token，包含所有用途，不剔除审核和触发判断；无输入时显示「—」。缓存命中已包含在输入量中，未报告用量或缓存数据的调用可能使统计偏低。</p>
      </template>
    </template>
  </component>
</template>
<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import AppSelect from "./AppSelect.vue";
import { cacheUtilization } from "../lib/llm-usage";
import { getGroupTokenUsage, type GroupUsageReport, type BotGroupSummary } from "../api";

const props = defineProps<{ profile: string; groups: BotGroupSummary[]; defaultOpen?: boolean; embedded?: boolean }>();
const opened = ref(!!props.embedded);
const loading = ref(false);
const error = ref("");
const hours = ref("24");
const report = ref<GroupUsageReport | null>(null);
const ranges = [
  { value: "1", label: "最近1小时" },
  { value: "24", label: "最近24小时" },
  { value: "168", label: "最近7天" },
  { value: "720", label: "最近30天" },
];
let generation = 0;
const number = (value: number) => value.toLocaleString("zh-CN");

function groupName(id: string, profile?: string): string {
  return props.groups.find((group) => group.group_id === id &&
    (!profile || !group.bot_profile_id || group.bot_profile_id === profile))?.group_name || "群聊";
}

async function load(): Promise<void> {
  const request = ++generation;
  loading.value = true;
  error.value = "";
  try {
    const result = await getGroupTokenUsage(props.profile, Number(hours.value));
    if (request === generation) report.value = { ...result, groups: [...result.groups].sort((a, b) => b.usage.total_tokens - a.usage.total_tokens) };
  } catch (cause) {
    if (request === generation) {
      report.value = null;
      error.value = cause instanceof Error ? cause.message : "读取用量失败";
    }
  } finally {
    if (request === generation) loading.value = false;
  }
}

function onToggle(event: Event): void {
  opened.value = (event.target as HTMLDetailsElement).open;
  if (opened.value) void load();
}

onMounted(() => { if (props.embedded) void load(); });

watch(() => [props.profile, hours.value], () => {
  generation++;
  report.value = null;
  if (opened.value) void load();
});
</script>
<style scoped>
.token-usage-panel{margin:10px 0;padding:12px;border:1px solid var(--border);border-radius:12px;background:var(--surface);}
.token-usage-panel.embedded{margin:0;padding:0;border:0;background:transparent;min-width:0;}
.embedded .token-toolbar{margin-top:0;}
summary{cursor:pointer;font-weight:600;}
.token-toolbar{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-top:16px;}
.token-toolbar :deep(.app-select){width:180px;min-width:150px;}
.token-table-wrap{overflow:auto;}
.token-table{width:100%;border-collapse:collapse;white-space:nowrap;font-variant-numeric:tabular-nums;}
.token-table th,.token-table td{padding:12px;text-align:right;border-bottom:1px solid var(--border);}
.token-table th:first-child,.token-table td:first-child{text-align:left;}
.token-table small{display:block;color:var(--muted);font-size:12px;margin-top:4px;}
.hint{font-size:12px;color:var(--muted);line-height:1.6;}
.cache-summary{font-size:13px;color:var(--text-secondary);}
.error{color:var(--err);}
</style>
