<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="residency-panel">
    <div class="residency-head">
      <div class="residency-title">
        <h3>每轮带上的工具</h3>
        <!-- 一句话说清这一屏是什么，剩下的解释跟着它该出现的时机走：估算口径写在
             数字旁边，缓存代价写在「有改动待保存」那一行，都不在这里预先铺开。 -->
        <p v-if="items.length" class="residency-lede">名单里 <strong>{{ summary.resident }}</strong> 个工具，每轮固定花 <strong>{{ formatTokens(summary.residentTokens) }}</strong>；其余 {{ summary.deferred }} 个只占 {{ formatTokens(summary.deferredTokens) }} 目录，模型用到时自己加载。</p>
      </div>
      <button class="btn primary" type="button" :disabled="loading || !profileID" @click="openPicker">添加工具</button>
    </div>
    <p v-if="loadError" role="alert" class="error-text">{{ loadError }}</p>
    <p v-if="loading">正在读取…</p>
    <template v-else>
      <p v-if="!profileID" class="hint">先保存这台机器人，再回来改名单。</p>
      <template v-else-if="items.length">
        <p v-if="dirty" class="residency-dirty" role="status">
          <template v-if="reset">保存后退回推荐名单，之后跟着版本走</template>
          <template v-else>{{ dirtyCount }} 个工具的待遇会变</template><template v-if="delta">，每轮{{ delta > 0 ? '多' : '少' }} {{ formatTokens(Math.abs(delta)) }}</template>。改动在页面底部保存时才写进去，那一下会让所有会话的工具列表变一次、缓存重算一轮。
          <button class="link-button" type="button" @click="discard">放弃</button>
        </p>

        <div class="residency-list">
          <p v-if="!residentRows.length" class="hint">名单是空的：这一轮请求不会带任何工具定义，模型每次用工具都要先加载一次。</p>
          <template v-for="row in residentRows" :key="row.id">
          <article class="residency-row" :class="{changed: pending[row.id] !== undefined}">
            <div class="residency-info">
              <strong>{{ row.name }} <span class="badge">{{ kindLabel(row) }}</span> <span v-if="pending[row.id] !== undefined" class="badge accent">待保存</span></strong>
              <p v-if="row.stale">这台机器人重启后还没跑过对话，暂时列不出它的说明和开销；名单本身仍然生效。</p>
              <p v-else>{{ expanded[row.id] ? row.detail : row.description }}</p>
              <button v-if="!row.stale && hasDetail(row)" type="button" class="link-button" @click="expanded[row.id] = !expanded[row.id]">{{ expanded[row.id] ? '收起' : '展开完整说明' }}</button>
              <small v-if="row.tools?.length">{{ row.tools.length }} 个工具：{{ row.tools.join('、') }}</small>
              <small v-if="excludedOf(row).length" class="residency-excluded">
                其中排除了 {{ excludedOf(row).map(child => child.name).join('、') }}
                <button type="button" class="link-button" @click="restore(row)">放回来</button>
              </small>
              <small v-if="row.resident_tokens" class="residency-cost">每轮 {{ formatTokens(residentCost(row)) }}</small>
            </div>
            <div class="residency-actions">
              <button v-if="childrenOf(row).length" class="btn" type="button" @click="opened[row.id] = !opened[row.id]">{{ opened[row.id] ? '收起' : '逐个挑' }}</button>
              <button class="btn" type="button" @click="remove(row)">移除</button>
            </div>
          </article>
          <article v-for="child in opened[row.id] ? childrenOf(row) : []" :key="child.id" class="residency-row residency-child" :class="{changed: pending[child.id] !== undefined}">
            <div class="residency-info">
              <strong>{{ child.name }} <span v-if="pending[child.id] !== undefined" class="badge accent">待保存</span></strong>
              <p>{{ child.description }}</p>
              <small class="residency-cost">每轮 {{ formatTokens(child.resident_tokens || 0) }}</small>
            </div>
            <div class="residency-actions">
              <button v-if="isResident(child)" class="btn" type="button" @click="remove(child)">移除</button>
              <button v-else class="btn" type="button" @click="add(child)">添加</button>
            </div>
          </article>
          </template>
        </div>

        <p class="residency-foot">
          <span class="hint">开销按实际发给模型的工具定义估算，不含系统提示词和历史消息。</span>
          <button class="link-button" type="button" :disabled="!savedList && !dirty" @click="resetAll">恢复推荐名单</button>
        </p>

        <Modal v-if="picking" title="添加到常驻名单" wide @close="picking = false">
          <p class="hint">这些现在只在目录里露一行，模型要用得先 tools_load，多一次往返。加进名单就每轮都带完整定义。</p>
          <input ref="pickerInput" v-model="query" class="input residency-search" type="search" placeholder="搜索名称或说明" aria-label="搜索可添加的工具" />
          <div class="residency-list">
            <p v-if="!candidateRows.length" class="hint">{{ query.trim() ? '没有匹配的项。' : '所有工具都已经在名单里了。' }}</p>
            <template v-for="row in candidateRows" :key="row.id">
              <article class="residency-row">
                <div class="residency-info">
                  <strong>{{ row.name }} <span class="badge">{{ kindLabel(row) }}</span></strong>
                  <p>{{ expanded[row.id] ? row.detail : row.description }}</p>
                  <button v-if="hasDetail(row)" type="button" class="link-button" @click="expanded[row.id] = !expanded[row.id]">{{ expanded[row.id] ? '收起' : '展开完整说明' }}</button>
                  <small v-if="row.tools?.length">{{ row.tools.length }} 个工具：{{ row.tools.join('、') }}</small>
                  <small v-if="row.resident_tokens" class="residency-cost">加进来每轮多 {{ formatTokens(addDelta(row)) }}</small>
                </div>
                <div class="residency-actions">
                  <button v-if="childrenOf(row).length" class="btn" type="button" @click="opened[row.id] = !opened[row.id]">{{ opened[row.id] ? '收起' : '逐个挑' }}</button>
                  <button class="btn" type="button" @click="add(row)">添加</button>
                </div>
              </article>
              <article v-for="child in opened[row.id] ? childrenOf(row) : []" :key="child.id" class="residency-row residency-child">
                <div class="residency-info">
                  <strong>{{ child.name }}</strong>
                  <p>{{ child.description }}</p>
                  <small class="residency-cost">加进来每轮多 {{ formatTokens(addDelta(child)) }}</small>
                </div>
                <div class="residency-actions">
                  <button v-if="isResident(child)" class="btn" type="button" @click="remove(child)">移除</button>
                  <button v-else class="btn" type="button" @click="add(child)">添加</button>
                </div>
              </article>
            </template>
          </div>
          <template #footer>
            <span class="hint">名单现在 {{ summary.resident }} 个工具 · 每轮 {{ formatTokens(summary.residentTokens) }}；关掉弹窗后在页面底部一起保存。</span>
            <button class="btn primary" type="button" @click="picking = false">完成</button>
          </template>
        </Modal>
      </template>
      <p v-else class="hint">这台机器人还没跑过一轮对话。工具目录是按平台、权限和插件开关逐轮拼出来的，跑过一次之后这里才知道它实际有哪些工具。</p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue';
