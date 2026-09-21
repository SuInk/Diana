<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="residency-panel">
    <header class="view-header">
      <div class="view-title">
        <h1>上下文</h1>
        <p>{{ botScope ? '常驻的每轮都带完整定义，按需的只留一行目录、用到再加载' : '选择机器人后调整档位' }}</p>
      </div>
      <div class="view-actions"><button class="btn" :disabled="loading" @click="load"><RefreshCw :size="15" />刷新</button></div>
    </header>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取…</p>
    <template v-else>
      <!-- 常驻一多，请求里的工具数组就长，前缀缓存也更容易被下一次改动整片作废。
           这句话写在最前面，是因为它是这一页唯一的代价，改之前该先知道。 -->
      <p class="hint">常驻项越多，每轮请求越贵；改这一页会让所有会话的工具列表变一次，缓存重算一轮。Skill 的档位在 Skills 标签里设，常驻的 Skill 会把正文直接带进上下文。</p>
      <p v-if="!botScope" class="hint">先在顶部选一个机器人。</p>
      <p v-else-if="!items.length" class="hint">这台机器人还没跑过一轮对话。工具目录是按平台、权限和插件开关逐轮拼出来的，跑过一次之后这里才知道它实际有哪些工具。</p>
      <template v-else>
        <section v-for="section in sections" :key="section.kind" class="residency-section">
          <h2>{{ section.label }}</h2>
          <p class="hint">{{ section.hint }}</p>
          <div class="residency-list">
            <article v-for="item in section.items" :key="item.id" class="residency-row">
              <div class="residency-info">
                <strong>{{ item.name }}</strong>
                <p>{{ item.description || (item.tools?.length ? `${item.tools.length} 个工具：${item.tools.join('、')}` : '') }}</p>
              </div>
              <div class="segmented residency-state" role="group" :aria-label="`${item.name} 的上下文档位`">
                <button v-for="tier in tiers" :key="String(tier.value)" type="button" :class="{active: current(item) === tier.value}" :disabled="busy === item.id" :title="tierHint(tier.value, item)" @click="setTier(item, tier.value)">{{ tier.label }}</button>
              </div>
            </article>
          </div>
        </section>
      </template>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { RefreshCw } from '@lucide/vue';
import { botScope } from '../bot-scope';
import { listAgentResidency, setAgentResidency, type AgentResidencyEntry } from '../api';
import { toastError, toastSuccess } from '../toast';

const items = ref<AgentResidencyEntry[]>([]), loading = ref(false), loadError = ref(''), busy = ref('');
// 三档而不是一个开关：默认档是内置名单按线上运行占比排出来的，用户没表态时该跟着它走，
// 而不是被迫在常驻和按需之间二选一。
const tiers = [
  {value: null, label: '默认'},
  {value: true, label: '常驻'},
  {value: false, label: '按需'},
] as const;
const sections = computed(() => [
  {kind: 'tool', label: '内置工具', hint: '每个工具一档。', items: items.value.filter(i => i.kind === 'tool')},
  {kind: 'mcp', label: 'MCP 服务', hint: '一档管这条服务的全部工具。', items: items.value.filter(i => i.kind === 'mcp')},
].filter(section => section.items.length));

const current = (item: AgentResidencyEntry) => item.resident === undefined ? null : item.resident;
function tierHint(value: boolean | null, item: AgentResidencyEntry) {
  if (value === null) return item.default ? '跟随默认：这一项默认常驻' : '跟随默认：这一项默认按需';
  return value ? '每轮都带完整定义，模型直接调用' : '只留一行目录，模型要用先 tools_load';
}
let generation = 0;
async function load() {
  const current = ++generation;
  loading.value = true;
  loadError.value = '';
  try {
    const result = await listAgentResidency(botScope.value);
    if (current === generation) items.value = result.items;
  } catch (e) {
    if (current === generation) loadError.value = String(e instanceof Error ? e.message : e);
  } finally {
    if (current === generation) loading.value = false;
  }
}
async function setTier(item: AgentResidencyEntry, value: boolean | null) {
  const profile = botScope.value;
  if (!profile || current(item) === value) return;
  busy.value = item.id;
  try {
    await setAgentResidency(profile, item.id, value);
    toastSuccess('档位已更新，后续会话生效');
    await load();
  } catch (e) {
    toastError(String(e instanceof Error ? e.message : e));
  } finally {
    busy.value = '';
  }
}
watch(botScope, load);
onMounted(load);
</script>

<style scoped>
.residency-panel{padding-top:20px}.residency-section{margin-top:22px}.residency-section h2{font-size:15px;margin:0}.residency-section .hint{margin:4px 0 0}.residency-list{border-top:1px solid var(--border);margin-top:10px}.residency-row{display:flex;align-items:center;gap:12px;padding:14px 0;border-bottom:1px solid var(--border)}.residency-info{flex:1;min-width:0;overflow-wrap:anywhere}.residency-info p{margin:4px 0 0;color:var(--muted)}.residency-state{flex-shrink:0}.residency-state button:disabled{opacity:.45;cursor:not-allowed}.error-text{color:var(--danger)}@media(max-width:600px){.residency-row{flex-wrap:wrap;gap:8px}.residency-info{flex-basis:100%}}
</style>