import { listAgentResidency, saveAgentResidencyList, type AgentResidencyEntry } from '../api';
import Modal from './Modal.vue';

// 档位跟着「正在编辑的这台机器人」走，不跟顶栏的全局作用域：在机器人配置页里改
// 的就是眼前这一台，两者不一致时按全局作用域取数会改错机器人。
const props = defineProps<{profile?: string}>();
const profileID = computed(() => props.profile ?? '');

type Tier = boolean | null;
const items = ref<AgentResidencyEntry[]>([]), loading = ref(false), loadError = ref('');
// savedList 是「这台机器人有没有自己的名单」。没有就跟着内置推荐走——这时候一个
// 工具不在名单里不代表不常驻，它可能正被推荐带着。
const savedList = ref(false);
const query = ref(''), picking = ref(false);
const pickerInput = ref<HTMLInputElement | null>(null);
const expanded = reactive<Record<string, boolean>>({}), opened = reactive<Record<string, boolean>>({});
// 改动先攒在这里再一起提交。每点一下就发一次请求，等于用户调十项就把所有会话的
// 前缀缓存作废十次——这一页自己写着那句代价，交互不能跟它对着干。
const pending = reactive<Record<string, Tier>>({});
// reset 是「退回推荐名单」：它不是把每一项都改一遍，而是把这台机器人的名单整个撤掉，
// 之后继续跟着版本走。所以它得单独记一笔，不能混在逐项改动里。
const reset = ref(false);

// 底下仍是三态（没配 / 常驻 / 按需），但界面不摆出来：用户要的是一份名单，加进来
// 或者拿出去。默认在名单里的点「移除」写成按需，不在的点「添加」写成常驻，「没配过」
// 只是它们的初始状态，不需要一个按钮专门表达。
const saved = (item: AgentResidencyEntry): Tier => item.resident === undefined ? null : item.resident;
// 按下「恢复推荐名单」之后，这一屏就该当作没有名单来显示。
const listed = computed(() => savedList.value && !reset.value);
// 按下「恢复推荐名单」之后，屏幕上就该立刻显示推荐名单的样子，而不是已存的那一份。
const current = (item: AgentResidencyEntry): Tier => pending[item.id] !== undefined ? pending[item.id] : reset.value ? null : saved(item);
const byID = computed(() => new Map(items.value.map(item => [item.id, item])));
const childrenOf = (item: AgentResidencyEntry) => items.value.filter(child => child.parent === item.id);
// 工具没单独表过态就跟着它所属的插件或 MCP 服务走，那一条也没表态才落到推荐名单。
function fallback(item: AgentResidencyEntry): boolean {
  const parent = item.parent ? byID.value.get(item.parent) : undefined;
  const tier = parent ? current(parent) : null;
  if (tier !== null) return tier;
  // 有名单就以名单为准：没写进去就是不常驻。没名单才回到内置推荐。
  return listed.value ? false : item.default;
}
const isResident = (item: AgentResidencyEntry) => current(item) ?? fallback(item);
const hasDetail = (item: AgentResidencyEntry) => !!item.detail && item.detail !== item.description;
const kindLabel = (item: AgentResidencyEntry) => item.kind === 'plugin' ? '插件' : item.kind === 'mcp' ? 'MCP' : item.parent ? '工具' : '内置工具';
const residentCost = (item: AgentResidencyEntry) => item.resident_tokens || 0;
const deferredCost = (item: AgentResidencyEntry) => item.deferred_tokens || 0;
function formatTokens(value: number) {
  if (!value) return '0 tok';
  return value < 1000 ? `${value} tok` : `${(value / 1000).toFixed(1)}k tok`;
}

// 整条在名单里、却单独被拿掉的那几个工具。不写出来的话，插件明明在名单里，它的某个
// 工具却不生效，用户只能靠展开才发现。
const excludedOf = (item: AgentResidencyEntry) => addedWhole(item) ? childrenOf(item).filter(child => !isResident(child)) : [];

function add(item: AgentResidencyEntry) {
  setTier(item, true);
  // 整条加回来时，之前单独排除掉的那几个不跟着回来——那是用户明说过的话，不该被
  // 一次「添加」悄悄抹掉。要一起回来有「放回来」。
}
function remove(item: AgentResidencyEntry) {
  setTier(item, false);
}
function restore(item: AgentResidencyEntry) {
  for (const child of childrenOf(item)) if (!isResident(child)) setTier(child, null);
}
function setTier(item: AgentResidencyEntry, value: Tier) {
  if (saved(item) === value && !reset.value) delete pending[item.id];
  else pending[item.id] = value;
}

const leaves = computed(() => items.value.filter(item => item.kind === 'tool'));
// 汇总只按工具算：分组行是容器，它的 token 是旗下工具的和，两边都算就重复了。
const summary = computed(() => leaves.value.reduce((total, item) => {
  if (isResident(item)) {
    total.resident += 1;
    total.residentTokens += residentCost(item);
  } else {
    total.deferred += 1;
    total.deferredTokens += deferredCost(item);
  }
  return total;
}, {resident: 0, deferred: 0, residentTokens: 0, deferredTokens: 0}));
// 已存下来的结果，用来和屏幕上的比出差额。口径和 fallback 一致，只是不看待保存的改动。
const savedResident = (item: AgentResidencyEntry): boolean => {
  const own = saved(item);
  if (own !== null) return own;
  const parent = item.parent ? byID.value.get(item.parent) : undefined;
  const inherited = parent ? saved(parent) : null;
  if (inherited !== null) return inherited;
  return savedList.value ? false : item.default;
};
const cost = (item: AgentResidencyEntry, resident: boolean) => resident ? residentCost(item) : deferredCost(item);
const delta = computed(() => leaves.value.reduce((sum, item) => sum + cost(item, isResident(item)) - cost(item, savedResident(item)), 0));
// 待保存的不是「点了几下」，而是「有几个工具的待遇会变」——用户关心的是后者，而且
// 一次插件级的增删本来就会带动好几个工具。
const dirtyCount = computed(() => leaves.value.filter(item => isResident(item) !== savedResident(item)).length);
const dirty = computed(() => reset.value || Object.keys(pending).length > 0);

// 一条插件或 MCP 服务整条在名单里时，它旗下的工具不再单独列一行：名单上的单位就是
// 用户加进来的那一个。整条不在名单里、其中某个工具被单独加进来了，那个工具自己上榜。
// 名单上的一行是「用户加进来的那一个」。整条插件或服务只有被明确加过才算一行；
// 没加过的时候，推荐名单是按工具推荐的，那就按工具列——否则一个插件会因为旗下
// 某个工具被推荐而整条上榜，再标一句「其中排除了另外两个」，说的全不是实情。
const addedWhole = (item: AgentResidencyEntry) => item.kind !== 'tool' && current(item) === true;
const onList = (item: AgentResidencyEntry) => {
  if (item.kind !== 'tool') return addedWhole(item);
  if (!isResident(item)) return false;
  const parent = item.parent ? byID.value.get(item.parent) : undefined;
  return !(parent && addedWhole(parent));
};
const byCost = (a: AgentResidencyEntry, b: AgentResidencyEntry) => residentCost(b) - residentCost(a) || a.name.localeCompare(b.name);
const residentRows = computed(() => items.value.filter(onList).sort(byCost));
const matches = (item: AgentResidencyEntry) => {
  const text = query.value.trim().toLowerCase();
  if (!text) return true;
  return `${item.name} ${item.description || ''} ${(item.tools || []).join(' ')}`.toLowerCase().includes(text);
};
// 可添加的：不在名单上的整条和独立工具。已经被某条在名单上的插件带进去的工具不在
// 这里出现——它们要单独挑，在那一条的「逐个挑」里。
const candidateRows = computed(() => items.value.filter(item => {
  // 属于某条插件或服务的工具不单独排队：它在那一条的「逐个挑」里加，列在这里
  // 只会让同一件事出现两个入口。
  if (item.parent) return false;
  if (item.kind === 'tool' ? isResident(item) : addedWhole(item)) return false;
  return matches(item);
}).sort(byCost));
// 「加进来每轮多多少」要按还没常驻的部分算：一条服务里已经单独加过的那个工具，
// 再把整条加进来并不会让它更贵。
const addDelta = (item: AgentResidencyEntry) => {
  const children = childrenOf(item);
  if (!children.length) return residentCost(item) - deferredCost(item);
  return children.reduce((sum, child) => isResident(child) ? sum : sum + residentCost(child) - deferredCost(child), 0);
};

// 挑完就关：弹窗每次打开都从空搜索开始，否则上次搜的词会把半个清单藏起来。
function openPicker() {
  query.value = '';
  picking.value = true;
  void nextTick(() => pickerInput.value?.focus());
}

let generation = 0;
async function load() {
  const current = ++generation;
  loading.value = true;
  loadError.value = '';
  try {
    const result = await listAgentResidency(profileID.value);
    if (current === generation) {
      items.value = result.items;
      savedList.value = !!result.listed;
    }
  } catch (e) {
    if (current === generation) loadError.value = String(e instanceof Error ? e.message : e);
  } finally {
    if (current === generation) loading.value = false;
  }
}
function resetAll() {
  discard();
  reset.value = true;
}
function discard() {
  for (const id of Object.keys(pending)) delete pending[id];
  reset.value = false;
}
// 名单不自己保存：它和这一页的其他配置同属一台机器人，两个保存按钮会让人以为
// 点了其中一个就全存上了。宿主页面保存成功后调这里，写完再拉一次最新状态。
async function applyPending() {
  const profile = profileID.value;
  if (!profile || !dirty.value) return;
  // 存的是整份名单，一次写完：名单本来就是一整件事，分几次写中间会出现「插件进去了、
  // 单独拿掉的那个还没拿掉」这种谁都不想要的状态，而它每变一次，所有会话的工具列表
  // 就跟着变一次。
  await saveAgentResidencyList(profile, reset.value ? null : residentRows.value.map(row => row.id));
  discard();
  await load();
}
defineExpose({applyPending, pendingCount: dirtyCount, dirty, discard, reload: load});
watch(profileID, () => { discard(); load(); });
onMounted(load);
</script>

<style scoped>
.residency-panel{display:flex;flex-direction:column;gap:10px}.residency-panel .hint{color:var(--muted);font-size:12px}
.residency-head{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;flex-wrap:wrap}.residency-title{flex:1;min-width:240px}.residency-head h3{margin:0;font-size:15px}.residency-lede{margin:6px 0 0;color:var(--text-secondary);font-size:12.5px}.residency-lede strong{color:var(--text);font-variant-numeric:tabular-nums}
.residency-dirty{margin:14px 0 0;padding:10px 12px;border-radius:var(--radius-sm);background:var(--accent-soft);color:var(--text-secondary);font-size:12px}.residency-dirty .link-button{margin-left:4px}
.residency-foot{display:flex;align-items:baseline;justify-content:space-between;gap:12px;flex-wrap:wrap;margin:12px 0 0}
.residency-list{border-top:1px solid var(--border);margin-top:10px}.residency-row{display:flex;align-items:center;gap:12px;padding:14px 0;border-bottom:1px solid var(--border)}.residency-child{padding-left:22px;border-left:2px solid var(--border)}.residency-row.changed{box-shadow:inset 2px 0 0 var(--accent);padding-left:10px}.residency-info{flex:1;min-width:0;overflow-wrap:anywhere}.residency-info p{margin:4px 0 0;color:var(--muted)}.residency-info strong{display:inline-flex;align-items:center;gap:6px;flex-wrap:wrap}.residency-info small{display:block;margin-top:4px;color:var(--muted)}.residency-excluded{color:var(--warn)}.residency-cost{font-variant-numeric:tabular-nums}
.residency-actions{flex-shrink:0;display:flex;gap:8px}
.residency-search{margin-top:10px;max-width:320px}.modal-footer .hint{flex:1;min-width:200px}
.link-button{background:none;border:0;padding:0;margin-top:4px;color:var(--accent);font:inherit;font-size:12px;cursor:pointer;text-decoration:underline}.error-text{color:var(--danger)}@media(max-width:600px){.residency-row{flex-wrap:wrap;gap:8px}.residency-info{flex-basis:100%}.residency-head .btn{align-self:flex-start}}
</style>
